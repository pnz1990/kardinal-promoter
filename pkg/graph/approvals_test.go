// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestBuilder_ApprovalGate: a gate with spec.approval adds one selector ref
// on the Bundle's Approvals, and its instances are in the ApprovalGates
// collection, which copies the policy and renders spec.approvals from that
// ref, filtered to the instance's environment and sorted by Approval name.
// Other gates stay in PolicyGates with no approvals; a Graph without approval
// gates has no ref and no ApprovalGates collection.
func TestBuilder_ApprovalGate(t *testing.T) {
	p := makeLinearPipeline("app", "test", "prod")
	approval := makePolicyGate("two-approvers", "platform-policies", "prod", "true")
	approval.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 2, AllowedGroups: []string{"release-managers"}, ExcludeAuthor: true}
	counter := makePolicyGate("one-ok", "platform-policies", "test", "approvals.count >= 1")
	plain := makePolicyGate("weekday", "platform-policies", "test", "!schedule.isWeekend")

	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{approval, counter, plain}})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)

	nodes := nodeByID(res.Graph.Spec.Nodes)
	ref, ok := nodes[graph.ApprovalsNodeID]
	require.True(t, ok, "the Approvals ref node")
	assert.Equal(t, map[string]interface{}{
		"apiVersion": "kardinal.io/v1alpha1", "kind": "Approval",
		"metadata": map[string]interface{}{"namespace": "default",
			"selector": map[string]interface{}{"matchLabels": map[string]interface{}{"kardinal.io/bundle": "app-v1"}}},
	}, ref.Ref)
	_, ok = nodes[graph.NodeApprovalGates]
	require.True(t, ok, "the ApprovalGates collection")

	approvalsRef := []interface{}{
		approvalObj("z-alice", "prod", "alice"), approvalObj("a-bob", "prod", "bob"), approvalObj("m-mia", "test", "mia"),
	}
	byName := map[string]map[string]interface{}{}
	for _, o := range renderObjectsWith(t, res.Graph, map[string]interface{}{graph.ApprovalsNodeID: approvalsRef}) {
		if o.Object["kind"] == "PolicyGate" {
			byName[fmt.Sprint(objLabels(o.Object)["kardinal.io/gate-template"])] = map[string]interface{}{"node": o.NodeID, "spec": o.Object["spec"]}
		}
	}
	require.Len(t, byName, 3)
	assert.Equal(t, graph.NodeApprovalGates, byName["two-approvers"]["node"])
	assert.Equal(t, graph.NodeApprovalGates, byName["one-ok"]["node"], "an expression on approvals.* reads them too")
	assert.Equal(t, graph.NodePolicyGates, byName["weekday"]["node"])

	spec := byName["two-approvers"]["spec"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{"required": 2, "allowedGroups": []interface{}{"release-managers"},
		"allowedUsers": []interface{}(nil), "excludeAuthor": true}, spec["approval"])
	assert.Equal(t, "true", spec["expression"])
	assert.Equal(t, true, spec["generated"])
	assert.Equal(t, "post-deploy", spec["when"])
	users := []string{}
	for _, a := range spec["approvals"].([]interface{}) {
		users = append(users, a.(map[string]interface{})["user"].(string))
	}
	assert.Equal(t, []string{"bob", "alice"}, users, "prod's Approvals, in name order")

	counted := byName["one-ok"]["spec"].(map[string]interface{})
	assert.Nil(t, counted["approval"], "no policy on a gate that only reads approvals.*")
	require.Len(t, counted["approvals"], 1, "test's Approvals")
	assert.NotContains(t, byName["weekday"]["spec"], "approvals")

	for _, g := range res.GateInstances {
		if g.Labels["kardinal.io/gate-template"] == "two-approvers" {
			assert.Equal(t, approval.Spec.Approval.Required, g.Spec.Approval.Required, "GateInstances carry the policy")
		}
	}

	res, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{plain}})
	require.NoError(t, err)
	nodes = nodeByID(res.Graph.Spec.Nodes)
	assert.NotContains(t, nodes, graph.ApprovalsNodeID, "no Approvals ref without an approval gate")
	assert.NotContains(t, nodes, graph.NodeApprovalGates)
}

// TestBuilder_ApprovalGateNoApprovals: with no Approval yet the collection
// renders spec.approvals as an empty list: the gate instance is created and
// never goes data-pending.
func TestBuilder_ApprovalGateNoApprovals(t *testing.T) {
	p := makeLinearPipeline("app", "prod")
	g := makePolicyGate("ok", "platform-policies", "prod", "true")
	g.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{g}})
	require.NoError(t, err)
	objs := renderObjectsWith(t, res.Graph, map[string]interface{}{graph.ApprovalsNodeID: []interface{}{}})
	var found bool
	for _, o := range objs {
		if o.Object["kind"] == "PolicyGate" {
			found = true
			spec := o.Object["spec"].(map[string]interface{})
			assert.Empty(t, spec["approvals"])
			assert.Equal(t, 1, spec["approval"].(map[string]interface{})["required"], "required defaults to 1")
		}
	}
	assert.True(t, found)
}

func approvalObj(name, env, user string) map[string]interface{} {
	return map[string]interface{}{"metadata": map[string]interface{}{"name": name},
		"spec": map[string]interface{}{"bundle": "app-v1", "environment": env, "user": user,
			"groups": []interface{}{}, "decision": "approve", "comment": ""}}
}
