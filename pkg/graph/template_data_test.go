// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// shapeChanged is the failure message of TestTemplateDataShape.
const shapeChanged = "graph.TemplateRules and TemplateEnvironments compare this data with what earlier controllers " +
	"wrote (a re-render on upgrade records the difference as GateChanged, #2271, #2338): a new or renamed key, or a " +
	"new value for the same template, records a false GateChanged for every promoting version at that upgrade. " +
	"Translate the earlier shape in TemplateRules, or bump RenderVersion and say so in the changelog; then update this test"

func sortedKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// #2338: the gate data a Graph's gate instances copy, which TemplateRules and
// TemplateEnvironments read across controller versions: each template
// entry's spec (and its approval policy), and each instance item's template
// index, environment and verify policy. It is the shape v0.12.0 to v0.13.0
// write.
func TestTemplateDataShape(t *testing.T) {
	p := makeLinearPipeline("app", "uat", "prod")
	cab := makePolicyGate("cab", "platform-policies", "prod", "true")
	cab.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	explore := verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Required: 1})
	window := makePolicyGate("window", "platform-policies", "prod", "true")
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{cab, explore, window}})
	require.NoError(t, err)
	var data map[string]interface{}
	for _, n := range res.Graph.Spec.Nodes {
		if n.ID == graph.NodePolicyGateData {
			data = n.Def
		}
	}
	require.NotNil(t, data, "the Graph has a %s node", graph.NodePolicyGateData)

	specs := map[string][]string{}
	var approval []string
	for _, raw := range data["templates"].([]interface{}) {
		tmpl := raw.(map[string]interface{})
		spec := tmpl["spec"].(map[string]interface{})
		specs[tmpl["template"].(string)] = sortedKeys(spec)
		if a, ok := spec["approval"].(map[string]interface{}); ok {
			approval = sortedKeys(a)
		}
	}
	plain := []string{"expression", "generated", "message", "recheckInterval"}
	assert.Equal(t, map[string][]string{
		"cab":     {"approval", "expression", "generated", "message", "recheckInterval"},
		"explore": plain,
		"window":  plain,
	}, specs, shapeChanged)
	assert.Equal(t, []string{"allowAgents", "allowedAgents", "allowedGroups", "allowedUsers", "excludeAuthor", "required"}, approval, shapeChanged)

	var verify []string
	items := 0
	for field, raw := range data {
		list, ok := raw.([]interface{})
		if !ok || field == "templates" {
			continue
		}
		for _, it := range list {
			item := it.(map[string]interface{})
			items++
			assert.Contains(t, item, "t", shapeChanged)
			assert.IsType(t, "", item["environment"], shapeChanged)
			if v, ok := item["verify"].(map[string]interface{}); ok {
				verify = sortedKeys(v)
			}
		}
	}
	assert.Equal(t, 3, items, "one instance of each gate, in prod")
	assert.Equal(t, []string{"allowedAgents", "allowedGroups", "allowedUsers", "check", "environment", "excludeAuthor",
		"maxFailures", "onInconclusive", "onTimeout", "required", "retries", "timeout"}, verify, shapeChanged)
}

// #2338: TemplateEnvironments names the environments each template's
// instances are in: an applies-to edit changes it, the rule does not.
func TestTemplateEnvironments(t *testing.T) {
	p := makeLinearPipeline("app", "uat", "prod")
	envs := func(gates ...kardinalv1alpha1.PolicyGate) map[string][]string {
		t.Helper()
		res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"), PolicyGates: gates})
		require.NoError(t, err)
		return graph.TemplateEnvironments(res.Graph)
	}
	window := makePolicyGate("window", "platform-policies", "prod", "true")
	key := window.Namespace + "/" + window.Name
	assert.Equal(t, []string{"prod"}, envs(window)[key])
	both := window.DeepCopy()
	both.Labels["kardinal.io/applies-to"] = "uat,prod"
	assert.Equal(t, []string{"prod", "uat"}, envs(*both)[key])
	edited := window.DeepCopy()
	edited.Spec.Expression = "false"
	assert.Equal(t, []string{"prod"}, envs(*edited)[key], "a rule edit does not move the gate")
}
