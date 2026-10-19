// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lockAs(dir, owner, class string) *machine {
	return &machine{lockDir: dir, owner: owner, lockClass: class, lockRetry: time.Second, log: func(string, ...any) {}}
}

// order takes the lock with each waiter (started in turn, each queued before
// the next starts) while holder holds it, releases it, and returns the order
// the waiters got it in.
func order(t *testing.T, dir string, holder *machine, waiters ...*machine) []string {
	t.Helper()
	require.NoError(t, holder.Lock(context.Background()))
	var mu sync.Mutex
	var got []string
	var wg sync.WaitGroup
	for i, w := range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, w.Lock(context.Background()))
			mu.Lock()
			got = append(got, w.owner)
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			w.Unlock()
		}()
		// The holder holds the lock: every waiter started so far is queued.
		require.Eventually(t, func() bool { return queued(dir) == i+1 }, 5*time.Second, time.Millisecond)
	}
	holder.Unlock()
	wg.Wait()
	return got
}

func queued(dir string) int {
	entries, _ := os.ReadDir(queueDir(dir))
	n := 0
	for _, e := range entries {
		if e.Name()[0] != '.' {
			n++
		}
	}
	return n
}

// TestFairLock_FIFO: waiters get the lock in the order they queued, however
// they are woken (#fair LOADLOCK: a mutex with pollers let a late poller
// win).
func TestFairLock_FIFO(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	got := order(t, dir, lockAs(dir, "holder", classNormal),
		lockAs(dir, "first", classNormal), lockAs(dir, "second", classNormal), lockAs(dir, "third", classNormal))
	assert.Equal(t, []string{"first", "second", "third"}, got)
	assert.NoDirExists(t, dir)
	assert.Zero(t, queued(dir), "every ticket is gone")
}

// TestFairLock_ReleaseJumpsTheQueue: a release gate's ticket is served
// before the normal ones queued ahead of it.
func TestFairLock_ReleaseJumpsTheQueue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	got := order(t, dir, lockAs(dir, "holder", classNormal),
		lockAs(dir, "normal-1", classNormal), lockAs(dir, "normal-2", classNormal), lockAs(dir, "release", classRelease))
	assert.Equal(t, []string{"release", "normal-1", "normal-2"}, got)
}

// deadPid is the pid of a process that has exited.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	return cmd.Process.Pid
}

// TestFairLock_DeadHolderAndWaiterAreReclaimed: a lock whose owner names a
// pid that is gone is removed, and so is the ticket of a waiter that is
// gone; a lock whose owner names no pid is never taken over.
func TestFairLock_DeadHolderAndWaiterAreReclaimed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	dead := deadPid(t)
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "owner"), []byte(fmt.Sprintf("killed run pid %d 2026-10-17T00:00:00Z\n", dead)), 0o644))
	require.NoError(t, os.MkdirAll(queueDir(dir), 0o755))
	ghost := fmt.Sprintf("%s-%019d-%d", classRelease, 1, dead) // sorts first
	require.NoError(t, os.WriteFile(filepath.Join(queueDir(dir), ghost), []byte("ghost\n"), 0o644))

	w := lockAs(dir, fmt.Sprintf("me pid %d now", os.Getpid()), classNormal)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, w.Lock(ctx))
	owner, err := os.ReadFile(filepath.Join(dir, "owner"))
	require.NoError(t, err)
	assert.Contains(t, string(owner), "me pid")
	assert.NoFileExists(t, filepath.Join(queueDir(dir), ghost))
	w.Unlock()

	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "owner"), []byte("a person, by hand\n"), 0o644))
	ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, w.Lock(ctx), context.DeadlineExceeded, "a lock without a pid is not taken over")
	assert.DirExists(t, dir)
}
