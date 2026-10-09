//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// TestGraph_SequentialByDefault checks the default ordering: without
// dependsOn or wave, each environment starts only after the one listed before
// it is Verified. uat waits on a PR, so prod must not even have a
// PromotionStep until the PR merges and uat turns Verified.
//
// Covers GRAPH-SEQ-01.
func TestGraph_SequentialByDefault(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "uat", "prod")
	a.apply(t, a.pipeline(map[string]string{"uat": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	test := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	uat := e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "WaitingForMerge", promoteTimeout)
	startedAfter(t, uat, verifiedAt(t, test), "uat starts after test is Verified")
	a.noStep(t, bundle, "prod", 20*time.Second)
	a.fileHas(t, "prod", fixtures.V1, "prod is untouched while uat waits")

	a.merge(t, a.openPR(t, bundle, "uat"))
	uat = e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	prod := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	startedAfter(t, prod, verifiedAt(t, uat), "prod starts after uat is Verified")
	a.running(t, "prod", imageV2, "prod is promoted last")
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestGraph_DependsOnFanOutFanIn checks dependsOn: eu and us both depend on
// test, so they wait on their PRs at the same time, and prod depends on both,
// so it starts only when the second of them is Verified.
//
// Covers GRAPH-DEP-01.
func TestGraph_DependsOnFanOutFanIn(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "eu", "us", "prod")
	p := a.pipeline(map[string]string{"eu": "pr-review", "us": "pr-review"})
	envSpec(t, p, "eu").DependsOn = []string{"test"}
	envSpec(t, p, "us").DependsOn = []string{"test"}
	envSpec(t, p, "prod").DependsOn = []string{"eu", "us"}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	checkFanOutFanIn(t, a, bundle, "test", "eu", "us", "prod")
}

// TestGraph_WavesRunInParallel checks wave: eu and us are wave 1, so they
// start together after test (the environment without a wave listed before
// them), and ap is wave 3, so it waits for every environment of the next
// lower wave, 1: the gap in the numbers is skipped.
//
// Covers GRAPH-WAVE-01.
func TestGraph_WavesRunInParallel(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "eu", "us", "ap")
	p := a.pipeline(map[string]string{"eu": "pr-review", "us": "pr-review"})
	envSpec(t, p, "eu").Wave = 1
	envSpec(t, p, "us").Wave = 1
	envSpec(t, p, "ap").Wave = 3
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	checkFanOutFanIn(t, a, bundle, "test", "eu", "us", "ap")
}

// checkFanOutFanIn promotes bundle through first, then left and right in
// parallel (both pr-review), then join. It checks that left and right wait on
// their PRs at the same time and that join has no step until both are
// Verified.
func checkFanOutFanIn(t *testing.T, a *app, bundle, first, left, right, join string) {
	t.Helper()
	e := a.e
	root := e.WaitStepState(t, a.ns, pipelineName, bundle, first, "Verified", promoteTimeout)
	l := e.WaitStepState(t, a.ns, pipelineName, bundle, left, "WaitingForMerge", promoteTimeout)
	r := e.WaitStepState(t, a.ns, pipelineName, bundle, right, "WaitingForMerge", promoteTimeout)
	startedAfter(t, l, verifiedAt(t, root), left+" starts after "+first)
	startedAfter(t, r, verifiedAt(t, root), right+" starts after "+first)
	leftPR, rightPR := a.openPR(t, bundle, left), a.openPR(t, bundle, right)
	a.noStep(t, bundle, join, 15*time.Second)

	a.merge(t, leftPR)
	e.WaitStepState(t, a.ns, pipelineName, bundle, left, "Verified", promoteTimeout)
	a.noStep(t, bundle, join, 15*time.Second)
	ps, _, err := e.Step(context.Background(), a.ns, pipelineName, bundle, right)
	require.NoError(t, err)
	assert.Equal(t, "WaitingForMerge", ps.Status.State, "%s still waits on its PR", right)

	a.merge(t, rightPR)
	r = e.WaitStepState(t, a.ns, pipelineName, bundle, right, "Verified", promoteTimeout)
	j := e.WaitStepState(t, a.ns, pipelineName, bundle, join, "Verified", promoteTimeout)
	startedAfter(t, j, verifiedAt(t, r), join+" starts after both "+left+" and "+right)
	a.running(t, join, imageV2, join+" is promoted")
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestGraph_DependencyErrorsStopPromotion checks the two ordering errors: a
// dependsOn that names no environment and a dependsOn cycle. Each Pipeline
// gets Ready=False (ValidationFailed) naming the problem, and a Bundle of it
// fails with InvalidSpec without creating a PromotionStep or touching git.
//
// Covers GRAPH-DEP-02.
func TestGraph_DependencyErrorsStopPromotion(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: []string{"test", "prod"},
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test", "prod"}}))}
	ctx := context.Background()
	before, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 10)
	require.NoError(t, err)

	cases := []struct {
		name, reason, ready, bundleMsg string
		deps                           map[string][]string
	}{{
		name: "unknown", reason: "InvalidPipeline",
		deps:      map[string][]string{"prod": {"staging"}},
		ready:     `environment "prod" has dependsOn "staging" which does not exist in this pipeline`,
		bundleMsg: `dependsOn unknown environment "staging"`,
	}, {
		name: "cycle", reason: "CircularDependency",
		deps:      map[string][]string{"test": {"prod"}, "prod": {"test"}},
		ready:     "circular dependency",
		bundleMsg: "circular dependency",
	}}
	for _, c := range cases {
		p := a.pipeline(nil)
		p.Name = c.name
		for env, deps := range c.deps {
			envSpec(t, p, env).DependsOn = deps
		}
		a.apply(t, p)
		waitPipeline(t, e, ns, c.name, time.Minute, "Ready=False ValidationFailed", func(p *v1alpha1.Pipeline) (bool, string) {
			ok, seen := framework.CondIs(p.Status.Conditions, "Ready", metav1.ConditionFalse, "ValidationFailed")
			return ok && strings.Contains(findCond(p.Status.Conditions, "Ready").Message, c.ready), seen
		})
		bundle := e.CreateBundle(t, ns, c.name, "--image", imageV2)
		b := e.WaitBundle(t, ns, bundle, time.Minute, "Failed with InvalidSpec "+c.reason, failedWith(c.reason, c.bundleMsg))
		assert.Empty(t, b.Status.GraphRef, "%s: no Graph is built", c.name)
		assert.Contains(t, findCond(b.Status.Conditions, "Ready").Message, "promotion failed: ")
	}

	framework.Consistently(t, 15*time.Second, "no promotion runs", func(ctx context.Context) (bool, string) {
		var steps v1alpha1.PromotionStepList
		if err := e.Client.List(ctx, &steps, client.InNamespace(ns)); err != nil {
			return false, err.Error()
		}
		return len(steps.Items) == 0, fmt.Sprintf("%d PromotionSteps", len(steps.Items))
	})
	after, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 10)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after), "nothing is committed")
}

// TestGraph_SizeLimitRefused checks the Graph size guard (ledger gap G10): a
// Pipeline whose Graph would not fit in one etcd object (here 20 environments
// with 4 PolicyGates of 16 KB expressions each, about 1.4 MB) fails its
// Bundle with GraphBuildFailed and a message that names the size and the fix,
// and no Graph, PromotionStep or gate instance is created. The same Pipeline
// with one small gate on its first environment builds its Graph and
// promotes the first environment.
//
// Covers GRAPH-SIZE-01.
func TestGraph_SizeLimitRefused(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	var envs []string
	for i := 0; i < 20; i++ {
		envs = append(envs, fmt.Sprintf("region%02d", i))
	}
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: envs,
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))}
	// Only the first environment is deployed (Argo CD) and health-checked;
	// the test ends once it is Verified.
	e.ArgoApp(t, a.argoApp(envs[0]), a.repo, fixtures.Path(envs[0]), ns)
	e.WaitArgoApp(t, a.argoApp(envs[0]), syncTimeout)
	p := a.pipeline(nil)
	for i := 1; i < len(p.Spec.Environments); i++ {
		p.Spec.Environments[i].Health = v1alpha1.HealthConfig{}
	}
	a.apply(t, p)
	long := strings.TrimSuffix(strings.Repeat("bundle.version != \"\" && ", 640), " && ")
	for _, env := range envs {
		for g := 0; g < 4; g++ {
			gate := &v1alpha1.PolicyGate{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-big-%d", env, g), Namespace: ns,
					Labels: map[string]string{"kardinal.io/applies-to": env}},
				Spec: v1alpha1.PolicyGateSpec{Expression: long, Message: strings.Repeat("m", 1000)},
			}
			require.NoError(t, e.Client.Create(ctx, gate))
		}
	}

	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	b := e.WaitBundle(t, ns, bundle, 2*time.Minute, "Failed with GraphBuildFailed (graph size)",
		failedWith("GraphBuildFailed", "graph size: the Graph for this Bundle would be about "))
	msg := findCond(b.Status.Conditions, "InvalidSpec").Message
	assert.Contains(t, msg, "over kardinal's limit of 1200000 bytes")
	assert.Contains(t, msg, "split the Pipeline")
	assert.Empty(t, b.Status.GraphRef, "no Graph is built")
	graphs, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, graphs.Items, "no Graph object")
	var steps v1alpha1.PromotionStepList
	require.NoError(t, e.Client.List(ctx, &steps, client.InNamespace(ns)))
	assert.Empty(t, steps.Items, "no PromotionStep")

	// Within the limit: one small gate, and the Bundle promotes.
	var gates v1alpha1.PolicyGateList
	require.NoError(t, e.Client.List(ctx, &gates, client.InNamespace(ns)))
	for i := range gates.Items {
		require.NoError(t, e.Client.Delete(ctx, &gates.Items[i]))
	}
	small := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "small", Namespace: ns,
			Labels: map[string]string{"kardinal.io/applies-to": envs[0]}},
		Spec: v1alpha1.PolicyGateSpec{Expression: `bundle.version != ""`},
	}
	require.NoError(t, e.Client.Create(ctx, small))
	next := e.CreateBundle(t, ns, pipelineName, "--image", imageV3)
	framework.Eventually(t, 2*time.Minute, "the smaller Graph to be accepted", func(ctx context.Context) (bool, string) {
		name := a.bundle(t, next).Status.GraphRef
		if name == "" {
			return false, "no status.graphRef"
		}
		g, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		st, reason, msg := graphCondition(g, "Accepted")
		return st == "True", fmt.Sprintf("Accepted=%s %s: %s", st, reason, msg)
	})
	e.WaitStepState(t, ns, pipelineName, next, envs[0], "Verified", promoteTimeout)
	a.fileHas(t, envs[0], fixtures.V3, "the first environment is promoted")
}

// TestGraph_GateAndPRStatusCollections checks the Graph shape for gate
// instances and PRStatuses (ledger gaps G9, G10): the Bundle's gate
// instances come from one PolicyGates collection node and its PRStatuses
// from one PRStatuses collection node, each a forEach over a def node, with
// one PromotionStep node per environment. kro labels each object with its
// collection node and lists it in the Graph's managedResources. The gate in
// the collection holds prod back while its expression is false, and prod
// promotes once it is true.
//
// Covers GRAPH-COLLECTIONS-01.
func TestGraph_GateAndPRStatusCollections(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "needs-open-label", "prod", openExpr, recheck))
	e.CreateGate(t, framework.Gate(a.ns, "always-open", "prod", "true", recheck))
	// The node list is the nodes shape's, whatever --graph-compact-above;
	// TestGraph_CompactShapeSmall checks the compact one.
	a.apply(t, nodesShape(a.pipeline(nil)))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

	name := a.bundle(t, bundle).Status.GraphRef
	require.NotEmpty(t, name)
	g, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	nodes, _, _ := unstructured.NestedSlice(g.Object, "spec", "nodes")
	var ids []string
	for _, n := range nodes {
		ids = append(ids, fmt.Sprint(n.(map[string]interface{})["id"]))
	}
	assert.ElementsMatch(t, []string{"bundle", "test", "prod", "PolicyGateData", "PolicyGates",
		"PRStatusData", "PRStatuses", "healthTest", "healthProd"}, ids, "Graph nodes")

	// Both gate instances exist, made by the PolicyGates collection.
	for _, tmpl := range []string{"needs-open-label", "always-open"} {
		gate := e.WaitGateReady(t, a.ns, bundle, "prod", tmpl, tmpl == "always-open", "", gateTimeout)
		assert.Equal(t, "PolicyGates", gate.Labels["kro.run/node-id"], "%s: made by the PolicyGates collection", tmpl)
		assert.Equal(t, "2", gate.Labels["kro.run/collection-size"], tmpl)
		assert.Equal(t, pipelineName, gate.Labels["kardinal.io/pipeline"], tmpl)
		assert.Equal(t, tmpl, gate.Labels["kardinal.io/gate-name"], tmpl)
		assert.Equal(t, "team", gate.Labels["kardinal.io/scope"], tmpl)
		assert.True(t, gate.Spec.Generated, tmpl)
	}
	var prs v1alpha1.PRStatusList
	require.NoError(t, e.Client.List(ctx, &prs, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/bundle": bundle}))
	require.Len(t, prs.Items, 2, "one PRStatus per environment")
	for _, pr := range prs.Items {
		assert.Equal(t, "PRStatuses", pr.Labels["kro.run/node-id"], pr.Name)
	}
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", time.Minute)
	assert.Contains(t, []string{prs.Items[0].Name, prs.Items[1].Name}, ps.Spec.PRStatusRef,
		"the step's prStatusRef names its PRStatus")

	// The gate holds prod, then lets it through.
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)
	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "needs-open-label", true, "= true", gateTimeout)
	prod := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Len(t, prod.Spec.RequiredGates, 2, "prod requires both gate instances")
	assertEnvAt(t, a, "prod", fixtures.V2)

	g, err = e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	managed, _, _ := unstructured.NestedSlice(g.Object, "status", "managedResources")
	byNode := map[string]int{}
	for _, m := range managed {
		byNode[fmt.Sprint(m.(map[string]interface{})["nodeID"])]++
	}
	assert.Equal(t, map[string]int{"PolicyGates": 2, "PRStatuses": 2, "test": 1, "prod": 1}, byNode,
		"managedResources by node")
}

// TestGraph_GateApplyFailureSurfaced checks what an operator sees when a gate
// instance cannot be created (ledger gap G11): with a ResourceQuota that allows
// one PolicyGate in the namespace, the second instance is refused, kro does
// not publish the PolicyGates collection, and prod waits. The Bundle's
// GatesCreated condition is False and names the missing instance and the
// quota error, and the Bundle does not fail. Once the quota is gone the
// instance is created, the condition turns True and prod promotes.
//
// Covers GRAPH-COLLECTIONS-02.
func TestGraph_GateApplyFailureSurfaced(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "first", "prod", "true", recheck))
	e.CreateGate(t, framework.Gate(a.ns, "second", "prod", "true", recheck))
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "one-gate-instance", Namespace: a.ns},
		// The two templates count too: room for them and one instance.
		Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{"count/policygates.kardinal.io": resource.MustParse("3")}},
	}
	require.NoError(t, e.Client.Create(ctx, quota))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	b := e.WaitBundle(t, a.ns, bundle, 2*time.Minute, "GatesCreated False naming the refused instance",
		func(b *v1alpha1.Bundle) (bool, string) {
			c := findCond(b.Status.Conditions, "GatesCreated")
			if c.Type == "" {
				return false, "no GatesCreated condition"
			}
			return c.Status == metav1.ConditionFalse && c.Reason == "ApplyFailed" &&
				strings.Contains(c.Message, "1 of 2 PolicyGate instances are not created") &&
				strings.Contains(c.Message, "exceeded quota"), string(c.Status) + " " + c.Reason + ": " + c.Message
		})
	assert.NotEqual(t, "Failed", b.Status.Phase, "a gate that cannot be created holds the Bundle; it does not fail it")
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	require.NoError(t, e.Client.Delete(ctx, quota))
	e.WaitBundle(t, a.ns, bundle, 3*time.Minute, "GatesCreated True", func(b *v1alpha1.Bundle) (bool, string) {
		c := findCond(b.Status.Conditions, "GatesCreated")
		if c.Type == "" {
			return false, "no GatesCreated condition"
		}
		return c.Status == metav1.ConditionTrue, string(c.Status) + " " + c.Message
	})
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGraph_SkipEnvironmentsBridges checks intent.skipEnvironments: a Bundle
// that skips uat goes from test straight to prod. uat gets no PromotionStep
// and keeps its version, and the Bundle is Verified.
//
// Covers GRAPH-SKIP-01.
func TestGraph_SkipEnvironmentsBridges(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "uat", "prod")
	a.apply(t, a.pipeline(nil))
	bundle := a.createBundle(t, v1alpha1.BundleSpec{Images: podinfoImages(fixtures.V2),
		Intent: &v1alpha1.BundleIntent{SkipEnvironments: []string{"uat"}}})

	test := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	prod := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	startedAfter(t, prod, verifiedAt(t, test), "prod follows test directly")
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	steps, err := e.Steps(context.Background(), a.ns, pipelineName, bundle)
	require.NoError(t, err)
	assert.Equal(t, []string{"prod", "test"}, framework.StepEnvs(steps), "uat has no step")
	a.running(t, "prod", imageV2, "prod is promoted")
	a.fileHas(t, "uat", fixtures.V1, "uat is skipped")
	a.running(t, "uat", imageV1, "uat is skipped")
}

// TestGraph_SkipNeedsPermission checks that an org-gated environment can be
// skipped only with a skip-permission PolicyGate. Without one the Bundle is
// refused (Failed, "skip denied") before any step runs; once the platform
// team adds a skip-permission gate for the environment to the org policy
// namespace, the same skip promotes.
//
// Covers GRAPH-SKIP-02.
func TestGraph_SkipNeedsPermission(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	// A name no other test uses: the skip-permission gate lives in the shared
	// org policy namespace and applies by environment name.
	gated := "uat-" + ns[len(ns)-8:]
	envs := []string{"test", gated, "prod"}
	a := &app{e: e, ns: ns, envs: envs, repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))}
	for _, env := range envs {
		e.ArgoApp(t, a.argoApp(env), a.repo, fixtures.Path(env), ns)
	}
	for _, env := range envs {
		e.WaitArgoApp(t, a.argoApp(env), syncTimeout)
	}
	a.apply(t, a.pipeline(nil))
	ctx := context.Background()
	orgGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "org-uat", Namespace: ns, Labels: map[string]string{
			"kardinal.io/scope": "org", "kardinal.io/applies-to": gated, "kardinal.io/type": "gate"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Message: "org gate", RecheckInterval: "10s"},
	}
	require.NoError(t, e.Client.Create(ctx, orgGate))
	skip := &v1alpha1.BundleIntent{SkipEnvironments: []string{gated}}

	denied := a.createBundle(t, v1alpha1.BundleSpec{Images: podinfoImages(fixtures.V2), Intent: skip})
	b := e.WaitBundle(t, ns, denied, time.Minute, "refused", failedWith("GraphBuildFailed", "skip denied"))
	assert.Contains(t, findCond(b.Status.Conditions, "InvalidSpec").Message, ns+"/org-uat", "names the org gate")
	assert.Empty(t, b.Status.GraphRef)
	assert.Zero(t, a.stepCount(t, denied), "a refused Bundle runs no step")

	const policyNS = "platform-policies"
	if err := e.Client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: policyNS}}); err != nil &&
		!apierrors.IsAlreadyExists(err) {
		t.Fatalf("create %s: %v", policyNS, err)
	}
	permit := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "skip-" + ns[len(ns)-8:], Namespace: policyNS, Labels: map[string]string{
			"kardinal.io/type": "skip-permission", "kardinal.io/applies-to": gated}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", SkipPermission: true, RecheckInterval: "10s"},
	}
	require.NoError(t, e.Client.Create(ctx, permit))
	t.Cleanup(func() {
		if err := e.Client.Delete(context.Background(), permit); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete %s/%s: %v", policyNS, permit.Name, err)
		}
	})

	allowed := a.createBundle(t, v1alpha1.BundleSpec{Images: podinfoImages(fixtures.V3), Intent: skip})
	e.WaitStepState(t, ns, pipelineName, allowed, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, ns, pipelineName, allowed, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, ns, allowed, "Verified", time.Minute)
	steps, err := e.Steps(ctx, ns, pipelineName, allowed)
	require.NoError(t, err)
	assert.Equal(t, []string{"prod", "test"}, framework.StepEnvs(steps), "the gated environment is skipped")
	a.running(t, "prod", imageV3, "the permitted skip promotes")
	a.fileHas(t, gated, fixtures.V1, "the skipped environment is untouched")
}

// TestGraph_TargetEnvironmentStops checks intent.targetEnvironment: a Bundle
// that targets uat promotes test and uat and is then Verified; prod gets no
// PromotionStep and keeps its version.
//
// Covers GRAPH-TARGET-01.
func TestGraph_TargetEnvironmentStops(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "uat", "prod")
	a.apply(t, a.pipeline(nil))
	bundle := a.createBundle(t, v1alpha1.BundleSpec{Images: podinfoImages(fixtures.V2),
		Intent: &v1alpha1.BundleIntent{TargetEnvironment: "uat"}})

	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	a.running(t, "uat", imageV2, "the target is promoted")
	a.noStep(t, bundle, "prod", 15*time.Second)
	a.fileHas(t, "prod", fixtures.V1, "past the target nothing changes")
	a.running(t, "prod", imageV1, "past the target nothing changes")
}

// TestGraph_PipelineEditUpdatesInPlace checks that editing the Pipeline of an
// in-flight Bundle updates its Graph in place: the Graph keeps its UID, the
// Verified test step is not run again, and a new environment added after prod
// is promoted once prod's PR merges.
//
// Covers GRAPH-INPLACE-01.
func TestGraph_PipelineEditUpdatesInPlace(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod", "final")
	p := a.pipeline(map[string]string{"prod": "pr-review"})
	final := *envSpec(t, p, "final")
	p.Spec.Environments = p.Spec.Environments[:2]
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	test := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	graphName := a.bundle(t, bundle).Status.GraphRef
	require.NotEmpty(t, graphName)
	g := getGraph(t, e, a.ns, graphName)

	a.updatePipeline(t, pipelineName, func(p *v1alpha1.Pipeline) {
		p.Spec.Environments = append(p.Spec.Environments, final)
	})
	framework.Eventually(t, time.Minute, "the Graph to get the final node", func(ctx context.Context) (bool, string) {
		cur, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Get(ctx, graphName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if cur.GetUID() != g.GetUID() {
			t.Fatalf("Graph %s was recreated (UID %s, was %s), not updated", graphName, cur.GetUID(), g.GetUID())
		}
		return cur.GetGeneration() > g.GetGeneration(), fmt.Sprintf("generation %d", cur.GetGeneration())
	})

	a.merge(t, a.openPR(t, bundle, "prod"))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "final", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	a.running(t, "final", imageV2, "the added environment is promoted")

	again, _, err := e.Step(context.Background(), a.ns, pipelineName, bundle, "test")
	require.NoError(t, err)
	assert.Equal(t, test.UID, again.UID, "the test step is the same object")
	assert.Equal(t, verifiedAt(t, test), verifiedAt(t, again), "the test step did not run again")
	assert.Len(t, auditActions(t, e, a.ns, bundle, "test", "PromotionStarted"), 1, "test started once")
}

// TestGraph_DeletedGraphIsRecreated checks self-healing: deleting the Graph of
// an in-flight Bundle makes the controller recreate it (a new UID). Its
// PromotionSteps are recreated with it, prod reuses the PR it had already
// opened instead of opening a second one, and the Bundle still reaches
// Verified. The deleted prod step held the close-pr finalizer, and the
// controller left its PR open and uncommented for the new step: prod has
// exactly one PR, the same one, open.
//
// Covers GRAPH-HEAL-01, STEP-DELETE-PR-03.
func TestGraph_DeletedGraphIsRecreated(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, bundle, "prod")
	// Without the finalizer nothing would decide about the PR, and the rest of
	// the test would pass for the wrong reason.
	require.Contains(t, stepFinalizers(t, e, a.ns, bundle, "prod"), closePRFinalizer,
		"the prod step holds the close-pr finalizer before the Graph is deleted")
	graphName := a.bundle(t, bundle).Status.GraphRef
	require.NotEmpty(t, graphName)
	old := getGraph(t, e, a.ns, graphName)

	ctx := context.Background()
	require.NoError(t, e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Delete(ctx, graphName, metav1.DeleteOptions{}))
	framework.Eventually(t, 2*time.Minute, "the Graph to be recreated", func(ctx context.Context) (bool, string) {
		cur, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Get(ctx, graphName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return cur.GetUID() != old.GetUID() && cur.GetDeletionTimestamp() == nil, "UID " + string(cur.GetUID())
	})
	assert.Equal(t, "Promoting", a.bundle(t, bundle).Status.Phase, "the Bundle continues")

	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	again := a.openPR(t, bundle, "prod")
	assert.Equal(t, pr.Number, again.Number, "prod reuses its open PR")
	prs, err := e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	assert.Len(t, prs, 1, "no second PR is opened")
	var prodPRs []string
	for _, p := range prs {
		if p.Head == prHead(bundle, "prod") {
			prodPRs = append(prodPRs, fmt.Sprintf("#%d %s", p.Number, p.State))
		}
	}
	assert.Equal(t, []string{fmt.Sprintf("#%d open", pr.Number)}, prodPRs, "prod has one PR, the one it opened first, still open")
	assert.Empty(t, e.PRComments(t, a.repo, pr.Number, "kardinal closed this PR"), "the PR kept for the new step is not commented on")
	a.merge(t, again)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	a.running(t, "prod", imageV2, "the Bundle reaches prod")
}

// getGraph reads the kro Graph name.
func getGraph(t *testing.T, e *framework.Env, ns, name string) metav1.Object {
	t.Helper()
	g, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Graph %s: %v", name, err)
	}
	return g
}

// auditActions returns the AuditEvents of bundle's step for env with action.
func auditActions(t *testing.T, e *framework.Env, ns, bundle, env, action string) []v1alpha1.AuditEvent {
	t.Helper()
	all, err := e.AuditEvents(context.Background(), ns, bundle)
	if err != nil {
		t.Fatalf("list AuditEvents: %v", err)
	}
	var out []v1alpha1.AuditEvent
	for _, ev := range all {
		if ev.Labels["kardinal.io/environment"] == env && ev.Labels["kardinal.io/action"] == action {
			out = append(out, ev)
		}
	}
	return out
}
