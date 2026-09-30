// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPipelineForFreezeGate: a PolicyGate named freeze-<pipeline> enqueues
// that Pipeline whoever owns it, so deleting a user gate that blocks the
// freeze gate lets a paused Pipeline create it at once. Other gates enqueue
// nothing.
func TestPipelineForFreezeGate(t *testing.T) {
	tests := []struct {
		name string
		gate string
		want []ctrl.Request
	}{
		{name: "freeze gate", gate: "freeze-app",
			want: []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: "team", Name: "app"}}}},
		{name: "pipeline name with the prefix", gate: "freeze-freeze-app",
			want: []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: "team", Name: "freeze-app"}}}},
		{name: "prefix only", gate: "freeze-"},
		{name: "other gate", gate: "no-weekend-deploys"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gate := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: tc.gate, Namespace: "team"}}
			assert.Equal(t, tc.want, pipelineForFreezeGate(context.Background(), gate))
		})
	}
}

// TestPipelineForBundle covers E2E-R05: a Bundle event enqueues the Pipeline
// it names, so status.phase turns Promoting when a Bundle starts even if a
// gate holds it before any PromotionStep exists.
func TestPipelineForBundle(t *testing.T) {
	tests := []struct {
		name     string
		pipeline string
		want     []ctrl.Request
	}{
		{name: "bundle of a pipeline", pipeline: "app",
			want: []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: "team", Name: "app"}}}},
		{name: "bundle without a pipeline"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "team"},
				Spec: kardinalv1alpha1.BundleSpec{Pipeline: tc.pipeline}}
			assert.Equal(t, tc.want, pipelineForBundle(context.Background(), b))
		})
	}
}

// TestBundlePhaseChanged: only Bundle updates that change status.phase
// re-derive the Pipeline phase; evidence and condition updates do not.
func TestBundlePhaseChanged(t *testing.T) {
	bundle := func(phase, msg string) *kardinalv1alpha1.Bundle {
		return &kardinalv1alpha1.Bundle{Status: kardinalv1alpha1.BundleStatus{Phase: phase,
			Conditions: []metav1.Condition{{Type: "Ready", Message: msg}}}}
	}
	assert.True(t, bundlePhaseChanged.Update(event.UpdateEvent{
		ObjectOld: bundle("Available", ""), ObjectNew: bundle("Promoting", "")}))
	assert.False(t, bundlePhaseChanged.Update(event.UpdateEvent{
		ObjectOld: bundle("Promoting", "a"), ObjectNew: bundle("Promoting", "b")}))
	assert.True(t, bundlePhaseChanged.Create(event.CreateEvent{Object: bundle("", "")}))
	assert.True(t, bundlePhaseChanged.Delete(event.DeleteEvent{Object: bundle("Promoting", "")}))
}
