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

func agentBundle() *kardinalv1alpha1.Bundle {
	b := makeBundle("app-v1", "app")
	b.Spec.Actor = &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer",
		Agent: "fixer", Sponsor: "alice"}
	return b
}

// gateSpecs renders g and returns each gate instance's spec by
// "<template>/<environment>".
func gateSpecs(t *testing.T, g *graph.Graph, refs map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	out := map[string]map[string]interface{}{}
	for _, o := range renderObjectsWith(t, g, refs) {
		if o.Object["kind"] != "PolicyGate" {
			continue
		}
		l := objLabels(o.Object)
		out[fmt.Sprintf("%v/%v", l["kardinal.io/gate-template"], l["kardinal.io/environment"])] = o.Object["spec"].(map[string]interface{})
	}
	return out
}

// TestBuilder_AgentGates: an agent's version gets one kardinal-agents-<ceiling>
// gate on every environment, from environments[].agents (unset is auto);
// a person's version gets none (docs/design/18-actor-policy.md section 7).
func TestBuilder_AgentGates(t *testing.T) {
	p := makeLinearPipeline("app", "test", "uat", "prod")
	p.Spec.Environments[1].Agents = kardinalv1alpha1.AutonomyApprove
	p.Spec.Environments[2].Agents = kardinalv1alpha1.AutonomyDeny

	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: agentBundle()})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	specs := gateSpecs(t, res.Graph, map[string]interface{}{graph.ApprovalsNodeID: []interface{}{}})
	require.Len(t, specs, 3)

	auto := specs["kardinal-agents-auto/test"]
	require.NotNil(t, auto)
	assert.Equal(t, "actor.kind != 'agent' || (agent.registered && !agent.suspended && agent.autonomy != 'deny')", auto["expression"])
	assert.Equal(t, "actor.kind == 'agent' && agent.autonomy == 'approve'", auto["approval"].(map[string]interface{})["requiredWhen"])

	approve := specs["kardinal-agents-approve/uat"]
	require.NotNil(t, approve)
	pol := approve["approval"].(map[string]interface{})
	assert.Equal(t, "actor.kind == 'agent'", pol["requiredWhen"])
	assert.Equal(t, true, pol["excludeAuthor"])
	assert.Equal(t, false, pol["allowAgents"], "the shorthand never admits agents")

	deny := specs["kardinal-agents-deny/prod"]
	require.NotNil(t, deny)
	assert.Equal(t, "actor.kind != 'agent'", deny["expression"])
	assert.Nil(t, deny["approval"])

	for _, inst := range res.GateInstances {
		assert.Equal(t, "system", inst.Labels["kardinal.io/scope"], inst.Name)
	}
	// kro types PolicyGateData from its data: every approval template entry
	// must carry each field the ApprovalGates collection reads, or the
	// Graph does not compile ("undefined field").
	for _, n := range res.Graph.Spec.Nodes {
		if n.ID != graph.NodePolicyGateData {
			continue
		}
		for _, tmpl := range n.Def["templates"].([]interface{}) {
			pol, ok := tmpl.(map[string]interface{})["spec"].(map[string]interface{})["approval"].(map[string]interface{})
			if !ok {
				continue
			}
			for _, f := range []string{"allowedUsers", "allowedGroups", "allowAgents", "allowedAgents"} {
				assert.Contains(t, pol, f)
			}
		}
	}

	human := makeBundle("app-v2", "app")
	res, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: human})
	require.NoError(t, err)
	assert.Empty(t, res.GateInstances, "a person's version has no agent gates")
}

// TestBuilder_AgentGatesReservedName: a user template named like the agent
// gates fails an agent version's build, and only an agent version's.
func TestBuilder_AgentGatesReservedName(t *testing.T) {
	p := makeLinearPipeline("app", "prod")
	own := makePolicyGate("kardinal-agents-auto", "default", "prod", "true")
	_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: agentBundle(),
		PolicyGates: []kardinalv1alpha1.PolicyGate{own}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserves")
	_, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{own}})
	require.NoError(t, err)
}

// TestBuilder_AgentGatesFleet: a fleet's targets take the fleet's ceiling.
func TestBuilder_AgentGatesFleet(t *testing.T) {
	p := makeLinearPipeline("app", "test", "prod")
	p.Spec.Environments[1].Agents = kardinalv1alpha1.AutonomyDeny
	p.Spec.Environments[1].Fleet = &kardinalv1alpha1.FleetSpec{Targets: []kardinalv1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: agentBundle()})
	require.NoError(t, err)
	byEnv := map[string]string{}
	for _, inst := range res.GateInstances {
		byEnv[inst.Labels["kardinal.io/environment"]] = inst.Labels["kardinal.io/gate-template"]
	}
	assert.Equal(t, "kardinal-agents-auto", byEnv["test"])
	for env, tmpl := range byEnv {
		if env != "test" {
			assert.Equal(t, "kardinal-agents-deny", tmpl, env)
		}
	}
	assert.Len(t, byEnv, 3)
}

// TestBuilder_ApprovalGateAgents: with allowAgents, the Graph copies the
// Approvals of the listed agents next to the allowed people, and no other
// agent's; without it, agents pass the filter only where anyone does (the
// gate then refuses them). The policy fields round-trip.
func TestBuilder_ApprovalGateAgents(t *testing.T) {
	p := makeLinearPipeline("app", "prod")
	g := makePolicyGate("agents-ok", "platform-policies", "prod", "true")
	g.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 2, AllowedGroups: []string{"release-managers"},
		AllowAgents: true, AllowedAgents: []string{"reviewer"}, RequiredWhen: "actor.kind == 'agent'"}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{g}})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	agentApproval := func(name, agent string) map[string]interface{} {
		a := approvalObj(name, "prod", "system:serviceaccount:ci:"+agent)
		a["spec"].(map[string]interface{})["actor"] = map[string]interface{}{"kind": "agent",
			"principal": "system:serviceaccount:ci:" + agent, "agent": agent, "sponsor": "bob"}
		return a
	}
	refs := map[string]interface{}{graph.ApprovalsNodeID: []interface{}{
		withGroups(approvalObj("a-alice", "prod", "alice"), "release-managers"),
		agentApproval("b-reviewer", "reviewer"),
		agentApproval("c-rogue", "rogue"),
		approvalObj("d-carl", "prod", "carl"),
	}}
	spec := gateSpecs(t, res.Graph, refs)["agents-ok/prod"]
	require.NotNil(t, spec)
	var users []string
	for _, a := range spec["approvals"].([]interface{}) {
		users = append(users, a.(map[string]interface{})["user"].(string))
	}
	assert.Equal(t, []string{"alice", "system:serviceaccount:ci:reviewer"}, users)
	pol := spec["approval"].(map[string]interface{})
	assert.Equal(t, true, pol["allowAgents"])
	assert.Equal(t, []interface{}{"reviewer"}, pol["allowedAgents"])
	assert.Equal(t, "actor.kind == 'agent'", pol["requiredWhen"])

	require.Len(t, res.GateInstances, 1)
	assert.Equal(t, g.Spec.Approval, res.GateInstances[0].Spec.Approval, "the policy round-trips")
}
