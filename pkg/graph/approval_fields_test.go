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

// TestBuilder_ApprovalFieldsReachTheInstance (spec 23 §8, live
// TestGate_ApprovalFields): a gate's approval.fields is copied into its
// instances, as the Graph renders them and in BuildResult.GateInstances,
// with empty optional keys left out (the instance's CRD rules refuse an
// empty pattern on a bool field). A gate without fields in the same Graph
// renders as before.
func TestBuilder_ApprovalFieldsReachTheInstance(t *testing.T) {
	p := makeLinearPipeline("app", "test", "prod")
	withFields := makePolicyGate("change", "default", "prod", `approvals.fields.ticketID.exists(id, id.startsWith("CHG"))`)
	withFields.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1, Fields: []kardinalv1alpha1.ApprovalField{
		{Name: "ticketID", Required: true, Pattern: `^CHG[0-9]{7}$`, Description: "ServiceNow change number"},
		{Name: "riskAcknowledged", Type: "bool", Required: true, MustBe: "true"},
		{Name: "tier", Type: "enum", Values: []string{"low", "high"}},
	}}
	plain := makePolicyGate("two-eyes", "default", "test", "approvals.met")
	plain.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{withFields, plain}})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)

	specs := gateSpecs(t, res.Graph, map[string]interface{}{graph.ApprovalsNodeID: []interface{}{}})
	change := specs["change/prod"]
	require.NotNil(t, change)
	assert.Equal(t, []interface{}{
		map[string]interface{}{"name": "ticketID", "type": "string", "required": true, "pattern": `^CHG[0-9]{7}$`,
			"description": "ServiceNow change number"},
		map[string]interface{}{"name": "riskAcknowledged", "type": "bool", "required": true, "mustBe": "true"},
		map[string]interface{}{"name": "tier", "type": "enum", "required": false, "values": []interface{}{"low", "high"}},
	}, change["approval"].(map[string]interface{})["fields"])
	twoEyes := specs["two-eyes/test"]
	require.NotNil(t, twoEyes)
	assert.NotContains(t, twoEyes["approval"].(map[string]interface{}), "fields")

	var found bool
	for _, inst := range res.GateInstances {
		if inst.Labels["kardinal.io/gate-template"] == "change" {
			found = true
			require.NotNil(t, inst.Spec.Approval)
			assert.Equal(t, []kardinalv1alpha1.ApprovalField{
				{Name: "ticketID", Type: "string", Required: true, Pattern: `^CHG[0-9]{7}$`, Description: "ServiceNow change number"},
				{Name: "riskAcknowledged", Type: "bool", Required: true, MustBe: "true"},
				{Name: "tier", Type: "enum", Values: []string{"low", "high"}},
			}, inst.Spec.Approval.Fields)
		}
	}
	assert.True(t, found)
}
