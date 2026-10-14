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
			{"upgrade-v0100", "1.30", "-"}, {"upgrade-v0100", "1.37", "-"},
		},
		ToolVersions: map[string]string{
			"KIND_NODE_1_35": "kindest/node:v1.35.8@sha256:a", "KIND_NODE_1_36": "kindest/node:v1.36.4@sha256:b",
			"KIND_NODE_1_37": "kindest/node:v1.37.0@sha256:c", "KIND_NODE_UPGRADE_V0100_1_30": "kindest/node:v1.30.13@sha256:d",
			"KIND_VERSION": "v0.33.0",
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

func TestPlan(t *testing.T) {
	opts := Options{Count: 2, FuzzTime: "1m", ScaleProfiles: []string{"full", "soak"}, ImageBuild: "host"}
	steps := Plan(fakeRepo(), opts)
	ids := byID(steps)
	assert.Len(t, ids, len(steps), "step ids are unique")

	for _, id := range []string{"e2e-core-135", "e2e-core-137", "e2e-gitlab-135", "e2e-gitlab-137", "e2e-multi-cluster-136",
		"e2e-upgrade-v0100-130", "e2e-upgrade-v0100-137", "scale-full", "scale-soak", "unit-race", "fuzz-FuzzA",
		"scenario-matrix", "govulncheck", "golangci-lint", "hygiene", "images", "docs", "bundle-size",
		"chart-profiles", "chart-crds-130", "chart-crds-137"} {
		assert.Contains(t, ids, id)
	}
	assert.NotContains(t, ids, "e2e-multi-cluster-135", "multi-cluster runs only on its matrix minor")
	assert.NotContains(t, ids, "e2e-scale-137", "scale runs as its profiles, not as a live suite")

	core := ids["e2e-core-135"]
	assert.Equal(t, areaE2E, core.Area)
	assert.Equal(t, 1, core.Clusters)
	assert.Contains(t, core.Script, "export KIND_CLUSTER=kp-rg-core-135 KIND_K8S=1.35 COUNT=2\n")
	assert.Contains(t, core.Script, "unset KARDINAL_E2E_GITHUB_TOKEN_FILE DEMO_GITHUB_TOKEN", "only the github suite sees a token")
	assert.Contains(t, core.Script, "bash hack/e2e/up.sh core\nbash hack/e2e/run.sh core\n")
	assert.Contains(t, core.Cleanup, "for c in kp-rg-core-135 kp-rg-core-135-spoke; do")
	assert.Equal(t, 2, ids["e2e-gitlab-137"].Clusters)

	up := ids["e2e-upgrade-v0100-130"]
	assert.Equal(t, areaUpgrade, up.Area)
	assert.Contains(t, up.Script, "COUNT=1\n", "the upgrade suite runs once")

	gh := ids["e2e-github-137"]
	assert.Contains(t, gh.NotRun, "never uses your gh login")
	assert.Empty(t, gh.Script)
	withToken := byID(Plan(fakeRepo(), Options{Count: 1, GitHubToken: true}))["e2e-github-137"]
	assert.Empty(t, withToken.NotRun)
	assert.NotContains(t, withToken.Script, "unset KARDINAL_E2E_GITHUB_TOKEN_FILE", "the github suite keeps its token")

	prev := ids["upgrade-v0110"]
	assert.Contains(t, prev.Missing, "nothing tests the upgrade from v0.11.0")
	r := fakeRepo()
	r.Matrix = append(r.Matrix, MatrixLine{"upgrade-v0110", "1.37", "-"})
	assert.NotContains(t, byID(Plan(r, opts)), "upgrade-v0110", "the suite exists: no missing step")
	assert.Contains(t, byID(Plan(r, opts)), "e2e-upgrade-v0110-137")

	assert.Contains(t, ids["scenario-enterprise"].Missing, "test/scenario/enterprise")
	r = fakeRepo()
	r.Enterprise = true
	ent := byID(Plan(r, opts))["scenario-enterprise"]
	assert.Empty(t, ent.Missing)
	assert.True(t, ent.Heavy)
	assert.Contains(t, ent.Script, "KARDINAL_SCENARIO_ENTERPRISE=full")

	full, soak := ids["scale-full"], ids["scale-soak"]
	assert.True(t, full.Heavy && soak.Heavy, "the scale profiles take the heavy-load lock")
	assert.True(t, ids["unit-race"].Heavy)
	assert.Contains(t, full.Script, "KARDINAL_E2E_SCALE_PROFILE=full")
	assert.Contains(t, full.Script, "KIND_K8S=1.37")
	assert.Contains(t, soak.Script, "export RUN='^TestScale_LoadSustained$'")
	assert.NotContains(t, full.Script, "TestScale_LoadSustained")

	assert.Contains(t, ids["fuzz-FuzzA"].Script, "-fuzz '^FuzzA$' -fuzztime 1m")
	assert.Contains(t, ids["unit-race"].Script, "go test -race -count=3")
	assert.Contains(t, ids["images"].Script, "IMAGE_BUILD=host")
	assert.Contains(t, ids["chart-crds-130"].Script, `echo "$KIND_NODE_UPGRADE_V0100_1_30"`, "the floor minor's node image")
	assert.Contains(t, ids["chart-crds-137"].Script, `echo "$KIND_NODE_1_37"`)
	for _, p := range profiles {
		assert.Contains(t, ids["chart-profiles"].Script, "echo '== "+p+"'")
	}

	assert.Equal(t, "upgrade-v0110", upgradeSuite("v0.11.0"))
	assert.Equal(t, "upgrade-v090", upgradeSuite("v0.9.0"))
}

// The plan of this checkout: every matrix suite has a step, every cluster
// step deletes the kp- clusters it creates, and every values profile exists.
func TestPlanOfThisRepo(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	r, err := LoadRepo(root)
	require.NoError(t, err)
	require.NotEmpty(t, r.FuzzTargets)
	steps := Plan(r, Options{Count: 1, FuzzTime: "1m", ScaleProfiles: []string{"full", "soak"}, ImageBuild: "docker"})
	ids := byID(steps)
	assert.Len(t, ids, len(steps), "step ids are unique")
	for _, l := range r.Matrix {
		if l.Suite == "scale" {
			continue
		}
		found := false
		for id := range ids {
			found = found || strings.HasPrefix(id, "e2e-"+l.Suite+"-1")
		}
		assert.True(t, found, "suite %s has a step", l.Suite)
	}
	for _, st := range steps {
		if st.Clusters == 0 || st.Script == "" {
			continue
		}
		assert.Contains(t, st.Script, "KIND_CLUSTER=kp-rg-", st.ID)
		name := strings.Fields(strings.SplitN(st.Script, "KIND_CLUSTER=", 2)[1])[0]
		assert.Contains(t, st.Cleanup, "for c in "+name+" "+name+"-spoke; do", st.ID)
	}
	for _, p := range profiles[1:] {
		assert.FileExists(t, filepath.Join(root, "hack", "release-gate", "profiles", p+".yaml"))
	}
	for _, f := range r.FuzzTargets {
		assert.True(t, strings.HasPrefix(f.Name, "Fuzz") && strings.HasPrefix(f.Dir, "./"), "%+v", f)
	}
}

// ── the scheduler ────────────────────────────────────────────────────────────

type fakeHost struct {
	mu     sync.Mutex
	ready  func() bool
	locked bool
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
	block                  chan struct{}
	onStart                func(id string)
}

func (f *fakeRunner) Run(ctx context.Context, st Step, _ string) error {
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
	results := []Result{
		{ID: "unit", Status: statusFail, Reason: "script: exit status 1", Seconds: 65},
		{ID: "e2e-core-137", Status: statusPass, Seconds: 600, Cached: true},
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
	assert.Contains(t, s, "| core on 1.37 | Live e2e | PASS (cached) | 1/0/0 | 10m0s |  |")
	assert.Contains(t, s, "| upgrade from v0.11.0 | Upgrade | MISSING: no suite |")
	assert.Contains(t, s, "10 rows, 8 covered and 2 not (8 covered, 2 todo)")
	assert.Contains(t, s, "): 3 not run, 7 passed.")
	assert.Contains(t, s, "- Image: `ctl:abc sha256:111 (host build)`")
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
