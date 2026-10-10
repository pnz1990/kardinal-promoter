// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// compactSim evaluates a compact Graph's state and wave def nodes with
// cel-go, given the steps, gates and PRStatuses that exist, the way kro does
// on each walk.
type compactSim struct {
	t     *testing.T
	g     *graph.Graph
	nodes map[string]graph.GraphNode
	// steps maps environment → state of its PromotionStep ("" for created).
	steps map[string]string
	// gatesReady maps gate instance name → ready.
	gatesReady map[string]bool
	prs        bool
	phase      string
	// waitingForSlot sets the Bundle's WaitingForSlot condition True.
	waitingForSlot bool
	// fleet maps a fleet target's environment to its fleet (the step label).
	fleet map[string]string
}

func newCompactSim(t *testing.T, g *graph.Graph) *compactSim {
	t.Helper()
	sim := &compactSim{t: t, g: g, nodes: nodeByID(g.Spec.Nodes), steps: map[string]string{},
		gatesReady: map[string]bool{}, prs: true, phase: "Promoting"}
	for _, o := range renderedOf(t, g, "PolicyGate") {
		sim.gatesReady[objName(o)] = false
	}
	return sim
}

// vars is the scope kro evaluates the compact defs in.
func (s *compactSim) vars() map[string]interface{} {
	v := map[string]interface{}{}
	for _, n := range s.g.Spec.Nodes {
		if n.Def != nil && n.ID != graph.NodePromotionState && n.ID != graph.NodePromotionWave &&
			n.ID != graph.NodePromotionProgress && n.ID != graph.NodePromotionEligible {
			v[n.ID] = n.Def
		}
	}
	var observed []interface{}
	for env, state := range s.steps {
		labels := map[string]interface{}{"kardinal.io/environment": env}
		if f := s.fleet[env]; f != "" {
			labels[graph.LabelFleet] = f
		}
		obj := map[string]interface{}{"metadata": map[string]interface{}{"labels": labels}}
		if state != "" {
			obj["status"] = map[string]interface{}{"state": state}
		}
		observed = append(observed, obj)
	}
	v[graph.NodeStepsObserved] = observed
	for _, o := range renderObjects(s.t, s.g) {
		if o.Object["kind"] != "PolicyGate" {
			continue
		}
		list, _ := v[o.NodeID].([]interface{})
		v[o.NodeID] = append(list, map[string]interface{}{
			"metadata": map[string]interface{}{"name": objName(o.Object)},
			"status":   map[string]interface{}{"ready": s.gatesReady[objName(o.Object)]},
		})
	}
	var prs []interface{}
	if s.prs {
		for _, o := range renderedOf(s.t, s.g, "PRStatus") {
			prs = append(prs, map[string]interface{}{"metadata": map[string]interface{}{"name": objName(o)}})
		}
	}
	v[graph.NodePRStatuses] = prs
	status := map[string]interface{}{"phase": s.phase}
	if s.waitingForSlot {
		status["conditions"] = []interface{}{
			map[string]interface{}{"type": "Ready", "status": "False"},
			map[string]interface{}{"type": graph.CondBundleWaitingForSlot, "status": "True"},
		}
	}
	v["bundle"] = map[string]interface{}{"status": status}
	return v
}

// def evaluates every field of def node id over vars.
func (s *compactSim) def(id string, vars map[string]interface{}) map[string]interface{} {
	s.t.Helper()
	out := map[string]interface{}{}
	for k, v := range s.nodes[id].Def {
		if str, ok := v.(string); ok {
			out[k] = evalCEL(s.t, str, vars)
		} else {
			out[k] = v
		}
	}
	return out
}

// wave returns the environments the Graph creates PromotionSteps for, and
// whether the Graph's PromotionProgress is ready.
func (s *compactSim) wave() (envs []string, complete bool) {
	s.t.Helper()
	vars := s.vars()
	vars[graph.NodePromotionState] = s.def(graph.NodePromotionState, vars)
	if _, ok := s.nodes[graph.NodePromotionEligible]; ok {
		vars[graph.NodePromotionEligible] = s.def(graph.NodePromotionEligible, vars)
	}
	wave := s.def(graph.NodePromotionWave, vars)
	for _, e := range wave["steps"].([]interface{}) {
		envs = append(envs, e.(map[string]interface{})["environment"].(string))
	}
	sort.Strings(envs)
	progress := s.def(graph.NodePromotionProgress, vars)
	vars[graph.NodePromotionProgress] = progress
	ready := s.nodes[graph.NodePromotionProgress].ReadyWhen
	require.Len(s.t, ready, 1)
	return envs, evalCEL(s.t, ready[0], vars).(bool)
}

// advance creates the steps the wave admits (state "") and returns them.
func (s *compactSim) advance() []string {
	envs, _ := s.wave()
	for _, e := range envs {
		if _, ok := s.steps[e]; !ok {
			s.steps[e] = ""
		}
	}
	return envs
}

func compactPipeline(envs ...kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
	p := pipelineOf("app", envs...)
	p.Annotations = map[string]string{graph.AnnotationGraphShape: graph.GraphShapeCompact}
	return p
}

// TestCompact_Shape checks the compact Graph: one PromotionSteps collection
// over the PromotionWave def instead of a node per environment, the DAG as
// data, and the steps rendered as the node shape renders them.
func TestCompact_Shape(t *testing.T) {
	p := compactPipeline(kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "uat"}, kardinalv1alpha1.EnvironmentSpec{Name: "prod"})
	gate := makePolicyGate("no-weekend", "platform-policies", "prod", "!schedule.isWeekend")
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{gate}})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	assert.True(t, res.Compact)
	var ids []string
	for _, n := range res.Graph.Spec.Nodes {
		ids = append(ids, n.ID)
	}
	assert.ElementsMatch(t, []string{"bundle", "PolicyGateData", "PolicyGates", "PRStatusData", "PRStatuses",
		"PromotionDAG", "StepsObserved", "PromotionState", "PromotionWave", "PromotionSteps", "PromotionProgress"}, ids)
	assert.Equal(t, map[string][]string{"test": nil, "uat": {"test"}, "prod": {"uat"}}, res.Upstreams)

	// The node shape's step for prod, and the compact one, are the same object.
	nodes, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: pipelineOf("app",
		kardinalv1alpha1.EnvironmentSpec{Name: "test"}, kardinalv1alpha1.EnvironmentSpec{Name: "uat"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod"}),
		Bundle: makeBundle("app-x7k2m", "app"), PolicyGates: []kardinalv1alpha1.PolicyGate{gate}})
	require.NoError(t, err)
	assert.False(t, nodes.Compact)
	prodNode := nodeByID(nodes.Graph.Spec.Nodes)["prod"].Template

	sim := newCompactSim(t, res.Graph)
	for name := range sim.gatesReady {
		sim.gatesReady[name] = true
	}
	sim.steps["test"], sim.steps["uat"] = "Verified", "Verified"
	vars := sim.vars()
	vars[graph.NodePromotionState] = sim.def(graph.NodePromotionState, vars)
	vars[graph.NodePromotionWave] = sim.def(graph.NodePromotionWave, vars)
	var prod map[string]interface{}
	for _, item := range vars[graph.NodePromotionWave].(map[string]interface{})["steps"].([]interface{}) {
		if item.(map[string]interface{})["environment"] == "prod" {
			vars["Step"] = item
			prod = renderValue(t, nodeByID(res.Graph.Spec.Nodes)[graph.NodePromotionSteps].Template, vars).(map[string]interface{})
		}
	}
	require.NotNil(t, prod, "prod is admitted")
	assert.Equal(t, objName(prodNode), objName(prod))
	compactLabels := objLabels(prod)
	assert.Contains(t, compactLabels, graph.LabelBundleUID, "a compact step carries its Bundle's UID")
	delete(compactLabels, graph.LabelBundleUID)
	assert.Equal(t, objLabels(prodNode), compactLabels)
	sel := nodeByID(res.Graph.Spec.Nodes)[graph.NodeStepsObserved].Ref["metadata"].(map[string]interface{})["selector"]
	assert.Equal(t, map[string]interface{}{"matchLabels": map[string]interface{}{
		"kardinal.io/pipeline": "app", "kardinal.io/bundle": "app-x7k2m", graph.LabelBundleUID: "",
		"kro.run/node-id": graph.NodePromotionSteps,
	}}, sel, "the Graph reads back only the steps it made")
	spec := prod["spec"].(map[string]interface{})
	nodeSpec := prodNode["spec"].(map[string]interface{})
	assert.Equal(t, nodeSpec["pipelineName"], spec["pipelineName"])
	assert.Equal(t, "app-x7k2m", spec["bundleName"])
	assert.Equal(t, nodeSpec["stepType"], spec["stepType"])
	assert.Equal(t, []interface{}{"Verified"}, spec["upstreamStates"])
	assert.Equal(t, []interface{}{res.GateInstances[0].Name}, spec["requiredGates"])
	assert.Equal(t, nodeSpec["prStatusRef"], spec["prStatusRef"])
}

// TestCompact_Admission walks a compact Graph through a promotion: an
// environment is admitted once its upstreams are Verified and its gates
// ready, whether or not the PRStatuses collection is published, and never
// after the Bundle is Superseded; a step that exists stays admitted whatever happens later; the
// Graph is complete only when every environment is Verified.
func TestCompact_Admission(t *testing.T) {
	p := compactPipeline(
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "eu", DependsOn: []string{"test"}},
		kardinalv1alpha1.EnvironmentSpec{Name: "us", DependsOn: []string{"test"}},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", DependsOn: []string{"eu", "us"}},
	)
	gates := []kardinalv1alpha1.PolicyGate{
		makePolicyGate("soak", "platform-policies", "prod", "true"),
		makePolicyGate("eu-window", "platform-policies", "eu", "true"),
	}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app"), PolicyGates: gates})
	require.NoError(t, err)
	sim := newCompactSim(t, res.Graph)
	gate := func(env string) string {
		for _, g := range res.GateInstances {
			if g.Labels["kardinal.io/environment"] == env {
				return g.Name
			}
		}
		t.Fatalf("no gate for %s", env)
		return ""
	}

	// The wave does not wait for the PRStatuses collection (G11): a PRStatus
	// that cannot be created holds only its own environment.
	sim.prs = false
	envs, done := sim.wave()
	assert.Equal(t, []string{"test"}, envs, "the root starts without the PRStatuses collection")
	assert.False(t, done)
	assert.Equal(t, []string{"test"}, sim.advance(), "the root starts")

	sim.steps["test"] = "Verified"
	assert.Equal(t, []string{"test", "us"}, sim.advance(), "eu waits for its gate")
	sim.gatesReady[gate("eu")] = true
	assert.Equal(t, []string{"eu", "test", "us"}, sim.advance())

	sim.steps["eu"] = "Verified"
	assert.Equal(t, []string{"eu", "test", "us"}, sim.advance(), "prod waits for us")
	sim.gatesReady[gate("eu")] = false
	assert.Equal(t, []string{"eu", "test", "us"}, sim.advance(), "a started step stays when its gate closes")

	sim.steps["us"] = "Verified"
	assert.Equal(t, []string{"eu", "test", "us"}, sim.advance(), "prod waits for its gate")
	sim.phase = "Superseded"
	sim.gatesReady[gate("prod")] = true
	envs, done = sim.wave()
	assert.Equal(t, []string{"eu", "test", "us"}, envs, "a Superseded Bundle starts nothing new")
	assert.False(t, done)

	sim.phase = "Rejected"
	envs, _ = sim.wave()
	assert.Equal(t, []string{"eu", "test", "us"}, envs, "a Rejected Bundle starts nothing new")

	// A Failed Bundle waiting for a maxConcurrentPromotions slot (#1349)
	// starts nothing new either.
	sim.phase = "Failed"
	sim.waitingForSlot = true
	envs, _ = sim.wave()
	assert.Equal(t, []string{"eu", "test", "us"}, envs, "a Bundle waiting for a slot starts nothing new")
	sim.waitingForSlot = false

	sim.phase = "Promoting"
	assert.Equal(t, []string{"eu", "prod", "test", "us"}, sim.advance())
	_, done = sim.wave()
	assert.False(t, done, "not complete until prod is Verified")
	sim.steps["prod"] = "Verified"
	_, done = sim.wave()
	assert.True(t, done)

	delete(sim.steps, "us")
	envs, _ = sim.wave()
	assert.Contains(t, envs, "us", "a deleted step whose upstreams are Verified is admitted again")
}

// TestCompact_ShapeChoice checks when the builder uses the compact shape: above
// DefaultCompactAbove environments, or when the Pipeline's
// kardinal.io/graph-shape annotation says so; an unknown value is refused.
func TestCompact_ShapeChoice(t *testing.T) {
	envs := func(n int) []kardinalv1alpha1.EnvironmentSpec {
		var out []kardinalv1alpha1.EnvironmentSpec
		for i := 0; i < n; i++ {
			out = append(out, kardinalv1alpha1.EnvironmentSpec{Name: fmt.Sprintf("e%03d", i)})
		}
		return out
	}
	tests := []struct {
		name       string
		envs       int
		annotation string
		above      int
		want       bool
		wantErr    string
	}{
		{name: "100 environments", envs: 100},
		{name: "101 environments", envs: 101, want: true},
		{name: "annotation compact", envs: 3, annotation: "compact", want: true},
		{name: "annotation nodes", envs: 150, annotation: "nodes"},
		{name: "builder threshold", envs: 3, above: 2, want: true},
		{name: "builder threshold zero", envs: 1, above: 0, want: true},
		{name: "unknown annotation", envs: 3, annotation: "flat", wantErr: `kardinal.io/graph-shape="flat"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := pipelineOf("app", envs(tc.envs)...)
			if tc.annotation != "" {
				p.Annotations = map[string]string{graph.AnnotationGraphShape: tc.annotation}
			}
			b := graph.NewBuilder()
			if tc.above != 0 || strings.Contains(tc.name, "zero") {
				b.CompactAbove = tc.above
			}
			res, err := b.Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app")})
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.ErrorIs(t, err, graph.ErrInvalid)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.Compact)
			assertKroValid(t, res.Graph)
		})
	}
}

// TestCompact_ChunksGateCollections checks that more gate instances than one
// collection may hold (kro's 1,000-item forEach limit) go into further
// collection nodes, and that the compact shape reads readiness from all of them.
func TestCompact_ChunksGateCollections(t *testing.T) {
	var envs []kardinalv1alpha1.EnvironmentSpec
	var gates []kardinalv1alpha1.PolicyGate
	for i := 0; i < 260; i++ {
		name := fmt.Sprintf("r%03d", i)
		envs = append(envs, kardinalv1alpha1.EnvironmentSpec{Name: name})
		for g := 0; g < 4; g++ {
			gates = append(gates, makePolicyGate(fmt.Sprintf("%s-g%d", name, g), "platform-policies", name, "true"))
		}
	}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: pipelineOf("app", envs...),
		Bundle: makeBundle("app-v1", "app"), PolicyGates: gates})
	require.NoError(t, err)
	assert.True(t, res.Compact)
	nodes := nodeByID(res.Graph.Spec.Nodes)
	require.Contains(t, nodes, "PolicyGates")
	require.Contains(t, nodes, "PolicyGates2")
	assert.Len(t, renderedOf(t, res.Graph, "PolicyGate"), 1040)
	byNode := map[string]int{}
	for _, o := range renderObjects(t, res.Graph) {
		byNode[o.NodeID]++
	}
	assert.Equal(t, graph.MaxCollectionItems, byNode["PolicyGates"])
	assert.Equal(t, 40, byNode["PolicyGates2"])
	state := nodes[graph.NodePromotionState].Def["readyGates"].(string)
	assert.Contains(t, state, "PolicyGates.filter(")
	assert.Contains(t, state, "PolicyGates2.filter(")
	assert.Equal(t, 1040+260+260, graph.ObjectCount(res.Graph))
}

// TestCompact_SameObjectKinds is the guard for features added later: a
// feature-rich Pipeline (dependsOn, waves, pr-review, org and team gates, a
// skip-permission gate, a Bundle intent) is built in both shapes, and the
// compact Graph must create objects of the same kinds as the node shape, or
// refuse with ErrInvalid (compactUnsupported). A feature that adds an object
// kind or a node to the node shape only fails here until it either has a
// compact implementation or registers a refusal. Extend the fixture with each
// new feature.
func TestCompact_SameObjectKinds(t *testing.T) {
	p := pipelineOf("app",
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "uat-eu", Wave: 1, DependsOn: []string{"test"}},
		kardinalv1alpha1.EnvironmentSpec{Name: "uat-us", Wave: 1, DependsOn: []string{"test"}},
		kardinalv1alpha1.EnvironmentSpec{Name: "canary", DependsOn: []string{"uat-eu", "uat-us"}},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", Approval: "pr-review", DependsOn: []string{"canary"}},
	)
	gates := []kardinalv1alpha1.PolicyGate{
		makePolicyGate("no-weekend", "platform-policies", "prod", "!schedule.isWeekend"),
		makePolicyGate("soak", "app-ns", "uat-eu", "upstream.test.soakMinutes >= 30"),
		// An org gate on the skipped canary: skipping it needs a
		// SkipPermission gate, so the Graph gets SkipPermissionGates.
		makePolicyGate("canary-freeze", "platform-policies", "canary", "!schedule.isWeekend"),
		// Reads a per-promotion MetricCheck template: the node shape creates a
		// MetricCheck instance for prod.
		makePolicyGate("error-budget", "app-ns", "prod", `metrics["error-rate"].result == "Pass"`),
	}
	// An approval gate (#1510): its instance comes from the ApprovalGates
	// collection and reads the Bundle's Approvals ref.
	approval := makePolicyGate("two-approvers", "platform-policies", "prod", "true")
	approval.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 2}
	gates = append(gates, approval)
	checks := []kardinalv1alpha1.MetricCheck{metricTemplate("error-rate", "up")}
	skip := makePolicyGate("allow-skip-canary", "platform-policies", "canary", "true")
	skip.Spec.SkipPermission = true
	skip.Labels["kardinal.io/type"] = "skip-permission"
	gates = append(gates, skip)
	policyNamespaces := []string{"platform-policies"}
	// Hooks, analyses and image verification are not in the fixture: the
	// compact shape refuses them (compactUnsupported, TestCompact_Refuses*).

	kinds := func(g *graph.Graph) map[string]bool {
		out := map[string]bool{}
		for _, n := range g.Spec.Nodes {
			if n.Template != nil {
				out[fmt.Sprint(n.Template["kind"])] = true
			}
		}
		return out
	}
	hasNode := func(g *graph.Graph, prefix string) bool {
		for _, n := range g.Spec.Nodes {
			if strings.HasPrefix(n.ID, prefix) {
				return true
			}
		}
		return false
	}

	bundles := map[string]func(b *kardinalv1alpha1.Bundle){
		"image": func(*kardinalv1alpha1.Bundle) {},
		"config": func(b *kardinalv1alpha1.Bundle) {
			b.Spec.Type, b.Spec.Images = "config", nil
			b.Spec.ConfigRef = &kardinalv1alpha1.ConfigRef{GitRepo: "https://github.com/org/config", CommitSHA: strings.Repeat("abc1", 10)}
		},
		"mixed": func(b *kardinalv1alpha1.Bundle) {
			b.Spec.Type = "mixed"
			b.Spec.ConfigRef = &kardinalv1alpha1.ConfigRef{GitRepo: "https://github.com/org/config", CommitSHA: strings.Repeat("abc1", 10)}
		},
	}
	for typ, mutate := range bundles {
		t.Run(typ, func(t *testing.T) {
			b := makeBundle("app-x7k2m", "app")
			b.Spec.Images = []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v1", Digest: ivDigest}}
			mutate(b)
			b.Spec.Intent = &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"canary"}}
			build := func(shape string) (*graph.BuildResult, error) {
				pp := p.DeepCopy()
				pp.Annotations = map[string]string{graph.AnnotationGraphShape: shape}
				return graph.NewBuilder().Build(graph.BuildInput{
					Pipeline: pp, Bundle: b, PolicyGates: gates, PolicyNamespaces: policyNamespaces, MetricChecks: checks,
					Analyses: analysisInput(smokeTemplate("AnalysisTemplate", "smoke", "1")),
				})
			}
			nodes, err := build(graph.GraphShapeNodes)
			require.NoError(t, err)
			// Every expression of both shapes reads only nodes they have
			// (assertKroValid runs assertRefsResolve).
			assertKroValid(t, nodes.Graph)
			for _, k := range []string{"PromotionStep", "PolicyGate", "PRStatus", "MetricCheck"} {
				require.True(t, kinds(nodes.Graph)[k], "the fixture exercises %s", k)
			}
			require.True(t, hasNode(nodes.Graph, graph.NodeSkipPermissionGates),
				"the fixture exercises %s", graph.NodeSkipPermissionGates)
			compact, err := build(graph.GraphShapeCompact)
			require.NoError(t, err, "the compact shape carries every feature of the fixture: no refusal")
			assertKroValid(t, compact.Graph)
			var compactMetrics []string
			for _, n := range compact.Graph.Spec.Nodes {
				if n.ID == graph.NodeMetricCheckData {
					for _, it := range n.Def["items"].([]interface{}) {
						compactMetrics = append(compactMetrics, it.(map[string]interface{})["name"].(string))
					}
				}
			}
			assert.ElementsMatch(t, mapKeys(metricNodes(t, nodes.Graph)), compactMetrics, "the same MetricCheck instances")
			assert.Equal(t, kinds(nodes.Graph), kinds(compact.Graph), "the compact Graph creates the same object kinds")
			assert.Equal(t, nodes.Environments, compact.Environments)
			assert.ElementsMatch(t, gateNames(nodes.GateInstances), gateNames(compact.GateInstances), "the same gate instances")
			assert.Contains(t, strings.Join(gateNames(compact.GateInstances), ","), "allow-skip-canary",
				"the compact Graph carries the SkipPermission gate instance")
		})
	}
}

func gateNames(gs []kardinalv1alpha1.PolicyGate) []string {
	out := make([]string, len(gs))
	for i := range gs {
		out[i] = gs[i].Name
	}
	return out
}

// TestCompact_RefusesHooks: a compact Graph is refused for a Pipeline with
// hooks, naming the feature, and the Pipeline reconciler's Pipeline-only
// check reports it.
func TestCompact_RefusesHooks(t *testing.T) {
	p := hookPipeline()
	p.Annotations = map[string]string{graph.AnnotationGraphShape: graph.GraphShapeCompact}
	_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app")})
	require.ErrorIs(t, err, graph.ErrInvalid)
	assert.Contains(t, err.Error(), "does not support pre- and post-deploy hooks (spec.environments[].hooks) yet")
	assert.Contains(t, graph.CompactUnsupported(graph.BuildInput{Pipeline: p}), "pre- and post-deploy hooks (spec.environments[].hooks)")
	assert.Empty(t, graph.CompactUnsupported(graph.BuildInput{Pipeline: makeLinearPipeline("app", "test")}))
}

// TestCompact_RefusesVerification: a compact Graph is refused for a
// Pipeline with spec.verification, naming the feature, and the Pipeline
// reconciler's Pipeline-only check reports it.
func TestCompact_RefusesVerification(t *testing.T) {
	p := analysisPipeline(kardinalv1alpha1.AnalysisTemplateRef{Name: "smoke"})
	p.Annotations = map[string]string{graph.AnnotationGraphShape: graph.GraphShapeCompact}
	_, err := buildAnalysis(t, p, analysisInput(smokeTemplate("AnalysisTemplate", "smoke", "1")))
	require.ErrorIs(t, err, graph.ErrInvalid)
	assert.Contains(t, err.Error(), "does not support Argo Rollouts analysis (spec.environments[].verification) yet")
	assert.Contains(t, graph.CompactUnsupported(graph.BuildInput{Pipeline: p}), "Argo Rollouts analysis (spec.environments[].verification)")
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestCompact_MetricCheckAdmission checks the compact shape's per-promotion
// MetricCheck instances (#1479): an instance is created once its
// environment's upstreams are all Verified, as in the node shape (before the
// environment's step, whose gate reads it), and stays once the environment
// has started even if an upstream is no longer Verified. Its rendered spec
// matches the node shape's, without the node shape's hold.
func TestCompact_MetricCheckAdmission(t *testing.T) {
	p := compactPipeline(
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", DependsOn: []string{"test"}},
	)
	gates := []kardinalv1alpha1.PolicyGate{
		makePolicyGate("errors", "default", "prod", `metrics["error-rate"].result == "Pass"`),
		makePolicyGate("errors-test", "default", "test", `metrics["error-rate"].result == "Pass"`),
	}
	metrics := []kardinalv1alpha1.MetricCheck{metricTemplate("error-rate", `rate(errors{v="{{ bundle.version }}"}[5m])`)}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app"),
		PolicyGates: gates, MetricChecks: metrics})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	sim := newCompactSim(t, res.Graph)
	admitted := func() []string {
		vars := sim.vars()
		vars[graph.NodePromotionState] = sim.def(graph.NodePromotionState, vars)
		var envs []string
		for _, m := range sim.def(graph.NodePromotionMetrics, vars)["items"].([]interface{}) {
			envs = append(envs, m.(map[string]interface{})["environment"].(string))
		}
		sort.Strings(envs)
		return envs
	}
	assert.Equal(t, []string{"test"}, admitted(), "the root has no upstreams")
	sim.steps["test"] = ""
	assert.Equal(t, []string{"test"}, admitted(), "prod waits for test to be Verified")
	sim.steps["test"] = "Verified"
	assert.Equal(t, []string{"prod", "test"}, admitted(), "prod's instance exists before prod's step")
	// Before prod's step starts, test leaving Verified takes prod's instance
	// out of the collection, so kro prunes it (QA #1543; documented in
	// pipeline-reference).
	sim.steps["test"] = "Failed"
	assert.Equal(t, []string{"test"}, admitted(), "an instance of an environment not started yet is deleted")
	sim.steps["test"] = "Verified"
	assert.Equal(t, []string{"prod", "test"}, admitted(), "and created again once test is Verified again")
	sim.steps["prod"] = ""
	sim.steps["test"] = "Failed"
	assert.Equal(t, []string{"prod", "test"}, admitted(), "a started environment keeps its instance")

	// The rendered spec is the node shape's, without the hold.
	nodesP := p.DeepCopy()
	nodesP.Annotations = map[string]string{graph.AnnotationGraphShape: graph.GraphShapeNodes}
	nodesRes, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: nodesP, Bundle: makeBundle("app-x7k2m", "app"),
		PolicyGates: gates, MetricChecks: metrics})
	require.NoError(t, err)
	var items []interface{}
	for _, n := range res.Graph.Spec.Nodes {
		if n.ID == graph.NodeMetricCheckData {
			items = n.Def["items"].([]interface{})
		}
	}
	require.Len(t, items, 2)
	assert.Equal(t, graph.ObjectCount(nodesRes.Graph), graph.ObjectCount(res.Graph),
		"the size guard counts the compact instances as the node shape's")
	for _, it := range items {
		item := it.(map[string]interface{})
		node := metricNodes(t, nodesRes.Graph)[item["name"].(string)]
		require.NotNil(t, node.Template, item["name"])
		want := node.Template["spec"].(map[string]interface{})
		got := item["spec"].(map[string]interface{})
		assert.Equal(t, want["suspend"], got["suspend"])
		assert.Equal(t, want["threshold"], got["threshold"])
		assert.Contains(t, got["query"], `v="v1"`, "placeholders rendered")
		if item["environment"] == "test" {
			assert.Equal(t, want["query"], got["query"], "no hold on the root in either shape")
		}
	}
}

// TestCompact_HeldEnvironment (#1528): in the compact shape, an environment
// the Pipeline holds on another Bundle's rollback is not admitted, however
// ready its upstreams and gates; the hold's own Bundle is admitted there.
func TestCompact_HeldEnvironment(t *testing.T) {
	p := compactPipeline(
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", DependsOn: []string{"test"}},
	)
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "app-rollback-abc123", Reason: "INC-42"}}
	for bundle, want := range map[string][]string{
		"app-x7k2m":           {"test"},
		"app-rollback-abc123": {"prod", "test"},
	} {
		res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle(bundle, "app")})
		require.NoError(t, err)
		assertKroValid(t, res.Graph)
		sim := newCompactSim(t, res.Graph)
		sim.advance()
		sim.steps["test"] = "Verified"
		envs, _ := sim.wave()
		assert.Equal(t, want, envs, bundle)
	}
}

// TestCompact_ApprovalGates checks approval gates (#1510) in the compact
// shape: the Graph has the Approvals ref and the ApprovalGates collection,
// every expression reads only nodes it has, the gate instance names match the
// node shape's, and an environment whose approval gate is not ready is not
// admitted while the others advance.
func TestCompact_ApprovalGates(t *testing.T) {
	p := compactPipeline(
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", DependsOn: []string{"test"}},
	)
	approval := makePolicyGate("two-approvers", "platform-policies", "prod", "true")
	approval.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 2}
	reads := makePolicyGate("one-ok", "platform-policies", "test", `approvals.count >= 1`)
	gates := []kardinalv1alpha1.PolicyGate{approval, reads}
	b := makeBundle("app-x7k2m", "app")
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b, PolicyGates: gates})
	require.NoError(t, err, "the compact shape carries approval gates")
	assertKroValid(t, res.Graph)
	nodes := nodeByID(res.Graph.Spec.Nodes)
	require.Contains(t, nodes, graph.ApprovalsNodeID)
	require.Contains(t, nodes, graph.NodeApprovalGates)
	assert.Contains(t, nodes[graph.NodePromotionState].Def["readyGates"], graph.NodeApprovalGates,
		"admission reads the approval gates' readiness")

	np := p.DeepCopy()
	np.Annotations = map[string]string{graph.AnnotationGraphShape: graph.GraphShapeNodes}
	nodesRes, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: np, Bundle: b, PolicyGates: gates})
	require.NoError(t, err)
	assert.ElementsMatch(t, gateNames(nodesRes.GateInstances), gateNames(res.GateInstances), "the same gate instances")

	sim := newCompactSim(t, res.Graph)
	var testGate, prodGate string
	for _, g := range res.GateInstances {
		switch g.Labels["kardinal.io/environment"] {
		case "test":
			testGate = g.Name
		case "prod":
			prodGate = g.Name
		}
	}
	require.NotEmpty(t, testGate)
	require.NotEmpty(t, prodGate)
	envs, _ := sim.wave()
	assert.Empty(t, envs, "test waits for its approvals gate")
	sim.gatesReady[testGate] = true
	assert.Equal(t, []string{"test"}, sim.advance())
	sim.steps["test"] = "Verified"
	assert.Equal(t, []string{"test"}, sim.advance(), "prod waits for two approvers")
	sim.gatesReady[prodGate] = true
	assert.Equal(t, []string{"prod", "test"}, sim.advance())
}
