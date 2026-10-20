// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestFairLock_HeadCheckedAgainUnderTheFlock (#2282 QA): a release ticket
// that comes between a waiter's head check and its turn goes first: the
// turn checks the head again under the flock. The gap is widened to 30ms,
// which made the stale head take the lock every time without the check.
func TestFairLock_HeadCheckedAgainUnderTheFlock(t *testing.T) {
	afterHeadCheck = func() { time.Sleep(30 * time.Millisecond) }
	defer func() { afterHeadCheck = nil }()
	for i := 0; i < 3; i++ {
		dir := filepath.Join(t.TempDir(), "LOADLOCK")
		got := order(t, dir, lockAs(dir, "holder", classNormal),
			lockAs(dir, "normal-1", classNormal), lockAs(dir, "normal-2", classNormal), lockAs(dir, "release", classRelease))
		assert.Equal(t, []string{"release", "normal-1", "normal-2"}, got, "round %d", i)
	}
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

// TestCountingLock_WhatCannotBeReadIsTheSafeAnswer (#2242 QA): a load that
// cannot be read or parsed is a busy host's, and a holder record that is
// there but cannot be read counts and holds the lock alone.
func TestCountingLock_WhatCannotBeReadIsTheSafeAnswer(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{"ok": "3.50 2.00 1.00 1/100 42\n", "empty": "", "garbage": "load? 1 2\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644))
	}
	assert.InDelta(t, 3.5, loadFrom(filepath.Join(dir, "ok")), 0.001)
	for _, name := range []string{"missing", "empty", "garbage"} {
		assert.Equal(t, 1, capacity(loadFrom(filepath.Join(dir, name))), "%s: a busy host's one holder", name)
	}

	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	lock := filepath.Join(dir, "LOADLOCK")
	slot := filepath.Join(slotsDir(lock), "1")
	require.NoError(t, os.MkdirAll(slot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(slot, "owner"), []byte(fmt.Sprintf("live pid %d now\n", os.Getpid())), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(slot, "owner"), 0o644) })
	records, exclusive := holders(lock)
	require.False(t, exclusive)
	require.Len(t, records, 1, "an unreadable record counts")
	assert.Contains(t, records[0], "exclusive=1", "and holds the lock alone")
	w := idleAs(lock, "w", classNormal)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, w.Lock(ctx), context.DeadlineExceeded, "no second holder beside a record it cannot read, on an idle host")
	assert.DirExists(t, slot, "nor is that slot reclaimed")
}

// TestCountingLock_AnyWaiterReclaims (#2242 QA): a waiter that is not the
// head removes a dead holder's slot and the view it leaves, so an older
// client at the head (which cannot judge the view's holders) is not wedged;
// it takes the lock only at the head.
func TestCountingLock_AnyWaiterReclaims(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	gone := exec.Command("true")
	require.NoError(t, gone.Run())
	slot := filepath.Join(slotsDir(dir), "1")
	require.NoError(t, os.MkdirAll(slot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(slot, "owner"), []byte(fmt.Sprintf("dead pid %d 2026-10-20T00:00:00Z\n", gone.Process.Pid)), 0o644))
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, viewMarker), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "owner"), []byte(fmt.Sprintf("dead pid:%d\n", gone.Process.Pid)), 0o644))
	// An older client's ticket at the head: a live process (this one) that
	// never reclaims.
	q := queueDir(dir)
	require.NoError(t, os.MkdirAll(q, 0o755))
	older := filepath.Join(q, fmt.Sprintf("1-%019d-%d", 1, os.Getpid()))
	require.NoError(t, os.WriteFile(older, []byte(fmt.Sprintf("older pid %d now %s\n", os.Getpid(), selfRecord(hostProc{}))), 0o644))

	w := idleAs(dir, "w", classNormal)
	w.lockRetry = 20 * time.Millisecond
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { done <- w.Lock(ctx) }()
	require.Eventually(t, func() bool {
		_, serr := os.Stat(slot)
		_, verr := os.Stat(dir)
		return errors.Is(serr, os.ErrNotExist) && errors.Is(verr, os.ErrNotExist)
	}, 10*time.Second, 20*time.Millisecond, "the waiter behind the head removed the dead slot and its view")
	select {
	case err := <-done:
		t.Fatalf("it took the lock ahead of the head: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	require.NoError(t, os.Remove(older))
	require.NoError(t, <-done)
	w.Unlock()
}

// TestFairLock_OwnerReadAgainBeforeTheRemoval (#2242 QA): an older client's
// lock judged dead is removed only while its owner is still the dead one: a
// LOCK another older client made meanwhile stays.
func TestFairLock_OwnerReadAgainBeforeTheRemoval(t *testing.T) {
	gone := exec.Command("true")
	require.NoError(t, gone.Run())
	for _, tc := range []struct {
		name    string
		swap    bool
		removed bool
		vanish  bool
	}{{"the owner is the same: removed", false, true, false}, {"another LOCK meanwhile: it stays", true, false, false},
		// #2242 QA-3: as the shell does. An owner judged dead that is gone
		// since is a new LOCK's being written: it stays.
		{"the owner gone since: it stays", false, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "LOADLOCK")
			require.NoError(t, os.Mkdir(dir, 0o755))
			owner := filepath.Join(dir, "owner")
			require.NoError(t, os.WriteFile(owner, []byte(fmt.Sprintf("older pid %d 2026-10-20T00:00:00Z\n", gone.Process.Pid)), 0o644))
			live := fmt.Sprintf("another older pid %d now %s\n", os.Getpid(), selfRecord(hostProc{}))
			beforeReclaimRemove = func() {
				if tc.swap {
					require.NoError(t, os.WriteFile(owner, []byte(live), 0o644))
				}
				if tc.vanish {
					require.NoError(t, os.Remove(owner))
				}
			}
			defer func() { beforeReclaimRemove = nil }()
			(&fairLock{lock: dir, log: func(string, ...any) {}, proc: hostProc{}}).reclaim()
			switch {
			case tc.removed:
				assert.NoDirExists(t, dir)
			case tc.vanish:
				assert.DirExists(t, dir, "a LOCK whose owner went since is left alone")
				// One that stays without an owner goes by the orphanAge rule.
				beforeReclaimRemove = nil
				old := time.Now().Add(-2 * orphanAge)
				require.NoError(t, os.Chtimes(dir, old, old))
				(&fairLock{lock: dir, log: func(string, ...any) {}, proc: hostProc{}}).reclaim()
				assert.NoDirExists(t, dir, "an ownerless LOCK goes after orphanAge")
			default:
				assert.Equal(t, live, readOwner(t, dir), "the new LOCK is left alone")
			}
		})
	}
}

// TestFairLock_QueueNeverWedges (#2282 QA): a ticket that cannot be read is
// judged by its name's pid and time; one untouched for staleBeat (a stopped,
// hung or stray waiter's, live pid or not) is removed, but never the
// reaper's own; waiters touch theirs on every poll; a normal ticket that
// has waited agePromote sorts as a release one.
func TestFairLock_QueueNeverWedges(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	gone := exec.Command("true")
	require.NoError(t, gone.Run())
	q := t.TempDir()
	put := func(class string, at time.Time, pid int, text string, mode os.FileMode, touched time.Time) string {
		p := filepath.Join(q, fmt.Sprintf("%s-%019d-%d", class, at.UnixNano(), pid))
		require.NoError(t, os.WriteFile(p, []byte(text), 0o644))
		require.NoError(t, os.Chtimes(p, touched, touched))
		require.NoError(t, os.Chmod(p, mode))
		return p
	}
	now, old := time.Now(), time.Now().Add(-time.Hour)
	live := fmt.Sprintf("w pid %d now %s beat=1\n", os.Getpid(), selfRecord(hostProc{}))
	unreadableGone := put(classNormal, now.Add(-time.Minute), gone.Process.Pid, "x", 0o000, now)
	unreadableLive := put(classNormal, now.Add(-time.Second), os.Getpid(), "x", 0o000, now)
	staleLive := put(classNormal, now.Add(-2*time.Minute), os.Getpid(), live, 0o644, old)
	strayPid1 := put(classRelease, now.Add(-3*time.Minute), 1, "stray\n", 0o644, old)
	ownStale := put(classNormal, now.Add(-4*time.Minute), os.Getpid(), live, 0o644, old)
	fresh := put(classNormal, now.Add(-5*time.Minute), os.Getpid(), live, 0o644, now)
	var logs []string
	reapTickets(hostProc{}, q, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }, nil, filepath.Base(ownStale))
	assert.NoFileExists(t, unreadableGone, "an unreadable ticket of a pid that is gone")
	assert.FileExists(t, unreadableLive, "an unreadable ticket of a live pid, touched now")
	assert.NoFileExists(t, staleLive, "a live pid's ticket untouched for an hour")
	assert.NoFileExists(t, strayPid1, "a stray ticket naming pid 1, untouched")
	assert.FileExists(t, ownStale, "never the reaper's own")
	assert.FileExists(t, fresh)
	assert.Contains(t, strings.Join(logs, "\n"), "untouched for")

	// Aging: the fresh normal ticket is 5 min old; one 3h old sorts before
	// a release ticket of a minute ago.
	require.NoError(t, os.Chmod(unreadableLive, 0o644))
	for _, p := range []string{unreadableLive, ownStale, fresh} {
		require.NoError(t, os.Remove(p))
	}
	aged := put(classNormal, now.Add(-3*time.Hour), os.Getpid(), live, 0o644, now)
	put(classRelease, now.Add(-time.Minute), os.Getpid(), live, 0o644, now)
	assert.Equal(t, filepath.Base(aged), firstTicket(q), "a normal ticket that waited 2h goes first")

	// A waiter touches its ticket on every poll: with staleBeat short, it
	// keeps its place behind a live holder while a stopped one's goes.
	staleBeat = 300 * time.Millisecond
	defer func() { staleBeat = 10 * time.Minute }()
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	h := lockAs(dir, fmt.Sprintf("h pid %d now", os.Getpid()), classNormal)
	require.NoError(t, h.Lock(context.Background()))
	require.NoError(t, os.MkdirAll(queueDir(dir), 0o755))
	stopped := filepath.Join(queueDir(dir), fmt.Sprintf("1-%019d-%d", now.Add(-time.Minute).UnixNano(), os.Getpid()))
	require.NoError(t, os.WriteFile(stopped, []byte(live), 0o644))
	require.NoError(t, os.Chtimes(stopped, old, old))
	w := lockAs(dir, fmt.Sprintf("w pid %d now", os.Getpid()), classNormal)
	w.lockRetry = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- w.Lock(context.Background()) }()
	require.Eventually(t, func() bool { _, err := os.Stat(stopped); return errors.Is(err, os.ErrNotExist) }, 5*time.Second, 20*time.Millisecond, "the stopped waiter's ticket goes")
	time.Sleep(time.Second) // longer than staleBeat: the waiter's own must stay
	entries, err := os.ReadDir(queueDir(dir))
	require.NoError(t, err)
	var mine []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			mine = append(mine, e.Name())
			info, err := e.Info()
			require.NoError(t, err)
			assert.Less(t, time.Since(info.ModTime()), staleBeat, "touched on every poll")
		}
	}
	assert.Len(t, mine, 1, "the waiter's own ticket stays")
	h.Unlock()
	require.NoError(t, <-done)
	w.Unlock()
}

// TestWithLockFlock_WaitCap (#2282 QA): a reclaim flock held by another
// (a waiter stopped or hung while it held it) is waited for flockWait at
// most, with the cause; the retries share one pending attempt (no goroutine
// or fd per retry); once it is let go, the next call takes it.
func TestWithLockFlock_WaitCap(t *testing.T) {
	q := t.TempDir()
	other, err := os.OpenFile(filepath.Join(q, ".reclaim"), os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	defer func() { _ = other.Close() }()
	require.NoError(t, unix.Flock(int(other.Fd()), unix.LOCK_EX))
	assert.Equal(t, time.Minute, flockWait, "a minute at most, as hack/loadlock.sh's and local-live.sh's")
	flockWait = 200 * time.Millisecond
	defer func() { flockWait = time.Minute }()
	ran := 0
	var fl flocker // one waiter, retrying
	start := time.Now()
	res := make(chan error, 1)
	go func() { res <- fl.with(q, func() error { ran++; return nil }) }()
	select {
	case err = <-res:
	case <-time.After(5 * time.Second):
		t.Fatal("no cap: it waits for the flock as long as it is held")
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no flock on "+filepath.Join(q, ".reclaim")+" in 200ms: a waiter stopped (Ctrl-Z) or hung")
	assert.Less(t, time.Since(start), 2*time.Second, "the cap, not a hang")
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		require.Error(t, fl.with(q, func() error { ran++; return nil }))
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), before, "one pending attempt, not one per retry")
	assert.Zero(t, ran, "nothing ran without the flock")
	require.NoError(t, unix.Flock(int(other.Fd()), unix.LOCK_UN))
	require.NoError(t, fl.with(q, func() error { ran++; return nil }))
	assert.Equal(t, 1, ran)
}

// TestRecordDead_SharedTable (#2242 QA-3): fairlock.go judges
// test/hack/testdata/lock-records.tsv as hack/loadlock.sh does (test/hack's
// TestLoadLock_RecordsTable): one table, so the clients cannot drift.
func TestRecordDead_SharedTable(t *testing.T) {
	gone := exec.Command("true")
	require.NoError(t, gone.Run())
	start, ok := hostProc{}.Start(os.Getpid())
	require.True(t, ok)
	repl := strings.NewReplacer("{LIVE}", strconv.Itoa(os.Getpid()), "{START}", strconv.FormatUint(start, 10),
		"{BOOT}", hostProc{}.BootID(), "{GONE}", strconv.Itoa(gone.Process.Pid), "{NOW}", time.Now().UTC().Format(time.RFC3339), "{NL}", "\n")
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "hack", "testdata", "lock-records.tsv"))
	require.NoError(t, err)
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		require.Len(t, f, 3, "%q", line)
		record := repl.Replace(f[2])
		assert.Equal(t, f[0] == "dead", recordDead(hostProc{}, record, time.Time{}), "%s: %q", f[0], record)
		n++
	}
	assert.Greater(t, n, 15)
}

// TestOwnerProblem (#2242 QA-3): an -owner that holds the word pid, a
// field's key or a control character is refused.
func TestOwnerProblem(t *testing.T) {
	for _, ok := range []string{"release-gate", "ENGINEER-4 #2107 run 4 (rapid)", "QA-2 probe"} {
		assert.NoError(t, ownerProblem(ok), ok)
	}
	for _, bad := range []string{"rerun pid 999999 x", "pid", "x-pid 9", "boot=abc x", "a start=1", "t ticket=x", "exclusive=1", "priority=release", "beat=1", "tab\there", "nl\nhere"} {
		assert.Error(t, ownerProblem(bad), "%q", bad)
	}
}

// TestDeadDir_EmptyOwner (#2242 QA-3): an empty owner file is no owner: one
// fresh (being written) stays, one older than orphanAge is reclaimed.
func TestDeadDir_EmptyOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	slot := filepath.Join(slotsDir(dir), "1")
	require.NoError(t, os.MkdirAll(slot, 0o755))
	owner := filepath.Join(slot, "owner")
	require.NoError(t, os.WriteFile(owner, nil, 0o644))
	f := &fairLock{lock: dir, log: func(string, ...any) {}, proc: hostProc{}}
	ok, _ := f.deadDir(slot)
	assert.False(t, ok, "an empty owner file just written: a holder writing it")
	old := time.Now().Add(-2 * orphanAge)
	require.NoError(t, os.Chtimes(owner, old, old))
	ok, _ = f.deadDir(slot)
	assert.False(t, ok, "the slot directory is new: still not old")
	require.NoError(t, os.Chtimes(slot, old, old))
	now := time.Now() // #2318 QA-2: the newer of the two mtimes counts
	require.NoError(t, os.Chtimes(owner, now, now))
	ok, _ = f.deadDir(slot)
	assert.False(t, ok, "the owner file is new (being written), though its directory is old")
	require.NoError(t, os.Chtimes(owner, old, old))
	ok, why := f.deadDir(slot)
	assert.True(t, ok, "empty and older than orphanAge: no owner")
	assert.Contains(t, why, "which has had no owner for")
	f.reclaim()
	assert.NoDirExists(t, slot, "reclaimed, not wedged forever")
}
