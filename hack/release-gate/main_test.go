// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeRepo() *Repo {
	return &Repo{
		Root: "/repo",
		Matrix: []MatrixLine{
			{"scale", "1.37", "-"},
			{"core", "1.35", "1/2"}, {"core", "1.35", "2/2"}, {"core", "1.37", "1/2"},
			{"gitlab", "1.37", "-"}, {"github", "1.37", "-"},
			{"multi-cluster", "1.36", "-"},
			{"upgrade-v0100", "1.35", "-"}, {"upgrade-v0100", "1.37", "-"},
		},
		ToolVersions: map[string]string{
			"KIND_NODE_1_35": "kindest/node:v1.35.8@sha256:a", "KIND_NODE_1_36": "kindest/node:v1.36.4@sha256:b",
			"KIND_NODE_1_37": "kindest/node:v1.37.0@sha256:c", "KIND_VERSION": "v0.33.0",
			"ENVTEST_K8S_MIN": "1.35.0", "ENVTEST_K8S_MAX": "1.37.0",
		},
		FuzzTargets: []FuzzTarget{{"FuzzA", "./pkg/a"}},
		PrevRelease: "v0.11.0",
	}
}

func byID(steps []Step) map[string]Step {
	out := map[string]Step{}
	for _, st := range steps {
		out[st.ID] = st
	}
	return out
}

func fakeOptions() Options {
	return Options{Count: 2, FuzzTime: "1m", ScaleProfiles: []string{"full", "soak"}, ImageBuild: "host", Floor: 35, Newest: 37}
}

func TestPlan(t *testing.T) {
	r := fakeRepo()
	floor, newest := r.KubeMinors()
	assert.Equal(t, []int{35, 37}, []int{floor, newest}, "the floor and the newest are hack/tool-versions.env's")
	steps := Plan(r, fakeOptions())
	ids := byID(steps)
	assert.Len(t, ids, len(steps), "step ids are unique")

	// Every live suite on exactly the floor and the newest minor:
	// multi-cluster too, not on its matrix.txt 1.36.
	for _, id := range []string{"e2e-core-135", "e2e-core-137", "e2e-gitlab-135", "e2e-gitlab-137", "e2e-github-135",
		"e2e-multi-cluster-135", "e2e-multi-cluster-137", "e2e-upgrade-v0100-135", "e2e-upgrade-v0100-137",
		"scale-full", "scale-soak", "unit-race", "fuzz-FuzzA", "scenario-matrix", "govulncheck", "golangci-lint",
		"hygiene", "images", "docs", "bundle-size", "chart-profiles", "chart-envtest"} {
		assert.Contains(t, ids, id)
	}
	for id := range ids {
		assert.NotContains(t, id, "-136", "%s: only the floor and the newest minor", id)
	}

	core := ids["e2e-core-135"]
	assert.Equal(t, 1, core.MinTests, "a live suite that ran no test proves nothing")
	assert.Contains(t, core.Script, "export KIND_CLUSTER=kp-rg-core-135 KIND_K8S=1.35 COUNT=2\n")
	assert.Contains(t, core.Script, `> "$STEP_DIR/images.txt"`, "the images the suite ran are recorded")
	assert.Contains(t, core.Cleanup, "for c in kp-rg-core-135 kp-rg-core-135-spoke; do")
	assert.Contains(t, ids["e2e-upgrade-v0100-135"].Script, "COUNT=1\n", "the upgrade suite runs once")
	assert.Contains(t, ids["e2e-github-137"].NotRun, "never uses your gh login")

	// A minor with no node image is missing evidence, not "not run".
	noImage := fakeRepo()
	delete(noImage.ToolVersions, "KIND_NODE_1_35")
	assert.Contains(t, byID(Plan(noImage, fakeOptions()))["e2e-core-135"].Missing, "no node image for Kubernetes 1.35")

	assert.Contains(t, ids["upgrade-v0110"].Missing, "nothing tests the upgrade from v0.11.0")
	assert.Contains(t, ids["scenario-enterprise"].Missing, "test/scenario/enterprise")

	// Each step's proof threshold.
	assert.Equal(t, 20000, ids["unit-race"].MinTests)
	assert.Equal(t, 40, ids["scenario-matrix"].MinTests)
	assert.Equal(t, 1, ids["fuzz-FuzzA"].MinTests)
	assert.Zero(t, ids["scenario-matrix"].MaxSkips, "the scenario matrix may not skip")

	// envtest on the floor and the newest, or missing.
	env := ids["chart-envtest"]
	assert.Empty(t, env.Missing)
	assert.Equal(t, 2, env.MinTests)
	assert.Contains(t, env.Script, `for v in "$ENVTEST_K8S_MIN" "$ENVTEST_K8S_MAX"; do`)
	assert.Contains(t, env.Script, "-json >> \"$STEP_DIR/tests.json\"")
	old := fakeRepo()
	old.ToolVersions["ENVTEST_K8S_MIN"] = "1.30.3"
	assert.Contains(t, byID(Plan(old, fakeOptions()))["chart-envtest"].Missing, "envtest runs 1.30.3 and 1.37.0")

	full, soak := ids["scale-full"], ids["scale-soak"]
	assert.True(t, full.Heavy && soak.Heavy)
	assert.Contains(t, full.Script, "KIND_K8S=1.37")
	assert.Contains(t, soak.Script, "export RUN='^TestScale_LoadSustained$'")
	assert.False(t, ids["fuzz-FuzzA"].Heavy)
	assert.True(t, byID(Plan(fakeRepo(), Options{FuzzTime: "5m", Floor: 35, Newest: 37}))["fuzz-FuzzA"].Heavy)
	assert.Equal(t, "upgrade-v0110", upgradeSuite("v0.11.0"))
}

// The plan of this checkout: every matrix suite has a step on the floor and
// the newest minor, every cluster step deletes its kp- clusters, every
// values profile exists, and envtest runs the two minors.
func TestPlanOfThisRepo(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	r, err := LoadRepo(root)
	require.NoError(t, err)
	floor, newest := r.KubeMinors()
	opts := Options{Count: 2, FuzzTime: "1m", ScaleProfiles: []string{"full", "soak"}, ImageBuild: "docker", Floor: floor, Newest: newest}
	steps := Plan(r, opts)
	ids := byID(steps)
	assert.Len(t, ids, len(steps))
	assert.Empty(t, ids["chart-envtest"].Missing, "hack/tool-versions.env's envtest minors are the floor and the newest")
	for _, l := range r.Matrix {
		if l.Suite == "scale" {
			continue
		}
		for _, m := range opts.Minors() {
			assert.Contains(t, ids, fmt.Sprintf("e2e-%s-1%d", l.Suite, m))
		}
	}
	for _, st := range steps {
		if st.Clusters == 0 || st.Script == "" {
			continue
		}
		name := strings.Fields(strings.SplitN(st.Script, "KIND_CLUSTER=", 2)[1])[0]
		assert.True(t, strings.HasPrefix(name, "kp-rg-"), st.ID)
		assert.Contains(t, st.Cleanup, "for c in "+name+" "+name+"-spoke; do", st.ID)
	}
	for _, p := range profiles[1:] {
		assert.FileExists(t, filepath.Join(root, "hack", "release-gate", "profiles", p+".yaml"))
	}
	_, err = readAllowlist(filepath.Join(root, "hack", "release-gate", "coverage-allow.txt"))
	require.NoError(t, err)
}

func TestPlanCI(t *testing.T) {
	steps := Plan(fakeRepo(), Options{Count: 2, FuzzTime: "1m", ScaleProfiles: []string{"full"}, LiveCI: true, Floor: 35, Newest: 37})
	ids := byID(steps)
	// Every suite on the floor and the newest, with its shards: gitlab on
	// 1.35 too, which matrix.txt does not list.
	for id, name := range map[string]string{
		"ci-core-135-1of2":     "core 1/2 on Kubernetes 1.35",
		"ci-core-137-2of2":     "core 2/2 on Kubernetes 1.37",
		"ci-gitlab-135":        "gitlab on Kubernetes 1.35",
		"ci-multi-cluster-137": "multi-cluster on Kubernetes 1.37",
		"ci-upgrade-v0100-135": "upgrade-v0100 on Kubernetes 1.35",
	} {
		require.Contains(t, ids, id)
		assert.Equal(t, name, ids[id].CIJob.Name())
		assert.Equal(t, 1, ids[id].MinTests)
	}
	assert.NotContains(t, ids, "ci-multi-cluster-136")
	assert.NotContains(t, ids, "e2e-core-135", "no local live suite")
	assert.Contains(t, ids, "scale-full", "scale stays local")
	assert.Equal(t, []string{"core", "gitlab", "github", "multi-cluster", "upgrade-v0100"}, ciSuitesOf(steps))
	assert.Equal(t, "1.35 1.37", minorsInput(Options{Floor: 35, Newest: 37}))
}

// ── the scheduler ────────────────────────────────────────────────────────────

type fakeHost struct {
	mu     sync.Mutex
	ready  func() bool
	locked bool
	// slots, when set, is how many cluster slots the host has.
	slots *int
}

func (h *fakeHost) Slots(n int) (func(), bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.slots == nil {
		return func() {}, true, ""
	}
	if *h.slots < n {
		return nil, false, "no slot"
	}
	*h.slots -= n
	return func() { h.mu.Lock(); *h.slots += n; h.mu.Unlock() }, true, ""
}

func (h *fakeHost) Ready() (bool, string) {
	if h.ready != nil && !h.ready() {
		return false, "busy"
	}
	return true, ""
}

func (h *fakeHost) Lock(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.locked {
		return errors.New("the lock is taken twice")
	}
	h.locked = true
	return nil
}

func (h *fakeHost) Unlock() {
	h.mu.Lock()
	h.locked = false
	h.mu.Unlock()
}

type fakeRunner struct {
	mu                     sync.Mutex
	clusters, local, heavy int
	maxClusters, maxLocal  int
	maxHeavy               int
	ran, cleaned           []string
	fail                   map[string]bool
	output                 map[string]string // a step's test output, when set
	noLog                  bool
	block                  chan struct{}
	onStart                func(id string)
}

// passingTests is a go test -json stream of n passing tests.
func passingTests(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `{"Action":"pass","Test":"Test%d"}`+"\n", i)
	}
	return b.String()
}

func (f *fakeRunner) Run(ctx context.Context, st Step, dir string) error {
	// The step's evidence: a log and, for a step with tests, enough of them
	// (or what output says, for a step that output names).
	if !f.noLog {
		_ = os.WriteFile(filepath.Join(dir, "log.txt"), []byte("ran "+st.ID+"\n"), 0o644)
	}
	if body, ok := f.output[st.ID]; st.Tests != "" && body != "\x00none" {
		if !ok {
			body = passingTests(max(1, st.MinTests))
		}
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, st.Tests)), 0o755)
		_ = os.WriteFile(filepath.Join(dir, st.Tests), []byte(body), 0o644)
	}
	f.mu.Lock()
	f.ran = append(f.ran, st.ID)
	f.clusters += st.Clusters
	if st.Clusters == 0 {
		f.local++
	}
	if st.Heavy {
		f.heavy++
	}
	f.maxClusters, f.maxLocal, f.maxHeavy = max(f.maxClusters, f.clusters), max(f.maxLocal, f.local), max(f.maxHeavy, f.heavy)
	f.mu.Unlock()
	if f.onStart != nil {
		f.onStart(st.ID)
	}
	switch {
	case f.block != nil:
		select {
		case <-f.block:
		case <-ctx.Done():
		}
	case st.Heavy:
		time.Sleep(30 * time.Millisecond)
	default:
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	f.clusters -= st.Clusters
	if st.Clusters == 0 {
		f.local--
	}
	if st.Heavy {
		f.heavy--
	}
	f.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.fail[st.ID] {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeRunner) Cleanup(st Step, _ string) {
	f.mu.Lock()
	f.cleaned = append(f.cleaned, st.ID)
	f.mu.Unlock()
}

func newGate(t *testing.T, budget int, h Host, r Runner) *Gate {
	return &Gate{Out: t.TempDir(), SHA: "abc", Clusters: budget, Host: h, Runner: r, Wait: time.Millisecond,
		Log: func(string, ...any) {}}
}

func schedSteps() []Step {
	return []Step{
		{ID: "c1", Clusters: 1, Script: "x", Cleanup: "y"},
		{ID: "c2", Clusters: 1, Script: "x", Cleanup: "y"},
		{ID: "c3", Clusters: 2, Script: "x", Cleanup: "y"},
		{ID: "c4", Clusters: 1, Script: "x", Cleanup: "y"},
		{ID: "big", Clusters: 4, Script: "x", Cleanup: "y"}, // over the budget: runs alone
		{ID: "h1", Clusters: 2, Heavy: true, Script: "x", Cleanup: "y"},
		{ID: "l1", Script: "x"}, {ID: "l2", Script: "x"}, {ID: "lh", Heavy: true, Script: "x"},
		{ID: "missing", Missing: "no test"}, {ID: "notrun", NotRun: "no token"},
	}
}

func TestGateRun_Budget(t *testing.T) {
	for _, budget := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			r := &fakeRunner{}
			results, err := newGate(t, budget, &fakeHost{}, r).Run(context.Background(), schedSteps())
			require.NoError(t, err)
			assert.Len(t, r.ran, 9, "every runnable step ran")
			assert.LessOrEqual(t, r.maxClusters, max(budget, 4), "never over the budget, except a step bigger than it alone")
			assert.Equal(t, 1, r.maxLocal, "one step without clusters at a time")
			assert.Equal(t, 1, r.maxHeavy, "one heavy step at a time")
			assert.ElementsMatch(t, []string{"c1", "c2", "c3", "c4", "big", "h1"}, r.cleaned, "every cluster step cleans up")
			got := map[string]string{}
			for _, res := range results {
				got[res.ID] = res.Status
			}
			assert.Equal(t, statusMissing, got["missing"])
			assert.Equal(t, statusNotRun, got["notrun"])
			for _, st := range schedSteps() {
				if st.Missing == "" && st.NotRun == "" {
					assert.Equal(t, statusPass, got[st.ID], "%s: the heavy-load lock is never taken twice", st.ID)
				}
			}
		})
	}
}

// With budget 2, a big step never overlaps another cluster step.
func TestGateRun_OversizeStepRunsAlone(t *testing.T) {
	var mu sync.Mutex
	running := map[string]bool{}
	var overlap []string
	r := &fakeRunner{}
	r.onStart = func(id string) {
		mu.Lock()
		defer mu.Unlock()
		for other := range running {
			if id == "big" || other == "big" {
				overlap = append(overlap, id+"+"+other)
			}
		}
		running[id] = true
		go func() { time.Sleep(4 * time.Millisecond); mu.Lock(); delete(running, id); mu.Unlock() }()
	}
	steps := []Step{{ID: "a", Clusters: 1, Script: "x"}, {ID: "big", Clusters: 4, Script: "x"}, {ID: "b", Clusters: 1, Script: "x"}}
	_, err := newGate(t, 2, &fakeHost{}, r).Run(context.Background(), steps)
	require.NoError(t, err)
	assert.Empty(t, overlap)
}

func TestGateRun_WaitsForTheHost(t *testing.T) {
	var ready atomic.Bool
	h := &fakeHost{ready: ready.Load}
	r := &fakeRunner{}
	g := newGate(t, 2, h, r)
	go func() { time.Sleep(30 * time.Millisecond); ready.Store(true) }()
	start := time.Now()
	_, err := g.Run(context.Background(), []Step{{ID: "c", Clusters: 1, Script: "x"}, {ID: "l", Script: "x"}})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond, "the cluster step waited for the host")
	assert.Equal(t, []string{"l", "c"}, r.ran, "a step without clusters does not wait for the host")
}

func TestGateRun_Resume(t *testing.T) {
	r := &fakeRunner{fail: map[string]bool{"b": true}}
	g := newGate(t, 1, &fakeHost{}, r)
	steps := []Step{{ID: "a", Script: "x"}, {ID: "b", Script: "x"}}
	results, err := g.Run(context.Background(), steps)
	require.NoError(t, err)
	assert.Equal(t, statusPass, results[0].Status)
	assert.Equal(t, statusFail, results[1].Status)
	assert.Contains(t, results[1].Reason, "boom")

	// Again on the same commit: only the failed step runs.
	r.ran, r.fail = nil, nil
	results, err = g.Run(context.Background(), steps)
	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, r.ran)
	assert.True(t, results[0].Cached)
	assert.Equal(t, statusPass, results[1].Status)

	// Another commit, or -fresh: everything.
	r.ran = nil
	g.SHA = "def"
	_, err = g.Run(context.Background(), steps)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a", "b"}, r.ran)
	r.ran = nil
	g.Fresh = true
	_, err = g.Run(context.Background(), steps)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a", "b"}, r.ran)
}

func TestGateRun_Interrupt(t *testing.T) {
	r := &fakeRunner{block: make(chan struct{})}
	g := newGate(t, 1, &fakeHost{}, r)
	ctx, cancel := context.WithCancel(context.Background())
	r.onStart = func(string) { cancel() }
	results, err := g.Run(ctx, []Step{{ID: "a", Clusters: 1, Script: "x", Cleanup: "y"}, {ID: "b", Clusters: 1, Script: "x"}})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"a"}, r.ran, "nothing starts after the interrupt")
	assert.Equal(t, []string{"a"}, r.cleaned, "the running step's clusters are deleted")
	assert.Equal(t, statusFail, results[0].Status)
	assert.Equal(t, statusFail, results[1].Status)
	assert.Contains(t, results[1].Reason, "interrupted")
	_, ok := g.cached(Step{ID: "a"})
	assert.False(t, ok, "an interrupted step is not cached")
}

// ── the real host and runner ─────────────────────────────────────────────────

func TestMachineLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LOADLOCK")
	a := &machine{lockDir: dir, owner: "a", lockRetry: 5 * time.Millisecond, log: func(string, ...any) {}}
	b := &machine{lockDir: dir, owner: "b", lockRetry: 5 * time.Millisecond, log: func(string, ...any) {}}
	require.NoError(t, a.Lock(context.Background()))
	owner, err := os.ReadFile(filepath.Join(dir, "owner"))
	require.NoError(t, err)
	assert.Equal(t, "a\n", string(owner))

	got := make(chan error, 1)
	go func() { got <- b.Lock(context.Background()) }()
	select {
	case <-got:
		t.Fatal("b took a lock a holds")
	case <-time.After(50 * time.Millisecond):
	}
	b.Unlock() // not b's: a's lock stays
	owner, err = os.ReadFile(filepath.Join(dir, "owner"))
	require.NoError(t, err, "a's lock is still there")
	assert.Equal(t, "a\n", string(owner))
	a.Unlock()
	require.NoError(t, <-got, "b gets it once a lets go")
	owner, err = os.ReadFile(filepath.Join(dir, "owner"))
	require.NoError(t, err)
	assert.Equal(t, "b\n", string(owner))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, a.Lock(ctx), context.Canceled, "waiting ends with the gate")
	b.Unlock()
	assert.NoDirExists(t, dir)

	none := &machine{}
	require.NoError(t, none.Lock(context.Background()), "no lock directory: no lock")
	none.Unlock()
}

func TestMachineReady(t *testing.T) {
	ok, why := (&machine{maxLoad: 1e9, minMemGB: 0}).Ready()
	assert.True(t, ok, why)
	ok, why = (&machine{maxLoad: -1, minMemGB: 0}).Ready()
	assert.False(t, ok)
	assert.Contains(t, why, "1-minute load")
	ok, why = (&machine{maxLoad: 1e9, minMemGB: 1e9}).Ready()
	assert.False(t, ok)
	assert.Contains(t, why, "GB of memory available")
}

func TestBashRunner(t *testing.T) {
	dir := t.TempDir()
	b := bashRunner{root: t.TempDir()}
	st := Step{ID: "s", Script: `echo "dir=$STEP_DIR kc=$KUBECONFIG"; echo out > "$STEP_DIR/out"`, Cleanup: `echo cleaned`}
	require.NoError(t, b.Run(context.Background(), st, dir))
	b.Cleanup(st, dir)
	log, err := os.ReadFile(filepath.Join(dir, "log.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(log), "dir="+dir+" kc="+os.DevNull)
	assert.Contains(t, string(log), "== cleanup\ncleaned\n")
	assert.FileExists(t, filepath.Join(dir, "out"))

	st = Step{ID: "c", Clusters: 1, Script: `echo "kc=$KUBECONFIG"; false; echo never`}
	err = b.Run(context.Background(), st, dir)
	require.Error(t, err, "set -e: a failing command fails the step")
	log, _ = os.ReadFile(filepath.Join(dir, "log.txt"))
	assert.Contains(t, string(log), "kc="+filepath.Join(dir, "kubeconfig"), "a cluster step's own kubeconfig, never the default")
	assert.NotContains(t, string(log), "never")

	// An interrupt stops the whole process group, a child included.
	ctx, cancel := context.WithCancel(context.Background())
	st = Step{ID: "k", Script: `sleep 60 & echo "child $!" > "$STEP_DIR/child"; wait`}
	go func() {
		for !fileExists(filepath.Join(dir, "child")) {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	require.Error(t, b.Run(ctx, st, dir))
	assert.Less(t, time.Since(start), 30*time.Second)
}

// ── the report ───────────────────────────────────────────────────────────────

const goTestJSON = `{"Action":"run","Test":"TestA"}
{"Action":"pass","Test":"TestA","Elapsed":1}
{"Action":"pass","Test":"TestA/sub","Elapsed":1}
{"Action":"fail","Test":"TestB","Elapsed":1}
{"Action":"skip","Test":"TestC","Elapsed":0}
{"Action":"pass","Package":"p","Elapsed":3}
not json
`

func TestCountTests(t *testing.T) {
	got, err := countTests(strings.NewReader(goTestJSON))
	require.NoError(t, err)
	assert.Equal(t, Totals{Run: 4, Passed: 2, Failed: 1, Skipped: 1}, got)
	got, err = countTests(strings.NewReader(goTestJSON + `{"Action":"fail","Package":"p"}` + "\n"))
	require.NoError(t, err)
	assert.Equal(t, Totals{Run: 4, Passed: 2, Failed: 2, Skipped: 1, PackageFailed: 1}, got, "a package-level fail counts")
}

func TestVerdict(t *testing.T) {
	pass := Result{Status: statusPass}
	assert.Equal(t, "GO", verdict([]Result{pass, pass}))
	assert.Equal(t, "INCOMPLETE", verdict([]Result{pass, {Status: statusNotRun}}))
	assert.Equal(t, "NO-GO", verdict([]Result{pass, {Status: statusNotRun}, {Status: statusFail}}))
	assert.Equal(t, "NO-GO", verdict([]Result{pass, {Status: statusMissing}}))
}

func TestRender(t *testing.T) {
	out := t.TempDir()
	steps := []Step{
		{ID: "unit", Area: areaUnit, Title: "unit tests", Tests: "tests.json"},
		{ID: "e2e-core-137", Area: areaE2E, Title: "core on 1.37", Tests: "e2e/test.json"},
		{ID: "upgrade-v0110", Area: areaUpgrade, Title: "upgrade from v0.11.0", Missing: "no suite"},
	}
	require.NoError(t, os.MkdirAll(filepath.Join(out, "unit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, "unit", "tests.json"), []byte(goTestJSON), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(out, "unit", "log.txt"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(out, "e2e-core-137", "e2e"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, "e2e-core-137", "e2e", "test.json"),
		[]byte(`{"Action":"pass","Test":"TestCore_X"}`+"\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(out, "images"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, "images", "images.txt"), []byte("ctl:abc sha256:111 (host build)\n"), 0o644))
	t0 := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.WriteFile(filepath.Join(out, "e2e-core-137", "ci-job.txt"), []byte("https://x/job/1\n"), 0o644))
	results := []Result{
		{ID: "unit", Status: statusFail, Reason: "script: exit status 1", Seconds: 65, Started: t0},
		{ID: "e2e-core-137", Status: statusPass, Seconds: 600, Cached: true, Started: t0},
		{ID: "upgrade-v0110", Status: statusMissing, Reason: "no suite"},
	}
	cov := Coverage{Rows: 10, ByStatus: map[string]int{"covered": 8, "todo": 2}, ByResult: map[string]int{"passed": 7, "not run": 3}}
	p := Provenance{SHA: "0123456789abcdef", Describe: "v0.11.0-5-g0123456", PrevRelease: "v0.11.0", GoVersion: "go1.26.9",
		Tools: [][2]string{{"kro", "v0.10.0-rc.0"}, {"KIND_NODE_1_37", "kindest/node:v1.37.0@sha256:c"}}, Images: readImages(out)}
	var b bytes.Buffer
	require.NoError(t, Render(&b, steps, results, out, cov, p, 90*time.Minute))
	s := b.String()
	assert.Contains(t, s, "# Release gate: 0123456789ab\n")
	assert.Contains(t, s, "**Verdict: NO-GO.** `0123456789abcdef` (v0.11.0-5-g0123456)")
	assert.Contains(t, s, "| Live e2e | PASS | 1 of 1 | 1 | 1 | 0 | 0 | 10m0s |")
	assert.Contains(t, s, "| Upgrade | FAIL | 0 of 1 | 0 | 0 | 0 | 0 | - |")
	assert.Contains(t, s, "| Unit, fuzz and scenarios | FAIL | 0 of 1 | 4 | 2 | 1 | 1 | 1m5s |")
	assert.Contains(t, s, "| **Total** | **NO-GO** | | **5** | **3** | **1** | **1** | 1h30m0s wall |")
	assert.Contains(t, s, "| unit tests | Unit, fuzz and scenarios | FAIL: script: exit status 1 | 2/1/1 | 1m5s | [log](unit/log.txt) |")
	assert.Contains(t, s, "| core on 1.37 | Live e2e | PASS (cached) | 1/0/0 | 10m0s | [CI job](https://x/job/1) |")
	assert.Contains(t, s, "| upgrade from v0.11.0 | Upgrade | MISSING: no suite |")
	assert.Contains(t, s, "10 rows, 8 covered and 2 not (8 covered, 2 todo)")
	assert.Contains(t, s, "): 3 not run, 7 passed.")
	assert.Contains(t, s, "- Image trivy scanned: `ctl:abc sha256:111 (host build)`")
	assert.Contains(t, s, "- KIND_NODE_1_37: `kindest/node:v1.37.0@sha256:c`")
	assert.Contains(t, s, "- Previous release: v0.11.0")
}

func TestReadCoverage(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "test", "e2e"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "test", "e2e", "coverage.tsv"),
		[]byte("id\ttier\tsuite\tstatus\nA\tlive\tcore\tcovered\nB\tunit\t-\ttodo\n"), 0o644))
	proof := filepath.Join(root, "proof.json")
	require.NoError(t, os.WriteFile(proof, []byte(`[{"id":"A","result":"passed"},{"id":"B","result":"todo"}]`), 0o644))
	c, err := readCoverage(root, proof)
	require.NoError(t, err)
	assert.Equal(t, 2, c.Rows)
	assert.Equal(t, map[string]int{"covered": 1, "todo": 1}, c.ByStatus)
	assert.Equal(t, map[string]int{"passed": 1, "todo": 1}, c.ByResult)
	c, err = readCoverage(root, filepath.Join(root, "none.json"))
	require.NoError(t, err)
	assert.Empty(t, c.ByResult)
}

func TestGateRun_Slots(t *testing.T) {
	slots := 0
	h := &fakeHost{slots: &slots}
	r := &fakeRunner{}
	g := newGate(t, 4, h, r)
	go func() { time.Sleep(30 * time.Millisecond); h.mu.Lock(); slots = 1; h.mu.Unlock() }()
	_, err := g.Run(context.Background(), []Step{{ID: "a", Clusters: 1, Script: "x"}, {ID: "b", Clusters: 1, Script: "x"}})
	require.NoError(t, err)
	assert.Equal(t, 1, r.maxClusters, "one host slot: one cluster at a time, whatever the budget")
	assert.Equal(t, 1, slots, "the slot is given back")
}

func TestGateRun_CIStepsRunTogether(t *testing.T) {
	r := &fakeRunner{block: make(chan struct{})}
	var started atomic.Int32
	r.onStart = func(string) {
		if started.Add(1) == 3 {
			close(r.block)
		}
	}
	j := &ciJob{ID: "x", Suite: "core", Minor: "1.37", Shard: "-"}
	steps := []Step{{ID: "a", CIJob: j}, {ID: "b", CIJob: j}, {ID: "c", CIJob: j}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := newGate(t, 1, &fakeHost{}, r).Run(ctx, steps)
	require.NoError(t, err, "the three wait for CI at once (a serial run would block)")
}

func TestMachineSlots(t *testing.T) {
	dir := t.TempDir()
	a := &machine{slotsDir: dir, slotMax: 3, owner: "a"}
	b := &machine{slotsDir: dir, slotMax: 3, owner: "b"}
	relA, ok, _ := a.Slots(2)
	require.True(t, ok)
	_, ok, why := b.Slots(2)
	assert.False(t, ok, "one slot left")
	assert.Contains(t, why, "fewer than 2 of the host's 3 cluster slots")
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 2, "a failed take leaves no slot behind")
	relB, ok, _ := b.Slots(1)
	require.True(t, ok)
	owner, err := os.ReadFile(filepath.Join(dir, "3", "owner"))
	require.NoError(t, err)
	assert.Equal(t, "b\n", string(owner))
	// a's slot 2 was cleared by hand and taken by b: a's release leaves it.
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "2")))
	relB2, ok, _ := b.Slots(1)
	require.True(t, ok)
	relA()
	entries, _ = os.ReadDir(dir)
	assert.Len(t, entries, 2, "a gives back its own slot, not the two b holds")
	relB2()
	relB()
	entries, _ = os.ReadDir(dir)
	assert.Empty(t, entries)
	none, ok, _ := (&machine{}).Slots(5)
	assert.True(t, ok, "no slots directory: no limit")
	none()
}

// fakeGH answers gh calls from a script of responses keyed by the call.
type fakeGH struct {
	mu    sync.Mutex
	calls []string
	reply func(call string) ([]byte, error)
}

func (f *fakeGH) gh(_ context.Context, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	return f.reply(call)
}

func (f *fakeGH) called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// fakeCI is a GitHub with one e2e-live.yml run (9) on commit abc, whose job
// "core on Kubernetes 1.37" (55) fails until it is re-run.
type fakeCI struct {
	t                   *testing.T
	views, runViews     atomic.Int32
	mu                  sync.Mutex
	conclusion, jobName string
	artifact            bool // the job's attempt-1 artifact exists
}

func (f *fakeCI) reply(call string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(call, "api repos/o/r/commits/br"):
		return []byte("abc\n"), nil
	case strings.HasPrefix(call, "workflow run e2e-live.yml --repo o/r --ref br -f suites=core gitlab -f count=2 -f minors=1.35 1.37"):
		return nil, nil
	case strings.HasPrefix(call, "run list"):
		now := time.Now().UTC().Format(time.RFC3339)
		return []byte(`[{"databaseId":7,"headSha":"other","createdAt":"` + now + `"},{"databaseId":9,"headSha":"abc","createdAt":"` + now + `"}]`), nil
	case call == "run view 9 --repo o/r --json status":
		if f.runViews.Add(1) == 1 {
			return []byte(`{"status":"in_progress"}`), nil
		}
		return []byte(`{"status":"completed"}`), nil
	case strings.HasPrefix(call, "run view 9 --repo o/r --job 55 --log"):
		return []byte("the job's log\n"), nil
	case strings.HasPrefix(call, "run view 9"):
		status := "in_progress"
		if f.views.Add(1) > 1 {
			status = "completed"
		}
		return []byte(`{"attempt":2,"jobs":[{"databaseId":55,"name":"` + f.jobName + `","status":"` + status +
			`","conclusion":"` + f.conclusion + `","url":"https://x/job/55"}]}`), nil
	case strings.HasPrefix(call, "run download 9 --repo o/r -n e2e-images -D "):
		dir := strings.Fields(call)[len(strings.Fields(call))-1]
		require.NoError(f.t, os.MkdirAll(dir, 0o755))
		require.NoError(f.t, os.WriteFile(filepath.Join(dir, "images.txt"), []byte("ghcr.io/x/controller:e2e sha256:777\n"), 0o644))
		return nil, nil
	case strings.HasPrefix(call, "run download 9 --repo o/r -n e2e-live-core-137-attempt-1 -D ") && f.artifact:
		art := strings.Fields(call)[len(strings.Fields(call))-1]
		require.NoError(f.t, os.MkdirAll(filepath.Join(art, "kardinal-e2e-core"), 0o755))
		require.NoError(f.t, os.WriteFile(filepath.Join(art, "kardinal-e2e-core", "test.json"), []byte(goTestJSON), 0o644))
		require.NoError(f.t, os.WriteFile(filepath.Join(art, "kardinal-e2e-core", "summary.json"), []byte("{}"), 0o644))
		return nil, nil
	case strings.HasPrefix(call, "run download 9"):
		return nil, errors.New("no artifact")
	case strings.HasPrefix(call, "run rerun 9 --repo o/r --job 55"):
		f.conclusion = "success"
		f.views.Store(0)
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected gh %s", call)
}

func TestCIRunner(t *testing.T) {
	out := t.TempDir()
	ci := &fakeCI{t: t, conclusion: "failure", jobName: "core on Kubernetes 1.37", artifact: true}
	f := &fakeGH{reply: ci.reply}
	runner := func(out string, sha string) *ciRunner {
		return &ciRunner{gh: f.gh, repo: "o/r", ref: "br", sha: sha, out: out, count: 2, suites: []string{"core", "gitlab"},
			minors: "1.35 1.37", poll: time.Millisecond, jobWait: 50 * time.Millisecond, log: func(string, ...any) {}}
	}
	st := Step{ID: "ci-core-137", CIJob: &ciJob{ID: "core-137", Suite: "core", Minor: "1.37", Shard: "-"}}
	dir := filepath.Join(out, st.ID)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	err := runner(out, "abc").Run(context.Background(), st, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CI job https://x/job/55: failure")
	assert.FileExists(t, filepath.Join(out, "ci-run.json"))
	logText, err := os.ReadFile(filepath.Join(dir, "log.txt"))
	require.NoError(t, err)
	assert.Equal(t, "CI job https://x/job/55: failure\nthe job's log\n", string(logText), "the step's log is the job's")
	assert.FileExists(t, filepath.Join(out, "ci-images", "images.txt"), "the IDs of the images the run's suites ran")

	// A later gate on the commit: the same run, no new dispatch, the failed
	// job re-run once the run has finished, its results fetched.
	st.ciRetry = true
	require.NoError(t, runner(out, "abc").Run(context.Background(), st, dir))
	assert.Equal(t, 1, f.called("workflow run"), "dispatched once per commit")
	assert.Equal(t, 1, f.called("run rerun 9 --repo o/r --job 55"))
	assert.Equal(t, 2, f.called("run view 9 --repo o/r --json status"), "the re-run waited for the whole run to finish")
	assert.FileExists(t, filepath.Join(dir, "e2e", "test.json"), "the attempt-1 artifact, the job's latest")

	// A green job that left no artifact proves nothing.
	ci.mu.Lock()
	ci.artifact = false
	ci.mu.Unlock()
	st.ciRetry = false
	dir2 := filepath.Join(out, "again")
	require.NoError(t, os.MkdirAll(dir2, 0o755))
	err = runner(out, "abc").Run(context.Background(), st, dir2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "left no e2e-live-core-137 artifact")

	// A job the run does not have is not waited for for ever.
	ci.mu.Lock()
	ci.jobName = "renamed"
	ci.mu.Unlock()
	err = runner(out, "abc").Run(context.Background(), st, dir2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `the CI run has no job "core on Kubernetes 1.37"`)

	// The branch is not at the gate's commit: no dispatch.
	err = runner(t.TempDir(), "def").Run(context.Background(), st, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the gate's commit")
	assert.Equal(t, 1, f.called("workflow run"))
}

func TestJobID(t *testing.T) {
	assert.Equal(t, "core-135-1of3", jobID(MatrixLine{"core", "1.35", "1/3"}))
	assert.Equal(t, "upgrade-v0100-130", jobID(MatrixLine{"upgrade-v0100", "1.30", "-"}))
	assert.Equal(t, "core 1/3 on Kubernetes 1.35", ciJob{Suite: "core", Minor: "1.35", Shard: "1/3"}.Name())
}

// An area's time is its span: two overlapping steps take the longer one's.
func TestRenderAreaSpan(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	steps := []Step{{ID: "a", Area: areaE2E, Title: "a"}, {ID: "b", Area: areaE2E, Title: "b"}}
	results := []Result{
		{ID: "a", Status: statusPass, Started: t0, Seconds: 3600},
		{ID: "b", Status: statusPass, Started: t0.Add(10 * time.Minute), Seconds: 3600},
	}
	var b bytes.Buffer
	require.NoError(t, Render(&b, steps, results, t.TempDir(), Coverage{ByStatus: map[string]int{}}, Provenance{SHA: "x"}, time.Hour))
	assert.Contains(t, b.String(), "| Live e2e | PASS | 2 of 2 | 0 | 0 | 0 | 0 | 1h10m0s |")
}

// TestGateProof (QA on #1872): exit 0 is not a pass without the evidence: a
// test output with at least MinTests tests and at most MaxSkips skips, and a
// log.
func TestGateProof(t *testing.T) {
	skip := `{"Action":"skip","Test":"TestScenarioMatrix"}` + "\n"
	tests := []struct {
		name, output string
		step         Step
		noLog        bool
		wantReason   string
	}{
		{name: "enough tests", step: Step{ID: "s", Script: "x", Tests: "tests.json", MinTests: 3}, output: passingTests(3)},
		{name: "no test output", step: Step{ID: "s", Script: "x", Tests: "tests.json", MinTests: 1}, output: "\x00none",
			wantReason: "not proven: no test output"},
		{name: "zero tests", step: Step{ID: "s", Script: "x", Tests: "tests.json", MinTests: 1}, output: "",
			wantReason: "0 tests ran, fewer than the 1"},
		{name: "fewer than the minimum", step: Step{ID: "s", Script: "x", Tests: "tests.json", MinTests: 40}, output: passingTests(39),
			wantReason: "39 tests ran, fewer than the 40"},
		{name: "everything skipped", step: Step{ID: "s", Script: "x", Tests: "tests.json", MinTests: 1}, output: skip,
			wantReason: "1 tests skipped, more than the 0 allowed"},
		{name: "allowed skips", step: Step{ID: "s", Script: "x", Tests: "tests.json", MinTests: 1, MaxSkips: 1}, output: passingTests(2) + skip},
		{name: "no log", step: Step{ID: "s", Script: "x"}, noLog: true, wantReason: "not proven: no log"},
		// QA on #1872: TestMain doing m.Run(); os.Exit(0) after a t.Errorf.
		{name: "a failed test with exit 0", step: Step{ID: "s", Script: "x", Tests: "tests.json", MinTests: 1},
			output:     passingTests(48) + `{"Action":"fail","Test":"TestBroken"}` + "\n",
			wantReason: "not proven: the test output records 1 failures (0 of them package-level)"},
		{name: "a package-level failure with exit 0", step: Step{ID: "s", Script: "x", Tests: "tests.json", MinTests: 1},
			output:     passingTests(5) + `{"Action":"fail","Package":"p"}` + "\n",
			wantReason: "records 1 failures (1 of them package-level)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{output: map[string]string{}, noLog: tt.noLog}
			if tt.output != "\x00none" {
				r.output["s"] = tt.output
			} else {
				r.output["s"] = "\x00none"
			}
			results, err := newGate(t, 1, &fakeHost{}, r).Run(context.Background(), []Step{tt.step})
			require.NoError(t, err)
			if tt.wantReason == "" {
				assert.Equal(t, statusPass, results[0].Status, results[0].Reason)
				return
			}
			assert.Equal(t, statusFail, results[0].Status)
			assert.Contains(t, results[0].Reason, tt.wantReason)
		})
	}
}

// TestGateCache (QA on #1872): a pass is reused only on the same commit, a
// clean tree then and now, the same key (scripts, thresholds, inputs), and
// with its evidence still there.
func TestGateCache(t *testing.T) {
	st := Step{ID: "fuzz", Script: "fuzz for 10s", Tests: "tests.json", MinTests: 1}
	r := &fakeRunner{}
	g := newGate(t, 1, &fakeHost{}, r)
	g.Inputs = "fuzztime=10s"
	run := func() Result {
		t.Helper()
		res, err := g.Run(context.Background(), []Step{st})
		require.NoError(t, err)
		return res[0]
	}
	assert.Equal(t, statusPass, run().Status)
	assert.True(t, run().Cached, "same commit, inputs and evidence: reused")
	assert.Len(t, r.ran, 1)

	g.Inputs = "fuzztime=5m"
	assert.False(t, run().Cached, "other inputs: run again")
	assert.Len(t, r.ran, 2)
	st.Script = "fuzz for 5m"
	assert.False(t, run().Cached, "another script: run again")
	assert.Len(t, r.ran, 3)

	require.NoError(t, os.Remove(filepath.Join(g.Out, "fuzz", "tests.json")))
	assert.False(t, run().Cached, "its evidence is gone: run again")
	require.NoError(t, os.Remove(filepath.Join(g.Out, "fuzz", "log.txt")))
	res := resultOf(g, st)
	assert.Equal(t, statusFail, res.Status, "a saved pass whose log is gone is not shown as a pass")
	assert.Contains(t, res.Reason, "not proven: no log")
	assert.Len(t, r.ran, 4)

	// A dirty tree reuses nothing, and its results are never reused.
	g.Dirty = true
	assert.False(t, run().Cached)
	assert.True(t, run().Dirty)
	assert.Len(t, r.ran, 6, "every dirty run runs")
	g.Dirty = false
	res = resultOf(g, st)
	assert.Equal(t, statusNotRun, res.Status, "a dirty pass does not stand for the clean tree")
	assert.Contains(t, res.Reason, "uncommitted changes")
	assert.False(t, run().Cached, "the clean tree runs it")
	assert.Len(t, r.ran, 7)
	g.Dirty = true
	assert.False(t, run().Cached, "a clean pass is not reused on a dirty tree either")
}

// TestVerdictCoverage (QA on #1872): the coverage proof decides with the steps.
func TestVerdictCoverage(t *testing.T) {
	pass := []Result{{Status: statusPass}}
	ok := Coverage{Unproven: map[string]string{}}
	assert.Equal(t, "GO", verdictOf(pass, ok))
	assert.Equal(t, "NO-GO", verdictOf(pass, Coverage{ProofError: "no live suite ran"}), "no proof is no pass")
	unproven := Coverage{Unproven: map[string]string{"CORE-01": "not run", "SCALE-01": "failed"}}
	assert.Equal(t, "NO-GO", verdictOf(pass, unproven))
	assert.Equal(t, []string{"CORE-01", "SCALE-01"}, unproven.Blocking())
	unproven.Allowed = map[string]string{"CORE-01": "needs a token", "SCALE-01": "the host is too small"}
	assert.Equal(t, "GO", verdictOf(pass, unproven), "every unproven row allowed, with its reason")
	assert.Equal(t, "NO-GO", verdictOf([]Result{{Status: statusFail}}, ok))

	var b bytes.Buffer
	require.NoError(t, Render(&b, nil, nil, t.TempDir(), Coverage{ProofError: "no live suite ran", ByStatus: map[string]int{}}, Provenance{SHA: "x"}, 0))
	assert.Contains(t, b.String(), "**Verdict: NO-GO.**")
	assert.Contains(t, b.String(), "**No coverage proof: no live suite ran.**")
	b.Reset()
	require.NoError(t, Render(&b, nil, nil, t.TempDir(), Coverage{ByStatus: map[string]int{}, Unproven: map[string]string{"CORE-01": "not run"}}, Provenance{SHA: "x"}, 0))
	assert.Contains(t, b.String(), "**1 covered rows are not proven** (NO-GO): CORE-01 (not run).")

	dir := t.TempDir()
	allow := filepath.Join(dir, "allow.txt")
	require.NoError(t, os.WriteFile(allow, []byte("# comment\nCORE-01 the github token is not on this host\n"), 0o600))
	got, err := readAllowlist(allow)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"CORE-01": "the github token is not on this host"}, got)
	require.NoError(t, os.WriteFile(allow, []byte("CORE-01\n"), 0o600))
	_, err = readAllowlist(allow)
	assert.ErrorContains(t, err, "has no reason")
}

// TestHostRules (QA on #1872): heavy steps need the lock and cluster steps
// the host's slots, unless the run opts out by name.
func TestHostRules(t *testing.T) {
	heavy, cluster := Step{ID: "unit-race", Script: "x", Heavy: true}, Step{ID: "e2e", Script: "x", Clusters: 1}
	assert.ErrorContains(t, hostRulesProblem([]Step{heavy}, "", "", false, false), "-lock DIR")
	assert.NoError(t, hostRulesProblem([]Step{heavy}, "/l", "", false, false))
	assert.NoError(t, hostRulesProblem([]Step{heavy}, "", "", true, false))
	assert.ErrorContains(t, hostRulesProblem([]Step{cluster}, "", "", false, false), "-slots DIR")
	assert.NoError(t, hostRulesProblem([]Step{cluster}, "", "/s", false, false))
	assert.NoError(t, hostRulesProblem([]Step{cluster}, "", "", false, true))
	assert.NoError(t, hostRulesProblem([]Step{{ID: "m", Missing: "x", Heavy: true}}, "", "", false, false), "a step that never runs needs nothing")
}
