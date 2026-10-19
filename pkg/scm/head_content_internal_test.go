// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
