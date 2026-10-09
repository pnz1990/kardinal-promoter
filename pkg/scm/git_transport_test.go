// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// gitHTTPBackend serves the bare repositories under root with git's own
// smart HTTP server (git http-backend), pushes enabled. slowRead, when set,
// throttles how fast the server reads a request body.
func gitHTTPBackend(t *testing.T, root string, slowRead time.Duration) *httptest.Server {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git http-backend needs the git binary")
	}
	h := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=kardinal"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slowRead > 0 && r.Method == http.MethodPost {
			r.Body = io.NopCloser(&slowReader{r: r.Body, every: slowRead})
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// slowReader reads at most 16 KiB per call, each after a pause.
type slowReader struct {
	r     io.Reader
	every time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	time.Sleep(s.every)
	if len(p) > 16<<10 {
		p = p[:16<<10]
	}
	return s.r.Read(p)
}

// TestGoGitClient_SlowHTTPPushIsNotCut (#1491): a push the server reads
// slowly takes longer than the idle bound in total but keeps moving data,
// so it succeeds: every write pushes the read deadline out too.
//
// Covers SCM-SSH-03.
func TestGoGitClient_SlowHTTPPushIsNotCut(t *testing.T) {
	const idle = 4 * time.Second
	defer scm.SetGitIdleTimeoutForTest(idle)()
	remote := seedBareRemote(t, map[string]string{"a.txt": "a\n"})
	require.NoError(t, exec.Command("git", "-C", remote, "config", "http.receivepack", "true").Run())
	root := filepath.Dir(remote)
	// About 1500 reads of 16 KiB, 4ms apart: the push lasts well past the
	// idle bound, while each read and write is far inside it.
	srv := gitHTTPBackend(t, root, 4*time.Millisecond)
	url := srv.URL + "/" + filepath.Base(remote)

	ctx := context.Background()
	work := filepath.Join(t.TempDir(), "w")
	c := scm.NewGoGitClient()
	require.NoError(t, c.Clone(ctx, url, "main", work, scm.GitAuth{}))
	big := make([]byte, 24<<20) // random, so the pack does not compress
	_, _ = rand.Read(big)
	require.NoError(t, os.WriteFile(filepath.Join(work, "big.bin"), big, 0o600))
	require.NoError(t, c.CommitAll(ctx, work, "big", "k", "k@example.com"))
	start := time.Now()
	// Bounded, so the old behaviour (a deadline only reads extend) fails the
	// test instead of hanging it: a write blocked on a stalled connection
	// does not see the context, so the server's connections are closed too.
	pushCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Push(pushCtx, work, "origin", "kardinal/b/prod", scm.GitAuth{}, false) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(120 * time.Second):
		srv.CloseClientConnections()
		<-done
		t.Fatal("the push hung: the idle bound does not extend on writes")
	}
	assert.Greater(t, time.Since(start), idle, "the push took longer than the idle bound in total")
	out, err := exec.Command("git", "-C", remote, "rev-parse", "kardinal/b/prod").CombinedOutput()
	require.NoError(t, err, "%s", out)
}

// TestGoGitClient_StalledHTTPServer (#1491): an HTTPS (here HTTP) git server
// that accepts the request and never answers fails the clone after the idle
// bound. It fails if the idle wrapping of git connections is removed.
//
// Covers SCM-SSH-03.
func TestGoGitClient_StalledHTTPServer(t *testing.T) {
	defer scm.SetGitIdleTimeoutForTest(300 * time.Millisecond)()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(20 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	err := scm.NewGoGitClient().Clone(context.Background(), srv.URL+"/a/b.git", "main", filepath.Join(t.TempDir(), "w"), scm.GitAuth{})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "the idle bound ended the clone: %v", err)
	assert.Contains(t, strings.ToLower(err.Error()), "timeout")
}

// TestGoGitClient_SSHBlackholeConnect (#1491): an ssh clone whose TCP
// connect never completes (a blackholed address) gives up at the connect
// limit: the dial scope's context is cancelled when the timer fires.
//
// Covers SCM-SSH-03.
func TestGoGitClient_SSHBlackholeConnect(t *testing.T) {
	defer scm.SetSSHTimeoutsForTest(300*time.Millisecond, 300*time.Millisecond)()
	defer scm.SetDialTCPForTest(func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done() // a SYN nobody answers
		return nil, ctx.Err()
	})()
	clientKey, _ := sshKeyPair(t)
	_, hostKey := sshKeyPair(t)
	auth := scm.GitAuth{SSHPrivateKey: clientKey, SSHKnownHosts: []byte("[192.0.2.1]:22 " + string(ssh.MarshalAuthorizedKey(hostKey)))}
	start := time.Now()
	// Bounded, so a regression (the connect not cancelled) fails the test
	// instead of hanging it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := scm.NewGoGitClient().Clone(ctx, "ssh://git@192.0.2.1:22/a/b.git", "main", filepath.Join(t.TempDir(), "w"), auth)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second, "the connect limit ended the clone: %v", err)
}
