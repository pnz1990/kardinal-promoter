// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/test/gitbackend"
)

// gitHTTPBackend serves the bare repositories under root with git's own
// smart HTTP server (git http-backend), pushes enabled, and no backend
// outliving the test (test/gitbackend, #1875). slowRead, when set, throttles
// how fast the server reads a request body.
func gitHTTPBackend(t *testing.T, root string, slowRead time.Duration) *httptest.Server {
	t.Helper()
	return gitbackend.Serve(t, root, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slowRead > 0 && r.Method == http.MethodPost {
				r.Body = io.NopCloser(&slowReader{r: r.Body, every: slowRead})
			}
			h.ServeHTTP(w, r)
		})
	})
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
