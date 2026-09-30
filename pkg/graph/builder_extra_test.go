// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Additional coverage tests for edge cases in builder.go and types.go.
package graph_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestBuilder_NilPipeline returns an error when pipeline is nil.
func TestBuilder_NilPipeline(t *testing.T) {
	b := graph.NewBuilder()
	_, err := b.Build(graph.BuildInput{Bundle: makeBundle("b", "p")})
	require.Error(t, err)
}

// TestBuilder_NilBundle returns an error when bundle is nil.
func TestBuilder_NilBundle(t *testing.T) {
	b := graph.NewBuilder()
	_, err := b.Build(graph.BuildInput{Pipeline: makeLinearPipeline("p", "test")})
	require.Error(t, err)
}

// TestBuilder_UnknownTargetEnv returns error for non-existent target.
func TestBuilder_UnknownTargetEnv(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("app", "test", "prod")
	bundle := makeBundle("app-v1", "app")
	bundle.Spec.Intent = &kardinalv1alpha1.BundleIntent{
		TargetEnvironment: "nonexistent",
	}
	_, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown target")
}

// TestBuilder_DependsOnUnknownEnv returns error when dependsOn references unknown env.
func TestBuilder_DependsOnUnknownEnv(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod", DependsOn: []string{"does-not-exist"}},
			},
		},
	}
	bundle := makeBundle("app-v1", "app")
	_, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.Error(t, err)
}

// TestBuilder_MultipleUpstreams verifies fan-in (multiple dependsOn values).
func TestBuilder_MultipleUpstreams(t *testing.T) {
	b := graph.NewBuilder()
	envs := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "eu"},
		{Name: "us"},
		{Name: "global", DependsOn: []string{"eu", "us"}},
	}
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
	bundle := makeBundle("multi-v1", "multi")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	// 1 Bundle ref + 3 envs × (1 PRStatus + 1 PromotionStep) = 7
	assert.Equal(t, 7, result.NodeCount)

	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	globalNode := nodeMap["global"]
	// global must reference both eu and us
	assert.True(t, containsCELRef(globalNode.Template, "eu"),
		"global node must reference eu")
	assert.True(t, containsCELRef(globalNode.Template, "us"),
		"global node must reference us")
}

// TestBuilder_GateInMultipleEnvs verifies gates can apply to multiple envs via comma.
func TestBuilder_GateInMultipleEnvs(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("app", "staging", "prod")
	bundle := makeBundle("app-v1", "app")

	// Gate applies to both staging and prod
	gate := kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "business-hours",
			Namespace: "platform-policies",
			Labels: map[string]string{
				"kardinal.io/applies-to": "staging,prod",
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: "schedule.hour >= 9"},
	}

	result, err := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: []kardinalv1alpha1.PolicyGate{gate},
	})
	require.NoError(t, err)
	// 1 Bundle ref + 2 PromotionStep + 2 PRStatus + 2 PolicyGate (one per env) = 7 nodes
	assert.Equal(t, 7, result.NodeCount)
}

// TestBuilder_SkipAllEnvironments returns error when all envs are skipped.
func TestBuilder_SkipAllEnvironments(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("app", "test")
	bundle := makeBundle("app-v1", "app")
	bundle.Spec.Intent = &kardinalv1alpha1.BundleIntent{
		SkipEnvironments: []string{"test"},
	}
	// skipEnvironments is filtered statically (ledger gap G2); skipping every
	// environment leaves nothing to promote.
	_, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.Error(t, err)
}

// TestBuilder_GraphLabels verifies the generated Graph has correct labels.
func TestBuilder_GraphLabels(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("nginx-demo", "test")
	bundle := makeBundle("nginx-demo-v1", "nginx-demo")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	assert.Equal(t, "nginx-demo", result.Graph.Labels["kardinal.io/pipeline"])
	assert.Equal(t, "nginx-demo-v1", result.Graph.Labels["kardinal.io/bundle"])
}

// TestBuilder_GraphAPIVersion verifies the generated Graph has correct APIVersion.
func TestBuilder_GraphAPIVersion(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("app", "test")
	bundle := makeBundle("app-v1", "app")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	assert.Equal(t, "kro.run/v1alpha1", result.Graph.APIVersion)
	assert.Equal(t, "Graph", result.Graph.Kind)
}

// TestBuilder_SlugifyUppercase verifies that uppercase chars in bundle names
// are lowercased in the graph name (slugify handles A-Z → a-z).
func TestBuilder_SlugifyUppercase(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("App", "test")
	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "MyApp-V1.2.3", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "App", Images: testImages},
	}

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)

	// Graph name must be lowercase and contain only [a-z0-9-]
	name := result.Graph.Name
	for _, c := range name {
		assert.True(t, (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-',
			"Graph name must contain only [a-z0-9-], got char %q in %q", c, name)
	}
}

// TestBuilder_UpstreamGating verifies that a PromotionStep's upstreamStates
// only resolve once the upstream is Verified. kro does not gate dependents on
// readyWhen for a standalone Graph, so the builder makes the reference itself
// unresolvable until then (ledger gap G1).
func TestBuilder_UpstreamGating(t *testing.T) {
	b := graph.NewBuilder()
	result, err := b.Build(graph.BuildInput{
		Pipeline: makeLinearPipeline("app", "test", "prod"),
		Bundle:   makeBundle("app-v1", "app"),
	})
	require.NoError(t, err)

	prodSpec := nodeByID(result.Graph.Spec.Nodes)["prod"].Template["spec"].(map[string]interface{})
	assert.Equal(t,
		[]interface{}{`${["Verified"].filter(x_, test.status.state == "Verified")[0]}`},
		prodSpec["upstreamStates"])
}

// TestBuilder_UpstreamGating_MultiRegion verifies that a step downstream of a
// forEach environment waits for every region to be Verified.
func TestBuilder_UpstreamGating_MultiRegion(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "fleet", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "canary", Regions: []string{"us-east-1", "eu-west-1", "ap-south-1"}},
				{Name: "prod"},
			},
		},
	}
	result, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: pipeline, Bundle: makeBundle("fleet-v1", "fleet")})
	require.NoError(t, err)

	prodSpec := nodeByID(result.Graph.Spec.Nodes)["prod"].Template["spec"].(map[string]interface{})
	assert.Equal(t,
		[]interface{}{`${["Verified"].filter(x_, size(canary) == 3 && canary.all(s_, s_.status.state == "Verified"))[0]}`},
		prodSpec["upstreamStates"],
		"size() guards against the vacuous all() on an empty or partially stamped collection")
}

// TestBuilder_ServiceAccountName verifies the Graph carries the applier
// ServiceAccount kro impersonates, with a default and an override.
func TestBuilder_ServiceAccountName(t *testing.T) {
	in := graph.BuildInput{Pipeline: makeLinearPipeline("app", "test"), Bundle: makeBundle("app-v1", "app")}

	result, err := graph.NewBuilder().Build(in)
	require.NoError(t, err)
	assert.Equal(t, graph.DefaultGraphServiceAccount, result.Graph.Spec.ServiceAccountName)

	b := graph.NewBuilder()
	b.ServiceAccountName = "custom-sa"
	result, err = b.Build(in)
	require.NoError(t, err)
	assert.Equal(t, "custom-sa", result.Graph.Spec.ServiceAccountName)
}

// TestBuilder_OnlyKroKeywords verifies every node uses only keywords the kro
// Graph schema defines and wraps readyWhen/includeWhen entries in ${...}.
func TestBuilder_OnlyKroKeywords(t *testing.T) {
	gate := makePolicyGate("no-weekend", "platform-policies", "prod", "!schedule.isWeekend")
	pipeline := makeLinearPipeline("app", "test", "prod")
	pipeline.Spec.Environments[1].Regions = []string{"us-east-1", "eu-west-1"}
	result, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline: pipeline, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{gate},
	})
	require.NoError(t, err)

	allowed := map[string]bool{"id": true, "template": true, "ref": true, "readyWhen": true, "includeWhen": true, "forEach": true}
	for _, n := range result.Graph.Spec.Nodes {
		raw, err := json.Marshal(n)
		require.NoError(t, err)
		var fields map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &fields))
		for k := range fields {
			assert.True(t, allowed[k], "node %q uses non-kro keyword %q", n.ID, k)
		}
		for _, expr := range append(append([]string{}, n.ReadyWhen...), n.IncludeWhen...) {
			assert.True(t, strings.HasPrefix(expr, "${") && strings.HasSuffix(expr, "}"),
				"node %q expression %q must be wrapped in ${...}", n.ID, expr)
		}
	}
}
