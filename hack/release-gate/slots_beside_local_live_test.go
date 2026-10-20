// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMachineSlots_BesideLocalLive (#2352 QA): the gate and
// hack/e2e/local-live.sh take cluster slots from one directory under one
// flock, DIR/.reclaim. A local-live run parked inside its take (a mkdir in
// front of PATH that makes slot 1 and sleeps) holds it: the gate, first in
// the queue (a release ticket), waits for it rather than taking slot 2
// beside a take midway. Then local-live is stopped there, gives its slot
// back, and the gate takes one, which nothing removes.
func TestMachineSlots_BesideLocalLive(t *testing.T) {
	h := newLiveHost(t)
	mkdir, err := exec.LookPath("mkdir")
	require.NoError(t, err)
	slow := filepath.Join(h.dir, "slow")
	require.NoError(t, os.MkdirAll(slow, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(slow, "mkdir"), []byte("#!/bin/sh\n"+mkdir+` "$@" || exit
case "$*" in */slots/1) sleep 2 ;; esac
`), 0o755))
	live := h.cmd("gitea")
	live.Env = append(live.Env, "LOCAL_LIVE_TEST_FAIL_PATH="+slow)
	out := &lockedBuf{}
	live.Stdout, live.Stderr = out, out
	require.NoError(t, live.Start())
	t.Cleanup(func() { _ = live.Process.Kill() })
	slot1 := filepath.Join(h.slots, "1")
	require.Eventually(t, func() bool { _, err := os.Stat(slot1); return err == nil }, 10*time.Second, 5*time.Millisecond,
		"local-live is in its take: %s", out)
	_, oerr := os.Stat(filepath.Join(slot1, "owner"))
	require.ErrorIs(t, oerr, os.ErrNotExist, "parked after its mkdir, before its owner write")

	gate := &machine{slotsDir: h.slots, slotMax: 2, owner: "the gate", lockClass: classRelease}
	type taken struct {
		release func()
		ok      bool
		why     string
	}
	got := make(chan taken, 1)
	go func() {
		release, ok, why := gate.Slots(1)
		got <- taken{release, ok, why}
	}()
	select {
	case r := <-got:
		t.Fatalf("the gate's take (%v: %s) ran while local-live's held the flock", r.ok, r.why)
	case <-time.After(700 * time.Millisecond):
	}
	_, err = os.Stat(filepath.Join(h.slots, "2"))
	assert.ErrorIs(t, err, os.ErrNotExist, "no slot made beside a take midway")

	require.NoError(t, live.Process.Signal(syscall.SIGTERM))
	err = live.Wait()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, "%s", out)
	assert.Equal(t, 143, ee.ExitCode(), "%s", out)
	var r taken
	select {
	case r = <-got:
	case <-time.After(20 * time.Second):
		t.Fatalf("the gate never took a slot once local-live let the flock go: %s", out)
	}
	require.True(t, r.ok, "%s", r.why)
	owners := h.owners(t)
	assert.Equal(t, []string{"the gate"}, owners, "the gate's slot, and none of local-live's left: %s", out)
	assert.NotContains(t, out.String(), "not ours any more")
	r.release()
	assert.Empty(t, h.owners(t), "the gate gives its slot back")
}

// liveHost is a fake host for hack/e2e/local-live.sh, as test/hack's
// localLiveHost: stub up.sh, run.sh and tools.sh, kind, docker and gh that
// do nothing, a real slots directory, and the test-mode token.
type liveHost struct {
	dir, slots, token string
}

func newLiveHost(t *testing.T) *liveHost {
	t.Helper()
	dir := t.TempDir()
	h := &liveHost{dir: dir, slots: filepath.Join(dir, "slots"), token: fmt.Sprintf("t%d-%d", time.Now().UnixNano(), os.Getpid())}
	for _, d := range []string{"bin", "e2e", "fake", "slots"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	for name, body := range map[string]string{
		"bin/kind":     "#!/usr/bin/env bash\ntouch \"$FAKE/clusters\"; [ \"$1 $2\" != \"get clusters\" ] || cat \"$FAKE/clusters\"\n",
		"bin/docker":   "#!/usr/bin/env bash\n[ \"$1\" != inspect ] || exit 1\n",
		"bin/gh":       "#!/usr/bin/env bash\n",
		"e2e/up.sh":    "#!/usr/bin/env bash\necho \"$KIND_CLUSTER\" >>\"$FAKE/clusters\"\n",
		"e2e/run.sh":   "#!/usr/bin/env bash\necho '{\"results\":[]}' >\"$E2E_OUT/summary.json\"\n",
		"e2e/tools.sh": "#!/usr/bin/env bash\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "e2e", ".local-live-test-token"), []byte(h.token+"\n"), 0o600))
	return h
}

func (h *liveHost) cmd(args ...string) *exec.Cmd {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	cmd := exec.Command("bash", append([]string{filepath.Join(root, "hack", "e2e", "local-live.sh")}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Join(h.dir, "bin")+":"+os.Getenv("PATH"),
		"KUBECONFIG=/dev/null", "KARDINAL_E2E_TOOLS=path", "KARDINAL_E2E_BUILD=", "KARDINAL_E2E_STATE=",
		"KIND_CLUSTER=", "KEEP=", "AGENT_BADGE=", "RETRY=0.05", "OWNER=local-live", "AGENT_ID=", "SLOT_MAX=2", "DRAIN_DEADLINE=",
		"LOCAL_LIVE_E2E_DIR="+filepath.Join(h.dir, "e2e"), "FAKE="+filepath.Join(h.dir, "fake"),
		"LOADLOCK="+filepath.Join(h.dir, "LOADLOCK"), "RELEASE_GATE_SLOTS="+h.slots,
		"LOCAL_LIVE_TEST=1", "LOCAL_LIVE_TEST_TOKEN="+h.token, "GOTOOLCHAIN=local",
		"LOCAL_LIVE_TEST_LOAD=0", "LOCAL_LIVE_TEST_INOTIFY=8192", "LOCAL_LIVE_TEST_SPACING=0")
	return cmd
}

// owners are the owner records of the host's slots, in slot order.
func (h *liveHost) owners(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(h.slots)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(h.slots, e.Name(), "owner"))
		if err != nil {
			out = append(out, e.Name()+": no owner")
			continue
		}
		out = append(out, strings.TrimSpace(string(b)))
	}
	return out
}

// lockedBuf is a bytes buffer safe for a process's output and a reader.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestMachineSlots_ReleaseUnderTheFlock: the gate gives its slots back
// under DIR/.reclaim too, waiting for a holder of it; one it cannot have in
// flockWait (a run stopped while it held it) still gives them back, and
// says so.
func TestMachineSlots_ReleaseUnderTheFlock(t *testing.T) {
	dir := t.TempDir()
	var logged lockedBuf
	m := &machine{slotsDir: dir, slotMax: 2, owner: "the gate", log: func(f string, a ...any) { _, _ = fmt.Fprintf(&logged, f+"\n", a...) }}
	hold := func() *os.File {
		f, err := os.OpenFile(filepath.Join(dir, ".reclaim"), os.O_CREATE|os.O_RDWR, 0o644)
		require.NoError(t, err)
		require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX))
		return f
	}
	released := func(release func()) chan struct{} {
		done := make(chan struct{})
		go func() { release(); close(done) }()
		return done
	}
	release, ok, why := m.Slots(1)
	require.True(t, ok, why)
	f := hold()
	done := released(release)
	select {
	case <-done:
		t.Fatal("it gave its slot back while another held the flock")
	case <-time.After(500 * time.Millisecond):
	}
	assert.DirExists(t, filepath.Join(dir, "1"))
	require.NoError(t, f.Close()) // the flock goes with it
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("no release once the flock was free")
	}
	assert.NoDirExists(t, filepath.Join(dir, "1"))

	flockWait = 300 * time.Millisecond
	defer func() { flockWait = time.Minute }()
	release, ok, why = m.Slots(1)
	require.True(t, ok, why)
	f = hold()
	defer func() { _ = f.Close() }()
	select {
	case <-released(release):
	case <-time.After(10 * time.Second):
		t.Fatal("a release waited past flockWait")
	}
	assert.NoDirExists(t, filepath.Join(dir, "1"), "its slot goes without the flock")
	assert.Contains(t, logged.String(), "giving the gate's cluster slots back without it")
}

// TestMachineSlots_TurnCheckedAgainUnderTheFlock (#2361 QA): the head and
// the room are checked again under the flock, so a release ticket queued
// while the gate waited for it goes first.
func TestMachineSlots_TurnCheckedAgainUnderTheFlock(t *testing.T) {
	dir := t.TempDir()
	m := &machine{slotsDir: dir, slotMax: 2, owner: "the gate"}
	f, err := os.OpenFile(filepath.Join(dir, ".reclaim"), os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX))
	type taken struct {
		ok  bool
		why string
	}
	got := make(chan taken, 1)
	go func() {
		_, ok, why := m.Slots(1)
		got <- taken{ok, why}
	}()
	select {
	case r := <-got:
		t.Fatalf("the gate did not wait for the flock: %v %s", r.ok, r.why)
	case <-time.After(300 * time.Millisecond): // past the check without it
	}
	ahead := fmt.Sprintf("0-%019d-%d", time.Now().UnixNano(), os.Getpid())
	require.NoError(t, os.WriteFile(filepath.Join(slotQueue(dir), ahead),
		[]byte(fmt.Sprintf("a release run pid %d now %s\n", os.Getpid(), selfRecord(hostProc{}))), 0o644))
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_UN))
	var r taken
	select {
	case r = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("no answer once the flock was free")
	}
	assert.False(t, r.ok, "it took a slot past the release ticket queued ahead of it")
	assert.Contains(t, r.why, "waits behind "+ahead)
	held, err := heldSlots(dir)
	require.NoError(t, err)
	assert.Zero(t, held)
}

// TestWithLockFlock_TimedOutAttemptsLetGo (#2361 QA): a call that timed out
// waiting for the flock leaves an attempt behind, which lets the flock go
// the moment it gets it: five of them once kept it from another client for
// five handoffs (25s).
func TestWithLockFlock_TimedOutAttemptsLetGo(t *testing.T) {
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, ".reclaim"), os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX))
	flockWait = 50 * time.Millisecond
	defer func() { flockWait = time.Minute }()
	for i := 0; i < 5; i++ {
		require.Error(t, withLockFlock(dir, func() error { return nil }), "the flock is held")
	}
	require.NoError(t, f.Close()) // the flock goes with it
	flockWait = 3 * time.Second
	start := time.Now()
	ran := false
	require.NoError(t, withLockFlock(dir, func() error { ran = true; return nil }))
	assert.True(t, ran)
	assert.Less(t, time.Since(start), time.Second, "the five timed-out attempts held it for no one")
}
