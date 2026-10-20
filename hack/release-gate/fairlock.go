// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// The host's heavy-load lock is fair. The lock is the directory LOCK with
// LOCK/owner (mkdir is atomic); its waiters queue in LOCK.queue, one ticket
// file each, named
//
//	<class>-<nanoseconds>-<pid>
//
// class 0 is a release gate's (-priority release), 1 everyone else's.
// hack/loadlock.sh follows the same protocol:
//
//   - Only the waiter whose ticket sorts first (release tickets first, then
//     the oldest) takes the lock; every waiter reclaims a lock whose holder
//     is gone (an older client at the head cannot judge the counting lock's
//     holders, #2242 QA). Both happen under flock(2) on LOCK.queue/.reclaim,
//     and a dead holder's lock is removed outright (no rename aside and
//     back: two reclaimers once moved a live holder's lock, #2202 QA).
//   - A record (the owner file, a ticket) is "<text> pid <N> <time>" with
//     key=value pairs appended: boot=<boot id> start=<the process's start
//     time, /proc/<pid>/stat field 22> ticket=<the holder's ticket>. It is
//     dead when its boot id is not this boot's, when the pid is gone, or
//     when the pid's start time is not the recorded one (the pid was
//     reused, as pids are soon after a reboot). A record without those keys
//     (an older client's) is dead when its pid is gone, or started after the
//     record's own time. A lock without a pid is never taken over, unless it
//     has no owner file at all for a minute (a holder that died between its
//     mkdir and its owner write).
//   - A waiter whose ticket is gone (cleaned by hand, or as stale) writes it
//     again under its name, so it keeps its place.
//   - Each waiter touches its ticket on every poll (a heartbeat, beat=1); a
//     ticket untouched for staleBeat (staleOld for an older client's, which
//     touches none) is a stopped, hung or stray waiter's, and any waiter
//     removes it whatever its pid. A ticket that cannot be read is judged by
//     its name's pid and time.
//   - A normal ticket that has waited agePromote sorts as a release one, and
//     -priority release needs $RELEASE_GATE_PRIORITY_TOKEN matching the
//     token file next to the lock (releaseTokenProblem).
//   - A waiter wakes on inotify events in the queue and the lock's
//     directory, or every poll at the latest.
//
// Clients that take the lock without a ticket (worktrees on code from
// before the queue) are not ordered by it: mkdir still excludes them, and a
// waiter logs when the lock is held by one.

// Ticket classes, sorted first to last.
const (
	classRelease = "0"
	classNormal  = "1"
)

// lockClassOf is the ticket class of a -priority value.
func lockClassOf(priority string) string {
	if priority == "release" {
		return classRelease
	}
	return classNormal
}

// queueDir is the directory the waiters of lock queue in.
func queueDir(lock string) string { return lock + ".queue" }

// orphanAge is how long a lock directory may have no owner file before it
// counts as a dead holder's.
const orphanAge = time.Minute

// procFS reads what the dead-record checks need from /proc (tests fake it).
type procFS interface {
	// BootID is this boot's id, or "" when it cannot be read (then no
	// record is judged another boot's).
	BootID() string
	// Start is pid's start time in clock ticks since boot (stat field 22);
	// ok is false when pid does not run. ticks is 0 when pid runs but its
	// start time cannot be read now (it is judged by boot id and pid only,
	// as start=unknown is): a failed read on a busy host must never make a
	// live holder look gone (#2242).
	Start(pid int) (ticks uint64, ok bool)
	// BootTime is when this boot started (/proc/stat btime).
	BootTime() time.Time
}

// hostProc is the real /proc.
type hostProc struct{}

var hostBootID = sync.OnceValue(func() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
})

func (hostProc) BootID() string { return hostBootID() }

func (hostProc) Start(pid int) (uint64, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	switch {
	case errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH):
		return 0, false
	case err != nil:
		return 0, true // runs, as far as this read can tell
	}
	start, ok := statStart(string(b))
	if !ok {
		return 0, true
	}
	return start, true
}

func (hostProc) BootTime() time.Time {
	b, _ := os.ReadFile("/proc/stat")
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "btime" {
			if s, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				return time.Unix(s, 0)
			}
		}
	}
	return time.Time{}
}

// statStart is field 22 of a /proc/<pid>/stat line; the command (field 2)
// is in parentheses and may hold spaces, so fields are counted after the
// last ')'.
func statStart(stat string) (uint64, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(stat[i+1:]) // field 3 onwards
	if len(f) < 20 {
		return 0, false
	}
	v, err := strconv.ParseUint(f[19], 10, 64)
	return v, err == nil
}

// clockTicks is USER_HZ, the unit of stat's start time (100 on Linux).
const clockTicks = 100

var (
	recordPid  = regexp.MustCompile(`\bpid (\d+)\b`)
	recordTime = regexp.MustCompile(`\bpid \d+ (\d{4}-\d\d-\d\dT[0-9:]+Z)\b`)
	recordKey  = regexp.MustCompile(`\b(boot|start|ticket)=(\S+)`)
)

// selfRecord is the key=value pairs this process appends to its records.
// Never start=0: a process that cannot read its own start time writes
// start=unknown, and the others judge its records by boot id and pid only.
func selfRecord(p procFS) string {
	start, ok := p.Start(os.Getpid())
	if !ok || start == 0 {
		return fmt.Sprintf("boot=%s start=unknown", p.BootID())
	}
	return fmt.Sprintf("boot=%s start=%d", p.BootID(), start)
}

// recordDead reports whether record (an owner file's or a ticket's text)
// names a process that is gone. at is the record's own time when the text
// has none (a ticket's name).
func recordDead(p procFS, record string, at time.Time) bool {
	m := recordPid.FindStringSubmatch(record)
	if m == nil {
		return false
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil {
		return false
	}
	keys := map[string]string{}
	for _, kv := range recordKey.FindAllStringSubmatch(record, -1) {
		keys[kv[1]] = kv[2]
	}
	if b, ok := keys["boot"]; ok {
		if cur := p.BootID(); cur != "" && b != cur {
			return true
		}
	}
	start, running := p.Start(pid)
	if !running {
		return true
	}
	if start == 0 {
		return false // runs; its start time cannot be read now
	}
	if s, ok := keys["start"]; ok {
		if s == "unknown" {
			return false // boot id and pid only
		}
		return s != strconv.FormatUint(start, 10)
	}
	// An older client's record: a pid that started after the record was
	// written is another process.
	if t := recordTime.FindStringSubmatch(record); t != nil {
		if parsed, err := time.Parse(time.RFC3339, t[1]); err == nil {
			at = parsed
		}
	}
	if at.IsZero() {
		return false
	}
	started := p.BootTime().Add(time.Duration(start) * time.Second / clockTicks)
	return started.After(at.Add(time.Second))
}

// seq makes this process's ticket names unique however fast it queues.
var seq atomic.Int64

// fairLock is one waiter of the lock: its ticket, then the lock.
type fairLock struct {
	fl          flocker
	lock, owner string
	class       string
	poll        time.Duration
	log         func(format string, args ...any)
	proc        procFS
	// load, when set, is the host's one-minute load (tests); nil reads
	// /proc/loadavg.
	load func() float64
	// exclusive: hold the lock alone (a heavy live suite). An exclusive
	// waiter takes a slot only when no slot is held, and while its record
	// (exclusive=1) holds one, no one else takes the other.
	exclusive bool
	// held is the owner record written when the lock was taken, slot the
	// slot directory it holds.
	held, slot string
	// warned are the pre-#2226 tickets already warned about.
	warned map[string]bool
}

// acquire queues a ticket and takes the lock when it is the first ticket and
// the lock is free; it removes the ticket either way.
func (f *fairLock) acquire(ctx context.Context) error {
	if f.proc == nil {
		f.proc = hostProc{}
	}
	f.warned = map[string]bool{}
	q := queueDir(f.lock)
	if err := os.MkdirAll(q, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", q, err)
	}
	name := newTicketName(f.class)
	ticket := filepath.Join(q, name)
	record := f.owner + " " + selfRecord(f.proc) + " priority=" + priorityOf(f.class) + " beat=1"
	if f.exclusive {
		record += " exclusive=1"
	}
	if err := writeTicket(q, name, record); err != nil {
		return err
	}
	defer func() { _ = os.Remove(ticket) }()
	// A freed slot (LOCK.d), a ticket (the queue) or LOCK itself wakes it.
	if err := os.MkdirAll(slotsDir(f.lock), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", slotsDir(f.lock), err)
	}
	wake, closeWake := watchDirs(q, filepath.Dir(f.lock), slotsDir(f.lock))
	defer closeWake()
	logged := ""
	for {
		if _, err := os.Stat(ticket); errors.Is(err, os.ErrNotExist) {
			if err := writeTicket(q, name, record); err != nil {
				return err
			}
			f.log("ticket %s was gone; queued it again in its place", name)
		}
		// The heartbeat: a ticket untouched for staleBeat is a stopped or
		// hung waiter's, and any waiter removes it (#2282 QA).
		now := time.Now()
		_ = os.Chtimes(ticket, now, now)
		reapTickets(f.proc, q, f.log, f.warned, name)
		first := firstTicket(q)
		if first == name {
			// The flock only when a take may succeed: a waiter stopped
			// while it waits seldom holds it.
			if f.mayTake() {
				took, err := f.tryHead(q, name, record)
				if err != nil || took {
					return err
				}
			}
		} else if f.reclaimDue() {
			// Any waiter removes the holders that are gone (#2242 QA: an
			// older client at the head cannot judge the view's holders, so
			// a new holder that died without its cleanup would wedge the
			// lock for everyone); only the head takes a slot.
			if err := f.fl.with(q, func() error { f.reclaim(); return nil }); err != nil {
				f.log("reclaim heavy-load lock %s: %v", f.lock, err)
			}
		}
		holder, _ := os.ReadFile(filepath.Join(f.lock, "owner"))
		h := strings.TrimSpace(string(holder))
		why := fmt.Sprintf("heavy-load lock %s is held by %s", f.lock, h)
		if h != "" && !strings.Contains(h, "ticket=") {
			why += " (a holder that took it without a ticket: the queue does not order it)"
		}
		if first != name {
			why = fmt.Sprintf("%s; ticket %s waits behind %s", why, name, first)
		}
		if why != logged {
			f.log("%s", why)
			logged = why
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		case <-time.After(f.poll):
		}
	}
}

// newTicketName is a waiter's ticket, <class>-<19-digit ns>-<pid>, unique in
// this process however fast it queues (seq).
func newTicketName(class string) string {
	ns := time.Now().UnixNano()
	for {
		last := seq.Load()
		if ns <= last {
			ns = last + 1
		}
		if seq.CompareAndSwap(last, ns) {
			break
		}
	}
	return fmt.Sprintf("%s-%019d-%d", class, ns, os.Getpid())
}

// writeTicket writes ticket name in q (a temporary name, then a rename),
// making q again when it was removed. A removal racing the write is
// retried; a write that keeps failing is an error.
func writeTicket(q, name, record string) error {
	var err error
	for try := 0; try < 3; try++ {
		if err = os.MkdirAll(q, 0o755); err != nil {
			continue
		}
		tmp := filepath.Join(q, "."+name)
		if err = os.WriteFile(tmp, []byte(record+"\n"), 0o644); err != nil {
			continue
		}
		if err = os.Rename(tmp, filepath.Join(q, name)); err == nil {
			return nil
		}
	}
	return fmt.Errorf("queue ticket %s: %w", name, err)
}

// The lock counts: up to maxHolders hold it at once while the host's
// one-minute load is under loadForTwo, one at or above it. Each holder has a
// slot directory LOCK.d/<n> with its owner record; LOCK exists while anyone
// holds it (an older client's mkdir LOCK still waits), with LOCK/owner
// listing every holder, one record per line, and LOCK/.counting marking it
// as this view (a LOCK without the marker is an older client's exclusive
// lock, which counts as full).
const (
	maxHolders  = 2
	loadForTwo  = 32.0
	viewMarker  = ".counting"
	slotsSuffix = ".d"
)

// slotsDir is the directory of lock's holder slots.
func slotsDir(lock string) string { return lock + slotsSuffix }

// hostLoad is the one-minute load average (/proc/loadavg).
// A load that cannot be read or parsed is a busy host's (#2242 QA): one
// holder.
func hostLoad() float64 { return loadFrom("/proc/loadavg") }

// loadFrom is the one-minute load of a /proc/loadavg file.
func loadFrom(path string) float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return loadForTwo
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return loadForTwo
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return loadForTwo
	}
	return v
}

// capacity is how many may hold the lock at load.
func capacity(load float64) int {
	if load < loadForTwo {
		return maxHolders
	}
	return 1
}

// withLockFlock runs fn under flock on q/.reclaim, which serializes every
// change to the lock, its slots and its view.
//
// It waits for the flock flockWait at most: a waiter stopped (Ctrl-Z) while
// it held it would wedge every other one, so they say so and try again.
func withLockFlock(q string, fn func() error) error {
	var fl flocker // one call: no attempt to keep
	return fl.with(q, fn)
}

// flocker takes the reclaim flock for one waiter, one call at a time. An
// attempt that times out stays pending for the waiter's next call, so a
// long hold costs one goroutine and one fd, not one per retry (#2282 QA).
type flocker struct{ pending *flockAttempt }

// flockAttempt is a pending flock on a reclaim file: got hands it to the
// waiter; dead is closed when it was given back unclaimed (flockHandoff).
type flockAttempt struct {
	path string
	got  chan error
	dead chan struct{}
	f    *os.File
}

func (fl *flocker) with(q string, fn func() error) error {
	path := filepath.Join(q, ".reclaim")
	deadline := time.NewTimer(flockWait)
	defer deadline.Stop()
	for {
		if fl.pending == nil || fl.pending.path != path {
			at, err := startFlock(path)
			if err != nil {
				return err
			}
			fl.pending = at
		}
		at := fl.pending
		select {
		case err := <-at.got:
			fl.pending = nil
			if err != nil {
				_ = at.f.Close()
				return fmt.Errorf("flock reclaim lock: %w", err)
			}
			defer func() {
				_ = unix.Flock(int(at.f.Fd()), unix.LOCK_UN)
				_ = at.f.Close()
			}()
			return fn()
		case <-at.dead:
			fl.pending = nil // given back while no one was asking: anew
		case <-deadline.C:
			return fmt.Errorf("no flock on %s in %s: a waiter stopped (Ctrl-Z) or hung while it held it? Resume or end it", path, flockWait)
		}
	}
}

// startFlock starts an attempt on path's flock.
func startFlock(path string) (*flockAttempt, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open reclaim lock: %w", err)
	}
	at := &flockAttempt{path: path, got: make(chan error), dead: make(chan struct{}), f: f}
	go func() {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX)
		select {
		case at.got <- err: // its waiter takes it (and unlocks it)
		case <-time.After(flockHandoff):
			// Its waiter did not ask again: given back, never held for nothing.
			if err == nil {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
			}
			_ = f.Close()
			close(at.dead)
		}
	}()
	return at, nil
}

// flockHandoff is how long a flock granted to a pending attempt waits for
// its waiter to take it before it is given back.
const flockHandoff = 5 * time.Second

// flockWait is how long a waiter waits for the reclaim flock (tests set it).
var flockWait = time.Minute

// reclaimDue reports whether a holder looks gone (judged again under the
// flock): only then does a waiter that is not the head take the flock.
func (f *fairLock) reclaimDue() bool {
	if _, exclusive := holders(f.lock); exclusive {
		ok, _ := f.deadDir(f.lock)
		return ok
	}
	entries, _ := os.ReadDir(slotsDir(f.lock))
	for _, e := range entries {
		if ok, _ := f.deadDir(filepath.Join(slotsDir(f.lock), e.Name())); ok {
			return true
		}
	}
	return false
}

// mayTake reports whether the head may find a slot it can take (judged
// again under the flock): a holder looks gone, or fewer than the capacity
// hold the lock and none holds it alone.
func (f *fairLock) mayTake() bool {
	if f.reclaimDue() {
		return true
	}
	held, exclusive := holders(f.lock)
	load := hostLoad()
	if f.load != nil {
		load = f.load()
	}
	if exclusive || len(held) >= capacity(load) || f.exclusive && len(held) > 0 {
		return false
	}
	for _, h := range held {
		if strings.Contains(h, "exclusive=1") {
			return false
		}
	}
	return true
}

// tryHead is the head waiter's turn, under the reclaim flock: it removes the
// slots (and an older client's exclusive lock) of holders that are gone,
// then takes a free slot when fewer than capacity hold the lock. took
// reports whether it holds the lock now.
func (f *fairLock) tryHead(q, name, record string) (took bool, err error) {
	err = f.fl.with(q, func() error {
		f.reclaim()
		held, exclusive := holders(f.lock)
		load := hostLoad()
		if f.load != nil {
			load = f.load()
		}
		if exclusive || len(held) >= capacity(load) {
			return nil
		}
		// A heavy holder holds the lock alone; a heavy waiter waits for it
		// to be free.
		if f.exclusive && len(held) > 0 {
			return nil
		}
		for _, h := range held {
			if strings.Contains(h, "exclusive=1") {
				return nil
			}
		}
		sd := slotsDir(f.lock)
		if err := os.MkdirAll(sd, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sd, err)
		}
		for n := 1; n <= maxHolders; n++ {
			slot := filepath.Join(sd, strconv.Itoa(n))
			if err := os.Mkdir(slot, 0o755); err != nil {
				if errors.Is(err, os.ErrExist) {
					continue
				}
				return fmt.Errorf("mkdir %s: %w", slot, err)
			}
			f.held = record + " ticket=" + name
			if err := os.WriteFile(filepath.Join(slot, "owner"), []byte(f.held+"\n"), 0o644); err != nil {
				_ = os.Remove(slot) // never hold a slot without an owner record; only an empty one, as the shell's rmdir
				return fmt.Errorf("write owner: %w", err)
			}
			if err := writeView(f.lock); err != nil {
				_ = os.Remove(filepath.Join(slot, "owner"))
				_ = os.Remove(slot)
				f.held = ""
				if errors.Is(err, errOlderHolder) {
					f.log("an older client took %s meanwhile; gave slot %s back", f.lock, slot)
					return nil
				}
				return err
			}
			f.slot = slot
			took = true
			return nil
		}
		return nil
	})
	return took, err
}

// holders returns the owner records of lock's slots, and whether lock is an
// older client's exclusive lock (LOCK without the view marker). A record
// that is there but cannot be read counts, and holds the lock alone
// (#2242 QA: where it cannot tell, the safe answer).
func holders(lock string) (records []string, exclusive bool) {
	if _, err := os.Stat(lock); err == nil {
		if _, err := os.Stat(filepath.Join(lock, viewMarker)); err != nil {
			return nil, true
		}
	}
	entries, _ := os.ReadDir(slotsDir(lock))
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(slotsDir(lock), e.Name(), "owner"))
		switch {
		case err == nil:
			records = append(records, strings.TrimSpace(string(b)))
		case !errors.Is(err, os.ErrNotExist):
			records = append(records, fmt.Sprintf("a holder whose record cannot be read (slot %s: %v) exclusive=1", e.Name(), err))
		}
	}
	return records, false
}

// writeView makes LOCK the view of the slots: it exists with the marker and
// LOCK/owner listing every holder while any slot is held, and is gone when
// none is. The caller holds the reclaim flock.
func writeView(lock string) error {
	held, exclusive := holders(lock)
	if exclusive {
		return errOlderHolder
	}
	if len(held) == 0 {
		return removeView(lock)
	}
	// A plain mkdir: a LOCK that is there without the marker is an older
	// client's, made since holders looked (#2242 QA): never take it over.
	switch err := os.Mkdir(lock, 0o755); {
	case err == nil:
		if err := os.WriteFile(filepath.Join(lock, viewMarker), nil, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", viewMarker, err)
		}
	case errors.Is(err, os.ErrExist):
		if _, err := os.Stat(filepath.Join(lock, viewMarker)); err != nil {
			return errOlderHolder
		}
	default:
		return fmt.Errorf("mkdir %s: %w", lock, err)
	}
	lines := make([]string, len(held))
	for i, h := range held {
		lines[i] = viewLine(h)
	}
	tmp := filepath.Join(lock, ".owner.tmp")
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("write view: %w", err)
	}
	return os.Rename(tmp, filepath.Join(lock, "owner"))
}

// removeView removes LOCK when it is the view, and only once its marker has
// gone: removing the marker is the test (#2242 QA), so a LOCK an older
// client made since holders looked (no marker) is never removed. LOCK stays
// there until the last Remove, so no one makes another meanwhile.
func removeView(lock string) error {
	if err := os.Remove(filepath.Join(lock, viewMarker)); err != nil {
		return nil
	}
	for _, f := range []string{"owner", ".owner.tmp"} {
		if err := os.Remove(filepath.Join(lock, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove the view %s: %w", lock, err)
		}
	}
	if err := os.Remove(lock); err != nil {
		return fmt.Errorf("remove the view %s (left with no owner; it goes in %s): %w", lock, orphanAge, err)
	}
	return nil
}

// errOlderHolder: LOCK is an older client's exclusive lock.
var errOlderHolder = errors.New("the lock is held by an older client")

// viewLine is a holder's record as LOCK/owner lists it: "pid N" written
// "pid:N". Older clients judge LOCK/owner by its first "pid N", and would
// remove the view while another slot's holder runs when that one is dead
// (#2242 QA); a view without one is never theirs to take over. New clients
// judge the slots.
func viewLine(record string) string {
	return recordPid.ReplaceAllString(record, "pid:$1")
}

// release gives up slot, only when its owner is still held, and updates
// the view.
func release(lock, slot, held string) (bool, error) {
	ours := false
	err := withLockFlock(queueDir(lock), func() error {
		b, err := os.ReadFile(filepath.Join(slot, "owner"))
		if err != nil || strings.TrimSpace(string(b)) != held {
			return nil
		}
		ours = true
		if err := os.RemoveAll(slot); err != nil {
			return fmt.Errorf("remove %s: %w", slot, err)
		}
		if err := writeView(lock); err != nil && !errors.Is(err, errOlderHolder) {
			return err
		}
		return nil
	})
	return ours, err
}

// reclaim removes the slots of holders that are gone (recordDead), or that
// have had no owner file for orphanAge, and an older client's exclusive lock
// in the same cases; then it refreshes the view when it removed a slot.
// Any waiter calls it, under the reclaim flock; only the head takes a slot.
// deadDir reports whether dir (a slot, or an older client's LOCK) is a
// holder's that is gone, or has had no owner file for orphanAge, and why.
func (f *fairLock) deadDir(dir string) (bool, string) {
	owner, err := os.ReadFile(filepath.Join(dir, "owner"))
	switch {
	case err == nil && recordDead(f.proc, string(owner), time.Time{}):
		return true, " of a holder that is gone: " + strings.TrimSpace(string(owner))
	case errors.Is(err, os.ErrNotExist):
		if st, serr := os.Stat(dir); serr == nil && time.Since(st.ModTime()) >= orphanAge {
			return true, fmt.Sprintf(", which has had no owner for %s", orphanAge)
		}
	}
	return false, ""
}

func (f *fairLock) reclaim() {
	dead := f.deadDir
	if _, exclusive := holders(f.lock); exclusive {
		if ok, why := dead(f.lock); ok {
			if beforeReclaimRemove != nil {
				beforeReclaimRemove()
			}
			// Only while its owner is still the dead one judged (read again
			// just before): an older client may have put a new LOCK there.
			now, err := os.ReadFile(filepath.Join(f.lock, "owner"))
			same := errors.Is(err, os.ErrNotExist) && !strings.HasPrefix(why, " of a holder") ||
				err == nil && why == " of a holder that is gone: "+strings.TrimSpace(string(now))
			if !same {
				return
			}
			f.log("removed heavy-load lock %s%s", f.lock, why)
			if err := os.RemoveAll(f.lock); err != nil {
				f.log("remove heavy-load lock %s: %v", f.lock, err)
			}
		}
		return
	}
	entries, _ := os.ReadDir(slotsDir(f.lock))
	reclaimed := false
	for _, e := range entries {
		slot := filepath.Join(slotsDir(f.lock), e.Name())
		if ok, why := dead(slot); ok {
			f.log("removed heavy-load lock slot %s%s", slot, why)
			if err := os.RemoveAll(slot); err != nil {
				f.log("remove heavy-load lock slot %s: %v", slot, err)
			}
			reclaimed = true
		}
	}
	// The view changes only with the slots (#2242 QA).
	if !reclaimed {
		return
	}
	if err := writeView(f.lock); err != nil && !errors.Is(err, errOlderHolder) {
		f.log("refresh heavy-load lock %s: %v", f.lock, err)
	}
}

// beforeReclaimRemove, nil but in tests, runs between judging an older
// client's lock dead and reading its owner again (a test puts another LOCK
// there meanwhile).
var beforeReclaimRemove func()

// reapTickets removes the tickets of waiters that are gone, and stale ones:
// a ticket untouched for staleBeat (one with beat=1) or staleOld (an older
// client's, which touches none) is a stopped, hung or stray waiter's,
// whatever its pid (#2282 QA: a head that does not move wedged everyone);
// its waiter queues it again in its place when it resumes. A ticket that
// cannot be read is judged by its name's pid and time. self, this waiter's
// own ticket, is never removed. It warns once (warned) about each ticket of
// a waiter on code from before #2226 (no boot id).
func reapTickets(p procFS, q string, log func(string, ...any), warned map[string]bool, self string) {
	entries, err := os.ReadDir(q)
	if err != nil {
		return
	}
	for _, e := range entries {
		parts := strings.Split(e.Name(), "-")
		if len(parts) != 3 || strings.HasPrefix(e.Name(), ".") || e.Name() == self {
			continue
		}
		ns, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		path := filepath.Join(q, e.Name())
		text, err := os.ReadFile(path)
		record := string(text)
		if err != nil {
			record = "(a ticket that cannot be read)"
		}
		if st, serr := os.Stat(path); serr == nil {
			limit := staleOld
			if strings.Contains(record, "beat=1") {
				limit = staleBeat
			}
			if age := time.Since(st.ModTime()); age > limit {
				if os.Remove(path) == nil {
					log("removed the ticket %s, untouched for %s (a waiter that is stopped, hung or gone; it queues again when it resumes)", e.Name(), age.Round(time.Second))
				}
				continue
			}
		}
		if err == nil && !strings.Contains(record, "boot=") && warned != nil && !warned[e.Name()] {
			warned[e.Name()] = true
			log("warning: ticket %s is a waiter on code from before #2226 (%s); it can move the lock from under its holder: rebase it onto main",
				e.Name(), strings.TrimSpace(record))
		}
		if !recordPid.MatchString(record) {
			record += " pid " + parts[2] // an older client's ticket text has no pid
		}
		if recordDead(p, record, time.Unix(0, ns)) {
			if os.Remove(path) == nil {
				log("removed the ticket %s of a waiter that is gone", e.Name())
			}
		}
	}
}

// staleBeat and staleOld are how long a ticket may go untouched: a
// heartbeat ticket (beat=1), an older client's. Tests set them.
var (
	staleBeat = 10 * time.Minute
	staleOld  = 30 * time.Minute
)

// agePromote is how long a normal ticket waits before it sorts as a release
// one (#2282 QA: release tickets never starve the others).
const agePromote = 2 * time.Hour

// priorityOf is a class's name in a record.
func priorityOf(class string) string {
	if class == classRelease {
		return "release"
	}
	return "normal"
}

// firstTicket is the name of the ticket that sorts first in q, or "".
func firstTicket(q string) string {
	entries, err := os.ReadDir(q)
	if err != nil {
		return ""
	}
	first, firstKey := "", ""
	now := time.Now().UnixNano()
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") {
			continue
		}
		key := n
		// A normal ticket that has waited agePromote sorts as a release one.
		if parts := strings.SplitN(n, "-", 3); len(parts) == 3 && parts[0] == classNormal {
			if ns, err := strconv.ParseInt(parts[1], 10, 64); err == nil && now-ns > int64(agePromote) {
				key = classRelease + "-" + parts[1] + "-" + parts[2]
			}
		}
		if first == "" || key < firstKey {
			first, firstKey = n, key
		}
	}
	return first
}

// watchDirs returns a channel that receives when an entry of one of dirs is
// created, removed or renamed (inotify), and a func that stops it. Without
// inotify the channel never receives: the caller's poll still runs.
func watchDirs(dirs ...string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return ch, func() {}
	}
	for _, d := range dirs {
		_, _ = unix.InotifyAddWatch(fd, d, unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO)
	}
	// A non-blocking fd in an os.File waits in Go's poller: Close ends a
	// pending Read.
	f := os.NewFile(uintptr(fd), "inotify")
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := f.Read(buf); err != nil {
				return
			}
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}()
	return ch, func() { _ = f.Close() }
}
