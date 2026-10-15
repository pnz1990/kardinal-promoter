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
	if fi, err := os.Stat(filepath.Join(dir, "log.txt")); err != nil || fi.Size() == 0 {
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
		g.Log("%s: taking the heavy-load lock", st.ID)
		if err := g.Host.Lock(ctx); err != nil {
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
	log       func(format string, args ...any)
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

// Slots makes n of slotsDir/1..slotMax (mkdir is atomic) and writes the
// owner into each; with fewer than n free it takes none.
func (m *machine) Slots(n int) (func(), bool, string) {
	if m.slotsDir == "" {
		return func() {}, true, ""
	}
	var taken []string
	release := func() {
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
			release()
			return nil, false, fmt.Sprintf("write cluster slot %s: %v", d, err)
		}
	}
	if len(taken) < n {
		release()
		return nil, false, fmt.Sprintf("fewer than %d of the host's %d cluster slots in %s are free", n, m.slotMax, m.slotsDir)
	}
	return release, true, ""
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

// Lock makes lockDir, the host's heavy-load lock (mkdir is atomic), and
// writes who holds it; while someone else holds it, it waits lockRetry and
// tries again.
func (m *machine) Lock(ctx context.Context) error {
	if m.lockDir == "" {
		return nil
	}
	for {
		err := os.Mkdir(m.lockDir, 0o755)
		if err == nil {
			return os.WriteFile(filepath.Join(m.lockDir, "owner"), []byte(m.owner+"\n"), 0o644)
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("mkdir %s: %w", m.lockDir, err)
		}
		holder, _ := os.ReadFile(filepath.Join(m.lockDir, "owner"))
		m.log("heavy-load lock %s is held by %s; retrying in %s", m.lockDir, strings.TrimSpace(string(holder)), m.lockRetry)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.lockRetry):
		}
	}
}

// Unlock removes the lock, only if this gate holds it.
func (m *machine) Unlock() {
	if m.lockDir == "" {
		return
	}
	holder, err := os.ReadFile(filepath.Join(m.lockDir, "owner"))
	if err != nil || strings.TrimSpace(string(holder)) != m.owner {
		m.log("heavy-load lock %s is not ours any more; left as it is", m.lockDir)
		return
	}
	if err := os.RemoveAll(m.lockDir); err != nil {
		m.log("remove the heavy-load lock: %v", err)
	}
}
