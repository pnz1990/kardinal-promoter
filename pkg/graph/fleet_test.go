// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
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
