// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
)

// TestBundleReconciler_FleetTargetRollbackDoesNotSupersede (D1): rolling one
// fleet target back (a newer rollback Bundle whose targetEnvironment is that
// target) does not supersede the fleet's Bundle, which goes on promoting the
// other targets. A newer rollback of the whole fleet, or a newer Bundle, does.
//
// Covers FLEET-07.
func TestBundleReconciler_FleetTargetRollbackDoesNotSupersede(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{
			{Name: "test"},
			{Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{Targets: []kardinalv1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}},
		}},
	}
	at := func(h int) metav1.Time { return metav1.Time{Time: time.Date(2026, 1, 1, h, 0, 0, 0, time.UTC)} }
	rollback := func(name, target string) *kardinalv1alpha1.Bundle {
		return &kardinalv1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: at(2),
				Labels: map[string]string{"kardinal.io/rollback": "true"}},
			Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "web", Intent: &kardinalv1alpha1.BundleIntent{TargetEnvironment: target}},
			Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
		}
	}
	for _, tc := range []struct {
		name       string
		newer      *kardinalv1alpha1.Bundle
		superseded bool
	}{
		{"rollback of one target", rollback("web-rollback-eu", "prod-eu"), false},
		{"rollback of the whole fleet", rollback("web-rollback-all", "prod"), true},
		{"a newer Bundle", &kardinalv1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: "web-v3", Namespace: "default", CreationTimestamp: at(2)},
			Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "web"},
			Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fleet := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "web-v2", Namespace: "default", CreationTimestamp: at(1)},
				Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "web"},
				Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
			}
			c := indexedBuilder(newScheme()).WithObjects(pipeline.DeepCopy(), fleet, tc.newer).WithStatusSubresource(fleet, tc.newer).Build()
			key := types.NamespacedName{Name: fleet.Name, Namespace: "default"}
			_, err := (&bundle.Reconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			require.NoError(t, err)
			var got kardinalv1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), key, &got))
			assert.Equal(t, tc.superseded, got.Status.Phase == "Superseded", got.Status.Phase)
		})
	}
}

// TestBundleReconciler_FleetTargetSupersededIsSettled (#1603 with D1): the
// rollback of one fleet target pushed there first, so the fleet Bundle's step
// for that target ended Superseded. The fleet Bundle is not superseded by it:
// it is Verified once every other environment is, and the target is shown
// Superseded. A Superseded step outside a fleet supersedes its Bundle.
//
// Covers FLEET-08.
func TestBundleReconciler_FleetTargetSupersededIsSettled(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{
			{Name: "test"},
			{Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{Targets: []kardinalv1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}},
		}},
	}
	target := func(env, name, state string) *kardinalv1alpha1.PromotionStep {
		s := lcStep("app-v2", env, name, state)
		s.Labels["kardinal.io/fleet"] = "prod"
		return s
	}
	for _, tc := range []struct {
		name  string
		steps []*kardinalv1alpha1.PromotionStep
		want  string
	}{
		{"a Superseded target and the others Verified", []*kardinalv1alpha1.PromotionStep{
			lcStep("app-v2", "test", "s-test", "Verified"), target("prod-eu", "s-eu", "Superseded"), target("prod-us", "s-us", "Verified")}, "Verified"},
		{"a Superseded target and another in flight", []*kardinalv1alpha1.PromotionStep{
			lcStep("app-v2", "test", "s-test", "Verified"), target("prod-eu", "s-eu", "Superseded"), target("prod-us", "s-us", "Promoting")}, "Promoting"},
		{"every target Superseded: replaced everywhere there", []*kardinalv1alpha1.PromotionStep{
			lcStep("app-v2", "test", "s-test", "Verified"), target("prod-eu", "s-eu", "Superseded"), target("prod-us", "s-us", "Superseded")}, "Superseded"},
		{"a Superseded step outside the fleet", []*kardinalv1alpha1.PromotionStep{
			lcStep("app-v2", "test", "s-test", "Superseded")}, "Superseded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{pipeline.DeepCopy(), lcBundle("app-v2", "image", "Promoting", time.Now().UTC())}
			for _, s := range tc.steps {
				objs = append(objs, s)
			}
			c := lcClient(objs...)
			lcReconcile(t, &bundle.Reconciler{Client: c}, "app-v2")
			got := lcGet(t, c, "app-v2")
			assert.Equal(t, tc.want, got.Status.Phase)
			for _, e := range got.Status.Environments {
				if e.Name == "prod-eu" {
					assert.Equal(t, "Superseded", e.Phase, "the fleet status names the Superseded target")
				}
			}
		})
	}
}
