// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// fakeProc is a /proc: the boot id, and the start time of each running pid.
type fakeProc struct {
	boot   string
	starts map[int]uint64
	booted time.Time
}

func (p fakeProc) BootID() string { return p.boot }
func (p fakeProc) Start(pid int) (uint64, bool) {
	s, ok := p.starts[pid]
	return s, ok
}
func (p fakeProc) BootTime() time.Time { return p.booted }

// TestRecordDead (#2202 QA): a record is dead when its boot id is another
// boot's, its pid is gone, or its pid's start time is not the recorded one;
// an older client's record (no keys) when its pid is gone or started after
// the record's time.
func TestRecordDead(t *testing.T) {
	booted := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	p := fakeProc{boot: "b-now", booted: booted, starts: map[int]uint64{
		42: 1000,           // started 10s after boot
		43: 100 * 3600 * 2, // started 2h after boot
	}}
	for _, tc := range []struct {
		name, record string
		at           time.Time
		dead         bool
	}{
		{"this boot, running, same start", "x pid 42 2026-10-19T00:00:10Z boot=b-now start=1000", time.Time{}, false},
		{"another boot's record", "x pid 42 2026-10-17T20:00:00Z boot=b-old start=1000", time.Time{}, true},
		{"the pid is gone", "x pid 44 2026-10-19T00:00:10Z boot=b-now start=1000", time.Time{}, true},
		{"the pid was reused: another start time", "x pid 42 2026-10-19T00:00:10Z boot=b-now start=999", time.Time{}, true},
		{"an older client's record, its process running", "x pid 42 2026-10-19T00:00:30Z", time.Time{}, false},
		{"an older client's record, its pid reused later", "x pid 43 2026-10-19T00:30:00Z", time.Time{}, true},
		{"an older client's ticket, by its name's time", "ghost pid 43", booted.Add(time.Hour), true},
		{"no pid: never dead", "a person, by hand", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.dead, recordDead(p, tc.record, tc.at))
		})
	}
	start, ok := statStart("1234 (a (weird) name) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 777 19 20")
	require.True(t, ok)
	assert.Equal(t, uint64(777), start, "field 22, counted after the command's last ')'")
}

// TestFairLock_PidReuseDoesNotWedge (#2202 QA): after a reboot the pids in a
// stale lock and a stale ticket belong to other, live processes. The other
// boot id makes both dead: the lock is reclaimed and the ticket removed.
func TestFairLock_PidReuseDoesNotWedge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	live := os.Getpid() // a live pid, as a reused one is
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "owner"),
		[]byte(fmt.Sprintf("before the reboot pid %d 2026-10-17T20:00:00Z boot=old start=5\n", live)), 0o644))
	require.NoError(t, os.MkdirAll(queueDir(dir), 0o755))
	ghost := fmt.Sprintf("%s-%019d-%d", classRelease, 1, live)
	require.NoError(t, os.WriteFile(filepath.Join(queueDir(dir), ghost),
		[]byte(fmt.Sprintf("waiter pid %d 2026-10-17T20:00:00Z boot=old start=5\n", live)), 0o644))
	host := hostProc{}
	w := lockAs(dir, fmt.Sprintf("me pid %d now", os.Getpid()), classNormal)
	w.proc = fakeProc{boot: host.BootID(), booted: host.BootTime(), starts: map[int]uint64{live: func() uint64 {
		s, _ := host.Start(live)
		return s
	}()}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, w.Lock(ctx), "neither the stale lock nor the stale ticket blocks")
	assert.NoFileExists(t, filepath.Join(queueDir(dir), ghost))
	w.Unlock()
}

// TestFairLock_MissingTicketIsQueuedAgain (#2202 QA): a waiter whose ticket
// is removed (the queue cleaned by hand) writes it again under its name and
// gets the lock in its turn.
func TestFairLock_MissingTicketIsQueuedAgain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	holder := lockAs(dir, "holder", classNormal)
	require.NoError(t, holder.Lock(context.Background()))
	var logs []string
	var mu sync.Mutex
	w := lockAs(dir, "waiter", classNormal)
	w.log = func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }
	got := make(chan error, 1)
	go func() { got <- w.Lock(context.Background()) }()
	require.Eventually(t, func() bool { return queued(dir) == 1 }, 5*time.Second, time.Millisecond)
	require.NoError(t, os.Remove(filepath.Join(queueDir(dir), firstTicket(queueDir(dir)))))
	require.Eventually(t, func() bool { return queued(dir) == 1 }, 15*time.Second, 5*time.Millisecond, "queued again")
	holder.Unlock()
	select {
	case err := <-got:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("the waiter whose ticket was removed never got the lock")
	}
	w.Unlock()
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, strings.Join(logs, "\n"), "queued it again in its place")
}

// TestFairLock_ConcurrentReclaimIsExclusive (#2202 QA's probe): 8 waiters
// wake together on a lock whose holder is gone. Only the head waiter
// reclaims it, under the reclaim flock, so no two ever hold the lock, and
// none waits forever. FAIRLOCK_STRESS sets the rounds (default 300).
func TestFairLock_ConcurrentReclaimIsExclusive(t *testing.T) {
	rounds := 300
	if v := os.Getenv("FAIRLOCK_STRESS"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		rounds = n
	}
	dead := deadPid(t)
	var inside, overlaps, wedged atomic.Int32
	for r := 0; r < rounds; r++ {
		dir := filepath.Join(t.TempDir(), "LOADLOCK")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "owner"), []byte(fmt.Sprintf("gone pid %d 2026-10-19T00:00:00Z\n", dead)), 0o644))
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m := lockAs(dir, fmt.Sprintf("w%d pid %d now", i, os.Getpid()), classNormal)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if err := m.Lock(ctx); err != nil {
					wedged.Add(1)
					return
				}
				if inside.Add(1) > 1 {
					overlaps.Add(1)
				}
				time.Sleep(200 * time.Microsecond)
				inside.Add(-1)
				m.Unlock()
			}()
		}
		wg.Wait()
		require.NoDirExists(t, dir, "round %d: the lock is released", r)
	}
	assert.Zero(t, overlaps.Load(), "two holders at once")
	assert.Zero(t, wedged.Load(), "waiters that never got the lock")
}
