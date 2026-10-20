// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	// The PromQL tests of test/helm run, never skip, in the gate (#2037).
	assert.Contains(t, ids["unit-race"].Script, "bash hack/ci/install-promtool.sh\n")
	assert.Contains(t, ids["unit-race"].Script, "KARDINAL_REQUIRE_PROMTOOL=1 go test -race")
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
	assert.Contains(t, ids["receipts-burst-full"].Missing, "no receipts-keyless suite", "fakeRepo has no keyless suite")
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

	// LoadRepo checked every row and job not-run.txt cites; it names the six
	// categories QA-2's v0.13 gate audit found the gate never runs (#2303),
	// one line each. A line added or removed changes this list too.
	categories := []struct {
		name  string
		words []string
	}{
		{"cloud KMS keys", []string{"AWS KMS", "Google Cloud KMS"}},
		{"EKS", []string{"EKS"}},
		{"the public-good sigstore", []string{"Fulcio", "Rekor", "TUF"}},
		{"ticketing vendors", []string{"ServiceNow", "Jira"}},
		{"publishing", []string{"ghcr.io", "cosign", "SLSA", "SBOM"}},
		{"other platforms", []string{"darwin/amd64", "darwin/arm64", "windows/amd64", "linux/arm64"}},
	}
	require.Len(t, r.NotRunItems, len(categories), "hack/release-gate/not-run.txt: one line per category")
	for _, c := range categories {
		n := 0
		for _, it := range r.NotRunItems {
			all := true
			for _, w := range c.words {
				all = all && strings.Contains(it.Item, w)
			}
			if all {
				n++
			}
		}
		assert.Equal(t, 1, n, "one not-run.txt line names %s (%v)", c.name, c.words)
	}
	rows, err := coverageRows(root)
	require.NoError(t, err)
	for _, it := range r.NotRunItems {
		for _, e := range it.Evidence {
			if e.Row != "" {
				assert.Equal(t, "covered", rows[e.Row].status, "%s cites %s", it.Item, e.Row)
			}
		}
	}
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

func (h *fakeHost) LockExclusive(ctx context.Context) error { return h.Lock(ctx) }

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
	onEnd                  func(id string) // when the step's run returns
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
	if f.onEnd != nil {
		f.onEnd(st.ID)
	}
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
	}
	// A step runs from its start to the return of its run: no timer guesses
	// when it ended (#2015).
	r.onEnd = func(id string) {
		mu.Lock()
		defer mu.Unlock()
		delete(running, id)
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
	a := &machine{lockDir: dir, owner: "a", lockRetry: 5 * time.Millisecond, log: func(string, ...any) {}, lockLoad: busy}
	b := &machine{lockDir: dir, owner: "b", lockRetry: 5 * time.Millisecond, log: func(string, ...any) {}, lockLoad: busy}
	require.NoError(t, a.Lock(context.Background()))
	owner, err := os.ReadFile(filepath.Join(dir, "owner"))
	require.NoError(t, err)
	assert.Regexp(t, `^a boot=\S+ start=\d+ priority=normal beat=1 ticket=1-\d{19}-\d+\n$`, string(owner), "the owner, with its process, class and ticket")

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
	assert.True(t, strings.HasPrefix(string(owner), "a "), "%s", owner)
	a.Unlock()
	require.NoError(t, <-got, "b gets it once a lets go")
	owner, err = os.ReadFile(filepath.Join(dir, "owner"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(owner), "b "), "%s", owner)

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
	notRun := []NotRunItem{
		{Item: "Real KMS signing", Reason: "no cloud account", Evidence: []Evidence{{Row: "RECEIPT-SIGN-06", Tier: "contract"}, {Job: "dry-run"}}},
		{Item: "Public-good sigstore", Reason: "a private sigstore only", Evidence: []Evidence{{Row: "RECEIPT-LIVE-05", Tier: "live"}}},
	}
	var b bytes.Buffer
	require.NoError(t, Render(&b, steps, results, out, cov, notRun, p, 90*time.Minute))
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
	assert.Contains(t, s, "by `make release-gate`. It does not prove the 2 items [not run by this gate](#not-run-by-this-gate).\n")
	assert.Contains(t, s, "\n## Not run by this gate\n\n")
	assert.Contains(t, s, "A contract row is a unit or contract test, not a live run")
	assert.Contains(t, s, "| Not run | Why | Evidence instead |\n|---|---|---|\n"+
		"| Real KMS signing | no cloud account | RECEIPT-SIGN-06 (contract), release.yml job `dry-run` |\n"+
		"| Public-good sigstore | a private sigstore only | RECEIPT-LIVE-05 (live) |\n")
	assert.Less(t, strings.Index(s, "## Coverage"), strings.Index(s, "## Not run by this gate"))
	assert.Less(t, strings.Index(s, "## Not run by this gate"), strings.Index(s, "## Provenance"))

	b.Reset()
	require.NoError(t, Render(&b, steps, results, out, cov, nil, p, 90*time.Minute))
	assert.Contains(t, b.String(), "\n## Not run by this gate\n\n**The not-run list was not loaded**: this report says nothing about what the gate does not run.\n",
		"readNotRun refuses an empty list, so no list means it was not loaded: the section says so, not that nothing is missing")
	assert.NotContains(t, b.String(), "It does not prove")
}

// notRunRoot is a checkout with a coverage.tsv, a release.yml and the given
// not-run.txt.
func notRunRoot(t *testing.T, notRun string) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range map[string]string{
		"test/e2e/coverage.tsv": "id\ttier\tsuite\tstatus\n" +
			"SIGN-01\tcontract\tunit\tcovered\nLIVE-01\tlive\tcore\tcovered\nTODO-01\tlive\tcore\ttodo\n",
		".github/workflows/release.yml": "on:\n  push: {}\njobs:\n  preflight:\n    runs-on: x\n  dry-run:\n    runs-on: x\n",
		"hack/release-gate/not-run.txt": notRun,
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(data), 0o644))
	}
	return root
}

// TestLoadRepoStopsOnABrokenNotRunList: LoadRepo, where the gate starts,
// returns readNotRun's error, so a broken not-run.txt stops the gate before
// it runs anything instead of leaving the report's section empty.
func TestLoadRepoStopsOnABrokenNotRunList(t *testing.T) {
	root := func(notRun string) string {
		t.Helper()
		r := notRunRoot(t, notRun)
		for name, data := range map[string]string{
			"hack/e2e/matrix.txt":    "core 1.37 - -\n",
			"hack/tool-versions.env": "KIND_NODE_1_37=kindest/node:v1.37.0\n",
		} {
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(r, name)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(r, name), []byte(data), 0o644))
		}
		for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "x"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = r
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
		}
		return r
	}
	r, err := LoadRepo(root("Real KMS | no cloud account | SIGN-01\n"))
	require.NoError(t, err)
	require.Len(t, r.NotRunItems, 1)
	for _, tc := range []struct{ name, notRun, err string }{
		{"malformed", "Real KMS | SIGN-01\n", "want item | reason | evidence"},
		{"a missing row", "Real KMS | no cloud account | SIGN-02\n", "SIGN-02, which is not a row"},
		{"empty", "# nothing\n", "lists nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadRepo(root(tc.notRun))
			assert.ErrorContains(t, err, tc.err)
		})
	}
}

// TestReadNotRun: every line names an item, a reason and evidence that
// exists, or the gate does not start (QA-2's v0.13 gate audit, #2303).
func TestReadNotRun(t *testing.T) {
	got, err := readNotRun(notRunRoot(t, "# comment\n\nReal KMS | no cloud account | SIGN-01, release.yml:dry-run\nEKS|no AWS|LIVE-01\n"))
	require.NoError(t, err)
	assert.Equal(t, []NotRunItem{
		{Item: "Real KMS", Reason: "no cloud account", Evidence: []Evidence{{Row: "SIGN-01", Tier: "contract"}, {Job: "dry-run"}}},
		{Item: "EKS", Reason: "no AWS", Evidence: []Evidence{{Row: "LIVE-01", Tier: "live"}}},
	}, got)

	for _, empty := range []string{"", "# nothing\n\n   \n"} {
		_, err = readNotRun(notRunRoot(t, empty))
		assert.ErrorContains(t, err, "not-run.txt lists nothing", "an empty list would read as proof of everything")
	}

	for _, tc := range []struct{ name, line, err string }{
		{"no reason", "Real KMS |  | SIGN-01", "not-run.txt:1: Real KMS has no reason"},
		{"no evidence", "Real KMS | no cloud account | ", "Real KMS has no evidence"},
		{"only commas for evidence", "Real KMS | no cloud account | , ,", "Real KMS has no evidence"},
		{"no item", " | no cloud account | SIGN-01", "not-run.txt:1: no item"},
		{"two fields", "Real KMS | SIGN-01", "want item | reason | evidence, got 2 fields"},
		{"a | in a field", "Real KMS | no cloud | account | SIGN-01", "got 4 fields"},
		{"a row coverage.tsv does not have", "Real KMS | no cloud account | SIGN-02", "Real KMS cites SIGN-02, which is not a row of test/e2e/coverage.tsv"},
		{"a row that is not covered", "Real KMS | no cloud account | TODO-01", "Real KMS cites TODO-01, whose status is todo, not covered"},
		{"a job release.yml does not have", "Real KMS | no cloud account | release.yml:publish", "Real KMS cites release.yml job publish, which .github/workflows/release.yml does not have"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readNotRun(notRunRoot(t, tc.line+"\n"))
			assert.ErrorContains(t, err, tc.err)
		})
	}

	root := notRunRoot(t, "")
	require.NoError(t, os.Remove(filepath.Join(root, "hack", "release-gate", "not-run.txt")))
	_, err = readNotRun(root)
	assert.ErrorContains(t, err, "read the gate's not-run list", "no list is no claim about what the gate does not run")
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
	assert.Contains(t, why, "2 cluster slots are in use in "+dir+", and the host has 3")
	held, err := heldSlots(dir)
	require.NoError(t, err)
	assert.Equal(t, 2, held, "a failed take leaves no slot behind")
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
	held, err = heldSlots(dir)
	require.NoError(t, err)
	assert.Equal(t, 2, held, "a gives back its own slot, not the two b holds")
	relB2()
	relB()
	held, err = heldSlots(dir)
	require.NoError(t, err)
	assert.Zero(t, held)
	tickets, _ := os.ReadDir(slotQueue(dir))
	assert.Empty(t, tickets, "a take gives its queue ticket back")
	none, ok, _ := (&machine{}).Slots(5)
	assert.True(t, ok, "no slots directory: no limit")
	none()
}

// TestMachineSlots_Queue: the gate takes cluster slots first come, first
// served with hack/e2e/local-live.sh (DIR/.queue): it waits behind an older
// live waiter's ticket, a release gate's ticket goes first, a gone waiter's
// ticket is removed, and a take or the run's end gives the ticket back.
func TestMachineSlots_Queue(t *testing.T) {
	dir := t.TempDir()
	q := slotQueue(dir)
	require.NoError(t, os.MkdirAll(q, 0o755))
	live := fmt.Sprintf("ENGINEER-1 local-live gitea 1.37 pid %d now %s", os.Getpid(), selfRecord(hostProc{}))
	// Older by a minute (one older than agePromote would sort as a release one).
	older := filepath.Join(q, fmt.Sprintf("1-%019d-%d", time.Now().Add(-time.Minute).UnixNano(), os.Getpid()))
	require.NoError(t, os.WriteFile(older, []byte(live+"\n"), 0o644))

	m := &machine{slotsDir: dir, slotMax: 2, owner: fmt.Sprintf("gate pid %d now", os.Getpid())}
	_, ok, why := m.Slots(1)
	assert.False(t, ok, "an older waiter is first")
	assert.Contains(t, why, "waits behind "+filepath.Base(older))
	ticket := m.slotTicket
	require.FileExists(t, filepath.Join(q, ticket), "the gate keeps its place")
	_, ok, _ = m.Slots(1)
	assert.False(t, ok)
	assert.Equal(t, ticket, m.slotTicket, "the same ticket, however often it asks")

	rel := &machine{slotsDir: dir, slotMax: 2, owner: fmt.Sprintf("release gate pid %d now", os.Getpid()), lockClass: classRelease}
	release, ok, why := rel.Slots(1)
	require.True(t, ok, "a release gate's ticket goes first: %s", why)
	release()

	require.NoError(t, os.Remove(older))
	release, ok, why = m.Slots(1)
	require.True(t, ok, why)
	release()
	assert.NoFileExists(t, filepath.Join(q, ticket), "a take gives the ticket back")

	gone := exec.Command("true")
	require.NoError(t, gone.Run())
	require.NoError(t, os.WriteFile(filepath.Join(q, fmt.Sprintf("1-%019d-%d", 2, gone.Process.Pid)), []byte("gone\n"), 0o644))
	release, ok, why = m.Slots(1)
	require.True(t, ok, "a gone waiter's ticket is removed: %s", why)
	release()

	require.NoError(t, os.WriteFile(older, []byte(live+"\n"), 0o644))
	_, ok, _ = m.Slots(1)
	require.False(t, ok)
	ticket = m.slotTicket
	m.DropSlotTicket()
	assert.NoFileExists(t, filepath.Join(q, ticket), "the run's end gives the ticket back")
	entries, _ := os.ReadDir(q)
	assert.Len(t, entries, 1, "only the older waiter's")
}

// TestMachineSlots_AboveSlotMaxCount (#2244): a slot directory above
// slot-max (a run with a larger SLOT_MAX took it) is never taken, and counts:
// the host runs slot-max clusters at most.
func TestMachineSlots_AboveSlotMaxCount(t *testing.T) {
	dir := t.TempDir()
	m := &machine{slotsDir: dir, slotMax: 2, owner: "gate"}
	for _, n := range []string{"3", "4"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, n), 0o755))
	}
	_, ok, why := m.Slots(1)
	assert.False(t, ok, "slots 1 and 2 are free, but two clusters run")
	assert.Contains(t, why, "2 cluster slots are in use")
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "4")))
	release, ok, why := m.Slots(1)
	require.True(t, ok, why)
	assert.DirExists(t, filepath.Join(dir, "1"), "slot 1, never one above slot-max")
	release()
	_, ok, _ = m.Slots(2)
	assert.False(t, ok, "slot 3 and two more would be three")
	assert.NoDirExists(t, filepath.Join(dir, "1"))

	// The host's slots: $SLOT_MAX, else SLOT_MAX=N in DIR/../host.env,
	// else 2; -slot-max over all; 1 to 4 from every source, or refused.
	host := t.TempDir()
	slots := filepath.Join(host, "slots")
	hostEnv := filepath.Join(host, "host.env")
	got := func(flagged int) (int, error) { return slotMaxOr(flagged, slots) }
	wantSlots := func(want, flagged int, why string) {
		t.Helper()
		n, err := got(flagged)
		require.NoError(t, err, why)
		assert.Equal(t, want, n, why)
	}
	refused := func(flagged int, want string) {
		t.Helper()
		_, err := got(flagged)
		require.Error(t, err)
		assert.Contains(t, err.Error(), want)
	}
	t.Setenv("SLOT_MAX", "")
	wantSlots(2, 0, "the host's default, as local-live.sh's")
	n, err := slotMaxDefault("")
	require.NoError(t, err)
	assert.Equal(t, 2, n, "no slots directory")
	require.NoError(t, os.WriteFile(hostEnv, []byte("# the host's capacity\nSLOT_MAX=3\r\n"), 0o644))
	wantSlots(3, 0, "host.env")
	n, err = slotMaxDefault(slots + "/")
	require.NoError(t, err)
	assert.Equal(t, 3, n, "a slots directory with a trailing slash")
	wantSlots(4, 4, "-slot-max over host.env")
	t.Setenv("SLOT_MAX", "1")
	wantSlots(1, 0, "$SLOT_MAX over host.env")
	for _, bad := range []string{"5", "999", "99999999999999999999", "abc", "0", "03", "-1", " 3"} {
		t.Setenv("SLOT_MAX", bad)
		refused(0, "SLOT_MAX="+bad+" ($SLOT_MAX): the host has 1 to 4 cluster slots")
	}
	t.Setenv("SLOT_MAX", "")
	for _, bad := range []string{"5", "999", "99999999999999999999", "abc", "0", "03", "-1", " 3"} {
		require.NoError(t, os.WriteFile(hostEnv, []byte("SLOT_MAX="+bad+"\n"), 0o644))
		refused(0, "SLOT_MAX="+bad+" ("+hostEnv+"): the host has 1 to 4 cluster slots")
	}
	for _, bad := range []int{5, 999, -1} {
		refused(bad, fmt.Sprintf("-slot-max %d: the host has 1 to 4 cluster slots", bad))
	}
	require.NoError(t, os.WriteFile(hostEnv, []byte("slot_max=9\n"), 0o644))
	wantSlots(2, 0, "another key is not SLOT_MAX")
	if os.Geteuid() != 0 {
		require.NoError(t, os.WriteFile(hostEnv, []byte("SLOT_MAX=3\n"), 0o644))
		require.NoError(t, os.Chmod(hostEnv, 0o000))
		wantSlots(2, 0, "a host.env that cannot be read: the fewest")
	}
}

// TestDrainLive (#2244 QA): a .drain record is judged by local-live.sh's
// record rule: another boot's, a gone pid's, or a reused pid's (another
// start time) is not live.
func TestDrainLive(t *testing.T) {
	proc := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proc, "sys", "kernel", "random"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proc, "sys", "kernel", "random", "boot_id"), []byte("b-now\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(proc, "4242"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proc, "4242", "stat"),
		[]byte("4242 (a (weird) name) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 777 19 20\n"), 0o644))
	for _, tc := range []struct {
		name, record string
		live         bool
	}{
		{"the drainer runs", "E local-live scale 1.37 draining the host for a heavy suite, pid 4242 now boot=b-now start=777", true},
		{"its pid was reused: another start time", "E local-live scale 1.37 draining the host for a heavy suite, pid 4242 now boot=b-now start=776", false},
		{"another boot's", "E local-live scale 1.37 draining the host for a heavy suite, pid 4242 now boot=b-old start=777", false},
		{"its pid is gone", "E local-live scale 1.37 draining the host for a heavy suite, pid 4243 now boot=b-now start=777", false},
		{"an older record: pid only", "E local-live scale 1.37 draining the host, pid 4242 now", true},
		{"no pid", "a person, by hand", true},
		{"this process's", fmt.Sprintf("gate pid %d now", os.Getpid()), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.live, drainLive(tc.record, proc))
		})
	}
}

// TestMachineSlots_HonourTheDrain (#2244 QA): while a heavy local-live
// suite drains the host (DIR/.drain naming a live process), the gate takes
// no cluster slot; a marker of a process that is gone, or of the gate
// itself, does not stop it.
func TestMachineSlots_HonourTheDrain(t *testing.T) {
	dir := t.TempDir()
	m := &machine{slotsDir: dir, slotMax: 4, owner: "gate"}
	sleeper := exec.Command("sleep", "30")
	require.NoError(t, sleeper.Start())
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	gone := exec.Command("true")
	require.NoError(t, gone.Run())
	for _, tc := range []struct {
		name, drain string
		takes       bool
	}{
		{"a live heavy run drains", fmt.Sprintf("ENGINEER-1 local-live scale 1.37 draining the host, pid %d now", sleeper.Process.Pid), false},
		{"a gone heavy run's marker", fmt.Sprintf("ENGINEER-1 local-live scale 1.37 draining the host, pid %d now", gone.Process.Pid), true},
		{"the gate's own marker", fmt.Sprintf("gate pid %d now", os.Getpid()), true},
		{"no marker", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(filepath.Join(dir, ".drain"))
			if tc.drain != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".drain"), []byte(tc.drain+"\n"), 0o644))
			}
			release, ok, why := m.Slots(1)
			assert.Equal(t, tc.takes, ok, why)
			if ok {
				release()
			} else {
				assert.Contains(t, why, "a heavy suite is draining the host")
			}
		})
	}
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
	views, resetAsked   atomic.Int32
	mu                  sync.Mutex
	conclusion, jobName string
	artifact            bool // the job's attempt-1 artifact exists
	runDone             bool // the last view saw the run completed
	limited             int  // views to answer with GitHub's rate-limit error
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
	case call == "api rate_limit --jq .resources.core.reset":
		f.resetAsked.Add(1)
		return []byte(fmt.Sprint(time.Now().Add(time.Second).Unix())), nil
	case strings.HasPrefix(call, "run view 9 --repo o/r --job 55 --log"):
		return []byte("the job's log\n"), nil
	case call == "run view 9 --repo o/r --json status,attempt,jobs":
		if f.limited > 0 {
			f.limited--
			return nil, errors.New("HTTP 403: API rate limit exceeded for user ID 1. (https://api.github.com/graphql)")
		}
		// The job finishes on the second look, the run on the third.
		n := f.views.Add(1)
		status, run := "in_progress", "in_progress"
		if n > 1 {
			status = "completed"
		}
		if n > 2 {
			run = "completed"
		}
		f.runDone = run == "completed"
		return []byte(`{"status":"` + run + `","attempt":2,"jobs":[{"databaseId":55,"name":"` + f.jobName + `","status":"` + status +
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
		if !f.runDone {
			return nil, errors.New("run 9 cannot be rerun; its workflow run is still in progress")
		}
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
			minors: "1.35 1.37", poll: time.Millisecond, jobWait: 50 * time.Millisecond, log: func(string, ...any) {},
			wait: func(context.Context, time.Duration) error { return nil }}
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
	assert.Equal(t, 1, f.called("run rerun 9 --repo o/r --job 55"), "once, after the whole run finished (the fake refuses earlier)")
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
	require.NoError(t, Render(&b, steps, results, t.TempDir(), Coverage{ByStatus: map[string]int{}}, nil, Provenance{SHA: "x"}, time.Hour))
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
	require.NoError(t, Render(&b, nil, nil, t.TempDir(), Coverage{ProofError: "no live suite ran", ByStatus: map[string]int{}}, nil, Provenance{SHA: "x"}, 0))
	assert.Contains(t, b.String(), "**Verdict: NO-GO.**")
	assert.Contains(t, b.String(), "**No coverage proof: no live suite ran.**")
	b.Reset()
	require.NoError(t, Render(&b, nil, nil, t.TempDir(), Coverage{ByStatus: map[string]int{}, Unproven: map[string]string{"CORE-01": "not run"}}, nil, Provenance{SHA: "x"}, 0))
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

// TestCIRunner_OnePollForAllSteps (#2096): 32 CI steps waiting together
// share one gh call per poll interval, not one each; GitHub's rate limit
// (403) and a 502 on that call are waited out (until the limit resets, from
// gh api rate_limit) instead of failing the steps; each step ends on its
// job's conclusion only.
func TestCIRunner_OnePollForAllSteps(t *testing.T) {
	const steps = 32
	var views, resets, limited atomic.Int32
	limited.Store(3)
	reply := func(call string) ([]byte, error) {
		switch call {
		case "api rate_limit --jq .resources.core.reset":
			resets.Add(1)
			return []byte(fmt.Sprint(time.Now().Add(time.Second).Unix())), nil
		case "run view 9 --repo o/r --json status,attempt,jobs":
			if limited.Add(-1) >= 0 {
				return nil, errors.New("HTTP 403: API rate limit exceeded for user ID 1.")
			}
			if limited.Load() == -1 {
				return nil, errors.New("HTTP 502: Bad Gateway (https://api.github.com/graphql)")
			}
			n := views.Add(1)
			var jobs []string
			for i := 0; i < steps; i++ {
				status := "in_progress"
				if n > 4 {
					status = "completed"
				}
				jobs = append(jobs, fmt.Sprintf(`{"databaseId":%d,"name":"core %d/%d on Kubernetes 1.37","status":"%s","conclusion":"failure","url":"https://x/job/%d"}`,
					100+i, i+1, steps, status, 100+i))
			}
			return []byte(`{"status":"in_progress","attempt":1,"jobs":[` + strings.Join(jobs, ",") + `]}`), nil
		}
		return nil, fmt.Errorf("no %s", call) // logs and images: not needed here
	}
	f := &fakeGH{reply: reply}
	out := t.TempDir()
	c := &ciRunner{gh: f.gh, repo: "o/r", ref: "br", sha: "abc", out: out, count: 1, suites: []string{"core"}, minors: "1.37",
		poll: 50 * time.Millisecond, jobWait: time.Minute, log: func(string, ...any) {},
		wait: func(context.Context, time.Duration) error { return nil }}
	c.run = &ciRun{ID: 9, SHA: "abc"}
	var wg sync.WaitGroup
	errs := make([]error, steps)
	for i := 0; i < steps; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st := Step{ID: fmt.Sprintf("ci-core-%d", i), CIJob: &ciJob{ID: fmt.Sprintf("core-137-%dof%d", i+1, steps), Suite: "core",
				Minor: "1.37", Shard: fmt.Sprintf("%d/%d", i+1, steps)}}
			dir := filepath.Join(out, st.ID)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			errs[i] = c.Run(context.Background(), st, dir)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.Error(t, err, "step %d", i)
		assert.Contains(t, err.Error(), fmt.Sprintf("CI job https://x/job/%d: failure", 100+i), "only the job's conclusion fails a step")
	}
	calls := f.called("run view 9 --repo o/r --json status,attempt,jobs")
	assert.Less(t, calls, 20, "one shared look per poll interval: %d calls for %d steps over 5 looks", calls, steps)
	assert.Positive(t, resets.Load(), "the rate limit's reset was read to wait for it")
}

func TestTransient(t *testing.T) {
	for msg, want := range map[string]bool{
		"HTTP 403: API rate limit exceeded for user ID 1.":             true,
		"HTTP 403: You have exceeded a secondary rate limit":           true,
		"HTTP 429: Too Many Requests":                                  true,
		"HTTP 502: Bad Gateway":                                        true,
		"HTTP 503: Service Unavailable":                                true,
		"read tcp 1.2.3.4: connection reset by peer":                   true,
		"HTTP 404: Not Found":                                          false,
		"no valid artifacts found to download":                         false,
		"run 9 cannot be rerun; its workflow run is still in progress": false,
	} {
		assert.Equal(t, want, transient(errors.New(msg)), msg)
	}
}

// TestPlanReceiptBurst (#2062): the soak set runs TestReceiptsKeyless_Burst
// once at full size (500 receipts, Fulcio at 10 rps) on the
// receipts-keyless suite's minor, holding the heavy-load lock; without the
// soak profile there is no such step.
func TestPlanReceiptBurst(t *testing.T) {
	r := fakeRepo()
	r.Matrix = append(r.Matrix, MatrixLine{"receipts-keyless", "1.35", "-"})
	burst := byID(Plan(r, fakeOptions()))["receipts-burst-full"]
	assert.Empty(t, burst.Missing)
	assert.True(t, burst.Heavy)
	for _, want := range []string{"export KARDINAL_E2E_RECEIPT_BURST=500", "export KARDINAL_E2E_RECEIPT_BURST_RPS=10",
		"export RUN='^TestReceiptsKeyless_Burst$'", "KIND_K8S=1.35", "bash hack/e2e/up.sh receipts-keyless"} {
		assert.Contains(t, burst.Script, want)
	}
	assert.Contains(t, burst.Cleanup, "kp-rg-receipts-burst")
	assert.NotContains(t, byID(Plan(r, Options{Count: 2, FuzzTime: "1m", ScaleProfiles: []string{"full"},
		ImageBuild: "host", Floor: 35, Newest: 37})), "receipts-burst-full", "only in the soak set")
}

// TestReleaseTokenProblem (#2282 QA): -priority release needs
// $RELEASE_GATE_PRIORITY_TOKEN to match DIR/.release-token (mode 600) next
// to the lock, else next to the slots directory.
func TestReleaseTokenProblem(t *testing.T) {
	host := t.TempDir()
	lock, slots := filepath.Join(host, "LOADLOCK"), filepath.Join(host, "CLUSTER-SLOTS")
	token := filepath.Join(host, ".release-token")
	t.Setenv("RELEASE_GATE_PRIORITY_TOKEN", "")
	assert.Error(t, releaseTokenProblem(lock, slots), "no token file")
	require.NoError(t, os.WriteFile(token, []byte("s3cret\n"), 0o600))
	assert.Error(t, releaseTokenProblem(lock, slots), "no token")
	t.Setenv("RELEASE_GATE_PRIORITY_TOKEN", "a-guess")
	assert.Error(t, releaseTokenProblem(lock, slots), "a wrong token")
	t.Setenv("RELEASE_GATE_PRIORITY_TOKEN", "s3cret")
	require.NoError(t, releaseTokenProblem(lock, slots), "the token")
	require.NoError(t, releaseTokenProblem("", slots), "next to the slots directory, with no lock")
	require.NoError(t, os.Chmod(token, 0o644))
	err := releaseTokenProblem(lock, slots)
	require.Error(t, err, "a token file others can read")
	assert.Contains(t, err.Error(), "-priority release needs RELEASE_GATE_PRIORITY_TOKEN matching "+token)
	assert.NoError(t, releaseTokenProblem("", ""), "no shared queue to jump")
}
