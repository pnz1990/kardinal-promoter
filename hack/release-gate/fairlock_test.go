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
	"golang.org/x/sys/unix"
)

// busy is a load at which one holder at a time holds the lock.
func busy() float64 { return 2 * loadForTwo }

// lockAs is a gate on a busy host: the lock admits one holder.
func lockAs(dir, owner, class string) *machine {
	return &machine{lockDir: dir, owner: owner, lockClass: class, lockRetry: time.Second, log: func(string, ...any) {}, lockLoad: busy}
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
		45: 0,              // runs; its start time cannot be read now
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
		{"start unknown: boot id and pid only", "x pid 42 2026-10-19T00:00:10Z boot=b-now start=unknown", time.Time{}, false},
		{"start unknown, another boot", "x pid 42 2026-10-19T00:00:10Z boot=b-old start=unknown", time.Time{}, true},
		// #2242: a read that fails on a busy host never makes a live holder
		// look gone.
		{"its start time cannot be read now", "x pid 45 2026-10-19T00:00:10Z boot=b-now start=1000", time.Time{}, false},
		{"an older client's, its start time cannot be read now", "x pid 45 2026-10-19T00:00:10Z", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.dead, recordDead(p, tc.record, tc.at))
		})
	}
	noBoot := p
	noBoot.boot = ""
	assert.False(t, recordDead(noBoot, "x pid 42 2026-10-19T00:00:10Z boot=b-old start=1000", time.Time{}),
		"this boot's id cannot be read: no record is judged another boot's")
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

// TestFairLock_QueueDirRemoved (#2226 QA): the queue directory removed by
// hand (rm -rf LOCK.queue) is made again, the waiter's ticket with it, and
// the waiter gets the lock.
func TestFairLock_QueueDirRemoved(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	holder := lockAs(dir, "holder", classNormal)
	require.NoError(t, holder.Lock(context.Background()))
	w := lockAs(dir, "waiter", classNormal)
	got := make(chan error, 1)
	go func() { got <- w.Lock(context.Background()) }()
	require.Eventually(t, func() bool { return queued(dir) == 1 }, 5*time.Second, time.Millisecond)
	// Removed at once, as rm -rf ends; the waiter may queue again before
	// RemoveAll of the old directory ends.
	require.NoError(t, os.Rename(queueDir(dir), queueDir(dir)+".gone"))
	require.NoError(t, os.RemoveAll(queueDir(dir)+".gone"))
	require.Eventually(t, func() bool { return queued(dir) == 1 }, 15*time.Second, 5*time.Millisecond, "queued again")
	holder.Unlock()
	select {
	case err := <-got:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("never got the lock")
	}
	w.Unlock()
}

// TestFairLock_OwnerWriteFailureGivesUp (#2226 QA): when the owner record
// cannot be written, the lock is given up, not held without an owner.
func TestFairLock_OwnerWriteFailureGivesUp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	require.NoError(t, os.MkdirAll(queueDir(dir), 0o755))
	require.NoError(t, os.MkdirAll(slotsDir(dir), 0o755))
	old := unix.Umask(0o277) // the slot directory mkdir makes is read-only
	defer unix.Umask(old)
	w := lockAs(dir, "w", classNormal)
	err := w.Lock(context.Background())
	unix.Umask(old)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write owner")
	assert.NoDirExists(t, dir, "no ownerless lock is left")
	assert.NoDirExists(t, filepath.Join(slotsDir(dir), "1"), "nor an ownerless slot")
}

// TestFairLock_OldTicketWarned (#2226 QA): a ticket from before #2226 (no
// boot id) is named in a warning: its reaper can move the lock.
func TestFairLock_OldTicketWarned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	holder := lockAs(dir, "holder", classNormal)
	require.NoError(t, holder.Lock(context.Background()))
	old := fmt.Sprintf("1-%019d-%d", time.Now().Add(time.Hour).UnixNano(), os.Getpid())
	require.NoError(t, os.WriteFile(filepath.Join(queueDir(dir), old),
		[]byte(fmt.Sprintf("ENGINEER-9 old worktree pid %d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))), 0o644))
	var mu sync.Mutex
	var logs []string
	w := lockAs(dir, "w", classNormal)
	w.log = func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = w.Lock(ctx)
	holder.Unlock()
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(logs, "\n")
	assert.Contains(t, joined, "warning: ticket "+old)
	assert.Contains(t, joined, "ENGINEER-9 old worktree")
	assert.Equal(t, 1, strings.Count(joined, "warning: ticket "+old), "warned once")
}

// idleAs is a gate on an idle host: the lock admits two holders.
func idleAs(dir, owner, class string) *machine {
	m := lockAs(dir, owner, class)
	m.lockLoad = func() float64 { return 0 }
	return m
}

// viewLines is LOCK/owner's holder records.
func viewLines(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "owner"))
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// TestCountingLock_TwoHoldersWhenIdle: under loadForTwo two hold the lock at
// once, a third waits, LOCK/owner lists both holders, and the lock is gone
// when the last lets go.
func TestCountingLock_TwoHoldersWhenIdle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	a, b, c := idleAs(dir, "a", classNormal), idleAs(dir, "b", classNormal), idleAs(dir, "c", classNormal)
	require.NoError(t, a.Lock(context.Background()))
	require.NoError(t, b.Lock(context.Background()), "a second holder while the host is idle")
	lines := viewLines(t, dir)
	require.Len(t, lines, 2, "LOCK/owner lists every holder")
	assert.True(t, strings.HasPrefix(lines[0], "a ") && strings.HasPrefix(lines[1], "b "), "%v", lines)

	got := make(chan error, 1)
	go func() { got <- c.Lock(context.Background()) }()
	select {
	case <-got:
		t.Fatal("a third holder")
	case <-time.After(300 * time.Millisecond):
	}
	a.Unlock()
	require.NoError(t, <-got, "the third gets a's slot")
	assert.Len(t, viewLines(t, dir), 2)
	b.Unlock()
	c.Unlock()
	assert.NoDirExists(t, dir, "no holder, no lock")
}

// TestCountingLock_OneHolderWhenBusy: at or above loadForTwo the lock admits
// one holder.
func TestCountingLock_OneHolderWhenBusy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	load := loadForTwo
	a := idleAs(dir, "a", classNormal)
	b := idleAs(dir, "b", classNormal)
	b.lockLoad = func() float64 { return load }
	require.NoError(t, a.Lock(context.Background()))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, b.Lock(ctx), context.DeadlineExceeded, "a second holder at load %v", load)
	a.Unlock()
	require.NoError(t, b.Lock(context.Background()))
	b.Unlock()
}

// TestCountingLock_ReleaseFirstForAFreedSlot: with both slots held, a
// release gate's ticket gets the first slot freed, before normal tickets
// queued ahead of it.
func TestCountingLock_ReleaseFirstForAFreedSlot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	h1, h2 := idleAs(dir, "h1", classNormal), idleAs(dir, "h2", classNormal)
	require.NoError(t, h1.Lock(context.Background()))
	require.NoError(t, h2.Lock(context.Background()))
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	for i, w := range []*machine{idleAs(dir, "n1", classNormal), idleAs(dir, "n2", classNormal), idleAs(dir, "rel", classRelease)} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, w.Lock(context.Background()))
			mu.Lock()
			order = append(order, w.owner)
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			w.Unlock()
		}()
		require.Eventually(t, func() bool { return queued(dir) == i+1 }, 5*time.Second, time.Millisecond)
	}
	// h2 keeps its slot: the one h1 frees passes from waiter to waiter.
	h1.Unlock()
	wg.Wait()
	assert.Equal(t, []string{"rel", "n1", "n2"}, order, "the freed slot goes to the release gate first")
	h2.Unlock()
}

// TestCountingLock_OlderExclusiveLockIsFull: a LOCK an older client made
// (no view marker) is exclusive: an idle host's counting waiter still waits.
func TestCountingLock_OlderExclusiveLockIsFull(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "owner"), []byte(fmt.Sprintf("older client pid %d now\n", os.Getpid())), 0o644))
	w := idleAs(dir, "w", classNormal)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, w.Lock(ctx), context.DeadlineExceeded)
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, w.Lock(context.Background()))
	w.Unlock()
}

// TestCountingLock_DeadSlotsReclaimed: slots whose holders are gone (pid
// gone, another boot) are reclaimed, and the view is rebuilt without them.
func TestCountingLock_DeadSlotsReclaimed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	for n, owner := range map[string]string{
		"1": fmt.Sprintf("gone pid %d 2026-10-19T00:00:00Z", deadPid(t)),
		"2": fmt.Sprintf("before the reboot pid %d 2026-10-17T20:00:00Z boot=not-this-boot start=5", os.Getpid()),
	} {
		slot := filepath.Join(slotsDir(dir), n)
		require.NoError(t, os.MkdirAll(slot, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(slot, "owner"), []byte(owner+"\n"), 0o644))
	}
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, viewMarker), nil, 0o644))
	w := idleAs(dir, fmt.Sprintf("me pid %d now", os.Getpid()), classNormal)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, w.Lock(ctx))
	lines := viewLines(t, dir)
	require.Len(t, lines, 1, "%v", lines)
	assert.True(t, strings.HasPrefix(lines[0], "me pid"))
	w.Unlock()
	assert.NoDirExists(t, dir)
}

// TestCountingLock_StressTwoAtMost: 8 waiters on an idle host, every round
// with dead holders in both slots: never more than two hold it, and none
// waits forever. FAIRLOCK_STRESS sets the rounds (default 300).
func TestCountingLock_StressTwoAtMost(t *testing.T) {
	rounds := 300
	if v := os.Getenv("FAIRLOCK_STRESS"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		rounds = n
	}
	dead := deadPid(t)
	var inside, most, wedged atomic.Int32
	for r := 0; r < rounds; r++ {
		dir := filepath.Join(t.TempDir(), "LOADLOCK")
		for _, n := range []string{"1", "2"} {
			slot := filepath.Join(slotsDir(dir), n)
			require.NoError(t, os.MkdirAll(slot, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(slot, "owner"), []byte(fmt.Sprintf("gone pid %d 2026-10-19T00:00:00Z\n", dead)), 0o644))
		}
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, viewMarker), nil, 0o644))
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m := idleAs(dir, fmt.Sprintf("w%d pid %d now", i, os.Getpid()), classNormal)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if err := m.Lock(ctx); err != nil {
					wedged.Add(1)
					return
				}
				n := inside.Add(1)
				for {
					old := most.Load()
					if n <= old || most.CompareAndSwap(old, n) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				inside.Add(-1)
				m.Unlock()
			}()
		}
		wg.Wait()
		require.NoDirExists(t, dir, "round %d: the lock is released", r)
	}
	assert.LessOrEqual(t, most.Load(), int32(maxHolders), "more than two holders at once")
	assert.Equal(t, int32(maxHolders), most.Load(), "two did hold it at once")
	assert.Zero(t, wedged.Load(), "waiters that never got the lock")
}

// TestCountingLock_OlderClientsAreLeftAlone (#2242 QA): the view lists its
// holders without the "pid N" an older client judges a lock by, and a LOCK
// an older client made after the slots were looked at is never taken over:
// writeView refuses it, and the slot is given back.
func TestCountingLock_OlderClientsAreLeftAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	w := idleAs(dir, fmt.Sprintf("w pid %d 2026-10-20T00:00:00Z", os.Getpid()), classNormal)
	require.NoError(t, w.Lock(context.Background()))
	view := strings.Join(viewLines(t, dir), "\n")
	assert.NotRegexp(t, `\bpid \d+\b`, view, "no pid N in the view")
	assert.Contains(t, view, fmt.Sprintf("pid:%d", os.Getpid()))
	w.Unlock()
	require.NoDirExists(t, dir)

	// An older client's LOCK appears after holders() saw none: a slot is
	// taken, and writeView finds LOCK without the marker.
	slot := filepath.Join(slotsDir(dir), "1")
	require.NoError(t, os.MkdirAll(slot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(slot, "owner"), []byte("new pid 1 now\n"), 0o644))
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "owner"), []byte("older pid 2 now\n"), 0o644))
	assert.ErrorIs(t, writeView(dir), errOlderHolder)
	assert.NoFileExists(t, filepath.Join(dir, viewMarker), "the older client's LOCK is not made a view")
	assert.Equal(t, "older pid 2 now\n", readOwner(t, dir), "nor its owner overwritten")

	// With no slot held, writeView removes LOCK only when it is the view:
	// an older client's LOCK made after holders() looked stays.
	require.NoError(t, os.RemoveAll(slotsDir(dir)))
	assert.ErrorIs(t, writeView(dir), errOlderHolder)
	assert.Equal(t, "older pid 2 now\n", readOwner(t, dir), "an older client's LOCK is never removed")

	// Nor when it appears after holders() looked and found the view
	// (#2242 QA): the removal itself is gated on the marker going.
	require.NoError(t, removeView(dir))
	assert.Equal(t, "older pid 2 now\n", readOwner(t, dir), "removeView leaves a LOCK with no marker")
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, viewMarker), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "owner"), []byte("w pid:1 now\n"), 0o644))
	require.NoError(t, removeView(dir))
	assert.NoDirExists(t, dir, "removeView removes the view")
}

// TestCountingLock_ReclaimLeavesTheViewWhenNothingGoes (#2242 QA): the head
// waiter rewrites the view only when it removed a slot.
func TestCountingLock_ReclaimLeavesTheViewWhenNothingGoes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	w := idleAs(dir, fmt.Sprintf("w pid %d 2026-10-20T00:00:00Z", os.Getpid()), classNormal)
	require.NoError(t, w.Lock(context.Background()))
	defer w.Unlock()
	owner := filepath.Join(dir, "owner")
	require.NoError(t, os.WriteFile(owner, []byte("as it was\n"), 0o644))
	head := &fairLock{lock: dir, log: func(string, ...any) {}, proc: hostProc{}}
	head.reclaim()
	assert.Equal(t, "as it was\n", readOwner(t, dir), "no slot went, so the view is as it was")

	// A dead holder's slot goes, and the view is rewritten.
	dead := filepath.Join(slotsDir(dir), "2")
	require.NoError(t, os.Mkdir(dead, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dead, "owner"), []byte("gone pid 2147480000 2026-10-20T00:00:00Z\n"), 0o644))
	head.reclaim()
	assert.NoDirExists(t, dead)
	assert.Contains(t, readOwner(t, dir), fmt.Sprintf("pid:%d", os.Getpid()))
}

func readOwner(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "owner"))
	require.NoError(t, err)
	return string(b)
}

// TestCountingLock_ExclusiveHoldsAlone (#2244 QA, item 5): a heavy suite
// holds the counting lock alone. On an idle host, while an exclusive holder
// holds it no one else does; an exclusive waiter waits for the lock to be
// free; two exclusive runs (two scale suites) never overlap.
func TestCountingLock_ExclusiveHoldsAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	x, n := idleAs(dir, "x", classNormal), idleAs(dir, "n", classNormal)
	require.NoError(t, x.LockExclusive(context.Background()))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	assert.ErrorIs(t, n.Lock(ctx), context.DeadlineExceeded, "no second holder beside an exclusive one")
	cancel()
	x.Unlock()
	require.NoError(t, n.Lock(context.Background()))
	ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
	assert.ErrorIs(t, x.LockExclusive(ctx), context.DeadlineExceeded, "an exclusive waiter waits for the lock to be free")
	cancel()
	n.Unlock()

	var inside, xInside, violations atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := idleAs(dir, fmt.Sprintf("w%d pid %d now", i, os.Getpid()), classNormal)
			exclusive := i%3 == 0
			lock := m.Lock
			if exclusive {
				lock = m.LockExclusive
			}
			for r := 0; r < 20; r++ {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := lock(ctx)
				cancel()
				if !assert.NoError(t, err) {
					return
				}
				all := inside.Add(1)
				if exclusive {
					if xInside.Add(1) > 1 || all > 1 {
						violations.Add(1)
					}
				} else if xInside.Load() > 0 || all > 2 {
					violations.Add(1)
				}
				time.Sleep(time.Millisecond)
				if exclusive {
					xInside.Add(-1)
				}
				inside.Add(-1)
				m.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, violations.Load(), "an exclusive holder shared the lock")
}

// TestPlan_ScaleStepsHoldTheLockAlone (#2244 QA, item 5): the scale suite's
// steps take the counting lock exclusively; other heavy steps share it.
func TestPlan_ScaleStepsHoldTheLockAlone(t *testing.T) {
	scale := 0
	for _, st := range Plan(fakeRepo(), Options{Count: 2, FuzzTime: "1m", ScaleProfiles: []string{"full", "soak"}, Floor: 35, Newest: 37}) {
		if strings.HasPrefix(st.ID, "scale-") {
			scale++
			assert.True(t, st.Exclusive && st.Heavy, "%s holds the heavy-load lock alone", st.ID)
		} else {
			assert.False(t, st.Exclusive, "%s shares it", st.ID)
		}
	}
	assert.Positive(t, scale)
}
