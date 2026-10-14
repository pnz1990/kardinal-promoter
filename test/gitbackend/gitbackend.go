// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package gitbackend serves bare git repositories over HTTP for tests, with
// git's own smart HTTP server (git http-backend), so that no backend outlives
// its test (#1875). A backend whose test ended mid-push (a failure, a go
// test -timeout) was left reading a closed pipe at 99% CPU for days. Each
// backend runs under timeout(1), which puts it in a process group of its own
// and kills the group after Bound however the test ends; the test's cleanup
// kills any backend still running when the test is over, and fails the test.
package gitbackend

import (
	"bytes"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Bound is the longest a test's git http-backend may run.
const Bound = 150 * time.Second

// Serve serves the bare repositories under root (GIT_PROJECT_ROOT), with
// pushes allowed to REMOTE_USER kardinal. wrap, when not nil, wraps the
// backend's handler (to throttle or authenticate requests). It skips the
// test without git or timeout(1).
func Serve(t testing.TB, root string, wrap func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git http-backend needs the git binary")
	}
	timeoutPath, err := exec.LookPath("timeout")
	if err != nil {
		t.Skip("git http-backend is bounded with timeout(1)")
	}
	var h http.Handler = &cgi.Handler{
		Path: timeoutPath, Args: []string{"-s", "KILL", strconv.Itoa(int(Bound.Seconds())), gitPath, "http-backend"},
		Dir: root, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=kardinal"},
	}
	if wrap != nil {
		h = wrap(h)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// Registered after srv.Close, so it runs first: Close waits for the
	// requests a stuck backend holds.
	t.Cleanup(func() {
		if left := Reap(root, 10*time.Second); len(left) > 0 {
			t.Errorf("git http-backend outlived the test: killed process groups %v", left)
		}
	})
	return srv
}

// Reap waits up to grace for this process's children working in root (the
// timeout(1) processes the backends run under) to exit, then kills the
// process groups of those left, and returns their ids.
func Reap(root string, grace time.Duration) []int {
	deadline := time.Now().Add(grace)
	for {
		left := childrenIn(root)
		if len(left) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			for _, pid := range left {
				_ = syscall.Kill(-pid, syscall.SIGKILL) // timeout(1) leads its own group
			}
			return left
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// childrenIn lists the live child processes of this process whose working
// directory is dir (Linux /proc; none elsewhere).
func childrenIn(dir string) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	self := strconv.Itoa(os.Getpid())
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		// "pid (comm) state ppid ...": comm may hold spaces, so split after ")".
		fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		if len(fields) < 2 || fields[0] == "Z" || fields[1] != self {
			continue
		}
		if cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd")); err == nil && cwd == dir {
			out = append(out, pid)
		}
	}
	return out
}
