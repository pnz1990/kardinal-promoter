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
	"time"

	"golang.org/x/sys/unix"
)

// The host's heavy-load lock is fair: the lock itself is still the directory
// LOCK with LOCK/owner (mkdir is atomic; every reader of the owner file keeps
// working), and its waiters queue in LOCK.queue, one ticket file each, named
//
//	<class>-<nanoseconds>-<pid>
//
// class 0 is a release gate's (-priority release), 1 everyone else's. Only
// the waiter whose ticket sorts first tries mkdir: release tickets first,
// then the oldest. hack/loadlock.sh queues the same way. A waiter wakes on
// inotify events in the queue and the lock's directory, or every poll at
// the latest. A ticket or a lock whose owner names the pid of a process
// that is gone is removed, so a killed waiter or holder does not block the
// queue (only "pid N" owners: a lock without one is never taken over).

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

var pidOf = regexp.MustCompile(`\bpid (\d+)\b`)

// alive reports whether the process pid runs on this host (EPERM: it does,
// as another user).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

// deadOwner reports whether owner (an owner file's text) names a pid whose
// process is gone.
func deadOwner(owner string) bool {
	m := pidOf.FindStringSubmatch(owner)
	if m == nil {
		return false
	}
	pid, err := strconv.Atoi(m[1])
	return err == nil && !alive(pid)
}

// fairLock is one waiter of the lock: its ticket, then the lock.
type fairLock struct {
	lock, owner string
	class       string
	poll        time.Duration
	log         func(format string, args ...any)
}

// acquire queues a ticket and takes the lock when it is the first ticket and
// the lock is free; it removes the ticket either way.
func (f *fairLock) acquire(ctx context.Context) error {
	q := queueDir(f.lock)
	if err := os.MkdirAll(q, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", q, err)
	}
	name := fmt.Sprintf("%s-%019d-%d", f.class, time.Now().UnixNano(), os.Getpid())
	ticket := filepath.Join(q, name)
	tmp := filepath.Join(q, "."+name)
	if err := os.WriteFile(tmp, []byte(f.owner+"\n"), 0o644); err != nil {
		return fmt.Errorf("write ticket: %w", err)
	}
	if err := os.Rename(tmp, ticket); err != nil {
		return fmt.Errorf("queue ticket: %w", err)
	}
	defer func() { _ = os.Remove(ticket) }()
	wake, closeWake := watchDirs(q, filepath.Dir(f.lock))
	defer closeWake()
	logged := ""
	for {
		reap(f.lock, f.log)
		first := firstTicket(q)
		if first == name {
			err := os.Mkdir(f.lock, 0o755)
			if err == nil {
				return os.WriteFile(filepath.Join(f.lock, "owner"), []byte(f.owner+"\n"), 0o644)
			}
			if !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("mkdir %s: %w", f.lock, err)
			}
		}
		holder, _ := os.ReadFile(filepath.Join(f.lock, "owner"))
		why := fmt.Sprintf("heavy-load lock %s is held by %s", f.lock, strings.TrimSpace(string(holder)))
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

// reap removes the tickets of waiters that are gone, and the lock when its
// holder is gone: it renames the lock aside first and puts it back if the
// owner it then reads is not the dead one (a new holder took it meanwhile).
func reap(lock string, log func(string, ...any)) {
	q := queueDir(lock)
	if entries, err := os.ReadDir(q); err == nil {
		for _, e := range entries {
			parts := strings.Split(e.Name(), "-")
			if len(parts) != 3 || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if pid, err := strconv.Atoi(parts[2]); err == nil && !alive(pid) {
				if os.Remove(filepath.Join(q, e.Name())) == nil {
					log("removed the ticket %s of a waiter that is gone", e.Name())
				}
			}
		}
	}
	owner, err := os.ReadFile(filepath.Join(lock, "owner"))
	if err != nil || !deadOwner(string(owner)) {
		return
	}
	aside := fmt.Sprintf("%s.stale-%d-%d", lock, os.Getpid(), time.Now().UnixNano())
	if os.Rename(lock, aside) != nil {
		return
	}
	if again, _ := os.ReadFile(filepath.Join(aside, "owner")); string(again) != string(owner) {
		_ = os.Rename(aside, lock) // not the dead holder's any more
		return
	}
	_ = os.RemoveAll(aside)
	log("removed heavy-load lock %s of a holder that is gone: %s", lock, strings.TrimSpace(string(owner)))
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
