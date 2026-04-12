// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kardinalv1alpha1.AddToScheme(s)
	return s
}

func newPipeline(name string, envs []kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{
				URL: "https://github.com/myorg/gitops.git",
			},
			Environments: envs,
		},
	}
}

// TestPipelineReconciler_SetsInitializingCondition verifies that a new Pipeline
// gets a Ready=False/Initializing condition after reconciliation.
func TestPipelineReconciler_SetsInitializingCondition(t *testing.T) {
	p := newPipeline("nginx-demo", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "uat", DependsOn: []string{"test"}},
		{Name: "prod", DependsOn: []string{"uat"}},
	})

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(p).
		WithStatusSubresource(p).
		Build()

	r := &pipeline.Reconciler{Client: c}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo", Namespace: "default"},
	})

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "nginx-demo", Namespace: "default",
	}, &got))

	require.Len(t, got.Status.Conditions, 1)
	cond := got.Status.Conditions[0]
	assert.Equal(t, "Ready", cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "Initializing", cond.Reason)
}

// TestPipelineReconciler_DuplicateEnvironmentNames verifies that a Pipeline with
// duplicate environment names gets a ValidationFailed condition.
func TestPipelineReconciler_DuplicateEnvironmentNames(t *testing.T) {
	p := newPipeline("bad-pipeline", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "test"}, // duplicate
	})

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(p).
		WithStatusSubresource(p).
		Build()

	r := &pipeline.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "bad-pipeline", Namespace: "default"},
	})
	require.NoError(t, err) // reconciler returns no error — error is in status

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "bad-pipeline", Namespace: "default",
	}, &got))

	require.Len(t, got.Status.Conditions, 1)
	cond := got.Status.Conditions[0]
	assert.Equal(t, "Ready", cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "ValidationFailed", cond.Reason)
	assert.Contains(t, cond.Message, "duplicate environment name")
}

// TestPipelineReconciler_DependsOnNonExistentEnv verifies that a Pipeline where
// dependsOn references an unknown environment gets a ValidationFailed condition.
func TestPipelineReconciler_DependsOnNonExistentEnv(t *testing.T) {
	p := newPipeline("bad-deps", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", DependsOn: []string{"staging"}}, // "staging" doesn't exist
	})

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(p).
		WithStatusSubresource(p).
		Build()

	r := &pipeline.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "bad-deps", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "bad-deps", Namespace: "default",
	}, &got))

	require.Len(t, got.Status.Conditions, 1)
	cond := got.Status.Conditions[0]
	assert.Equal(t, "Ready", cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "ValidationFailed", cond.Reason)
	assert.Contains(t, cond.Message, "staging")
}

// TestPipelineReconciler_Idempotent verifies that if a Pipeline already has the
// correct Initializing condition, reconcile is a no-op.
func TestPipelineReconciler_Idempotent(t *testing.T) {
	p := newPipeline("nginx-demo", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod", DependsOn: []string{"test"}},
	})
	// Pre-populate the condition as if a previous reconcile already ran
	p.Status.Conditions = []metav1.Condition{
		{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "Initializing",
			Message:            "Pipeline initialized, awaiting first Bundle",
			LastTransitionTime: metav1.Now(),
		},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(p).
		WithStatusSubresource(p).
		Build()

	r := &pipeline.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo", Namespace: "default"},
	})
	require.NoError(t, err)

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "nginx-demo", Namespace: "default",
	}, &got))

	require.Len(t, got.Status.Conditions, 1)
	assert.Equal(t, "Initializing", got.Status.Conditions[0].Reason)
}

// TestPipelineReconciler_NotFound verifies that a missing Pipeline is handled
// gracefully (deleted between event and reconcile).
func TestPipelineReconciler_NotFound(t *testing.T) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	r := &pipeline.Reconciler{Client: c}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "gone", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// TestDerivePhase_NoSteps verifies that an empty step list yields Unknown.
func TestDerivePhase_NoSteps(t *testing.T) {
	assert.Equal(t, "Unknown", pipeline.DerivePhase(nil))
	assert.Equal(t, "Unknown", pipeline.DerivePhase([]kardinalv1alpha1.PromotionStep{}))
}

// TestDerivePhase_AllVerified verifies that all-Verified steps yield Ready.
func TestDerivePhase_AllVerified(t *testing.T) {
	now := metav1.Now()
	steps := []kardinalv1alpha1.PromotionStep{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-test", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", Environment: "test"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-prod", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", Environment: "prod"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
		},
	}
	assert.Equal(t, "Ready", pipeline.DerivePhase(steps))
}

// TestDerivePhase_OneFailed verifies that a Failed step yields Degraded.
func TestDerivePhase_OneFailed(t *testing.T) {
	now := metav1.Now()
	steps := []kardinalv1alpha1.PromotionStep{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-test", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", Environment: "test"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-prod", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", Environment: "prod"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Failed"},
		},
	}
	assert.Equal(t, "Degraded", pipeline.DerivePhase(steps))
}

// TestDerivePhase_Promoting verifies that Promoting steps yield Unknown (not yet done).
func TestDerivePhase_Promoting(t *testing.T) {
	now := metav1.Now()
	steps := []kardinalv1alpha1.PromotionStep{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-test", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", Environment: "test"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-prod", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", Environment: "prod"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Promoting"},
		},
	}
	assert.Equal(t, "Unknown", pipeline.DerivePhase(steps))
}

// TestDerivePhase_MultiBundle_NewFailed_OldVerified verifies that when a new bundle
// has a Failed step, Degraded is shown (not Ready from the old Verified step).
func TestDerivePhase_MultiBundle_NewFailed_OldVerified(t *testing.T) {
	old := metav1.NewTime(metav1.Now().Add(-1 * 3600 * 1e9)) // 1 hour ago
	now := metav1.Now()
	steps := []kardinalv1alpha1.PromotionStep{
		// Old bundle — Verified in prod
		{
			ObjectMeta: metav1.ObjectMeta{Name: "old-prod", Namespace: "default", CreationTimestamp: old},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", Environment: "prod"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
		},
		// New bundle — Failed in prod
		{
			ObjectMeta: metav1.ObjectMeta{Name: "new-prod", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", Environment: "prod"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Failed"},
		},
	}
	// Most recent step per env wins: new-prod is Failed → Degraded
	assert.Equal(t, "Degraded", pipeline.DerivePhase(steps))
}
