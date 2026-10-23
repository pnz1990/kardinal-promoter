// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitIn runs git in dir with a fixed identity, for test setup.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// bigHeadRemote is a bare remote whose main has a.txt, and whose branch
// kardinal/b/prod is a commit on main adding big (size bytes, random or,
// with zeros, all zero) and a.txt v2. It returns the remote, a work tree of
// main with the very same change committed (same tree and parent, another
// message; with !same, without one extra file the branch's head has), and
// the branch's head.
func bigHeadRemote(t *testing.T, size int, zeros, allowFilter, same bool) (remote, ours, head string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	gitIn(t, root, "init", "-q", "--bare", remote)
	gitIn(t, remote, "config", "uploadpack.allowFilter", map[bool]string{true: "true", false: "false"}[allowFilter])
	seed := filepath.Join(root, "seed")
	gitIn(t, root, "clone", "-q", remote, seed)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o600))
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "seed")
	gitIn(t, seed, "push", "-q", "origin", "HEAD:main")
	blob := make([]byte, size)
	if !zeros {
		_, err := rand.Read(blob)
		require.NoError(t, err)
	}
	change := func(dir, msg string, extra bool) string {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "big"), blob, 0o600))
		if extra {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "evil"), []byte("x\n"), 0o600))
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v2\n"), 0o600))
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", msg)
		return gitIn(t, dir, "rev-parse", "HEAD")
	}
	theirs := filepath.Join(root, "theirs")
	gitIn(t, root, "clone", "-q", remote, theirs)
	head = change(theirs, "someone else's", !same)
	gitIn(t, theirs, "push", "-q", "origin", "HEAD:refs/heads/kardinal/b/prod")
	ours = filepath.Join(root, "ours")
	gitIn(t, root, "clone", "-q", remote, ours)
	change(ours, "kardinal: promote", false)
	return remote, ours, head
}

// allocatedBy is how many bytes f allocates.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestSameContentAsHead_Bounded (QA-4 on #2232): a head on kardinal's
// branch is anyone's who may push there, and its blobs can be huge. The
// same-content check never keeps one. With partial clone (uploadpack
// allowFilter) the server sends the commit object alone; without it, the
// pack is read as a stream that stops at the commit, which git sends
// first. Either way a 24 MiB blob, random or all zeros (which inflates far
// past its packed size), costs no memory: the head with the same content
// is adopted and one with other content refused, both well under the
// blob's size.
//
// Covers GIT-PUSH-FOREIGN-04.
func TestSameContentAsHead_Bounded(t *testing.T) {
	const size = 24 << 20
	c := NewGoGitClient()
	ctx := context.Background()
	for _, tc := range []struct {
		name                     string
		zeros, allowFilter, same bool
	}{
		{name: "partial clone, same content", allowFilter: true, same: true},
		{name: "partial clone, other content", allowFilter: true},
		{name: "no partial clone, same content", same: true},
		{name: "no partial clone, other content"},
		{name: "no partial clone, zeros, same content", zeros: true, same: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ours, head := bigHeadRemote(t, size, tc.zeros, tc.allowFilter, tc.same)
			var same bool
			var err error
			alloc := allocatedBy(func() {
				same, err = c.SameContentAsHead(ctx, ours, "origin", "kardinal/b/prod", TokenAuth(""), head)
			})
			require.NoError(t, err)
			assert.Equal(t, tc.same, same)
			assert.Less(t, alloc, uint64(8<<20), "allocated %d bytes for a %d byte blob", alloc, size)
		})
	}
	t.Run("past the bound: refused, not an error", func(t *testing.T) {
		defer func(old int64) { maxHeadPeek = old }(maxHeadPeek)
		maxHeadPeek = 64 // bytes: less than the pack header and the commit
		_, ours, head := bigHeadRemote(t, 1<<10, false, true, true)
		same, err := c.SameContentAsHead(ctx, ours, "origin", "kardinal/b/prod", TokenAuth(""), head)
		require.NoError(t, err, "the step refuses the head as someone else's (PRBranchForeignCommit)")
		assert.False(t, same)
	})
}

// envUploadPackProxy, set to the real git-upload-pack, makes the test
// binary an upload-pack in front of it (uploadPackProxy);
// envUploadPackStall, set too, makes it stall once the pack begins.
const (
	envUploadPackProxy = "KARDINAL_TEST_UPLOAD_PACK_PROXY"
	envUploadPackStall = "KARDINAL_TEST_UPLOAD_PACK_STALL"
)

func init() {
	if real := os.Getenv(envUploadPackProxy); real != "" {
		os.Exit(uploadPackProxy(real, os.Args[len(os.Args)-1]))
	}
}

// uploadPackProxy runs the real upload-pack for repo and forwards what it
// says, but re-cuts the pack (sideband band 1) into 200-byte packets sent
// 64 KiB at a time, and where the pack ends keeps sending band-1 packets
// for ever: a server still writing, as a big pack is, when the client has
// what it wants, with the packets after the commit already in the
// client's buffers when it stops reading.
func uploadPackProxy(real, repo string) int {
	// A test that fails waiting on the proxy leaves it behind: it ends by
	// itself, after any test's 30 s guard.
	time.AfterFunc(2*time.Minute, func() { os.Exit(0) })
	cmd := exec.Command(real, repo)
	cmd.Stdin = os.Stdin
	out, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return 1
	}
	r, w := bufio.NewReader(out), bufio.NewWriterSize(os.Stdout, 64<<10)
	pkt := func(payload []byte) error {
		if _, err := fmt.Fprintf(w, "%04x", len(payload)+4); err != nil {
			return err
		}
		_, err := w.Write(payload)
		return err
	}
	inPack := false
	for {
		var size [4]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			break
		}
		n, err := strconv.ParseUint(string(size[:]), 16, 16)
		if err != nil {
			return 1
		}
		if n < 4 {
			if inPack {
				break // the pack's end: keep sending instead
			}
			_, _ = w.Write(size[:])
			_ = w.Flush()
			continue
		}
		payload := make([]byte, n-4)
		if _, err := io.ReadFull(r, payload); err != nil {
			return 1
		}
		if payload[0] != 1 {
			_ = pkt(payload)
			if !inPack {
				_ = w.Flush()
			}
			continue
		}
		inPack = true
		if os.Getenv(envUploadPackStall) != "" {
			// A server that stalls part-way through its response: nothing
			// more is sent, and the stream stays open (until the watchdog).
			select {}
		}
		for data := payload[1:]; len(data) > 0; {
			k := min(len(data), 200)
			_ = pkt(append([]byte{1}, data[:k]...))
			data = data[k:]
		}
	}
	filler := append([]byte{1}, make([]byte, 200)...)
	for pkt(filler) == nil {
	}
	return 0
}

// TestSameContentAsHead_ServerKeepsWriting (#2364): the server is still
// writing when the scan has the commit, and the packets after it are
// already in the client's buffers (uploadPackProxy, in front of the real
// upload-pack). The check returns at once: it reads until the cancelled
// transport fails, which kills the local upload-pack. Reading one byte, as
// it did, was served from those buffers, nothing killed the upload-pack,
// and Close waited for it forever: TestSameContentAsHead_Bounded timed out
// pkg/scm's CI shard whenever a loaded runner let the pack's next packets
// arrive with the commit's.
//
// Covers GIT-PUSH-FOREIGN-05.
func TestSameContentAsHead_ServerKeepsWriting(t *testing.T) {
	real, err := exec.LookPath("git-upload-pack")
	if err != nil {
		out, gerr := exec.Command("git", "--exec-path").Output()
		require.NoError(t, gerr)
		real = filepath.Join(strings.TrimSpace(string(out)), "git-upload-pack")
	}
	self, err := os.Executable()
	require.NoError(t, err)
	_, ours, head := bigHeadRemote(t, 1<<10, false, false, true)
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git-upload-pack"), []byte(
		"#!/bin/sh\n"+envUploadPackProxy+"='"+real+"' exec '"+self+"' -test.run='^$' \"$@\"\n"), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := NewGoGitClient()
	for _, tc := range []struct {
		name string
		peek int64 // maxHeadPeek
		same bool
	}{
		{name: "the check's bound", peek: maxHeadPeek, same: true},
		// QA-2 on #2374: a peek bound below go-git's buffers must not bound
		// the reads that reach the transport (past it the head is refused).
		{name: "a 64-byte bound", peek: 64, same: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(old int64) { maxHeadPeek = old }(maxHeadPeek)
			maxHeadPeek = tc.peek
			for i := range 10 {
				type result struct {
					same bool
					err  error
				}
				done := make(chan result, 1)
				go func() {
					same, err := c.SameContentAsHead(context.Background(), ours, "origin", "kardinal/b/prod", TokenAuth(""), head)
					done <- result{same, err}
				}()
				select {
				case r := <-done:
					require.NoError(t, r.err, "try %d", i)
					assert.Equal(t, tc.same, r.same, "try %d", i)
				case <-time.After(30 * time.Second):
					t.Fatalf("try %d: the check did not return in 30s: it waits on an upload-pack still writing", i)
				}
			}
		})
	}
}

// TestSameContentAsHead_ServerStalls (#2375): the server stops sending
// part-way through its response and keeps the stream open (uploadPackProxy
// stalling where the pack begins). The peek has a deadline of its own,
// headPeekTimeout, so the check returns with an error saying so, which the
// step retries like any fetch error, instead of holding the worker until
// the caller's context ends; a caller's sooner deadline wins.
//
// Covers GIT-PUSH-FOREIGN-06.
func TestSameContentAsHead_ServerStalls(t *testing.T) {
	real, err := exec.LookPath("git-upload-pack")
	if err != nil {
		out, gerr := exec.Command("git", "--exec-path").Output()
		require.NoError(t, gerr)
		real = filepath.Join(strings.TrimSpace(string(out)), "git-upload-pack")
	}
	self, err := os.Executable()
	require.NoError(t, err)
	_, ours, head := bigHeadRemote(t, 1<<10, false, false, true)
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git-upload-pack"), []byte(
		"#!/bin/sh\n"+envUploadPackStall+"=1 "+envUploadPackProxy+"='"+real+"' exec '"+self+"' -test.run='^$' \"$@\"\n"), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(old time.Duration) { headPeekTimeout = old }(headPeekTimeout)
	headPeekTimeout = 2 * time.Second
	c := NewGoGitClient()
	check := func(ctx context.Context) (time.Duration, error) {
		start := time.Now()
		done := make(chan error, 1)
		go func() {
			_, err := c.SameContentAsHead(ctx, ours, "origin", "kardinal/b/prod", TokenAuth(""), head)
			done <- err
		}()
		select {
		case err := <-done:
			return time.Since(start), err
		case <-time.After(30 * time.Second):
			t.Fatal("the check did not return in 30s: the peek has no deadline")
			return 0, nil
		}
	}
	took, err := check(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not send the head's commit within 2s")
	assert.Less(t, took, 15*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	took, err = check(ctx)
	require.Error(t, err, "the caller's sooner deadline")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the caller's deadline, as a context error")
	assert.NotContains(t, err.Error(), "within 2s", "the caller's deadline ended it, not the peek's")
	assert.Less(t, took, 2*time.Second)
}

// packObjectHeader is a packfile object header: type typ, inflated size.
func packObjectHeader(typ byte, size int64) []byte {
	b := []byte{typ<<4 | byte(size&0x0f)}
	size >>= 4
	for size > 0 {
		b[len(b)-1] |= 0x80
		b = append(b, byte(size&0x7f))
		size >>= 7
	}
	return b
}

// TestScanForCommit_Bound: a pack whose objects before the commit are
// declared past the bound (one object, or all of them together), or a
// stream that ends at the read bound, is errHeadTooLarge: refused before
// anything is inflated, so a server that sends the tree first cannot make
// the check read it.
func TestScanForCommit_Bound(t *testing.T) {
	const limit = 1 << 20
	pack := func(objects ...[]byte) *io.LimitedReader {
		b := []byte("PACK\x00\x00\x00\x02")
		b = binary.BigEndian.AppendUint32(b, uint32(len(objects)))
		for _, o := range objects {
			b = append(b, o...)
		}
		return &io.LimitedReader{R: bytes.NewReader(b), N: limit}
	}
	want := plumbing.NewHash("1111111111111111111111111111111111111111")
	_, err := scanForCommit(pack(packObjectHeader(3, 300<<20)), want, limit)
	require.ErrorIs(t, err, errHeadTooLarge, "a 300 MiB blob first")
	_, err = scanForCommit(pack(packObjectHeader(2, limit+1)), want, limit)
	require.ErrorIs(t, err, errHeadTooLarge, "a tree past the bound first")
	body := make([]byte, 64<<10)
	_, err = rand.Read(body)
	require.NoError(t, err)
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, err = zw.Write(body)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	truncated := pack(append(packObjectHeader(3, int64(len(body))), z.Bytes()...))
	truncated.N = 1 << 10 // the bound falls inside the blob
	_, err = scanForCommit(truncated, want, limit)
	require.ErrorIs(t, err, errHeadTooLarge, "the read bound reached")
}
