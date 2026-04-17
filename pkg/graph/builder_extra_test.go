// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Additional coverage tests for edge cases in builder.go and types.go.
package graph_test

import (
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
	// 3 envs × (1 PRStatus + 1 PromotionStep) = 6
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
	// 2 PromotionStep + 2 PRStatus Watch + 2 PolicyGate (one per env) = 6 nodes
	assert.Equal(t, 7, result.NodeCount)
}

// TestBuilder_SkipAllEnvironments: with includeWhen (#619), skipping all envs
// does NOT return a Build error — the Graph is emitted with all nodes having
// includeWhen: ${!bundle.spec.intent.skipEnvironments.exists(s, s == "test")}.
// krocodile excludes them at runtime.
func TestBuilder_SkipAllEnvironments(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("app", "test")
	bundle := makeBundle("app-v1", "app")
	bundle.Spec.Intent = &kardinalv1alpha1.BundleIntent{
		SkipEnvironments: []string{"test"},
	}
	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err, "Build succeeds: includeWhen handles filtering at runtime (#619)")
	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	assert.Contains(t, nodeMap, "test", "test node present with includeWhen skip expression")
	testNode := nodeMap["test"]
	require.NotEmpty(t, testNode.IncludeWhen, "test must have includeWhen skip expression")
	assert.Contains(t, testNode.IncludeWhen[0], "skipEnvironments.exists")
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
	assert.Equal(t, "experimental.kro.run/v1alpha1", result.Graph.APIVersion)
	assert.Equal(t, "Graph", result.Graph.Kind)
}

// TestBuilder_SlugifyUppercase verifies that uppercase chars in bundle names
// are lowercased in the graph name (slugify handles A-Z → a-z).
func TestBuilder_SlugifyUppercase(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("App", "test")
	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "MyApp-V1.2.3", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "App"},
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
