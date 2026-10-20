// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Statuses of a step result.
const (
	statusPass    = "pass"
	statusFail    = "fail"
	statusNotRun  = "not run"
	statusMissing = "missing"
	// statusPinned: hack/e2e/matrix.txt pins the suite to other minors
	// (Step.Pinned). Reported, never run, not against the verdict.
	statusPinned = "pinned"
)

// Result is a step's outcome, STEP_DIR/result.json. A pass is cached: a
// later gate reuses it, and runs only the rest, when it ran on the same
// commit, on a clean tree, with the same inputs (Key).
type Result struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	SHA    string `json:"sha"`
	// Key is the hash of what the step ran: its scripts, its thresholds, the
	// tool versions and the gate's options (Gate.key).
	Key string `json:"key"`
	// Dirty: the tree had uncommitted changes. Such a result is reported,
	// marked, and never reused.
	Dirty   bool      `json:"dirty,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Started time.Time `json:"started"`
	Seconds float64   `json:"seconds"`
	Cached  bool      `json:"-"`
}

// Host is what the scheduler asks of the machine; tests fake it.
type Host interface {
	// Ready says whether a new cluster may start now (load and memory), and
	// why not.
	Ready() (bool, string)
	// Slots takes n of the host's cluster slots (shared with other users of
	// the host), or none and why not.
	Slots(n int) (release func(), ok bool, why string)
	// Lock takes the heavy-load lock, waiting for it until ctx ends.
	Lock(ctx context.Context) error
	// LockExclusive takes it as its only holder (a heavy live suite: the
	// counting lock's second holder waits).
	LockExclusive(ctx context.Context) error
	Unlock()
}

// Runner runs a step's script and its cleanup; tests fake it.
type Runner interface {
	Run(ctx context.Context, st Step, dir string) error
	Cleanup(st Step, dir string)
}

// Gate runs the steps of a plan.
type Gate struct {
	Out string // results directory of this commit
	SHA string
	// Dirty: the tree has uncommitted changes, so nothing is reused and
	// every result is marked.
	Dirty bool
	// Inputs is what every step's key includes besides its own scripts: the
	// tool versions (hack/tool-versions.env) and the gate's options.
	Inputs   string
	Clusters int // cluster budget
	Fresh    bool
	Host     Host
	Runner   Runner
	Wait     time.Duration // between checks for a free slot, a ready host
	Log      func(format string, args ...any)
	now      func() time.Time
}

func (g *Gate) dir(st Step) string { return filepath.Join(g.Out, st.ID) }

func (g *Gate) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// key is the hash of everything a step's result depends on besides the
// commit: its scripts, test thresholds and CI job, and the gate's inputs.
func (g *Gate) key(st Step) string {
	h := sha256.New()
	job := ""
	if st.CIJob != nil {
		job = st.CIJob.Name()
	}
	for _, part := range []string{st.ID, st.Script, st.Cleanup, st.Tests, strconv.Itoa(st.MinTests), strconv.Itoa(st.MaxSkips), job, g.Inputs} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cached returns the step's earlier pass, when it can stand for this run:
// the same commit, a clean tree then and now, the same key, and its log and
// test output still there.
func (g *Gate) cached(st Step) (Result, bool) {
	if g.Dirty {
		return Result{}, false
	}
	data, err := os.ReadFile(filepath.Join(g.dir(st), "result.json"))
	if err != nil {
		return Result{}, false
	}
	var r Result
	if json.Unmarshal(data, &r) != nil || r.Status != statusPass || r.SHA != g.SHA || r.Dirty || r.Key != g.key(st) {
		return Result{}, false
	}
	if g.proven(st, g.dir(st)) != "" {
		return Result{}, false
	}
	r.Cached = true
	return r, true
}

// proven returns "" when a step that exited 0 left its proof: a log, and,
// for a step with tests, a test output that records at least MinTests tests
// and at most MaxSkips skips. Else it says what is missing; the step is not
// a pass.
func (g *Gate) proven(st Step, dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "log.txt")); err != nil {
		return "no log"
	}
	if st.Tests == "" {
		return ""
	}
	f, err := os.Open(filepath.Join(dir, st.Tests))
	if err != nil {
		return "no test output (" + st.Tests + ")"
	}
	defer func() { _ = f.Close() }()
	t, err := countTests(f)
	if err != nil {
		return "unreadable test output: " + err.Error()
	}
	if t.Failed > 0 {
		// go test can exit 0 with failures recorded (a TestMain that exits
		// 0): what the output says decides.
		return fmt.Sprintf("the test output records %d failures (%d of them package-level)", t.Failed, t.PackageFailed)
	}
	if t.Run < max(1, st.MinTests) {
		return fmt.Sprintf("%d tests ran, fewer than the %d this step proves", t.Run, max(1, st.MinTests))
	}
	if t.Skipped > st.MaxSkips {
		return fmt.Sprintf("%d tests skipped, more than the %d allowed (see the report's skipped tests)", t.Skipped, st.MaxSkips)
	}
	return ""
}

// failedBefore says whether the step's saved result on this commit is a failure.
func (g *Gate) failedBefore(st Step) bool {
	data, err := os.ReadFile(filepath.Join(g.dir(st), "result.json"))
	if err != nil {
		return false
	}
	var r Result
	return json.Unmarshal(data, &r) == nil && r.SHA == g.SHA && r.Key == g.key(st) && r.Status == statusFail
}

func (g *Gate) save(r Result) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(g.Out, r.ID), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(g.Out, r.ID, "result.json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

// Run runs every step that has no cached pass, up to the cluster budget at
// once (a step that needs more than the whole budget runs alone), one step
// without clusters at a time, one heavy step at a time under the host's
// lock, and a new cluster only when the host is ready. It returns every
// step's result in plan order.
func (g *Gate) Run(ctx context.Context, steps []Step) ([]Result, error) {
	results := make([]Result, len(steps))
	var pending []int
	for i, st := range steps {
		switch {
		case st.Missing != "":
			results[i] = Result{ID: st.ID, Status: statusMissing, SHA: g.SHA, Reason: st.Missing}
		case st.NotRun != "":
			results[i] = Result{ID: st.ID, Status: statusNotRun, SHA: g.SHA, Reason: st.NotRun}
		case st.Pinned != "":
			results[i] = Result{ID: st.ID, Status: statusPinned, SHA: g.SHA, Reason: st.Pinned}
		default:
			if r, ok := g.cached(st); ok && !g.Fresh {
				results[i] = r
				g.Log("%s: passed earlier on this commit, cached", st.ID)
				continue
			}
			if st.CIJob != nil && !g.Fresh {
				steps[i].ciRetry = g.failedBefore(st)
			}
			pending = append(pending, i)
		}
	}

	var mu sync.Mutex
	usedClusters, localBusy, heavyBusy := 0, false, false
	done := make(chan int, len(steps))
	running := 0
	fits := func(st Step) bool {
		if st.CIJob != nil {
			return true // a runner of its own
		}
		if st.Heavy && heavyBusy {
			return false
		}
		if st.Clusters == 0 {
			return !localBusy
		}
		return usedClusters == 0 || usedClusters+st.Clusters <= g.Clusters
	}
	releases := map[int]func(){}
	start := func(i int) {
		st := steps[i]
		running++
		if st.CIJob != nil {
			go func() {
				r := g.runStep(ctx, st)
				mu.Lock()
				results[i] = r
				mu.Unlock()
				done <- i
			}()
			return
		}
		if st.Clusters == 0 {
			localBusy = true
		}
		usedClusters += st.Clusters
		if st.Heavy {
			heavyBusy = true
		}
		go func() {
			r := g.runStep(ctx, st)
			mu.Lock()
			results[i] = r
			mu.Unlock()
			done <- i
		}()
	}
	finish := func(i int) {
		st := steps[i]
		running--
		if release := releases[i]; release != nil {
			release()
			delete(releases, i)
		}
		if st.CIJob != nil {
			return
		}
		if st.Clusters == 0 {
			localBusy = false
		}
		usedClusters -= st.Clusters
		if st.Heavy {
			heavyBusy = false
		}
	}

	lastWait := map[string]time.Time{}
	waiting := func(id, why string) {
		// At most every five minutes per step: the load changes every check.
		if t, ok := lastWait[id]; !ok || g.clock().Sub(t) >= 5*time.Minute {
			g.Log("waiting to start %s: %s", id, why)
			lastWait[id] = g.clock()
		}
	}
	for len(pending) > 0 || running > 0 {
		if ctx.Err() == nil {
			var rest []int
			for _, i := range pending {
				st := steps[i]
				if !fits(st) {
					rest = append(rest, i)
					continue
				}
				if st.Clusters > 0 {
					if ok, why := g.Host.Ready(); !ok {
						waiting(st.ID, why)
						rest = append(rest, i)
						continue
					}
					release, ok, why := g.Host.Slots(st.Clusters)
					if !ok {
						waiting(st.ID, why)
						rest = append(rest, i)
						continue
					}
					releases[i] = release
				}
				start(i)
			}
			pending = rest
		} else {
			for _, i := range pending {
				results[i] = Result{ID: steps[i].ID, Status: statusFail, SHA: g.SHA, Reason: "the gate was interrupted"}
			}
			pending = nil
		}
		if running == 0 {
			if len(pending) == 0 {
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(g.Wait):
			}
			continue
		}
		select {
		case i := <-done:
			finish(i)
		case <-time.After(g.Wait):
		}
	}
	// A step that waited for slots to the end gives its place back.
	if d, ok := g.Host.(interface{ DropSlotTicket() }); ok {
		d.DropSlotTicket()
	}
	return results, ctx.Err()
}

// runStep runs one step: the heavy-load lock first if it needs one, then
// the script and its cleanup, and saves the result.
func (g *Gate) runStep(ctx context.Context, st Step) Result {
	dir := g.dir(st)
	r := Result{ID: st.ID, SHA: g.SHA, Key: g.key(st), Dirty: g.Dirty}
	if err := os.RemoveAll(dir); err != nil {
		r.Status, r.Reason = statusFail, err.Error()
		return r
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.Status, r.Reason = statusFail, err.Error()
		return r
	}
	if st.Heavy {
		g.Log("%s: taking the heavy-load lock%s", st.ID, map[bool]string{true: " alone", false: ""}[st.Exclusive])
		lock := g.Host.Lock
		if st.Exclusive {
			lock = g.Host.LockExclusive
		}
		if err := lock(ctx); err != nil {
			r.Status, r.Reason = statusFail, "heavy-load lock: "+err.Error()
			_ = g.save(r)
			return r
		}
		defer g.Host.Unlock()
	}
	g.Log("%s: start (%s)", st.ID, filepath.Join(dir, "log.txt"))
	r.Started = g.clock()
	err := g.Runner.Run(ctx, st, dir)
	if st.Cleanup != "" {
		g.Runner.Cleanup(st, dir)
	}
	r.Seconds = g.clock().Sub(r.Started).Seconds()
	switch {
	case err != nil:
		r.Status, r.Reason = statusFail, err.Error()
	case g.proven(st, dir) != "":
		// Exit 0 is not a pass by itself: the evidence must be there.
		r.Status, r.Reason = statusFail, "not proven: "+g.proven(st, dir)
	default:
		r.Status = statusPass
	}
	if err := g.save(r); err != nil {
		g.Log("%s: %v", st.ID, err)
	}
	g.Log("%s: %s in %s", st.ID, r.Status, time.Duration(r.Seconds*float64(time.Second)).Round(time.Second))
	return r
}

// ── the real host and runner ─────────────────────────────────────────────────

// bashRunner runs scripts with bash in the repository root.
type bashRunner struct {
	root string
	env  []string
}

func (b bashRunner) command(ctx context.Context, script string, st Step, dir string, log io.Writer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "bash", "-c", "set -euo pipefail\n"+script)
	cmd.Dir = b.root
	kubeconfig := os.DevNull
	if st.Clusters > 0 {
		kubeconfig = filepath.Join(dir, "kubeconfig")
	}
	cmd.Env = append(append(os.Environ(), b.env...), "STEP_DIR="+dir, "KUBECONFIG="+kubeconfig)
	cmd.Stdout, cmd.Stderr = log, log
	// Its own process group, so an interrupt stops kind, helm and go test too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = time.Minute
	return cmd
}

func (b bashRunner) Run(ctx context.Context, st Step, dir string) error {
	log, err := os.Create(filepath.Join(dir, "log.txt"))
	if err != nil {
		return fmt.Errorf("create log: %w", err)
	}
	defer func() { _ = log.Close() }()
	if _, err := fmt.Fprintf(log, "== %s (%s)\n", st.ID, st.Title); err != nil {
		return fmt.Errorf("write log: %w", err)
	}
	if err := b.command(ctx, st.Script, st, dir, log).Run(); err != nil {
		return fmt.Errorf("script: %w (see log.txt)", err)
	}
	return nil
}

// Cleanup runs on its own context: it must delete the clusters after an
// interrupt too.
func (b bashRunner) Cleanup(st Step, dir string) {
	log, err := os.OpenFile(filepath.Join(dir, "log.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = log.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, _ = fmt.Fprintln(log, "== cleanup")
	if err := b.command(ctx, st.Cleanup, st, dir, log).Run(); err != nil {
		_, _ = fmt.Fprintf(log, "cleanup failed: %v\n", err)
	}
}

// machine is the real host: /proc/loadavg, /proc/meminfo and a mkdir lock.
type machine struct {
	maxLoad   float64
	minMemGB  float64
	slotsDir  string // the host's cluster slots, slotsDir/1..slotMax ("" for none)
	slotMax   int
	lockDir   string // "" for no lock
	owner     string
	lockRetry time.Duration
	lockClass string // classRelease or classNormal ("": normal)
	proc      procFS // nil: the host's /proc
	heldMu    sync.Mutex
	held      string         // the owner record this gate wrote when it took the lock
	slot      string         // the slot directory of the lock it holds
	lockLoad  func() float64 // nil: /proc/loadavg (tests set it)
	log       func(format string, args ...any)
	// The cluster slots' queue ticket while a step waits for slots, and its
	// record (slotQueue).
	slotMu     sync.Mutex
	slotTicket string
	slotRecord string
	// slotFlock takes SLOTS_DIR/.reclaim for the takes: the flock
	// hack/e2e/local-live.sh's takes and reclaims hold (#2352 QA).
	slotFlock flocker
}

func (m *machine) Ready() (bool, string) {
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(data)); len(f) > 0 {
			if l, err := strconv.ParseFloat(f[0], 64); err == nil && l > m.maxLoad {
				return false, fmt.Sprintf("1-minute load %.0f is over %.0f", l, m.maxLoad)
			}
		}
	}
	if gb, ok := memAvailableGB(); ok && gb < m.minMemGB {
		return false, fmt.Sprintf("%.0f GB of memory available, under %.0f", gb, m.minMemGB)
	}
	return true, ""
}

// slotQueue is the cluster slots' queue, DIR/.queue, shared with
// hack/e2e/local-live.sh (#2242's ticket protocol): a waiter's ticket
// <class>-<ns>-<pid>, release gates' (-priority release) before everyone
// else's, then the oldest; only the first takes slots, so a waiter that
// polls often never overtakes one that polls seldom. A heavy local-live run
// draining the host is the head while it drains.
func slotQueue(dir string) string { return filepath.Join(dir, ".queue") }

// Slots makes n of slotsDir/1..slotMax (mkdir is atomic) and writes the
// owner into each, when this gate's ticket is first in the slots' queue;
// with fewer than n free, or slotMax held in all, it takes none and keeps
// its place. The takes, and their release, hold slotsDir/.reclaim, as
// local-live.sh's do: no slot is made outside that flock, which a
// local-live run stopped mid-mkdir counts on (it gives up a slot with no
// owner yet as its own, #2352 QA), and no two clients take at once.
func (m *machine) Slots(n int) (func(), bool, string) {
	if m.slotsDir == "" {
		return func() {}, true, ""
	}
	m.slotMu.Lock()
	defer m.slotMu.Unlock()
	proc := m.proc
	if proc == nil {
		proc = hostProc{}
	}
	q := slotQueue(m.slotsDir)
	if m.slotTicket == "" {
		class := m.lockClass
		if class == "" {
			class = classNormal
		}
		m.slotTicket = newTicketName(class)
		m.slotRecord = m.owner + " " + selfRecord(proc) + " priority=" + priorityOf(class) + " beat=1"
	}
	if _, err := os.Stat(filepath.Join(q, m.slotTicket)); err != nil {
		// Gone (removed as stale while the gate did not ask): queued again
		// in its place.
		if err := writeTicket(q, m.slotTicket, m.slotRecord); err != nil {
			return nil, false, fmt.Sprintf("queue for a cluster slot: %v", err)
		}
	} else {
		now := time.Now() // the heartbeat
		_ = os.Chtimes(filepath.Join(q, m.slotTicket), now, now)
	}
	logf := m.log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	reapTickets(proc, q, logf, nil, m.slotTicket)
	// A heavy local-live suite draining the host (DIR/.drain, written by
	// hack/e2e/local-live.sh) keeps every other run's cluster off it, the
	// gate's too, unless the gate wrote it. A marker that is there but
	// cannot be read is a drain (#2242 QA: the safe answer).
	if drain, err := os.ReadFile(filepath.Join(m.slotsDir, ".drain")); err == nil && drainLive(string(drain), "") {
		return nil, false, "a heavy suite is draining the host: " + strings.TrimSpace(string(drain))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Sprintf("a drain marker that cannot be read: %v", err)
	}
	// The head and the room first without the flock (a waiter that cannot
	// take never holds it), then again under it, with the takes.
	if why, ok := m.slotTurn(q, n); !ok {
		return nil, false, why
	}
	var (
		release func()
		ok      bool
		why     string
	)
	if err := m.slotFlock.with(m.slotsDir, func() error {
		release, ok, why = m.takeSlots(q, n, logf)
		return nil
	}); err != nil {
		return nil, false, err.Error()
	}
	return release, ok, why
}

// slotTurn reports whether this gate's ticket is first in q and n slots are
// free, or why not.
func (m *machine) slotTurn(q string, n int) (string, bool) {
	if first := firstTicket(q); first != m.slotTicket {
		return fmt.Sprintf("cluster slot ticket %s waits behind %s", m.slotTicket, first), false
	}
	// Every held slot directory counts, one above slotMax too (a run with a
	// larger SLOT_MAX took it): the host runs slotMax clusters at most.
	held, err := heldSlots(m.slotsDir)
	if err != nil {
		return fmt.Sprintf("cannot count the cluster slots in %s: %v", m.slotsDir, err), false
	}
	if held+n > m.slotMax {
		return fmt.Sprintf("%d cluster slots are in use in %s, and the host has %d", held, m.slotsDir, m.slotMax), false
	}
	return "", true
}

// takeSlots is Slots' take, under the slots' .reclaim flock: the turn again,
// then each mkdir and its owner write. Its release takes the flock again;
// one it cannot have in flockWait (a run stopped while it held it) still
// gives this gate's slots back, as the owner check keeps every other's.
func (m *machine) takeSlots(q string, n int, logf func(string, ...any)) (func(), bool, string) {
	if why, ok := m.slotTurn(q, n); !ok {
		return nil, false, why
	}
	var taken []string
	drop := func() {
		for _, d := range taken {
			if holder, err := os.ReadFile(filepath.Join(d, "owner")); err == nil && strings.TrimSpace(string(holder)) == m.owner {
				_ = os.RemoveAll(d)
			}
		}
	}
	for i := 1; i <= m.slotMax && len(taken) < n; i++ {
		d := filepath.Join(m.slotsDir, strconv.Itoa(i))
		if os.Mkdir(d, 0o755) != nil {
			continue
		}
		taken = append(taken, d)
		if err := os.WriteFile(filepath.Join(d, "owner"), []byte(m.owner+"\n"), 0o644); err != nil {
			drop()
			return nil, false, fmt.Sprintf("write cluster slot %s: %v", d, err)
		}
	}
	if len(taken) < n {
		drop()
		return nil, false, fmt.Sprintf("fewer than %d of the host's %d cluster slots in %s are free", n, m.slotMax, m.slotsDir)
	}
	_ = os.Remove(filepath.Join(q, m.slotTicket))
	m.slotTicket = ""
	release := func() {
		if err := withLockFlock(m.slotsDir, func() error { drop(); return nil }); err != nil {
			logf("%v; giving the gate's cluster slots back without it", err)
			drop()
		}
	}
	return release, true, ""
}

// DropSlotTicket gives this gate's place in the slots' queue back (the run
// is over, or interrupted, with a step still waiting).
func (m *machine) DropSlotTicket() {
	m.slotMu.Lock()
	defer m.slotMu.Unlock()
	if m.slotTicket != "" && m.slotsDir != "" {
		_ = os.Remove(filepath.Join(slotQueue(m.slotsDir), m.slotTicket))
	}
	m.slotTicket = ""
}

// heldSlots counts dir's numbered slot directories.
func heldSlots(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() {
			n++
		}
	}
	return n, nil
}

var (
	drainPid   = regexp.MustCompile(`(?:^|\s)pid (\d+)(?:\s|$)`)
	drainBoot  = regexp.MustCompile(`(?:^|\s)boot=(\S+)`)
	drainStart = regexp.MustCompile(`(?:^|\s)start=(\d+)(?:\s|$)`)
)

// drainLive reports whether a .drain record names a live process other than
// this one, by local-live.sh's record rule (#2244 QA): the process is gone
// when the record's boot id is not this boot's, its pid does not run, or
// its start time (/proc/<pid>/stat field 22) differs, so a reused pid is
// not the drainer. A record without a pid counts as live; local-live.sh
// reclaims a dead one. proc is the /proc to read ("" for the host's).
func drainLive(record, proc string) bool {
	if proc == "" {
		proc = "/proc"
	}
	m := drainPid.FindStringSubmatch(record)
	if m == nil {
		return true
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil || pid == os.Getpid() {
		return false
	}
	if b := drainBoot.FindStringSubmatch(record); b != nil {
		boot, err := os.ReadFile(filepath.Join(proc, "sys/kernel/random/boot_id"))
		if err == nil && strings.TrimSpace(string(boot)) != b[1] {
			return false
		}
	}
	stat, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	if st := drainStart.FindStringSubmatch(record); st != nil {
		// Field 22, counted after the command's last ')': the command can
		// hold spaces and parentheses.
		rest := string(stat)
		if i := strings.LastIndexByte(rest, ')'); i >= 0 {
			rest = rest[i+1:]
		}
		if f := strings.Fields(rest); len(f) > 19 && f[19] != st[1] {
			return false
		}
	}
	return true
}

func memAvailableGB() (float64, bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) >= 2 && fields[0] == "MemAvailable:" {
			kb, err := strconv.ParseFloat(fields[1], 64)
			return kb / (1 << 20), err == nil
		}
	}
	return 0, false
}

// Lock takes lockDir, the host's heavy-load lock, in its fair queue
// (fairlock.go): a ticket of lockClass, served release tickets first, then
// in order. While it waits it wakes on the queue's changes, or every
// lockRetry.
func (m *machine) Lock(ctx context.Context) error { return m.lock(ctx, false) }

// LockExclusive is Lock as the lock's only holder.
func (m *machine) LockExclusive(ctx context.Context) error { return m.lock(ctx, true) }

func (m *machine) lock(ctx context.Context, exclusive bool) error {
	if m.lockDir == "" {
		return nil
	}
	class := m.lockClass
	if class == "" {
		class = classNormal
	}
	poll := m.lockRetry
	if poll <= 0 {
		poll = 10 * time.Second
	}
	f := &fairLock{lock: m.lockDir, owner: m.owner, class: class, poll: poll, log: m.log, proc: m.proc, load: m.lockLoad,
		exclusive: exclusive}
	if err := f.acquire(ctx); err != nil {
		return err
	}
	m.heldMu.Lock()
	m.held, m.slot = f.held, f.slot
	m.heldMu.Unlock()
	return nil
}

// Unlock gives up this gate's slot of the lock, only if it still holds it.
func (m *machine) Unlock() {
	if m.lockDir == "" {
		return
	}
	m.heldMu.Lock()
	held, slot := m.held, m.slot
	m.held, m.slot = "", ""
	m.heldMu.Unlock()
	if held == "" {
		m.log("heavy-load lock %s is not ours any more; left as it is", m.lockDir)
		return
	}
	ours, err := release(m.lockDir, slot, held)
	if err != nil {
		m.log("release the heavy-load lock: %v", err)
	}
	if !ours {
		m.log("heavy-load lock %s is not ours any more; left as it is", m.lockDir)
	}
}
