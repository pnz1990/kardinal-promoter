// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// The race detector makes go-git hash and pack the 8 MiB push here a few
// times slower, and the test is about deadlines, not memory races:
// idleConn's locking is covered under -race by the other idle-bound tests
// in git_transport_test.go. CI runs this file in the build checks job's
// non-race go test step.

//go:build !race

package scm_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/test/gitbackend"
)

// TestGoGitClient_SlowHTTPPushIsNotCut (#1491): a push the server reads
// slowly takes longer than the idle bound in total but keeps moving data,
// so it succeeds: every write pushes the read deadline out too.
//
// What paces the upload is the test's handler, not git (#2035): it reads
// the body off the network 16 KiB at a time, 20ms apart, for 1.25 times the
// idle bound, and then at full speed; git http-backend reads what arrived
// from memory. A git process slowed by a loaded host so no longer stalls
// the client's writes past the bound (it did: 24 MiB through a git that
// read it as it came, #2035), and the time from the client's last write to
// the answer is git's work on what is left, not the slow drain of
// megabytes the kernel buffered. The server's connections have a 256 KiB
// receive buffer, so the client keeps writing for as long as the paced
// reads last. The test checks both: the paced reads lasted longer than the
// bound, and the upload was still coming when they ended.
//
// Covers SCM-SSH-03.
func TestGoGitClient_SlowHTTPPushIsNotCut(t *testing.T) {
	const idle = 4 * time.Second
	defer scm.SetGitIdleTimeoutForTest(idle)()
	remote := seedBareRemote(t, map[string]string{"a.txt": "a\n"})
	require.NoError(t, exec.Command("git", "-C", remote, "config", "http.receivepack", "true").Run())
	root := filepath.Dir(remote)
	uploads := make(chan *pacedBody, 1)
	srv := gitbackend.ServeWith(t, root, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
				h.ServeHTTP(w, r)
				return
			}
			b := newPacedBody(r.Body, 16<<10, 20*time.Millisecond, idle+idle/4)
			select {
			case uploads <- b:
			default:
			}
			r.Body = b
			h.ServeHTTP(w, r)
			<-b.filled // the handler is done with r.Body only once fill is
		})
	}, func(s *httptest.Server) {
		s.Config.ConnState = func(c net.Conn, st http.ConnState) {
			if tc, ok := c.(*net.TCPConn); ok && st == http.StateNew {
				_ = tc.SetReadBuffer(256 << 10)
			}
		}
	})
	url := srv.URL + "/" + filepath.Base(remote)

	ctx := context.Background()
	work := filepath.Join(t.TempDir(), "w")
	c := scm.NewGoGitClient()
	require.NoError(t, c.Clone(ctx, url, "main", work, scm.GitAuth{}))
	big := make([]byte, 8<<20) // random, so the pack does not compress
	_, _ = rand.Read(big)
	require.NoError(t, os.WriteFile(filepath.Join(work, "big.bin"), big, 0o600))
	require.NoError(t, c.CommitAll(ctx, work, "big", "k", "k@example.com"))
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
	b := <-uploads
	<-b.paced
	assert.Greater(t, b.pacedFor, idle, "the server read the upload slowly for longer than the idle bound")
	assert.False(t, b.ranOut, "the upload was still coming when the slow reads ended: the client wrote for all of them")
	out, err := exec.Command("git", "-C", remote, "rev-parse", "kardinal/b/prod").CombinedOutput()
	require.NoError(t, err, "%s", out)
}

// pacedBody is a request body read off the network by its own goroutine:
// chunk bytes every interval for pace, then at full speed. Its reader (git
// http-backend) reads what has arrived, so how fast the reader is does not
// change how fast the network is read.
type pacedBody struct {
	mu   sync.Mutex
	cond *sync.Cond
	buf  bytes.Buffer
	err  error

	// paced is closed when the paced reads end, after pacedFor (how long
	// they lasted) and ranOut (the upload ended before them) are set.
	paced    chan struct{}
	pacedFor time.Duration
	ranOut   bool
	// filled is closed when the body has been read to its end.
	filled chan struct{}
}

func newPacedBody(r io.Reader, chunk int, every, pace time.Duration) *pacedBody {
	b := &pacedBody{paced: make(chan struct{}), filled: make(chan struct{})}
	b.cond = sync.NewCond(&b.mu)
	go b.fill(r, chunk, every, pace)
	return b
}

func (b *pacedBody) fill(r io.Reader, chunk int, every, pace time.Duration) {
	defer close(b.filled)
	start := time.Now()
	p := make([]byte, 256<<10)
	pacing := true
	endPace := func(ranOut bool) {
		b.pacedFor, b.ranOut, pacing = time.Since(start), ranOut, false
		close(b.paced)
	}
	for {
		n := len(p)
		if pacing {
			if time.Since(start) >= pace {
				endPace(false)
			} else {
				time.Sleep(every)
				n = chunk
			}
		}
		n, err := r.Read(p[:n])
		b.mu.Lock()
		b.buf.Write(p[:n])
		if err != nil {
			b.err = err
		}
		b.cond.Broadcast()
		b.mu.Unlock()
		if err != nil {
			if pacing {
				endPace(true)
			}
			return
		}
	}
}

func (b *pacedBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.buf.Len() == 0 && b.err == nil {
		b.cond.Wait()
	}
	if b.buf.Len() > 0 {
		return b.buf.Read(p)
	}
	return 0, b.err
}

func (b *pacedBody) Close() error { return nil }
