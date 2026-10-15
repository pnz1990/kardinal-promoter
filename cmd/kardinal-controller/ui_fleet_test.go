// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPipelineListResponse_Fleet (D1): a fleet environment is one node of
// environmentTopology, with its target environments and pacing in fleet;
// the environment after it waits for the fleet, and environmentStates and
// deployed are keyed by target environment, which the fleet board rolls up.
//
// Covers FLEET-05.
func TestPipelineListResponse_Fleet(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	p := v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}}
	p.Spec.Environments = []v1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod", Fleet: &v1alpha1.FleetSpec{MaxConcurrent: 2, MaxUnavailable: ptr(intstr.FromString("34%")),
			Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}, {Name: "ap"}}}},
		{Name: "audit"},
	}
	b := v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "web-b1", Namespace: "team", CreationTimestamp: at},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "web", Images: []v1alpha1.ImageRef{{Repository: "r/web", Tag: "1.2.0"}}},
		Status: v1alpha1.BundleStatus{Phase: "Promoting", Environments: []v1alpha1.EnvironmentStatus{
			{Name: "test", Phase: "Verified"}, {Name: "prod-eu", Phase: "Verified"}, {Name: "prod-us", Phase: "Promoting"}}}}
	step := func(env, state string) v1alpha1.PromotionStep {
		s := v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: "web-b1-" + env, Namespace: "team", CreationTimestamp: at},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "web", BundleName: b.Name, Environment: env},
			Status: v1alpha1.PromotionStepStatus{State: state}}
		if state == "Verified" {
			s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, LastTransitionTime: at}}
		}
		return s
	}
	steps := []v1alpha1.PromotionStep{step("test", "Verified"), step("prod-eu", "Verified"), step("prod-us", "Promoting")}
	got := pipelineListResponse([]v1alpha1.Pipeline{p}, []v1alpha1.Bundle{b}, steps, nil, time.Now(), nil)
	require.Len(t, got, 1)
	topo := got[0].EnvironmentTopology
	require.Len(t, topo, 3)
	assert.Nil(t, topo[0].Fleet)
	assert.Equal(t, &uiFleet{Targets: []string{"prod-eu", "prod-us", "prod-ap"}, MaxConcurrent: 2, MaxUnavailable: ptr(2),
		MaxUnavailableSpec: "34%"}, topo[1].Fleet, "34% of 3 targets, rounded up")
	assert.Equal(t, []string{"test"}, topo[1].Upstreams, "the fleet waits for what it depends on")
	assert.Equal(t, []string{"prod"}, topo[2].Upstreams, "the environment after a fleet waits for the fleet")
	assert.Equal(t, "Verified", got[0].EnvironmentStates["prod-eu"])
	assert.Equal(t, "Promoting", got[0].EnvironmentStates["prod-us"])
	assert.Equal(t, "1.2.0", got[0].Deployed["prod-eu"].Version)

	// A selector fleet that could not be resolved says why.
	p.Spec.Environments[1].Fleet = &v1alpha1.FleetSpec{Selector: &v1alpha1.FleetSelector{Kind: v1alpha1.FleetSelectorApplication,
		MatchLabels: map[string]string{"tier": "prod"}}}
	p.Status.Fleets = []v1alpha1.FleetStatus{{Environment: "prod", Message: "list Applications: forbidden"}}
	got = pipelineListResponse([]v1alpha1.Pipeline{p}, []v1alpha1.Bundle{b}, steps, nil, time.Now(), nil)
	assert.Equal(t, &uiFleet{Targets: []string{}, Message: "list Applications: forbidden"}, got[0].EnvironmentTopology[1].Fleet)
}

// TestPipelineListResponse_Actors (#1908): the pipeline list says who shipped
// the active Bundle and each environment's Bundle, in short, for the fleet
// board, the Agents filter and the station map; a Bundle without an actor
// stamp has none.
func TestPipelineListResponse_Actors(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	p := v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}}
	p.Spec.Environments = []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}
	agent := &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer", Agent: "fixer", Sponsor: "alice"}
	b1 := v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "team", CreationTimestamp: at},
		Spec:   v1alpha1.BundleSpec{Type: "image", Pipeline: "web", Images: []v1alpha1.ImageRef{{Repository: "r/web", Tag: "1.0.0"}}},
		Status: v1alpha1.BundleStatus{Phase: "Verified"}}
	b2 := v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "web-2", Namespace: "team", CreationTimestamp: metav1.NewTime(at.Add(time.Hour))},
		Spec:   v1alpha1.BundleSpec{Type: "image", Pipeline: "web", Actor: agent, Images: []v1alpha1.ImageRef{{Repository: "r/web", Tag: "2.0.0"}}},
		Status: v1alpha1.BundleStatus{Phase: "Promoting", Environments: []v1alpha1.EnvironmentStatus{{Name: "test", Phase: "Verified"}}}}
	step := func(b, env string) v1alpha1.PromotionStep {
		return v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: b + "-" + env, Namespace: "team", CreationTimestamp: at},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "web", BundleName: b, Environment: env},
			Status: v1alpha1.PromotionStepStatus{State: "Verified", Conditions: []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, LastTransitionTime: at}}}}
	}
	got := pipelineListResponse([]v1alpha1.Pipeline{p}, []v1alpha1.Bundle{b1, b2},
		[]v1alpha1.PromotionStep{step("web-1", "prod"), step("web-2", "test")}, nil, time.Now(), nil)
	require.Len(t, got, 1)
	want := &uiActorRef{Kind: "agent", Principal: "system:serviceaccount:ci:fixer", Agent: "fixer", Sponsor: "alice", Text: "agent fixer for alice"}
	assert.Equal(t, want, got[0].ActiveBundleActor)
	assert.Equal(t, want, got[0].Deployed["test"].Actor)
	assert.Nil(t, got[0].Deployed["prod"].Actor, "a Bundle without an actor stamp")
}

// TestGateVerify_VerifierActor (#1908): each verdict of a verify gate carries
// its writer in short, from the gate's copy of the Verdict, and its high
// findings; a copy without an actor stamp (a v0.11-style writer) has none.
func TestGateVerify_VerifierActor(t *testing.T) {
	g := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "qa", Namespace: "team"},
		Spec: v1alpha1.PolicyGateSpec{Verify: &v1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat", Required: 2},
			Verdicts: []v1alpha1.GateVerdict{
				{Name: "v1", Result: v1alpha1.VerdictPass, By: "system:serviceaccount:agents:qa", Findings: 3, HighFindings: 1,
					Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:agents:qa", Agent: "qa", Sponsor: "bob"}},
				{Name: "v2", Result: "inconclusive", By: "carol"},
			}},
		Status: v1alpha1.PolicyGateStatus{Verdicts: []v1alpha1.GateVerdictStatus{
			{Name: "v1", Verifier: "agent qa for bob", Result: v1alpha1.VerdictPass, Counted: true, Findings: 3},
			{Name: "v2", Verifier: "carol", Result: "inconclusive", Counted: false, Reason: "inconclusive: holds"},
		}}}
	v := gateVerify(g)
	require.NotNil(t, v)
	require.Len(t, v.Verdicts, 2)
	assert.Equal(t, &uiActorRef{Kind: "agent", Principal: "system:serviceaccount:agents:qa", Agent: "qa", Sponsor: "bob", Text: "agent qa for bob"},
		v.Verdicts[0].VerifierActor)
	assert.Equal(t, 1, v.Verdicts[0].HighFindings)
	assert.Nil(t, v.Verdicts[1].VerifierActor, "no actor stamp on the copy")
	assert.Equal(t, 1, v.Passed)
}
