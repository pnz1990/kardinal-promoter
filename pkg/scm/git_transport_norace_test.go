// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// The race detector makes go-git hash and pack the 24 MiB push here about
// three times slower (30s instead of 10s), and the test is about deadlines,
// not memory races: idleConn's locking is covered under -race by the other
// idle-bound tests in git_transport_test.go. CI runs this file in the
// build checks job's non-race go test step.

//go:build !race

package scm_test

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

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
