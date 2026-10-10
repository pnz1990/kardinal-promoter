// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestCheckRunsCreated: a HookRun or AnalysisRun of the compact Graph that
// kro names in a failed ResourcesConverged is reported on the Bundle, with
// its environment and kro's message; the condition goes away once kro names
// none (idempotent).
func TestCheckRunsCreated(t *testing.T) {
	runGraph := func(status metav1.ConditionStatus, msg string) *graph.Graph {
		g := &graph.Graph{}
		g.Spec.Nodes = []graph.GraphNode{
			{ID: graph.NodeHookRunData, Def: map[string]interface{}{"items": []interface{}{
				map[string]interface{}{"name": "p-b-prod-pre-migrate-1a2b3c4d", "environment": "prod"}}}},
			{ID: graph.NodeAnalysisRunData, Def: map[string]interface{}{"items": []interface{}{
				map[string]interface{}{"name": "p-b-uat-smoke-5e6f7a8b", "environment": "uat"}}}},
			{ID: graph.NodeRenderRunData, Def: map[string]interface{}{"items": []interface{}{
				map[string]interface{}{"name": "p-b-stage-render", "environment": "stage"}}}},
		}
		g.Status.Conditions = []metav1.Condition{{Type: "ResourcesConverged", Status: status, Reason: "ApplyFailed", Message: msg}}
		return g
	}
	tests := []struct {
		name    string
		graph   *graph.Graph
		wantMsg []string // nil: no condition
	}{
		{name: "hook denied", graph: runGraph(metav1.ConditionFalse,
			`apply "HookRuns": hookruns.kardinal.io "p-b-prod-pre-migrate-1a2b3c4d" is forbidden: exceeded quota`),
			wantMsg: []string{"p-b-prod-pre-migrate-1a2b3c4d (prod)", "that environment waits", "exceeded quota"}},
		{name: "analysis denied", graph: runGraph(metav1.ConditionFalse, `analysisruns "p-b-uat-smoke-5e6f7a8b": invalid`),
			wantMsg: []string{"p-b-uat-smoke-5e6f7a8b (uat)"}},
		{name: "render denied", graph: runGraph(metav1.ConditionFalse, `renderruns.kardinal.io "p-b-stage-render" is forbidden`),
			wantMsg: []string{"p-b-stage-render (stage)"}},
		{name: "another node", graph: runGraph(metav1.ConditionFalse, `apply "PolicyGates": denied`)},
		{name: "converged", graph: runGraph(metav1.ConditionTrue, "")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &kardinalv1alpha1.Bundle{}
			b.Status.Conditions = []metav1.Condition{{Type: condRunsCreated, Status: metav1.ConditionFalse, Reason: "ApplyFailed", Message: "old"}}
			checkRunsCreated(b, tc.graph)
			c := meta.FindStatusCondition(b.Status.Conditions, condRunsCreated)
			if tc.wantMsg == nil {
				assert.Nil(t, c, "removed once kro names no run")
				return
			}
			require.NotNil(t, c)
			assert.Equal(t, metav1.ConditionFalse, c.Status)
			for _, m := range tc.wantMsg {
				assert.Contains(t, c.Message, m)
			}
			before := c.DeepCopy()
			checkRunsCreated(b, tc.graph)
			after := meta.FindStatusCondition(b.Status.Conditions, condRunsCreated)
			assert.Equal(t, before.Message, after.Message)
			assert.Equal(t, before.LastTransitionTime, after.LastTransitionTime)
		})
	}
}
