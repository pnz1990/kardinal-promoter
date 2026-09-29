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

// TestDetectCycle_MessageNamesTheCause checks that a cycle error names the
// part of the spec that made each edge, and that the fix hint does not ask to
// remove a dependsOn reference when the cycle has none.
func TestDetectCycle_MessageNamesTheCause(t *testing.T) {
	cases := []struct {
		name     string
		envs     []kardinalv1alpha1.EnvironmentSpec
		contains []string
		absent   []string
	}{
		{
			// b (wave 1) follows staging by list order, staging follows a
			// (wave 2) by list order, and wave 2 waits for wave 1.
			name: "waves listed out of order around a sequential env",
			envs: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "a", Wave: 2},
				{Name: "staging"},
				{Name: "b", Wave: 1},
			},
			contains: []string{
				"circular dependency",
				"a → b → staging → a (cycle!)",
				"a (wave 2) waits for all of wave 1, which includes b",
				"wave 1 starts after staging, the environment listed before it",
				"staging has no dependsOn, so it follows a, listed before it",
				"Fix: list the waves in ascending order (wave 1 before wave 2), or set dependsOn on b or staging",
			},
			absent: []string{"remove one of the dependsOn"},
		},
		{
			name: "explicit dependsOn cycle",
			envs: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat", DependsOn: []string{"prod"}},
				{Name: "prod", DependsOn: []string{"uat"}},
			},
			contains: []string{
				"prod → uat → prod (cycle!)",
				"prod dependsOn uat; uat dependsOn prod",
				"Fix: remove one of the dependsOn references",
			},
			absent: []string{"wave"},
		},
		{
			name: "dependsOn against a wave",
			envs: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "a", Wave: 1, DependsOn: []string{"b"}},
				{Name: "b", Wave: 2},
			},
			contains: []string{
				"a dependsOn b",
				"b (wave 2) waits for all of wave 1, which includes a",
				"Fix: remove one of the dependsOn references",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "p"},
				Spec: kardinalv1alpha1.PipelineSpec{
					Git:          kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo"},
					Environments: tc.envs,
				},
			}
			err := graph.DetectCycle(pipeline)
			require.Error(t, err)
			for _, s := range tc.contains {
				assert.Contains(t, err.Error(), s)
			}
			for _, s := range tc.absent {
				assert.NotContains(t, err.Error(), s)
			}
		})
	}
}
