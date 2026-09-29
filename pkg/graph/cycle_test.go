// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func TestDetectCycle_NoCycle_LinearChain(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "linear"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat"},
				{Name: "prod"},
			},
		},
	}
	assert.NoError(t, graph.DetectCycle(pipeline), "linear chain should have no cycle")
}

func TestDetectCycle_NoCycle_ExplicitFanOut(t *testing.T) {
	// test → uat, test → staging (fan-out, not a cycle)
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "fan-out"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat", DependsOn: []string{"test"}},
				{Name: "staging", DependsOn: []string{"test"}},
				{Name: "prod", DependsOn: []string{"uat", "staging"}},
			},
		},
	}
	assert.NoError(t, graph.DetectCycle(pipeline), "fan-out DAG should have no cycle")
}

func TestDetectCycle_DirectCycle_TwoNodes(t *testing.T) {
	// uat → prod, prod → uat: direct 2-node cycle
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "two-node-cycle"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat", DependsOn: []string{"prod"}},
				{Name: "prod", DependsOn: []string{"uat"}},
			},
		},
	}
	err := graph.DetectCycle(pipeline)
	require.Error(t, err, "2-node cycle should be detected")
	assert.Contains(t, err.Error(), "circular", "error message should mention cycle")
}

func TestDetectCycle_IndirectCycle_ThreeNodes(t *testing.T) {
	// a → b, b → c, c → a: indirect 3-node cycle
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "three-node-cycle"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "a", DependsOn: []string{"c"}},
				{Name: "b", DependsOn: []string{"a"}},
				{Name: "c", DependsOn: []string{"b"}},
			},
		},
	}
	err := graph.DetectCycle(pipeline)
	require.Error(t, err, "3-node cycle should be detected")
	assert.Contains(t, err.Error(), "circular", "error message should mention cycle")
}

func TestDetectCycle_SelfLoop(t *testing.T) {
	// prod → prod: self-loop
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "self-loop"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod", DependsOn: []string{"prod"}},
			},
		},
	}
	err := graph.DetectCycle(pipeline)
	require.Error(t, err, "self-loop should be detected")
}
