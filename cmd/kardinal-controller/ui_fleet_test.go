// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
		{Name: "prod", Fleet: &v1alpha1.FleetSpec{MaxConcurrent: 2, MaxUnavailable: ptr(1),
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
	assert.Equal(t, &uiFleet{Targets: []string{"prod-eu", "prod-us", "prod-ap"}, MaxConcurrent: 2, MaxUnavailable: ptr(1)}, topo[1].Fleet)
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
