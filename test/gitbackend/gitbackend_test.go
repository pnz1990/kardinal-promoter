// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitbackend_test

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/gitbackend"
)

func needProc(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// TestServe: a clone through the backend works, and a push the client never
// finishes leaves a backend that Reap kills with its whole process group.
func TestServe(t *testing.T) {
	needProc(t)
	root := t.TempDir()
	git(t, root, "init", "-q", "--bare", "repo.git")
	srv := gitbackend.Serve(t, root, nil)
	git(t, t.TempDir(), "clone", "-q", srv.URL+"/repo.git", "w")
	assert.Empty(t, gitbackend.Reap(root, 5*time.Second), "a finished request leaves no backend")

	// A receive-pack whose body never ends: the backend waits on its stdin.
	body, feed := io.Pipe()
	defer feed.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/repo.git/git-receive-pack", body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	req.ContentLength = 1 << 20 // net/http/cgi needs a length; the body never gets there
	go func() {
		if resp, err := srv.Client().Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	go func() { _, _ = feed.Write([]byte("00")) }()
	var left []int
	require.Eventually(t, func() bool {
		left = gitbackend.Reap(root, 0)
		return len(left) > 0
	}, 10*time.Second, 20*time.Millisecond, "the stuck backend is found and killed")
	assert.Eventually(t, func() bool { return len(gitbackend.Reap(root, 0)) == 0 }, 10*time.Second, 20*time.Millisecond)
	for _, pid := range left {
		assert.Error(t, syscall.Kill(-pid, 0), "process group %d is gone", pid)
	}
}

// TestReap: the whole group dies, a grandchild too (timeout -> git
// http-backend -> git receive-pack), and another directory's process is not
// touched.
func TestReap(t *testing.T) {
	needProc(t)
	timeoutPath, err := exec.LookPath("timeout")
	if err != nil {
		t.Skip("needs timeout(1)")
	}
	dir := t.TempDir()
	cmd := exec.Command(timeoutPath, "-s", "KILL", "300", "sh", "-c", "sleep 300 & echo $! > grandchild; wait")
	cmd.Dir = dir
	require.NoError(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, "grandchild"))
		return err == nil
	}, 10*time.Second, 10*time.Millisecond)
	data, err := os.ReadFile(filepath.Join(dir, "grandchild"))
	require.NoError(t, err)
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)

	assert.Empty(t, gitbackend.Reap(t.TempDir(), 0), "another directory's process is not ours to kill")
	assert.Equal(t, []int{cmd.Process.Pid}, gitbackend.Reap(dir, 100*time.Millisecond))
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the backend's group was not killed")
	}
	assert.Eventually(t, func() bool { return syscall.Kill(grandchild, 0) != nil }, 10*time.Second, 10*time.Millisecond,
		"the grandchild died with the group")
	assert.Empty(t, gitbackend.Reap(dir, 0), "nothing left")
}
