// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// silentSSHServer accepts TCP connections and never answers, and counts
// the connections still open.
func silentSSHServer(t *testing.T) (addr string, open func() int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	conns := make(chan net.Conn, 64)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns <- c
		}
	}()
	var held []net.Conn
	t.Cleanup(func() {
		for _, c := range held {
			_ = c.Close()
		}
	})
	return ln.Addr().String(), func() int {
		for {
			select {
			case c := <-conns:
				held = append(held, c)
				continue
			default:
			}
			break
		}
		n := 0
		for _, c := range held {
			// Read what the client sent (its ssh banner) until the end:
			// a closed connection ends in EOF, an open one in a timeout.
			buf := make([]byte, 4096)
			for {
				_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
				if _, err := c.Read(buf); err != nil {
					if !isEOF(err) {
						n++
					}
					break
				}
			}
		}
		return n
	}
}

func isEOF(err error) bool {
	var ne net.Error
	return err != nil && (!errors.As(err, &ne) || !ne.Timeout())
}

// TestGoGitClient_SSHHandshakeTimeoutLeaksNothing (#1491): a clone from an
// ssh server that never finishes the handshake gives up at the connect
// limit, closes its connection, and leaves no goroutine behind, five times
// over.
//
// Covers SCM-SSH-03.
func TestGoGitClient_SSHHandshakeTimeoutLeaksNothing(t *testing.T) {
	defer scm.SetSSHTimeoutsForTest(300*time.Millisecond, 300*time.Millisecond)()
	clientKey, _ := sshKeyPair(t)
	_, hostKey := sshKeyPair(t)
	addr, open := silentSSHServer(t)
	host, port, _ := net.SplitHostPort(addr)
	auth := scm.GitAuth{SSHPrivateKey: clientKey, SSHKnownHosts: []byte("[" + host + "]:" + port + " " + string(ssh.MarshalAuthorizedKey(hostKey)))}

	// One clone first, so the goroutines that stay for the process (the
	// HTTP transport's, go-git's) are counted in the baseline.
	_ = scm.NewGoGitClient().Clone(context.Background(), "ssh://git@"+addr+"/a/b.git", "main", filepath.Join(t.TempDir(), "w"), auth)
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		start := time.Now()
		err := scm.NewGoGitClient().Clone(context.Background(), "ssh://git@"+addr+"/a/b.git", "main", filepath.Join(t.TempDir(), "w"), auth)
		require.Error(t, err)
		assert.Less(t, time.Since(start), 3*time.Second, "clone %d gives up at the connect limit: %v", i, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		buf := make([]byte, 1<<20)
		t.Fatalf("goroutines: %d before, %d after five timed-out clones:\n%s", before, after, buf[:runtime.Stack(buf, true)])
	}
	assert.Zero(t, open(), "every connection the timed-out clones opened is closed")
}

// TestGoGitClient_SSHCancelledStep: the step's context reaches the ssh
// dial: cancelling it ends a clone stuck in the handshake at once.
//
// Covers SCM-SSH-03.
func TestGoGitClient_SSHCancelledStep(t *testing.T) {
	defer scm.SetSSHTimeoutsForTest(time.Minute, time.Minute)()
	clientKey, _ := sshKeyPair(t)
	_, hostKey := sshKeyPair(t)
	addr, _ := silentSSHServer(t)
	host, port, _ := net.SplitHostPort(addr)
	auth := scm.GitAuth{SSHPrivateKey: clientKey, SSHKnownHosts: []byte("[" + host + "]:" + port + " " + string(ssh.MarshalAuthorizedKey(hostKey)))}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	err := scm.NewGoGitClient().Clone(ctx, "ssh://git@"+addr+"/a/b.git", "main", filepath.Join(t.TempDir(), "w"), auth)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "the cancelled step ends the clone: %v", err)
}

// TestIdleConn: a Read that waits longer than the idle bound fails with a
// timeout; data resets the bound; an explicit deadline replaces it until
// cleared.
//
// Covers SCM-SSH-03.
func TestIdleConn(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := scm.NewIdleConnForTest(a, 100*time.Millisecond)
	defer c.Close()
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = b.Write([]byte("x"))
	}()
	buf := make([]byte, 1)
	_, err := c.Read(buf)
	require.NoError(t, err, "data within the bound")
	start := time.Now()
	_, err = c.Read(buf)
	var ne net.Error
	require.True(t, errors.As(err, &ne) && ne.Timeout(), "idle past the bound: %v", err)
	assert.Less(t, time.Since(start), time.Second)

	require.NoError(t, c.SetDeadline(time.Now().Add(400*time.Millisecond)))
	start = time.Now()
	_, err = c.Read(buf)
	require.Error(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond, "an explicit deadline replaces the idle bound")
}
