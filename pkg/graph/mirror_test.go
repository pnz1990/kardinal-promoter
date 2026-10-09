// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestBuilder_GateMirror: a pr-review environment with gates gets a
// GateMirror patch item targeting its PromotionStep by literal name, whose
// spec.live.gates is that environment's gate instances (of every gate
// collection) with their live ready and reason, sorted by name. An auto
// environment, or a pr-review one without gates, is not mirrored; a Graph
// with no mirrored environment has no mirror nodes.
func TestBuilder_GateMirror(t *testing.T) {
	p := makeLinearPipeline("app", "test", "uat", "prod")
	for i := range p.Spec.Environments {
		if p.Spec.Environments[i].Name != "test" {
			p.Spec.Environments[i].Approval = "pr-review"
		}
	}
	freeze := makePolicyGate("freeze", "platform-policies", "prod", "!changewindow.isBlocked('q4')")
	approval := makePolicyGate("two", "platform-policies", "prod", "true")
	approval.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 2}
	onTest := makePolicyGate("weekday", "platform-policies", "test", "!schedule.isWeekend")
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{freeze, approval, onTest}})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)

	nodes := nodeByID(res.Graph.Spec.Nodes)
	data, ok := nodes[graph.NodeGateMirrorData]
	require.True(t, ok)
	steps := data.Def["steps"].([]interface{})
	require.Len(t, steps, 1, "only prod: test is auto, uat has no gate")
	step := steps[0].(map[string]interface{})
	assert.Equal(t, "prod", step["environment"])
	assert.Equal(t, objName(nodes["prod"].Template), step["name"], "the patch targets prod's step by its literal name")

	mirror, ok := nodes[graph.NodeGateMirror]
	require.True(t, ok)
	assert.Equal(t, []map[string]string{{"Mirror": "${GateMirrorData.steps}"}}, mirror.ForEach)
	require.NotNil(t, mirror.Patch)
	assert.Equal(t, "PromotionStep", mirror.Patch["kind"])
	assert.Equal(t, map[string]interface{}{"name": "${Mirror.name}"}, mirror.Patch["metadata"])
	expr := mirror.Patch["spec"].(map[string]interface{})["live"].(map[string]interface{})["gates"].(string)

	gate := func(name, env string, status map[string]interface{}) map[string]interface{} {
		g := map[string]interface{}{"metadata": map[string]interface{}{"name": name,
			"labels": map[string]interface{}{"kardinal.io/environment": env}}}
		if status != nil {
			g["status"] = status
		}
		return g
	}
	vars := map[string]interface{}{
		"Mirror": step,
		graph.NodePolicyGates: []interface{}{
			gate("z-freeze", "prod", map[string]interface{}{"ready": false, "reason": "frozen"}),
			gate("weekday", "test", map[string]interface{}{"ready": true}),
		},
		graph.NodeApprovalGates: []interface{}{gate("a-two", "prod", nil)},
	}
	got := evalCEL(t, expr, vars)
	assert.Equal(t, []interface{}{
		map[string]interface{}{"name": "a-two", "ready": false, "reason": ""},
		map[string]interface{}{"name": "z-freeze", "ready": false, "reason": "frozen"},
	}, got, "prod's gates of both collections, a gate without status not ready")

	res, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{onTest}})
	require.NoError(t, err)
	nodes = nodeByID(res.Graph.Spec.Nodes)
	assert.NotContains(t, nodes, graph.NodeGateMirror, "no pr-review environment with gates")
	assert.NotContains(t, nodes, graph.NodeGateMirrorData)
}
