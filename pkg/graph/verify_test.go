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

func verifyGateFor(appliesTo string, p kardinalv1alpha1.GateVerifyPolicy) kardinalv1alpha1.PolicyGate {
	g := makePolicyGate("explore", "platform-policies", appliesTo, "true")
	g.Spec.Verify = &p
	return g
}

// verdictObj is a Verdict object as the Verdicts selector ref lists it.
func verdictObj(name, env, check, result, uid string, actor map[string]interface{}) map[string]interface{} {
	spec := map[string]interface{}{"pipeline": "app", "bundle": "app-v1", "bundleUID": uid, "environment": env,
		"check": check, "result": result, "summary": name, "by": "system:serviceaccount:default:qa",
		"findings": []interface{}{map[string]interface{}{"severity": "high", "title": "x"},
			map[string]interface{}{"severity": "low", "title": "y"}}}
	if actor != nil {
		spec["actor"] = actor
	}
	return map[string]interface{}{"metadata": map[string]interface{}{"name": name, "creationTimestamp": "2026-10-14T10:00:00Z"},
		"spec": spec}
}

func agentActor(agent string) map[string]interface{} {
	return map[string]interface{}{"kind": "agent", "principal": "system:serviceaccount:default:" + agent,
		"agent": agent, "sponsor": "bob"}
}

// TestBuilder_VerifyGate: a gate with spec.verify adds the Verdicts selector
// ref, its instances are in the live gate collection with their verify
// policy (the verified environment resolved per instance), and spec.verdicts
// renders the Verdicts of this Bundle UID, Pipeline, environment and check
// from allowed writers, in name order, with their findings counted.
func TestBuilder_VerifyGate(t *testing.T) {
	p := makeLinearPipeline("app", "test", "uat", "prod")
	gate := verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Required: 2,
		AllowedAgents: []string{"qa"}})
	plain := makePolicyGate("weekday", "platform-policies", "uat", "!schedule.isWeekend")
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{gate, plain}})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	nodes := nodeByID(res.Graph.Spec.Nodes)
	require.Contains(t, nodes, graph.VerdictsNodeID)
	assert.NotContains(t, nodes, graph.ApprovalsNodeID, "no approval gate: no Approvals ref")
	assert.NotContains(t, nodes, graph.NodeAgentRuns, "no verify.run: no AgentRuns")
	require.Contains(t, nodes, graph.NodeVerifyGates)
	assert.NotContains(t, nodes[graph.NodeVerifyGates].Template["spec"], "approval",
		"no template has spec.approval, so spec.?approval would not compile in kro")

	uid := "uid-app-v1"
	verdicts := []interface{}{
		verdictObj("z-r2", "uat", "explore", "pass", uid, agentActor("qa")),
		verdictObj("a-r1", "uat", "explore", "fail", uid, agentActor("qa")),
		verdictObj("b-other-agent", "uat", "explore", "pass", uid, agentActor("intruder")),
		verdictObj("c-other-check", "uat", "smoke", "pass", uid, agentActor("qa")),
		verdictObj("d-other-env", "test", "explore", "pass", uid, agentActor("qa")),
		verdictObj("e-earlier-bundle", "uat", "explore", "pass", "uid-of-an-earlier-app-v1", agentActor("qa")),
		verdictObj("f-person", "uat", "explore", "pass", uid, nil),
	}
	var spec map[string]interface{}
	for _, o := range renderObjectsWith(t, res.Graph, map[string]interface{}{graph.VerdictsNodeID: verdicts}) {
		if o.Object["kind"] == "PolicyGate" && objLabels(o.Object)["kardinal.io/gate-template"] == "explore" {
			assert.Equal(t, graph.NodeVerifyGates, o.NodeID)
			spec = o.Object["spec"].(map[string]interface{})
		}
		if o.Object["kind"] == "PolicyGate" && objLabels(o.Object)["kardinal.io/gate-template"] == "weekday" {
			assert.NotContains(t, o.Object["spec"], "verdicts")
		}
	}
	require.NotNil(t, spec)
	v := spec["verify"].(map[string]interface{})
	assert.Equal(t, "uat", v["environment"], "the default: prod's only direct upstream")
	assert.Equal(t, "explore", v["check"])
	assert.EqualValues(t, 2, v["required"])
	assert.Equal(t, true, v["excludeAuthor"])
	assert.NotContains(t, spec, "approvals", "no approval gate in the Graph")
	var names []string
	for _, x := range spec["verdicts"].([]interface{}) {
		m := x.(map[string]interface{})
		names = append(names, m["name"].(string))
		assert.EqualValues(t, 2, m["findings"])
		assert.EqualValues(t, 1, m["highFindings"])
		assert.Equal(t, "2026-10-14T10:00:00Z", m["createdAt"])
	}
	assert.Equal(t, []string{"a-r1", "f-person", "z-r2"}, names,
		"this Bundle UID, uat, explore; agents only when allowed; anyone who is not an agent with no allowedUsers")

	for _, g := range res.GateInstances {
		if g.Labels["kardinal.io/gate-template"] == "explore" {
			require.NotNil(t, g.Spec.Verify)
			assert.Equal(t, "uat", g.Spec.Verify.Environment, "GateInstances carry the resolved policy")
		}
	}
}

// TestBuilder_VerifyGateWithApprovals: approval gates stay in ApprovalGates
// and verify gates are in VerifyGates; a verify gate copies Approvals only
// when it reads them, and neither collection renders spec.approval when no
// template has one (kro types the template list from its data).
func TestBuilder_VerifyGateWithApprovals(t *testing.T) {
	p := makeLinearPipeline("app", "uat", "prod")
	verify := verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "explore"})
	approval := makePolicyGate("approvers", "platform-policies", "prod", "true")
	approval.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{verify, approval}})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	refs := map[string]interface{}{
		graph.ApprovalsNodeID: []interface{}{approvalObj("a", "prod", "alice")},
		graph.VerdictsNodeID:  []interface{}{verdictObj("v", "uat", "explore", "pass", "uid-app-v1", nil)},
	}
	got := map[string]map[string]interface{}{}
	for _, o := range renderObjectsWith(t, res.Graph, refs) {
		if o.Object["kind"] == "PolicyGate" {
			got[fmt.Sprint(objLabels(o.Object)["kardinal.io/gate-template"])] = o.Object["spec"].(map[string]interface{})
		}
	}
	assert.Len(t, got["approvers"]["approvals"], 1)
	assert.Empty(t, got["approvers"]["verdicts"])
	assert.Nil(t, got["approvers"]["verify"])
	assert.Empty(t, got["explore"]["approvals"])
	assert.Len(t, got["explore"]["verdicts"], 1)
}

// TestBuilder_VerifyGateRefused: a verify gate that could never pass is a
// build error, with the reason.
func TestBuilder_VerifyGateRefused(t *testing.T) {
	cases := []struct {
		name      string
		pipeline  *kardinalv1alpha1.Pipeline
		appliesTo string
		env       string
		want      string
	}{
		{"a root environment has no upstream", makeLinearPipeline("app", "prod"), "prod", "", "0 direct upstreams"},
		{"itself", makeLinearPipeline("app", "uat", "prod"), "prod", "prod", "verifies prod itself"},
		{"not upstream", makeLinearPipeline("app", "uat", "prod"), "uat", "prod", "not upstream of uat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := verifyGateFor(tc.appliesTo, kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Environment: tc.env})
			_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: tc.pipeline, Bundle: makeBundle("app-v1", "app"),
				PolicyGates: []kardinalv1alpha1.PolicyGate{g}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	// Fan-in without verify.environment is ambiguous.
	p := makeLinearPipeline("app", "eu", "us", "prod")
	p.Spec.Environments[1].DependsOn = []string{}
	p.Spec.Environments[2].DependsOn = []string{"eu", "us"}
	_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "explore"})}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 direct upstreams")
	// A transitive upstream may be named.
	_, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: makeLinearPipeline("app", "test", "uat", "prod"),
		Bundle: makeBundle("app-v1", "app"), PolicyGates: []kardinalv1alpha1.PolicyGate{
			verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Environment: "test"})}})
	require.NoError(t, err)
}

// TestBuilder_AgentRuns: verify.run adds AgentRun data with one item per run
// and attempt (retries only with onInconclusive: retry), names of at most 52
// characters, the read-back refs, an admission def and a collection, in both
// shapes. Two gates asking for the same check share the runs.
func TestBuilder_AgentRuns(t *testing.T) {
	for _, shape := range []string{graph.GraphShapeNodes, graph.GraphShapeCompact} {
		t.Run(shape, func(t *testing.T) {
			p := makeLinearPipeline("a-rather-long-pipeline-name", "test", "uat", "prod", "prod-eu")
			p.Annotations = map[string]string{graph.AnnotationGraphShape: shape}
			retry := kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat", Required: 2, OnInconclusive: "retry", Retries: 1,
				Run: &kardinalv1alpha1.GateVerifyRun{Template: "qa-explorer", Count: 3}}
			g1 := verifyGateFor("prod", retry)
			g2 := verifyGateFor("prod-eu", kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat",
				Run: &kardinalv1alpha1.GateVerifyRun{Template: "qa-explorer", Count: 1}})
			g2.Name = "explore-eu"
			res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "a-rather-long-pipeline-name"),
				PolicyGates: []kardinalv1alpha1.PolicyGate{g1, g2}})
			require.NoError(t, err)
			assertKroValid(t, res.Graph)
			nodes := nodeByID(res.Graph.Spec.Nodes)
			for _, id := range []string{graph.NodeAgentRunData, graph.NodeAgentRunsAdmitted, graph.NodeAgentRuns, graph.NodeAgentRunState, "refAgentRuns"} {
				require.Contains(t, nodes, id)
			}
			if shape == graph.GraphShapeNodes {
				assert.Contains(t, nodes, "refVerifySteps")
			}
			items := nodes[graph.NodeAgentRunData].Def["items"].([]interface{})
			require.Len(t, items, 6, "3 runs x 2 attempts, shared by both gates")
			for _, x := range items {
				it := x.(map[string]interface{})
				assert.LessOrEqual(t, len(it["name"].(string)), graph.MaxAgentRunNameLength)
				assert.Equal(t, "uat", it["targetNamespace"], "the resource health check's namespace")
				if it["attempt"] == int64(2) {
					assert.NotEmpty(t, it["prev"])
				}
			}
			tmpl := nodes[graph.NodeAgentRuns].Template
			spec := tmpl["spec"].(map[string]interface{})
			assert.Equal(t, "v1", spec["version"])
			assert.Contains(t, spec["recorded"], graph.VerdictsNodeID)
			admitted := nodes[graph.NodeAgentRunsAdmitted].Def["items"].(string)
			assert.Contains(t, admitted, "verified")
			assert.Contains(t, admitted, "Superseded")
		})
	}
}

// TestBuilder_AgentRunAdmission evaluates the admission def: nothing until
// uat is Verified; then the first attempt of each run; a retry only after
// the previous attempt finished without a pass or a fail; and an existing
// AgentRun is kept, even once the Bundle is Superseded.
func TestBuilder_AgentRunAdmission(t *testing.T) {
	p := makeLinearPipeline("app", "uat", "prod")
	g := verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "explore", OnInconclusive: "retry", Retries: 1,
		Run: &kardinalv1alpha1.GateVerifyRun{Template: "qa", Count: 1}})
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{g}})
	require.NoError(t, err)
	nodes := nodeByID(res.Graph.Spec.Nodes)
	data := nodes[graph.NodeAgentRunData].Def
	items := data["items"].([]interface{})
	first, second := items[0].(map[string]interface{})["name"].(string), items[1].(map[string]interface{})["name"].(string)
	step := items[0].(map[string]interface{})["step"].(string)
	admitted := func(phase string, existing, finished, decided, verified []interface{}) []string {
		vars := map[string]interface{}{
			graph.NodeAgentRunData: data,
			graph.NodeAgentRunState: map[string]interface{}{"existing": existing, "finished": finished,
				"decided": decided, "verified": verified},
			"bundle": map[string]interface{}{"status": map[string]interface{}{"phase": phase, "conditions": []interface{}{}}},
		}
		var out []string
		for _, x := range evalCEL(t, nodes[graph.NodeAgentRunsAdmitted].Def["items"].(string), vars).([]interface{}) {
			out = append(out, x.(map[string]interface{})["name"].(string))
		}
		return out
	}
	none := []interface{}{}
	assert.Empty(t, admitted("Promoting", none, none, none, none), "uat not Verified")
	assert.Equal(t, []string{first}, admitted("Promoting", none, none, none, []interface{}{step}))
	assert.Equal(t, []string{first}, admitted("Promoting", []interface{}{first}, none, none, []interface{}{step}), "running")
	assert.Equal(t, []string{first}, admitted("Promoting", []interface{}{first}, []interface{}{first}, []interface{}{first},
		[]interface{}{step}), "a pass or fail: no retry")
	assert.Equal(t, []string{first, second}, admitted("Promoting", []interface{}{first}, []interface{}{first}, none,
		[]interface{}{step}), "finished without a pass or fail: retried")
	assert.Equal(t, []string{first}, admitted("Superseded", []interface{}{first}, []interface{}{first}, none,
		[]interface{}{step}), "Superseded: kept, no new attempt")
	assert.NotEqual(t, first, second)
}

// TestBuilder_RunVerdicts: a run-bound Verdict counts only when the run's
// own ServiceAccount wrote it and it names an AgentRun of this Graph whose
// agent is allowed; its actor and run index come from that AgentRun, not
// from the Verdict. A Verdict claiming another run, written by anyone else,
// or for an AgentRun someone else created, is not copied.
func TestBuilder_RunVerdicts(t *testing.T) {
	p := makeLinearPipeline("app", "uat", "prod")
	g := verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Required: 2, AllowedAgents: []string{"qa"},
		Run: &kardinalv1alpha1.GateVerifyRun{Template: "qa", Count: 2}})
	bundle := makeBundle("app-v1", "app")
	bundle.UID = "uid-app-v1"
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: bundle, PolicyGates: []kardinalv1alpha1.PolicyGate{g}})
	require.NoError(t, err)
	r1 := graph.AgentRunName("app", "app-v1", "uat", "explore", 1, 1)
	r2 := graph.AgentRunName("app", "app-v1", "uat", "explore", 2, 1)
	agentRun := func(name string, idx int, agent string, applied bool) map[string]interface{} {
		labels := map[string]interface{}{"kardinal.io/bundle-uid": "uid-app-v1"}
		if applied {
			labels["kro.run/node-id"] = "AgentRuns"
		}
		return map[string]interface{}{"metadata": map[string]interface{}{"name": name, "labels": labels},
			"spec":   map[string]interface{}{"check": "explore", "environment": "uat", "run": idx, "attempt": 1},
			"status": map[string]interface{}{"agent": agent, "sponsor": "bob", "phase": "Succeeded"}}
	}
	runVerdict := func(name, run, by string) map[string]interface{} {
		v := verdictObj(name, "uat", "explore", "pass", "uid-app-v1", nil)
		spec := v["spec"].(map[string]interface{})
		spec["by"] = by
		spec["run"] = map[string]interface{}{"name": run, "index": 9, "attempt": 1}
		return v
	}
	refs := map[string]interface{}{
		"refAgentRuns": []interface{}{agentRun(r1, 1, "qa", true), agentRun(r2, 2, "intruder", true),
			agentRun("forged", 1, "qa", false)},
		graph.VerdictsNodeID: []interface{}{
			runVerdict(r1, r1, graph.RunServiceAccountUser("default", r1)),                   // counts, as run 1 of agent qa
			runVerdict(r2, r2, graph.RunServiceAccountUser("default", r2)),                   // run 2's agent is not allowed
			runVerdict("claim-"+r1, r1, graph.RunServiceAccountUser("default", r1)),          // not named after its run
			runVerdict("x", "x", "alice"),                                                    // a run claim by someone else
			runVerdict("forged", "forged", graph.RunServiceAccountUser("default", "forged")), // not kro's AgentRun
		},
	}
	var verdicts []interface{}
	for _, o := range renderObjectsWith(t, res.Graph, refs) {
		if o.Object["kind"] == "PolicyGate" {
			verdicts = o.Object["spec"].(map[string]interface{})["verdicts"].([]interface{})
		}
	}
	require.Len(t, verdicts, 1)
	v := verdicts[0].(map[string]interface{})
	assert.Equal(t, r1, v["name"])
	assert.Equal(t, map[string]interface{}{"kind": "agent", "principal": graph.RunServiceAccountUser("default", r1),
		"agent": "qa", "sponsor": "bob"}, v["actor"])
	assert.EqualValues(t, 1, v["run"].(map[string]interface{})["index"], "the index is the AgentRun's, not the Verdict's 9")
}
