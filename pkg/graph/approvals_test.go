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
		approvalFor("b-eve", "prod", "eve", "uid-of-an-earlier-app-v1"),
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
	assert.Equal(t, []string{"bob", "alice"}, users, "prod's Approvals of this Bundle UID, in name order")

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
	return approvalFor(name, env, user, "uid-app-v1")
}

// approvalFor is an Approval of the Bundle with UID uid (the renderer gives
// the Bundle ref "uid-<name>").
func approvalFor(name, env, user, uid string) map[string]interface{} {
	return map[string]interface{}{"metadata": map[string]interface{}{"name": name},
		"spec": map[string]interface{}{"bundle": "app-v1", "bundleUID": uid, "environment": env, "user": user,
			"groups": []interface{}{}, "decision": "approve", "comment": ""}}
}

// TestBuilder_ApprovalsCapped: the Graph copies at most 101 Approvals into a
// gate instance, so the gate can tell it got more than it counts.
func TestBuilder_ApprovalsCapped(t *testing.T) {
	p := makeLinearPipeline("app", "prod")
	g := makePolicyGate("ok", "platform-policies", "prod", "true")
	g.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{g}})
	require.NoError(t, err)
	var many []interface{}
	for i := range 150 {
		many = append(many, approvalObj(fmt.Sprintf("a%03d", i), "prod", fmt.Sprintf("u%03d", i)))
	}
	for _, o := range renderObjectsWith(t, res.Graph, map[string]interface{}{graph.ApprovalsNodeID: many}) {
		if o.Object["kind"] == "PolicyGate" {
			assert.Len(t, o.Object["spec"].(map[string]interface{})["approvals"], 101)
		}
	}
}

// TestBuilder_SkipPermissionApprovalRefused: approvals on a skip-permission
// gate fail the build with a reason, instead of a gate that never passes.
func TestBuilder_SkipPermissionApprovalRefused(t *testing.T) {
	p := makeLinearPipeline("app", "test", "uat", "prod")
	org := makePolicyGate("org-uat", "platform-policies", "uat", "true")
	perm := makePolicyGate("skip-uat", "platform-policies", "uat", "true")
	perm.Labels["kardinal.io/type"] = "skip-permission"
	perm.Spec.SkipPermission = true
	perm.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	b := makeBundle("app-v1", "app")
	b.Spec.Intent = &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"uat"}}
	_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b,
		PolicyGates: []kardinalv1alpha1.PolicyGate{org, perm}, PolicyNamespaces: []string{"platform-policies"}})
	require.Error(t, err)
	assert.ErrorIs(t, err, graph.ErrInvalid)
	assert.Contains(t, err.Error(), "approvals are not supported on skip-permission gates")
}
