// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// fleetPipeline is test → prod (a fleet of eu, us, ap, two at a time) → post.
func fleetPipeline(maxConcurrent int) *kardinalv1alpha1.Pipeline {
	return pipelineOf("app",
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", Path: "clusters/prod", Fleet: &kardinalv1alpha1.FleetSpec{
			MaxConcurrent: maxConcurrent,
			Targets: []kardinalv1alpha1.FleetTarget{
				{Name: "eu"},
				{Name: "us", Path: "us/prod"},
				{Name: "ap", Health: &kardinalv1alpha1.HealthConfig{Type: "resource",
					Resource: &kardinalv1alpha1.ResourceRef{Name: "ap-app", Namespace: "ap"}}},
			},
		}},
		kardinalv1alpha1.EnvironmentSpec{Name: "post"},
	)
}

// TestFleet_Expansion checks how a fleet environment becomes one environment
// per target: names, order, upstreams, paths, health, gates per target, and
// the compact shape.
func TestFleet_Expansion(t *testing.T) {
	gate := makePolicyGate("change-freeze", "platform-policies", "prod", "true")
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: fleetPipeline(2),
		Bundle: makeBundle("app-x7k2m", "app"), PolicyGates: []kardinalv1alpha1.PolicyGate{gate}})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	assert.True(t, res.Compact, "a Pipeline with a fleet is compact")
	assert.Equal(t, []string{"test", "prod-eu", "prod-us", "prod-ap", "post"}, res.Environments)
	assert.Equal(t, map[string][]string{
		"test": nil, "prod-eu": {"test"}, "prod-us": {"test"}, "prod-ap": {"test"},
		"post": {"prod-eu", "prod-us", "prod-ap"},
	}, res.Upstreams)
	var gateEnvs []string
	for _, g := range res.GateInstances {
		gateEnvs = append(gateEnvs, g.Labels["kardinal.io/environment"])
	}
	assert.ElementsMatch(t, []string{"prod-eu", "prod-us", "prod-ap"}, gateEnvs, "a gate on the fleet applies to every target")

	p := fleetPipeline(2)
	eu, ok := graph.EnvironmentSpecFor(p, "prod-eu")
	require.True(t, ok)
	assert.Equal(t, "clusters/prod/eu", eu.Path, "default path: the fleet's path and the target")
	assert.Nil(t, eu.Fleet)
	us, _ := graph.EnvironmentSpecFor(p, "prod-us")
	assert.Equal(t, "us/prod", us.Path)
	ap, _ := graph.EnvironmentSpecFor(p, "prod-ap")
	assert.Equal(t, "ap-app", ap.Health.Resource.Name)
	_, ok = graph.EnvironmentSpecFor(p, "prod")
	assert.False(t, ok, "the fleet environment itself is not promoted")
	assert.Equal(t, "prod", graph.FleetOf(p, "prod-us"))
	assert.Empty(t, graph.FleetOf(p, "test"))

	order, err := graph.PromotedEnvironments(p, makeBundle("app-x7k2m", "app"))
	require.NoError(t, err)
	assert.Equal(t, []string{"test", "prod-eu", "prod-us", "prod-ap", "post"}, order)
}

// TestFleet_Pacing walks a fleet of three targets, two at a time: the first
// two start once their upstream is Verified, the third when one of them is
// Verified, a Failed target keeps its place, and the environment after the
// fleet waits for every target.
func TestFleet_Pacing(t *testing.T) {
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: fleetPipeline(2), Bundle: makeBundle("app-x7k2m", "app")})
	require.NoError(t, err)
	assert.Contains(t, nodeByID(res.Graph.Spec.Nodes), graph.NodePromotionEligible)
	sim := newCompactSim(t, res.Graph)
	sim.fleet = map[string]string{"prod-eu": "prod", "prod-us": "prod", "prod-ap": "prod"}

	assert.Equal(t, []string{"test"}, sim.advance())
	sim.steps["test"] = "Verified"
	assert.Equal(t, []string{"prod-eu", "prod-us", "test"}, sim.advance(), "two targets at a time, in fleet order")
	sim.steps["prod-us"] = "Failed"
	assert.Equal(t, []string{"prod-eu", "prod-us", "test"}, sim.advance(), "a Failed target keeps its place")
	sim.steps["prod-us"] = "Verified" // retried
	assert.Equal(t, []string{"prod-ap", "prod-eu", "prod-us", "test"}, sim.advance())
	sim.steps["prod-eu"] = "Verified"
	assert.NotContains(t, sim.advance(), "post", "post waits for every target")
	sim.steps["prod-ap"] = "Verified"
	assert.Contains(t, sim.advance(), "post")

	// maxConcurrent 0: every ready target at once.
	res, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: fleetPipeline(0), Bundle: makeBundle("app-x7k2m", "app")})
	require.NoError(t, err)
	sim = newCompactSim(t, res.Graph)
	sim.fleet = map[string]string{"prod-eu": "prod", "prod-us": "prod", "prod-ap": "prod"}
	sim.steps["test"] = "Verified"
	assert.Equal(t, []string{"prod-ap", "prod-eu", "prod-us", "test"}, sim.advance())
}

// TestFleet_Intent checks a Bundle's intent with fleets: targetEnvironment
// may name a fleet (the Graph stops after its targets); skipping a fleet is
// refused.
func TestFleet_Intent(t *testing.T) {
	b := makeBundle("app-x7k2m", "app")
	b.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: "prod"}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: fleetPipeline(2), Bundle: b})
	require.NoError(t, err)
	assert.Equal(t, []string{"test", "prod-eu", "prod-us", "prod-ap"}, res.Environments)

	b.Spec.Intent = &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"prod"}}
	_, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: fleetPipeline(2), Bundle: b})
	require.Error(t, err)
	assert.ErrorIs(t, err, graph.ErrInvalid)
	assert.Contains(t, err.Error(), `skipping a fleet is not supported`)
}

// TestFleet_Invalid checks the fleet errors: the nodes shape (annotated, or
// pinned by the Bundle's existing Graph), a selector the
// controller has not resolved (or could not), no targets, and a target name
// that is not a DNS label or is taken.
func TestFleet_Invalid(t *testing.T) {
	selector := func() *kardinalv1alpha1.Pipeline {
		return pipelineOf("app", kardinalv1alpha1.EnvironmentSpec{Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{
			Selector: &kardinalv1alpha1.FleetSelector{MatchLabels: map[string]string{"fleet": "prod"}}}})
	}
	tests := []struct {
		name    string
		p       func() *kardinalv1alpha1.Pipeline
		shape   string // BuildInput.Shape: the Bundle's existing Graph
		wantErr string
	}{
		{name: "nodes shape", p: func() *kardinalv1alpha1.Pipeline {
			p := fleetPipeline(2)
			p.Annotations = map[string]string{graph.AnnotationGraphShape: graph.GraphShapeNodes}
			return p
		}, wantErr: `a Pipeline with a fleet environment needs the "compact" shape`},
		{name: "Bundle pinned to the nodes shape", p: func() *kardinalv1alpha1.Pipeline { return fleetPipeline(2) },
			shape: graph.GraphShapeNodes, wantErr: `cannot promote a fleet environment; the fleet applies to new Bundles`},
		{name: "selector not resolved", p: selector, wantErr: `its selector has not been resolved yet`},
		{name: "selector failed", p: func() *kardinalv1alpha1.Pipeline {
			p := selector()
			p.Status.Fleets = []kardinalv1alpha1.FleetStatus{{Environment: "prod", Message: "argoproj.io Applications are not served"}}
			return p
		}, wantErr: `could not be resolved: argoproj.io Applications are not served`},
		{name: "no targets", p: func() *kardinalv1alpha1.Pipeline {
			p := selector()
			p.Status.Fleets = []kardinalv1alpha1.FleetStatus{{Environment: "prod"}}
			return p
		}, wantErr: `fleet environment "prod" has no targets`},
		{name: "name taken", p: func() *kardinalv1alpha1.Pipeline {
			p := fleetPipeline(1)
			p.Spec.Environments = append(p.Spec.Environments, kardinalv1alpha1.EnvironmentSpec{Name: "prod-eu"})
			return p
		}, wantErr: `environment name "prod-eu" is already used`},
		{name: "name too long", p: func() *kardinalv1alpha1.Pipeline {
			p := fleetPipeline(1)
			p.Spec.Environments[1].Name = "production-environment-for-the-whole-company-x"
			p.Spec.Environments[1].Fleet.Targets[0].Name = "europe-west-1-a-zone-b"
			return p
		}, wantErr: `is not a DNS label of at most 63 characters`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: tc.p(), Bundle: makeBundle("app-x7k2m", "app"),
				Shape: tc.shape})
			require.Error(t, err)
			assert.ErrorIs(t, err, graph.ErrInvalid)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	// A resolved selector builds, with the members in status order.
	p := selector()
	p.Status.Fleets = []kardinalv1alpha1.FleetStatus{{Environment: "prod", Targets: []kardinalv1alpha1.FleetTarget{
		{Name: "a", Path: "apps/a"}, {Name: "b", Path: "apps/b"}}}}
	p.ObjectMeta = metav1.ObjectMeta{Name: "app", Namespace: "default"}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
	require.NoError(t, err)
	assert.Equal(t, []string{"prod-a", "prod-b"}, res.Environments)
}

// bigFleet is test → prod, a fleet of n targets t00..t<n-1>, maxConcurrent
// at a time, stopping at maxUnavailable failures (nil: unset).
func bigFleet(n, maxConcurrent int, maxUnavailable *int) *kardinalv1alpha1.Pipeline {
	var targets []kardinalv1alpha1.FleetTarget
	for i := 0; i < n; i++ {
		targets = append(targets, kardinalv1alpha1.FleetTarget{Name: fmt.Sprintf("t%02d", i),
			Labels: map[string]string{"tier": []string{"canary", "main"}[min(i, 1)]}})
	}
	return pipelineOf("app",
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{
			MaxConcurrent: maxConcurrent, MaxUnavailable: maxUnavailable, Targets: targets}},
	)
}

func fleetSim(t *testing.T, p *kardinalv1alpha1.Pipeline) *compactSim {
	t.Helper()
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	sim := newCompactSim(t, res.Graph)
	sim.fleet = map[string]string{}
	for _, e := range res.Environments {
		if graph.FleetOf(p, e) != "" {
			sim.fleet[e] = graph.FleetOf(p, e)
		}
	}
	return sim
}

func inFlight(sim *compactSim) int {
	n := 0
	for env, st := range sim.steps {
		if sim.fleet[env] != "" && st != "Verified" {
			n++
		}
	}
	return n
}

// TestFleet_FiftyTargetsFiveAtATime walks a 50-target fleet with
// maxConcurrent 5 to the end: never more than 5 targets in flight, targets
// start in fleet order, and every target is Verified once.
//
// Covers FLEET-06.
func TestFleet_FiftyTargetsFiveAtATime(t *testing.T) {
	sim := fleetSim(t, bigFleet(50, 5, nil))
	sim.advance()
	sim.steps["test"] = "Verified"
	var order []string
	for round := 0; round < 100; round++ {
		sim.advance()
		require.LessOrEqual(t, inFlight(sim), 5, "round %d", round)
		// Verify the oldest in-flight target.
		var flying []string
		for env, st := range sim.steps {
			if sim.fleet[env] != "" && st == "" {
				flying = append(flying, env)
			}
		}
		if len(flying) == 0 {
			break
		}
		sort.Strings(flying)
		sim.steps[flying[0]] = "Verified"
		order = append(order, flying[0])
	}
	require.Len(t, order, 50)
	assert.True(t, sort.StringsAreSorted(order), "targets in fleet order: %v", order)
	_, complete := sim.wave()
	assert.True(t, complete)
}

// TestFleet_MaxUnavailableStopsTheRollout: with maxUnavailable 2, the
// second failure stops new targets; the ones in flight still finish, and a
// failure retried to Verified lets the rollout go on.
//
// Covers FLEET-06.
func TestFleet_MaxUnavailableStopsTheRollout(t *testing.T) {
	two := 2
	sim := fleetSim(t, bigFleet(10, 3, &two))
	sim.steps["test"] = "Verified"
	assert.Equal(t, []string{"prod-t00", "prod-t01", "prod-t02", "test"}, sim.advance())
	sim.steps["prod-t00"] = "Failed"
	sim.steps["prod-t01"] = "Verified"
	assert.Contains(t, sim.advance(), "prod-t03", "one failure: the rollout goes on")
	sim.steps["prod-t02"] = "Failed"
	before := len(sim.steps)
	sim.advance()
	assert.Len(t, sim.steps, before, "two failures: no target starts")
	sim.steps["prod-t03"] = "Verified" // in flight, it finished
	sim.advance()
	assert.Len(t, sim.steps, before, "still stopped")
	sim.steps["prod-t02"] = "Verified" // retried
	assert.Contains(t, sim.advance(), "prod-t04", "below maxUnavailable again: the rollout resumes")
}

// TestFleet_TargetSelector: a selector of kind Target picks static targets
// by label, in their order.
//
// Covers FLEET-03.
func TestFleet_TargetSelector(t *testing.T) {
	p := bigFleet(4, 2, nil)
	p.Spec.Environments[1].Fleet.Selector = &kardinalv1alpha1.FleetSelector{Kind: kardinalv1alpha1.FleetSelectorTarget,
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}}}}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
	require.NoError(t, err)
	assert.Equal(t, []string{"test", "prod-t01", "prod-t02", "prod-t03"}, res.Environments, "t00 is the canary, not main")

	p.Spec.Environments[1].Fleet.Selector.MatchExpressions = nil
	p.Spec.Environments[1].Fleet.Selector.MatchLabels = map[string]string{"tier": "nope"}
	_, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no targets")
}

// TestFleet_TargetAddedMidRollout: a target added at the end of the list
// while the fleet rolls out (the Graph rebuilt in place) is promoted after
// the targets before it; the targets already Verified stay so, and the environment
// after the fleet waits for it too.
//
// Covers FLEET-06.
func TestFleet_TargetAddedMidRollout(t *testing.T) {
	p := fleetPipeline(1)
	sim := fleetSim(t, p)
	sim.steps["test"] = "Verified"
	sim.advance()
	sim.steps["prod-eu"] = "Verified"
	sim.advance()

	p.Spec.Environments[1].Fleet.Targets = append(p.Spec.Environments[1].Fleet.Targets, kardinalv1alpha1.FleetTarget{Name: "sa"})
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
	require.NoError(t, err)
	steps := sim.steps
	sim = newCompactSim(t, res.Graph)
	sim.steps = steps
	sim.fleet = map[string]string{"prod-eu": "prod", "prod-us": "prod", "prod-ap": "prod", "prod-sa": "prod"}
	for _, env := range []string{"prod-us", "prod-ap", "prod-sa"} {
		got := sim.advance()
		assert.Contains(t, got, env)
		assert.NotContains(t, got, "post", "post waits for every target, the new one too")
		sim.steps[env] = "Verified"
	}
	assert.Contains(t, sim.advance(), "post")
	assert.Equal(t, "Verified", sim.steps["prod-eu"], "a Verified target is not promoted again")
}

// TestFleet_TargetAddedTakesItsPlace: targets start in target order (list
// position, or name for a selector), so a target added at the front of the
// list starts before targets that were already waiting.
//
// Covers FLEET-06.
func TestFleet_TargetAddedTakesItsPlace(t *testing.T) {
	p := fleetPipeline(1)
	sim := fleetSim(t, p)
	sim.steps["test"] = "Verified"
	assert.Contains(t, sim.advance(), "prod-eu")
	sim.steps["prod-eu"] = "Verified"

	targets := p.Spec.Environments[1].Fleet.Targets
	p.Spec.Environments[1].Fleet.Targets = append([]kardinalv1alpha1.FleetTarget{{Name: "aa"}}, targets...)
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
	require.NoError(t, err)
	steps := sim.steps
	sim = newCompactSim(t, res.Graph)
	sim.steps = steps
	sim.fleet = map[string]string{"prod-aa": "prod", "prod-eu": "prod", "prod-us": "prod", "prod-ap": "prod"}
	got := sim.advance()
	assert.Contains(t, got, "prod-aa", "the new first target starts first")
	assert.NotContains(t, got, "prod-us", "one place: prod-us, queued before the edit, waits")
}

// TestFleet_TargetRemovedMidRollout: a target removed while it is in
// flight leaves the wave (kro prunes its PromotionStep, whose finalizer
// closes its PR), its place is freed, and the environment after the fleet
// waits only for the targets that remain.
//
// Covers FLEET-06.
func TestFleet_TargetRemovedMidRollout(t *testing.T) {
	p := fleetPipeline(1)
	sim := fleetSim(t, p)
	sim.steps["test"] = "Verified"
	assert.Contains(t, sim.advance(), "prod-eu")

	p.Spec.Environments[1].Fleet.Targets = p.Spec.Environments[1].Fleet.Targets[1:] // eu removed, in flight
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
	require.NoError(t, err)
	steps := sim.steps
	sim = newCompactSim(t, res.Graph)
	sim.steps = steps
	sim.fleet = map[string]string{"prod-eu": "prod", "prod-us": "prod", "prod-ap": "prod"}
	got := sim.advance()
	assert.NotContains(t, got, "prod-eu", "the removed target leaves the wave: kro prunes its step")
	delete(sim.steps, "prod-eu") // pruned
	got = sim.advance()
	assert.Contains(t, got, "prod-us", "its place is free")
	sim.steps["prod-us"] = "Verified"
	sim.advance()
	sim.steps["prod-ap"] = "Verified"
	assert.Contains(t, sim.advance(), "post", "post waits only for the targets that remain")
}

// TestFleet_PerPromotionMetricChecks: a gate on a fleet environment that
// reads a per-promotion MetricCheck gets an instance per target, each
// admitted once the fleet's upstreams are Verified (the compact shape's rule,
// #1543), and the fleet still paces the steps.
//
// Covers FLEET-06.
func TestFleet_PerPromotionMetricChecks(t *testing.T) {
	p := bigFleet(4, 1, nil)
	gates := []kardinalv1alpha1.PolicyGate{makePolicyGate("errors", "default", "prod", `metrics["error-rate"].result == "Pass"`)}
	metrics := []kardinalv1alpha1.MetricCheck{metricTemplate("error-rate", `rate(errors{v="{{ bundle.version }}"}[5m])`)}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app"),
		PolicyGates: gates, MetricChecks: metrics})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	assert.Len(t, res.GateInstances, 4, "one gate instance per target")
	var items []interface{}
	for _, n := range res.Graph.Spec.Nodes {
		if n.ID == graph.NodeMetricCheckData {
			items = n.Def["items"].([]interface{})
		}
	}
	var envs []string
	for _, it := range items {
		envs = append(envs, it.(map[string]interface{})["environment"].(string))
	}
	assert.ElementsMatch(t, []string{"prod-t00", "prod-t01", "prod-t02", "prod-t03"}, envs, "one MetricCheck instance per target")
}

// TestFleet_CompactUnsupportedFeatures: a Pipeline with a fleet is always
// compact, so a feature the compact shape does not carry (hooks) refuses
// its Bundles with the feature's name instead of building a Graph without it.
//
// Covers FLEET-06.
func TestFleet_CompactUnsupportedFeatures(t *testing.T) {
	p := bigFleet(3, 1, nil)
	p.Spec.Environments[1].Hooks = []kardinalv1alpha1.HookSpec{hook("smoke", "post", hookJob)}
	_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hooks")
	assert.NotEmpty(t, graph.CompactUnsupported(graph.BuildInput{Pipeline: p}), "the Pipeline reconciler reports it too")
}

// TestFleet_SinkEnvironments (DORA): a fleet that is the last environment
// has every target as a sink; a fleet followed by another environment has
// none.
//
// Covers FLEET-06.
func TestFleet_SinkEnvironments(t *testing.T) {
	p := bigFleet(3, 1, nil)
	sinks, err := graph.SinkEnvironments(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"prod-t00", "prod-t01", "prod-t02"}, sinks)

	p.Spec.Environments = append(p.Spec.Environments, kardinalv1alpha1.EnvironmentSpec{Name: "audit"})
	sinks, err = graph.SinkEnvironments(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"audit"}, sinks)
}

// TestFleet_MaxUnavailableCountsEveryFailure: AbortedByAlarm and RollingBack
// count toward maxUnavailable as Failed does (the Bundle reconciler's
// failedState).
//
// Covers FLEET-06.
func TestFleet_MaxUnavailableCountsEveryFailure(t *testing.T) {
	for _, failed := range []string{"Failed", "AbortedByAlarm", "RollingBack"} {
		t.Run(failed, func(t *testing.T) {
			one := 1
			sim := fleetSim(t, bigFleet(5, 2, &one))
			sim.steps["test"] = "Verified"
			sim.advance()
			sim.steps["prod-t00"] = failed
			sim.steps["prod-t01"] = "Verified"
			before := len(sim.steps)
			sim.advance()
			assert.Len(t, sim.steps, before, "%s stops the rollout at maxUnavailable 1", failed)
		})
	}
}

// TestFleet_LastGoodMembers: a selector fleet whose status.fleets entry has
// a message and targets (the last good membership, kept on a read error)
// builds with those targets; one with a message and no targets is refused.
//
// Covers FLEET-03.
func TestFleet_LastGoodMembers(t *testing.T) {
	p := pipelineOf("app", kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{
			Selector: &kardinalv1alpha1.FleetSelector{MatchLabels: map[string]string{"tier": "prod"}}}})
	p.Status.Fleets = []kardinalv1alpha1.FleetStatus{{Environment: "prod", Message: "list Applications in argocd: timeout",
		Targets: []kardinalv1alpha1.FleetTarget{{Name: "eu", Path: "clusters/eu"}}}}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
	require.NoError(t, err)
	assert.Equal(t, []string{"test", "prod-eu"}, res.Environments)

	p.Status.Fleets[0].Targets = nil
	_, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
	assert.ErrorContains(t, err, "could not be resolved: list Applications in argocd: timeout")
}

// TestFleet_HeldByFleetHold: a hold on the fleet (kardinal rollback --env
// <fleet> --hold) marks every target's DAG entry held for any other Bundle,
// and none for the held rollback Bundle itself; a target's own hold applies
// to that target only.
//
// Covers FLEET-07.
func TestFleet_HeldByFleetHold(t *testing.T) {
	held := func(p *kardinalv1alpha1.Pipeline, bundle string) map[string]bool {
		res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle(bundle, "app")})
		require.NoError(t, err)
		out := map[string]bool{}
		for _, n := range res.Graph.Spec.Nodes {
			if n.ID != graph.NodePromotionDAG {
				continue
			}
			for _, e := range n.Def["steps"].([]interface{}) {
				m := e.(map[string]interface{})
				out[m["environment"].(string)] = m["held"].(bool)
			}
		}
		return out
	}
	p := bigFleet(2, 1, nil)
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "app-rollback-a", Reason: "r"}}
	assert.Equal(t, map[string]bool{"test": false, "prod-t00": true, "prod-t01": true}, held(p, "app-v3"))
	assert.Equal(t, map[string]bool{"test": false, "prod-t00": false, "prod-t01": false}, held(p, "app-rollback-a"))

	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{{Environment: "prod-t01", Bundle: "app-rollback-b", Reason: "r"}}
	assert.Equal(t, map[string]bool{"test": false, "prod-t00": false, "prod-t01": true}, held(p, "app-v3"))
}

// TestFleet_PRReviewGateMirror (#1565 QA): the targets of a pr-review fleet
// with a gate get the GateMirror patch, as the fleet environment would, so
// their PRs' kardinal/gates check follows the gates (spec.live). Without it
// the check stays in error and a protected branch never lets the PR merge.
func TestFleet_PRReviewGateMirror(t *testing.T) {
	p := bigFleet(3, 1, nil)
	for i := range p.Spec.Environments {
		if p.Spec.Environments[i].Fleet != nil {
			p.Spec.Environments[i].Approval = "pr-review"
		}
	}
	gates := []kardinalv1alpha1.PolicyGate{makePolicyGate("freeze", "default", "prod", "true")}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"), PolicyGates: gates})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	data, ok := nodeByID(res.Graph.Spec.Nodes)[graph.NodeGateMirrorData]
	require.True(t, ok, "a GateMirror for the fleet's targets")
	var envs []string
	for _, s := range data.Def["steps"].([]interface{}) {
		envs = append(envs, s.(map[string]interface{})["environment"].(string))
	}
	assert.Equal(t, []string{"prod-t00", "prod-t01", "prod-t02"}, envs)
}

// TestFleet_BundleStrategyOfTargets (#1565 QA): the update strategy check of
// a config Bundle reads a fleet target's spec, which is its fleet's: a fleet
// with update.strategy argocd refuses the Bundle as an argocd environment
// does, naming the target.
func TestFleet_BundleStrategyOfTargets(t *testing.T) {
	p := bigFleet(2, 1, nil)
	for i := range p.Spec.Environments {
		if p.Spec.Environments[i].Fleet != nil {
			p.Spec.Environments[i].Update.Strategy = "argocd"
		}
	}
	b := makeBundle("app-v1", "app")
	b.Spec.Type = "config"
	b.Spec.ConfigRef = &kardinalv1alpha1.ConfigRef{GitRepo: "https://github.com/org/config", CommitSHA: "abc123"}
	_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `environment "prod-t00" uses update.strategy argocd`)
}

// TestFleet_PacingNeverPrunesAnObservedStep (#1565 QA): pacing (maxConcurrent,
// maxUnavailable, the rank of the waiting targets) only limits new
// admissions. A target whose step StepsObserved lists stays in the wave
// whatever the observed states say, even when they are stale, and whatever
// the pacing would decide for it now: a failure reaching maxUnavailable,
// maxConcurrent lowered, or a new target ranked before it.
func TestFleet_PacingNeverPrunesAnObservedStep(t *testing.T) {
	one := 1
	for _, tc := range []struct {
		name   string
		states map[string]string // the observed states of the two admitted targets
		edit   func(p *kardinalv1alpha1.Pipeline)
	}{
		{name: "states not observed yet", states: map[string]string{"prod-t00": "", "prod-t01": ""}},
		{name: "a stale Failed reaches maxUnavailable", states: map[string]string{"prod-t00": "", "prod-t01": "Failed"}},
		{name: "a Verified not observed yet", states: map[string]string{"prod-t00": "HealthChecking", "prod-t01": ""}},
		{name: "maxConcurrent lowered to 1", states: map[string]string{"prod-t00": "", "prod-t01": ""},
			edit: func(p *kardinalv1alpha1.Pipeline) { p.Spec.Environments[1].Fleet.MaxConcurrent = 1 }},
		{name: "a new target ranked first", states: map[string]string{"prod-t00": "", "prod-t01": ""},
			edit: func(p *kardinalv1alpha1.Pipeline) {
				f := p.Spec.Environments[1].Fleet
				f.Targets = append([]kardinalv1alpha1.FleetTarget{{Name: "aa"}}, f.Targets...)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := bigFleet(6, 2, &one)
			sim := fleetSim(t, p)
			sim.steps["test"] = "Verified"
			assert.Equal(t, []string{"prod-t00", "prod-t01", "test"}, sim.advance())
			if tc.edit != nil {
				tc.edit(p)
				res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
				require.NoError(t, err)
				steps, fleet := sim.steps, sim.fleet
				sim = newCompactSim(t, res.Graph)
				sim.steps, sim.fleet = steps, fleet
				sim.fleet["prod-aa"] = "prod"
			}
			for env, st := range tc.states {
				sim.steps[env] = st
			}
			wave, _ := sim.wave()
			assert.Equal(t, []string{"prod-t00", "prod-t01", "test"}, wave, "the observed steps stay; no new target starts")
		})
	}
}
