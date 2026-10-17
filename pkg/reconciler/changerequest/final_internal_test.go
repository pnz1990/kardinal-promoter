// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changerequest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestBecameFinalAndBundleInstances: the Bundle watch passes only a move to
// Superseded or Rejected, or back out of either (polling resumes), and maps it to that Bundle's instances only.
func TestBecameFinalAndBundleInstances(t *testing.T) {
	b := func(phase string) *kardinalv1alpha1.Bundle {
		return &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "checkout"},
			Status: kardinalv1alpha1.BundleStatus{Phase: phase}}
	}
	for _, tc := range []struct {
		from, to string
		want     bool
	}{
		{"Promoting", "Superseded", true},
		{"Promoting", "Rejected", true},
		{"Verified", "Rejected", true},
		{"Promoting", "Verified", false},
		{"Superseded", "Superseded", false},
		{"Superseded", "Rejected", false},
		{"Superseded", "Promoting", true},
		{"Rejected", "Verified", true},
	} {
		assert.Equal(t, tc.want, becameFinal.Update(event.UpdateEvent{ObjectOld: b(tc.from), ObjectNew: b(tc.to)}), "%s → %s", tc.from, tc.to)
	}
	assert.False(t, becameFinal.Create(event.CreateEvent{Object: b("Superseded")}))

	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	cr := func(name, bundle string) *kardinalv1alpha1.ChangeRequest {
		return &kardinalv1alpha1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "checkout",
			Labels: map[string]string{labelBundle: bundle}}}
	}
	other := cr("x", "app-v1")
	other.Namespace = "elsewhere"
	r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(cr("a", "app-v1"), cr("b", "app-v2"), other).Build()}
	assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "checkout", Name: "a"}}},
		r.bundleInstances(context.Background(), b("Superseded")))
}
