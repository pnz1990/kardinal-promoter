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
	"sort"
	"strconv"
	"strings"
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
//     the oldest) tries the lock, and only it may reclaim a lock whose
//     holder is gone. It does both under flock(2) on LOCK.queue/.reclaim, and
//     removes a dead holder's lock outright (no rename aside and back: two
//     reclaimers once moved a live holder's lock, #2202 QA).
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
//   - A waiter whose ticket is gone (cleaned by hand) writes it again under
//     its name, so it keeps its place.
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
	BootID() string
	// Start is pid's start time in clock ticks since boot (stat field 22);
	// ok is false when pid does not run.
	Start(pid int) (ticks uint64, ok bool)
	// BootTime is when this boot started (/proc/stat btime).
	BootTime() time.Time
}

// hostProc is the real /proc.
type hostProc struct{}

func (hostProc) BootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

func (hostProc) Start(pid int) (uint64, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	return statStart(string(b))
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
	if !ok {
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
	if b, ok := keys["boot"]; ok && b != p.BootID() {
		return true
	}
	start, running := p.Start(pid)
	if !running {
		return true
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
	lock, owner string
	class       string
	poll        time.Duration
	log         func(format string, args ...any)
	proc        procFS
	// held is the owner record written when the lock was taken.
	held string
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
	name := fmt.Sprintf("%s-%019d-%d", f.class, ns, os.Getpid())
	ticket := filepath.Join(q, name)
	record := f.owner + " " + selfRecord(f.proc)
	if err := writeTicket(q, name, record); err != nil {
		return err
	}
	defer func() { _ = os.Remove(ticket) }()
	wake, closeWake := watchDirs(q, filepath.Dir(f.lock))
	defer closeWake()
	logged := ""
	for {
		if _, err := os.Stat(ticket); errors.Is(err, os.ErrNotExist) {
			if err := writeTicket(q, name, record); err != nil {
				return err
			}
			f.log("ticket %s was gone; queued it again in its place", name)
		}
		reapTickets(f.proc, q, f.log, f.warned)
		first := firstTicket(q)
		if first == name {
			took, err := f.tryHead(q, name, record)
			if err != nil || took {
				return err
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

// tryHead is the head waiter's turn, under flock on q/.reclaim: it removes
// the lock of a holder that is gone, then tries mkdir. took reports whether
// it holds the lock now.
func (f *fairLock) tryHead(q, name, record string) (took bool, err error) {
	rf, err := os.OpenFile(filepath.Join(q, ".reclaim"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false, fmt.Errorf("open reclaim lock: %w", err)
	}
	defer func() { _ = rf.Close() }()
	if err := unix.Flock(int(rf.Fd()), unix.LOCK_EX); err != nil {
		return false, fmt.Errorf("flock reclaim lock: %w", err)
	}
	defer func() { _ = unix.Flock(int(rf.Fd()), unix.LOCK_UN) }()
	f.reclaim()
	if err := os.Mkdir(f.lock, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("mkdir %s: %w", f.lock, err)
	}
	f.held = record + " ticket=" + name
	if err := os.WriteFile(filepath.Join(f.lock, "owner"), []byte(f.held+"\n"), 0o644); err != nil {
		_ = os.Remove(f.lock) // never hold a lock without an owner record; only an empty one, as the shell's rmdir
		return false, fmt.Errorf("write owner: %w", err)
	}
	return true, nil
}

// reclaim removes the lock when its holder is gone (recordDead), or when it
// has had no owner file for orphanAge. Only the head waiter calls it, under
// the reclaim flock.
func (f *fairLock) reclaim() {
	owner, err := os.ReadFile(filepath.Join(f.lock, "owner"))
	switch {
	case err == nil && recordDead(f.proc, string(owner), time.Time{}):
		f.log("removed heavy-load lock %s of a holder that is gone: %s", f.lock, strings.TrimSpace(string(owner)))
	case errors.Is(err, os.ErrNotExist):
		st, serr := os.Stat(f.lock)
		if serr != nil || time.Since(st.ModTime()) < orphanAge {
			return
		}
		f.log("removed heavy-load lock %s, which has had no owner for %s", f.lock, orphanAge)
	default:
		return
	}
	if err := os.RemoveAll(f.lock); err != nil {
		f.log("remove heavy-load lock %s: %v", f.lock, err)
	}
}

// reapTickets removes the tickets of waiters that are gone.
// It warns once (warned) about each ticket of a waiter on code from before
// #2226 (no boot id): its reaper can move the lock from under its holder.
func reapTickets(p procFS, q string, log func(string, ...any), warned map[string]bool) {
	entries, err := os.ReadDir(q)
	if err != nil {
		return
	}
	for _, e := range entries {
		parts := strings.Split(e.Name(), "-")
		if len(parts) != 3 || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ns, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		text, err := os.ReadFile(filepath.Join(q, e.Name()))
		if err != nil {
			continue
		}
		record := string(text)
		if !strings.Contains(record, "boot=") && warned != nil && !warned[e.Name()] {
			warned[e.Name()] = true
			log("warning: ticket %s is a waiter on code from before #2226 (%s); it can move the lock from under its holder: rebase it onto main",
				e.Name(), strings.TrimSpace(record))
		}
		if !recordPid.MatchString(record) {
			record += " pid " + parts[2] // an older client's ticket text has no pid
		}
		if recordDead(p, record, time.Unix(0, ns)) {
			if os.Remove(filepath.Join(q, e.Name())) == nil {
				log("removed the ticket %s of a waiter that is gone", e.Name())
			}
		}
	}
}

// firstTicket is the name of the ticket that sorts first in q, or "".
func firstTicket(q string) string {
	entries, err := os.ReadDir(q)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); !strings.HasPrefix(n, ".") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
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
