// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPipelineSpecHash_FleetMembers (D1): the resolved fleet members are
// part of the hash a Bundle in flight is rebuilt on; the status message is
// not, so a selector read error that keeps the last members, or a skipped
// object, rebuilds nothing.
//
// Covers FLEET-06.
func TestPipelineSpecHash_FleetMembers(t *testing.T) {
	p := &kardinalv1alpha1.Pipeline{}
	p.Spec.Environments = []kardinalv1alpha1.EnvironmentSpec{{Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{
		Selector: &kardinalv1alpha1.FleetSelector{MatchLabels: map[string]string{"tier": "prod"}}}}}
	p.Status.Fleets = []kardinalv1alpha1.FleetStatus{{Environment: "prod", Targets: []kardinalv1alpha1.FleetTarget{{Name: "eu"}}}}
	base := pipelineSpecHashFor(p)

	p.Status.Fleets[0].Message = "list Applications in argocd: timeout"
	assert.Equal(t, base, pipelineSpecHashFor(p), "a message alone rebuilds nothing")

	p.Status.Fleets[0].Targets = append(p.Status.Fleets[0].Targets, kardinalv1alpha1.FleetTarget{Name: "us"})
	assert.NotEqual(t, base, pipelineSpecHashFor(p), "a new member rebuilds the Graph")
}

// TestKeepRemovedFleetTargets (D1): before an in-place Graph update prunes
// the step of a fleet target the Pipeline no longer has, its record (state,
// message, PR) goes to status.retiredSteps, once; the steps of the targets
// that remain, and of other environments, are not recorded.
//
// Covers FLEET-07.
func TestKeepRemovedFleetTargets(t *testing.T) {
	p := &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}}
	p.Spec.Environments = []kardinalv1alpha1.EnvironmentSpec{{Name: "test"},
		{Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{Targets: []kardinalv1alpha1.FleetTarget{{Name: "eu"}}}}}
	step := func(env, fleet, state string) *kardinalv1alpha1.PromotionStep {
		s := &kardinalv1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: "web-b1-" + env, Namespace: "default",
			Labels: map[string]string{"kardinal.io/bundle": "web-b1"}},
			Spec:   kardinalv1alpha1.PromotionStepSpec{PipelineName: "web", BundleName: "web-b1", Environment: env},
			Status: kardinalv1alpha1.PromotionStepStatus{State: state, Message: "PR #3 is open", PRURL: "https://x/pull/3"}}
		if fleet != "" {
			s.Labels["kardinal.io/fleet"] = fleet
		}
		return s
	}
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		step("test", "", "Verified"), step("prod-eu", "prod", "Verified"), step("prod-us", "prod", "WaitingForMerge")).Build()
	b := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "web-b1", Namespace: "default"}}
	r := &Reconciler{Client: c}
	for range 2 {
		r.keepRemovedFleetTargets(context.Background(), zerolog.Nop(), b, p)
	}
	require.Len(t, b.Status.RetiredSteps, 1, "prod-us left the fleet; recorded once")
	got := b.Status.RetiredSteps[0]
	assert.Equal(t, "prod-us", got.Environment)
	assert.Equal(t, "WaitingForMerge", got.State)
	assert.Equal(t, "https://x/pull/3", got.PRURL)
}

// nopTranslator counts Translate calls.
type nopTranslator struct{ calls int }

func (m *nopTranslator) Translate(context.Context, *kardinalv1alpha1.Pipeline, *kardinalv1alpha1.Bundle) (string, error) {
	m.calls++
	return "g", nil
}

// TestEnsurePipelineSpecCurrent_KeepsRemovedTargets (D1): the in-place
// Graph update for a changed Pipeline records the removed target's step
// before it translates; a Pipeline whose fleets cannot be resolved records
// nothing (every target would look removed).
//
// Covers FLEET-07.
func TestEnsurePipelineSpecCurrent_KeepsRemovedTargets(t *testing.T) {
	p := &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}}
	p.Spec.Environments = []kardinalv1alpha1.EnvironmentSpec{{Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{
		Targets: []kardinalv1alpha1.FleetTarget{{Name: "eu"}}}}}
	gone := &kardinalv1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: "web-b1-prod-us", Namespace: "default",
		Labels: map[string]string{"kardinal.io/bundle": "web-b1", "kardinal.io/fleet": "prod"}},
		Spec: kardinalv1alpha1.PromotionStepSpec{Environment: "prod-us"}, Status: kardinalv1alpha1.PromotionStepStatus{State: "WaitingForMerge"}}
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gone).Build()
	tr := &nopTranslator{}
	r := &Reconciler{Client: c, Translator: tr}
	b := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "web-b1", Namespace: "default"}}
	b.Status.PipelineSpecHash = "old"
	require.NoError(t, r.ensurePipelineSpecCurrent(context.Background(), zerolog.Nop(), b, p))
	assert.Equal(t, 1, tr.calls)
	require.Len(t, b.Status.RetiredSteps, 1)
	assert.Equal(t, "prod-us", b.Status.RetiredSteps[0].Environment)

	// An unresolved selector fleet: nothing is recorded.
	unresolved := p.DeepCopy()
	unresolved.Spec.Environments[0].Fleet = &kardinalv1alpha1.FleetSpec{Selector: &kardinalv1alpha1.FleetSelector{
		MatchLabels: map[string]string{"tier": "prod"}}}
	b2 := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "web-b1", Namespace: "default"}}
	r.keepRemovedFleetTargets(context.Background(), zerolog.Nop(), b2, unresolved)
	assert.Empty(t, b2.Status.RetiredSteps)
}
