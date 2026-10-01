// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package rollbackpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

// TestPoliciesForStep_ListError verifies a failed RollbackPolicy List is
// logged at error level with the step's namespace and name (B49). Policies
// are not polled, so this log is the only trace of the lost event.
func TestPoliciesForStep_ListError(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("cache not synced")
		},
	}).Build()
	r := &Reconciler{Client: c}

	var buf bytes.Buffer
	ctx := zerolog.New(&buf).WithContext(context.Background())
	step := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{
		Name: "app-v2-prod", Namespace: "team-a",
		Labels: map[string]string{labelPipeline: "app", labelEnvironment: "prod", labelBundle: "app-v2"},
	}}
	assert.Nil(t, r.policiesForStep(ctx, step))

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry), "one JSON log line: %q", buf.String())
	assert.Equal(t, "error", entry["level"])
	assert.Equal(t, "team-a", entry["namespace"])
	assert.Equal(t, "app-v2-prod", entry["promotionstep"])
	assert.Equal(t, "cache not synced", entry["error"])
}
