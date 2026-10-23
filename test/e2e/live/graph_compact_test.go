//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// compactNodeIDs are the node IDs of a compact Graph with gates and no
// skip permissions.
var compactNodeIDs = []string{"bundle", "PolicyGateData", "PolicyGates", "PRStatusData", "PRStatuses",
	"PromotionDAG", "StepsObserved", "PromotionState", "PromotionWave", "PromotionSteps", "PromotionProgress"}

// graphNodeIDs lists the node IDs of Graph g.
func graphNodeIDs(g *unstructured.Unstructured) []string {
	nodes, _, _ := unstructured.NestedSlice(g.Object, "spec", "nodes")
	var ids []string
	for _, n := range nodes {
		ids = append(ids, fmt.Sprint(n.(map[string]interface{})["id"]))
	}
	return ids
}

// TestGraph_CompactShapeSmall checks the compact Graph shape on a whole
// promotion (the Pipeline's kardinal.io/graph-shape: compact): one
// PromotionSteps collection admitted by a def over the DAG, no node per
// environment. uat starts only after test is Verified and waits on its PR;
// prod's gate holds it; the Graph is not Ready until every environment is
// Verified. A newer Bundle supersedes the first, which starts no new
// environment, and the newer one promotes to prod.
//
// Covers GRAPH-COMPACT-01.
func TestGraph_CompactShapeSmall(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "uat", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "needs-open-label", "prod", openExpr, recheck))
	p := a.pipeline(map[string]string{"uat": "pr-review"})
	p.Annotations = map[string]string{graph.AnnotationGraphShape: graph.GraphShapeCompact}
	a.apply(t, p)

	first := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	test := e.WaitStepState(t, a.ns, pipelineName, first, "test", "Verified", promoteTimeout)
	uat := e.WaitStepState(t, a.ns, pipelineName, first, "uat", "WaitingForMerge", promoteTimeout)
	startedAfter(t, uat, verifiedAt(t, test), "uat starts after test is Verified")
	assert.Equal(t, "PromotionSteps", uat.Labels["kro.run/node-id"], "made by the PromotionSteps collection")

	g := bundleGraph(t, e, a.ns, first)
	assert.ElementsMatch(t, compactNodeIDs, graphNodeIDs(g), "compact Graph nodes")
	st, _, msg := graphCondition(g, "Ready")
	assert.NotEqual(t, "True", st, "the Graph is not Ready while uat waits: %s", msg)

	a.merge(t, a.openPR(t, first, "uat"))
	e.WaitStepState(t, a.ns, pipelineName, first, "uat", "Verified", promoteTimeout)
	a.noStep(t, first, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	// A newer Bundle supersedes the first; the first starts nothing more.
	second := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	e.WaitBundlePhase(t, a.ns, first, "Superseded", 2*time.Minute)
	e.SetBundleLabel(t, a.ns, second, openLabel, "true")
	e.WaitStepState(t, a.ns, pipelineName, second, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, second, "uat", "WaitingForMerge", promoteTimeout)
	a.merge(t, a.openPR(t, second, "uat"))
	prod := e.WaitStepState(t, a.ns, pipelineName, second, "prod", "Verified", promoteTimeout)
	assert.Equal(t, []string{"Verified"}, prod.Spec.UpstreamStates)
	assert.Len(t, prod.Spec.RequiredGates, 1)
	assertEnvAt(t, a, "prod", fixtures.V3)
	e.WaitBundlePhase(t, a.ns, second, "Verified", time.Minute)
	a.noStep(t, first, "prod", time.Second)
	framework.Eventually(t, 2*time.Minute, "the second Bundle's Graph to be Ready", func(ctx context.Context) (bool, string) {
		st, reason, msg := graphCondition(bundleGraph(t, e, a.ns, second), "Ready")
		return st == "True", st + " " + reason + ": " + msg
	})
}

// compactHealthDeployment is the Deployment every environment of the
// wavePipeline tests checks. It runs the pause image kind's node
// has: the test measures kardinal and kro, not a GitOps engine syncing 300
// environments.
const compactHealthDeployment = "compact-health"

// TestCompact_Shape300 promotes a Bundle through a Pipeline of 300
// environments (30 waves of 10), each with a PolicyGate, the shape a large
// fleet runs and one the node-per-environment Graph cannot hold (etcd refuses
// it). The Graph is compact automatically (more than 100 environments): about
// a dozen nodes and well under the 1.2 MB guard. Every environment is
// Verified with the new version in git, every gate instance exists and the
// Graph is Ready only at the end.
//
// It and TestCompact_Shape300Batched are the compact suite's tests (#2349,
// #2366): this one takes 15 to 30 minutes on a
// GitHub-hosted runner, nearly all of it in the waves' steps (ten writers
// pushing one branch of a repo that grows by ten commits a wave; the Graph
// reaches the next wave within seconds, logWaveTimes), and in the core suite
// it pushed its shard past the 60-minute job limit. It does not run in
// parallel: committing 300 overlays in one git server request, then 300
// promotions, would slow the git server down for any other test.
//
// Covers GRAPH-COMPACT-02.
func TestCompact_Shape300(t *testing.T) {
	shape300(t, false)
}

// TestCompact_Shape300Batched is TestCompact_Shape300 with spec.git.writes:
// batched (#2366): each wave's ten environments are written by the Bundle's
// BranchWrite in rounds (one clone, one commit per environment, one push)
// instead of ten turns of their own. Every environment is still Verified on
// its own commit, and the wave log says how many rounds wrote each wave, and
// the controller's clones and pushes per round.
//
// Covers GRAPH-COMPACT-09.
func TestCompact_Shape300Batched(t *testing.T) {
	shape300(t, true)
}

// shape300 promotes a Bundle through 30 waves of 10 environments, with
// batched writes or not.
func shape300(t *testing.T, batched bool) {
	e := framework.New(t)
	ctx := context.Background()
	const waves, perWave = 30, 10
	a, p := wavePipeline(t, e, waves, perWave, func(string) string { return "true" })
	if batched {
		p.Spec.Git.Writes = v1alpha1.WritesBatched
	}
	ns, envs := a.ns, a.envs
	// The controller's git counters before and after: what the waves spent
	// on clones, pushes and rebases (#2366). The compact suite runs this
	// test alone, on one controller replica.
	before, beforeErr := e.ControllerMetrics(ctx)
	start := time.Now()
	a.apply(t, p)
	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)

	// Mid-way the Graph is compact, within the guard, and not Ready.
	e.WaitStepState(t, ns, pipelineName, bundle, envs[perWave], "Verified", 10*time.Minute)
	g := bundleGraph(t, e, ns, bundle)
	name := g.GetName()
	wantNodes := compactNodeIDs
	if batched {
		wantNodes = append(append([]string(nil), compactNodeIDs...), graph.NodeBranchWrites, "refBranchWrites")
	}
	assert.ElementsMatch(t, wantNodes, graphNodeIDs(g), "compact Graph nodes")
	st, _, _ := graphCondition(g, "Ready")
	assert.NotEqual(t, "True", st, "the Graph is not Ready mid-way")

	e.WaitBundlePhase(t, ns, bundle, "Verified", 40*time.Minute)
	var steps v1alpha1.PromotionStepList
	require.NoError(t, e.Client.List(ctx, &steps, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}))
	require.Len(t, steps.Items, len(envs))
	for _, s := range steps.Items {
		assert.Equal(t, "Verified", s.Status.State, s.Name)
	}
	logWaveTimes(t, steps.Items, perWave)
	rounds := 0
	if batched {
		rounds = logRounds(t, e, ns, bundle, steps.Items, perWave)
	}
	if after, err := e.ControllerMetrics(ctx); beforeErr == nil && err == nil {
		logGitSpend(t, before, after, rounds)
	} else {
		t.Logf("git spend not measured: controller metrics: before %v, after %v", beforeErr, err)
	}
	logConflicts(t, e, start)
	var gates v1alpha1.PolicyGateList
	require.NoError(t, e.Client.List(ctx, &gates, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}))
	assert.Len(t, gates.Items, len(envs), "one gate instance per environment")
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(envs[len(envs)-1])+"/kustomization.yaml"),
		"newTag: "+fixtures.V2, "the last environment has the new version in git")

	g = bundleGraph(t, e, ns, bundle)
	raw, err := json.Marshal(g.Object)
	require.NoError(t, err)
	t.Logf("Graph %s: %d nodes, %d bytes (spec and status)", name, len(graphNodeIDs(g)), len(raw))
	assert.Less(t, len(raw), graph.MaxGraphBytes, "the applied Graph stays under the guard")
	framework.Eventually(t, 2*time.Minute, "the Graph to be Ready", func(ctx context.Context) (bool, string) {
		st, reason, msg := graphCondition(bundleGraph(t, e, ns, bundle), "Ready")
		return st == "True", st + " " + reason + ": " + msg
	})
}

// logWaveTimes logs, for each wave of a wavePipeline's verified steps, when
// its first step was created (the Graph reached the wave), how long its steps
// took from creation to Verified (status.stateTransitionTime), and the gap
// from the previous wave's last Verified to this wave's first step: where a
// slow run's time goes (#2349). It also sums what the wave's git-push steps
// spent on one another (#2366): pushes refused because the branch moved,
// rebases, and fresh clones (status.outputs pushRefusals, rebasesTotal,
// pushRestarts), with the wave's longest git-clone and git-push runs (the
// last run of each, status.steps).
func logWaveTimes(t *testing.T, steps []v1alpha1.PromotionStep, perWave int) {
	t.Helper()
	type wave struct {
		created, verified        time.Time
		longest, clone, push     time.Duration
		refusals, rebases, fresh int
	}
	count := func(s v1alpha1.PromotionStep, key string) int {
		n, _ := strconv.Atoi(s.Status.Outputs[key])
		return n
	}
	waves := map[int]*wave{}
	for _, s := range steps {
		var w, r int
		if _, err := fmt.Sscanf(s.Spec.Environment, "w%02d-r%d", &w, &r); err != nil || s.Status.StateTransitionTime == nil {
			continue
		}
		created, verified := s.CreationTimestamp.Time, s.Status.StateTransitionTime.Time
		if waves[w] == nil {
			waves[w] = &wave{created: created, verified: verified}
		}
		wv := waves[w]
		if created.Before(wv.created) {
			wv.created = created
		}
		if verified.After(wv.verified) {
			wv.verified = verified
		}
		wv.longest = max(wv.longest, verified.Sub(created))
		// pkg/steps/steps' OutputPushRefusals, OutputRebasesTotal and
		// OutputPushRestarts.
		wv.refusals += count(s, "pushRefusals")
		wv.rebases += count(s, "rebasesTotal")
		wv.fresh += count(s, "pushRestarts")
		for _, st := range s.Status.Steps {
			d := time.Duration(st.DurationMs) * time.Millisecond
			switch st.Name {
			case "git-clone":
				wv.clone = max(wv.clone, d)
			case "git-push":
				wv.push = max(wv.push, d)
			}
		}
	}
	var refusals, rebases, fresh int
	var start, prev time.Time
	var lines []string
	for w := 1; waves[w] != nil; w++ {
		wv := waves[w]
		if w == 1 {
			start = wv.created
		}
		gap := time.Duration(0)
		if !prev.IsZero() {
			gap = wv.created.Sub(prev)
		}
		lines = append(lines, fmt.Sprintf("wave %02d: reached at +%s, %d steps verified in %s (longest step %s), gap after the previous wave %s; "+
			"%d pushes refused, %d rebases, %d fresh clones; longest git-clone %s, git-push %s",
			w, wv.created.Sub(start).Round(time.Second), perWave, wv.verified.Sub(wv.created).Round(time.Second),
			wv.longest.Round(time.Second), gap.Round(time.Second), wv.refusals, wv.rebases, wv.fresh,
			wv.clone.Round(100*time.Millisecond), wv.push.Round(100*time.Millisecond)))
		refusals, rebases, fresh = refusals+wv.refusals, rebases+wv.rebases, fresh+wv.fresh
		prev = wv.verified
	}
	t.Logf("wave times (#2349, #2366):\n%s\nall waves: %d pushes refused, %d rebases, %d fresh clones",
		strings.Join(lines, "\n"), refusals, rebases, fresh)
}

// logConflicts logs how many of the controller's log lines since since are
// update conflicts ("the object has been modified"), and how many of those
// name a BranchWrite (#2405 review: a round's status writes must not
// conflict with the Graph's rewrites of spec.requested).
func logConflicts(t *testing.T, e *framework.Env, since time.Time) {
	t.Helper()
	all, bw := 0, 0
	logs := e.ControllerLogs(t, since)
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, "the object has been modified") {
			all++
			if strings.Contains(strings.ToLower(l), "branchwrite") {
				bw++
			}
		}
	}
	t.Logf("update conflicts (#2405): %d controller log lines with \"the object has been modified\", %d of them naming a BranchWrite "+
		"(%d KiB of logs read; the kubelet keeps only the current log file)", all, bw, len(logs)/1024)
}

// logGitSpend logs the controller's git clones, pushes and rebases between
// two reads of its metrics (kardinal_git_operations_total and
// kardinal_git_operation_duration_seconds): how many of each, by result, and
// the seconds spent in each, so a run's git time splits into fresh-clone
// restarts against rebases and pushes (#2366).
func logGitSpend(t *testing.T, before, after framework.Metrics, rounds int) {
	t.Helper()
	delta := func(name string, labels map[string]string) float64 {
		return after.Sum(name, labels) - before.Sum(name, labels)
	}
	var lines []string
	for _, op := range []string{"clone", "push", "rebase"} {
		n := delta("kardinal_git_operation_duration_seconds_count", map[string]string{"operation": op})
		secs := delta("kardinal_git_operation_duration_seconds_sum", map[string]string{"operation": op})
		mean := 0.0
		if n > 0 {
			mean = secs / n
		}
		var results []string
		for _, r := range after.LabelValues("kardinal_git_operations_total", "result") {
			if d := delta("kardinal_git_operations_total", map[string]string{"operation": op, "result": r}); d > 0 {
				results = append(results, fmt.Sprintf("%s %.0f", r, d))
			}
		}
		lines = append(lines, fmt.Sprintf("%-6s %5.0f in %7.1fs (mean %.2fs): %s", op, n, secs, mean, strings.Join(results, ", ")))
	}
	if rounds > 0 {
		clones := delta("kardinal_git_operation_duration_seconds_count", map[string]string{"operation": "clone"})
		pushes := delta("kardinal_git_operation_duration_seconds_count", map[string]string{"operation": "push"})
		lines = append(lines, fmt.Sprintf("per round (%d BranchWrite rounds): %.2f clones, %.2f pushes", rounds,
			clones/float64(rounds), pushes/float64(rounds)))
	}
	t.Logf("git spend (#2366, controller metrics):\n%s", strings.Join(lines, "\n"))
}

// logRounds logs how the Bundle's BranchWrite wrote a batched wavePipeline
// (#2366): its rounds, how many environments each wrote, and how many rounds
// each wave took; every step's commit is its environment's own (the
// BranchWrite's result). It returns the number of rounds.
func logRounds(t *testing.T, e *framework.Env, ns, bundle string, steps []v1alpha1.PromotionStep, perWave int) int {
	t.Helper()
	var list v1alpha1.BranchWriteList
	require.NoError(t, e.Client.List(context.Background(), &list, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}))
	require.Len(t, list.Items, 1, "one BranchWrite for the Bundle's branch")
	bw := list.Items[0]
	members := map[int64]int{}
	commits := map[string]bool{}
	for env, res := range bw.Status.Environments {
		members[res.Round]++
		assert.NotEmpty(t, res.CommitSHA, "%s: written", env)
		assert.False(t, commits[res.CommitSHA], "%s: its own commit", env)
		commits[res.CommitSHA] = true
	}
	for _, s := range steps {
		res := bw.Status.Environments[s.Spec.Environment]
		assert.Equal(t, res.CommitSHA, s.Status.Outputs["commitSHA"], "%s: the step's commit is its write", s.Name)
	}
	waveRounds := map[int]map[int64]bool{}
	for _, s := range steps {
		var w, r int
		if _, err := fmt.Sscanf(s.Spec.Environment, "w%02d-r%d", &w, &r); err != nil {
			continue
		}
		if waveRounds[w] == nil {
			waveRounds[w] = map[int64]bool{}
		}
		waveRounds[w][bw.Status.Environments[s.Spec.Environment].Round] = true
	}
	var sizes []string
	for r := int64(1); r <= bw.Status.Round; r++ {
		sizes = append(sizes, strconv.Itoa(members[r]))
	}
	var perWaveRounds []string
	for w := 1; waveRounds[w] != nil; w++ {
		perWaveRounds = append(perWaveRounds, strconv.Itoa(len(waveRounds[w])))
	}
	t.Logf("BranchWrite %s (#2366): %d rounds for %d environments (%d a wave); environments per round: %s; rounds per wave: %s",
		bw.Name, bw.Status.Round, len(bw.Status.Environments), perWave, strings.Join(sizes, " "), strings.Join(perWaveRounds, " "))
	return int(bw.Status.Round)
}

// wavePipeline sets up a Pipeline of waves × perWave environments
// ("w01-r0", ...), each in its own overlay of one repo, with resource health
// on compactHealthDeployment and one PolicyGate per environment whose
// expression gateExpr gives. It creates the repo, the Deployment and the
// gates, and returns the app and the Pipeline, not yet applied. The repo also
// gets an overlay for each of spare, which the Pipeline does not list.
func wavePipeline(t *testing.T, e *framework.Env, waves, perWave int, gateExpr func(env string) string,
	spare ...string) (*app, *v1alpha1.Pipeline) {
	t.Helper()
	ns := e.Namespace(t)
	var envs []string
	for w := 1; w <= waves; w++ {
		for i := 0; i < perWave; i++ {
			envs = append(envs, fmt.Sprintf("w%02d-r%d", w, i))
		}
	}
	files := map[string][]byte{}
	for _, env := range append(append([]string(nil), envs...), spare...) {
		files[fixtures.Path(env)+"/kustomization.yaml"] = []byte(fmt.Sprintf(
			"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nimages:\n  - name: %s\n    newTag: %s\n",
			fixtures.Image, fixtures.V1))
	}
	a := &app{e: e, ns: ns, envs: envs, repo: e.Repo(t, ns, files)}
	createCompactHealth(t, e, ns)

	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: pipelineName, Namespace: ns},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: a.repo.CloneURL, Branch: a.repo.Branch,
			SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName}}},
	}
	for i, env := range envs {
		p.Spec.Environments = append(p.Spec.Environments, waveEnv(ns, env, i/perWave+1))
		e.CreateGate(t, framework.Gate(ns, "gate-"+env, env, gateExpr(env), recheck))
	}
	return a, p
}

// createCompactHealth creates compactHealthDeployment in ns and waits until
// it is available.
func createCompactHealth(t *testing.T, e *framework.Env, ns string) {
	t.Helper()
	one := int32(1)
	labels := map[string]string{"app": compactHealthDeployment}
	require.NoError(t, e.Client.Create(context.Background(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: compactHealthDeployment, Namespace: ns},
		Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pause",
					Image: "registry.k8s.io/pause:3.10", ImagePullPolicy: corev1.PullIfNotPresent}}}}},
	}))
	framework.Eventually(t, 3*time.Minute, "health Deployment available", func(ctx context.Context) (bool, string) {
		var d appsv1.Deployment
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: compactHealthDeployment}, &d); err != nil {
			return false, err.Error()
		}
		return d.Status.AvailableReplicas == 1, fmt.Sprintf("available %d", d.Status.AvailableReplicas)
	})
}

// waveEnv is environment env of a wavePipeline in wave.
func waveEnv(ns, env string, wave int) v1alpha1.EnvironmentSpec {
	return v1alpha1.EnvironmentSpec{
		Name: env, Wave: wave, Path: fixtures.Path(env), Approval: "auto",
		Update: v1alpha1.UpdateConfig{Strategy: "kustomize"},
		Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "5m",
			Resource: &v1alpha1.ResourceRef{Name: compactHealthDeployment, Namespace: ns}},
	}
}

// stepUIDs maps each environment of bundle to its PromotionStep's UID.
func stepUIDs(t *testing.T, e *framework.Env, ns, bundle string) map[string]types.UID {
	t.Helper()
	var steps v1alpha1.PromotionStepList
	require.NoError(t, e.Client.List(context.Background(), &steps, client.InNamespace(ns),
		client.MatchingLabels{"kardinal.io/bundle": bundle}))
	out := map[string]types.UID{}
	for _, s := range steps.Items {
		out[s.Spec.Environment] = s.UID
	}
	return out
}

// TestGraph_ShapeKeptAcrossThreshold checks that a Bundle in flight keeps its
// Graph's shape when a Pipeline edit crosses the compact threshold (100
// environments), in both directions: the Graph is updated in place in the
// same shape, the steps that exist keep their UIDs (kro prunes nothing), and
// the Bundle finishes in every environment of the edited Pipeline. Switching
// the shape mid-flight would delete every PromotionStep of the old shape.
// The second wave's gates hold the Bundle while the Pipeline is edited.
//
// It does not run in parallel (100-overlay repos, see TestCompact_Shape300).
//
// Covers GRAPH-COMPACT-03.
func TestGraph_ShapeKeptAcrossThreshold(t *testing.T) {
	for _, tc := range []struct {
		name      string
		envs      int // 4 waves of envs/4, plus one more in the compact case
		grow      bool
		wantShape string
	}{
		{name: "100 to 101 stays nodes", envs: 100, grow: true, wantShape: "nodes"},
		{name: "101 to 100 stays compact", envs: 101, wantShape: "compact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := framework.New(t)
			if above := controllerCompactAbove(t, e); above != graph.DefaultCompactAbove {
				t.Skipf("the controller runs with --graph-compact-above=%d; this test crosses the default, %d",
					above, graph.DefaultCompactAbove)
			}
			const perWave = 25
			gate := func(env string) string {
				if env[:3] == "w02" {
					return openExpr
				}
				return "true"
			}
			a, p := wavePipeline(t, e, 4, perWave, gate, "w04-r25")
			ns := a.ns
			if tc.envs > len(p.Spec.Environments) {
				p.Spec.Environments = append(p.Spec.Environments, waveEnv(ns, "w04-r25", 4))
			}
			a.apply(t, p)
			bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
			for _, env := range a.envs[:perWave] {
				e.WaitStepState(t, ns, pipelineName, bundle, env, "Verified", 10*time.Minute)
			}
			g := bundleGraph(t, e, ns, bundle)
			require.Equal(t, tc.wantShape, g.GetLabels()["kardinal.io/graph-shape"])
			gen := g.GetGeneration()
			before := stepUIDs(t, e, ns, bundle)
			require.Len(t, before, perWave, "wave 1 only: wave 2's gates hold the Bundle")

			// Cross the threshold. The Pipeline reconciler writes its status
			// (status.environments) as the steps move, so the update retries
			// on a conflict.
			var envCount int
			a.updatePipeline(t, pipelineName, func(cur *v1alpha1.Pipeline) {
				if tc.grow {
					cur.Spec.Environments = append(cur.Spec.Environments, waveEnv(ns, "w04-r25", 4))
				} else {
					cur.Spec.Environments = cur.Spec.Environments[:len(cur.Spec.Environments)-1]
				}
				envCount = len(cur.Spec.Environments)
			})
			framework.Eventually(t, 2*time.Minute, "the Graph updated in place", func(ctx context.Context) (bool, string) {
				g := bundleGraph(t, e, ns, bundle)
				return g.GetGeneration() > gen, fmt.Sprintf("generation %d", g.GetGeneration())
			})
			g = bundleGraph(t, e, ns, bundle)
			assert.Equal(t, tc.wantShape, g.GetLabels()["kardinal.io/graph-shape"], "the shape is kept")
			framework.Consistently(t, 20*time.Second, "no PromotionStep is pruned", func(ctx context.Context) (bool, string) {
				now := stepUIDs(t, e, ns, bundle)
				for env, uid := range before {
					if now[env] != uid {
						return false, fmt.Sprintf("%s: step %s became %s", env, uid, now[env])
					}
				}
				return true, ""
			})

			e.SetBundleLabel(t, ns, bundle, openLabel, "true")
			e.WaitBundlePhase(t, ns, bundle, "Verified", 20*time.Minute)
			assert.Len(t, stepUIDs(t, e, ns, bundle), envCount, "every environment of the edited Pipeline")
		})
	}
}

// controllerCompactAbove returns the controller's --graph-compact-above, or
// graph.DefaultCompactAbove when it is not set.
func controllerCompactAbove(t *testing.T, e *framework.Env) int {
	t.Helper()
	dep, err := e.Kube.AppsV1().Deployments(framework.ControllerNamespace).Get(context.Background(),
		framework.ControllerService, metav1.GetOptions{})
	require.NoError(t, err)
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, arg := range c.Args {
			if v, ok := strings.CutPrefix(arg, "--graph-compact-above="); ok {
				n, err := strconv.Atoi(v)
				require.NoError(t, err, "controller arg %s", arg)
				return n
			}
		}
	}
	return graph.DefaultCompactAbove
}
