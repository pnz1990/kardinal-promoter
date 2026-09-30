// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package rollbackpolicy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPoliciesForStep verifies the PromotionStep watch enqueues only the
// RollbackPolicies that monitor the step's Bundle in its environment (C04-gates-07).
func TestPoliciesForStep(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	rp := func(name, env, bundle string) *v1alpha1.RollbackPolicy {
		return &v1alpha1.RollbackPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       v1alpha1.RollbackPolicySpec{PipelineName: "app", Environment: env, BundleRef: bundle},
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		rp("match", "prod", "app-v2"),
		rp("other-bundle", "prod", "app-v1"),
		rp("other-env", "uat", "app-v2"),
	).Build()
	r := &Reconciler{Client: c}

	step := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{
		Name: "app-v2-prod", Namespace: "default",
		Labels: map[string]string{labelPipeline: "app", labelEnvironment: "prod", labelBundle: "app-v2"},
	}}
	assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "match", Namespace: "default"}}},
		r.policiesForStep(context.Background(), step))

	specOnly := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-v2-prod", Namespace: "default",
			Labels: map[string]string{labelPipeline: "app", labelEnvironment: "prod"},
		},
		Spec: v1alpha1.PromotionStepSpec{BundleName: "app-v2"},
	}
	assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "match", Namespace: "default"}}},
		r.policiesForStep(context.Background(), specOnly), "spec.bundleName identifies the Bundle without the label")

	unlabelled := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"}}
	assert.Empty(t, r.policiesForStep(context.Background(), unlabelled))
}
