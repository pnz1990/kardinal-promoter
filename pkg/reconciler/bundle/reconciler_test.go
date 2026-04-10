// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kardinalv1alpha1.AddToScheme(s)
	return s
}

// mockTranslator is a test double for BundleTranslator.
type mockTranslator struct {
	graphName string
	err       error
	called    bool
}

func (m *mockTranslator) Translate(_ context.Context,
	_ *kardinalv1alpha1.Pipeline, _ *kardinalv1alpha1.Bundle) (string, error) {
	m.called = true
	return m.graphName, m.err
}

// TestBundleReconciler_SetsAvailablePhase verifies that a Bundle with an empty
// status.phase is set to Available after reconciliation.
func TestBundleReconciler_SetsAvailablePhase(t *testing.T) {
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1",
			Namespace: "default",
		},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
		},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(b).
		WithStatusSubresource(b).
		Build()

	r := &bundle.Reconciler{Client: c}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})

	require.NoError(t, err)
	// Requeue expected to immediately advance to Promoting
	assert.True(t, result.Requeue)

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "nginx-demo-v1", Namespace: "default",
	}, &got))
	assert.Equal(t, "Available", got.Status.Phase)
}

// TestBundleReconciler_AvailableToPromoting verifies that an Available Bundle
// with a Translator triggers graph creation and advances to Promoting.
func TestBundleReconciler_AvailableToPromoting(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
			},
		},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
		},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	translator := &mockTranslator{graphName: "nginx-demo-v1-graph"}
	r := &bundle.Reconciler{Client: c, Translator: translator}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})

	require.NoError(t, err)
	assert.True(t, translator.called, "Translator.Translate must have been called")
	assert.False(t, result.Requeue, "no requeue after advancing to Promoting")

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "nginx-demo-v1", Namespace: "default",
	}, &got))
	assert.Equal(t, "Promoting", got.Status.Phase)
}

// TestBundleReconciler_TranslationError sets bundle to Failed when Translator errors.
func TestBundleReconciler_TranslationError(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	translator := &mockTranslator{err: assert.AnError}
	r := &bundle.Reconciler{Client: c, Translator: translator}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})
	require.Error(t, err)

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "nginx-demo-v1", Namespace: "default",
	}, &got))
	assert.Equal(t, "Failed", got.Status.Phase)
}

// TestBundleReconciler_NoTranslatorSkipsPromotion verifies that Available
// bundles are left in place when no Translator is configured.
func TestBundleReconciler_NoTranslatorSkipsPromotion(t *testing.T) {
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(b).WithStatusSubresource(b).Build()

	r := &bundle.Reconciler{Client: c} // no Translator
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.False(t, result.Requeue)

	// Phase must still be Available
	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "nginx-demo-v1", Namespace: "default",
	}, &got))
	assert.Equal(t, "Available", got.Status.Phase)
}

// TestBundleReconciler_Idempotent verifies that reconciling an already-Available
// bundle twice is safe.
func TestBundleReconciler_Idempotent(t *testing.T) {
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(b).WithStatusSubresource(b).Build()

	r := &bundle.Reconciler{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})
	require.NoError(t, err)
}

// TestBundleReconciler_NotFound verifies that a missing Bundle returns no error.
func TestBundleReconciler_NotFound(t *testing.T) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	r := &bundle.Reconciler{Client: c}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "gone", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// TestBundleReconciler_PromotingPhaseIsNoOp verifies that Promoting bundles are skipped.
func TestBundleReconciler_PromotingPhaseIsNoOp(t *testing.T) {
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(b).WithStatusSubresource(b).Build()

	translator := &mockTranslator{graphName: "graph"}
	r := &bundle.Reconciler{Client: c, Translator: translator}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.False(t, translator.called, "Translator must NOT be called for Promoting bundles")
}

// TestBundleReconciler_Supersession verifies that creating a new Bundle for a Pipeline
// supersedes older Promoting bundles.
func TestBundleReconciler_Supersession(t *testing.T) {
	// Old bundle is Promoting for the same pipeline.
	oldBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-v1",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}
	// New bundle: no status yet.
	newBundleObj := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v2", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(oldBundle, newBundleObj).
		WithStatusSubresource(oldBundle, newBundleObj).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v2", Namespace: "default"},
	})
	require.NoError(t, err)

	// New bundle should be Available.
	var gotNew kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v2", Namespace: "default"}, &gotNew))
	assert.Equal(t, "Available", gotNew.Status.Phase)

	// Old bundle should be Superseded.
	var gotOld kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"}, &gotOld))
	assert.Equal(t, "Superseded", gotOld.Status.Phase)
}

// TestBundleReconciler_Supersession_DifferentPipeline verifies that bundles for
// different pipelines are NOT superseded.
func TestBundleReconciler_Supersession_DifferentPipeline(t *testing.T) {
	otherPipelineBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "other-pipeline-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "other-pipeline"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}
	newBundleObj := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v2", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(otherPipelineBundle, newBundleObj).
		WithStatusSubresource(otherPipelineBundle, newBundleObj).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v2", Namespace: "default"},
	})
	require.NoError(t, err)

	// Other pipeline bundle must remain Promoting (not superseded).
	var gotOther kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "other-pipeline-v1", Namespace: "default"}, &gotOther))
	assert.Equal(t, "Promoting", gotOther.Status.Phase)
}

// TestBundleReconciler_Supersession_IdempotentForSuperseded verifies that
// already-Superseded bundles are not touched again.
func TestBundleReconciler_Supersession_IdempotentForSuperseded(t *testing.T) {
	alreadySuperseded := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v0", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Superseded"},
	}
	newBundleObj := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v2", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(alreadySuperseded, newBundleObj).
		WithStatusSubresource(alreadySuperseded, newBundleObj).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v2", Namespace: "default"},
	})
	require.NoError(t, err)

	// Still Superseded (not re-patched to a different value).
	var gotOld kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v0", Namespace: "default"}, &gotOld))
	assert.Equal(t, "Superseded", gotOld.Status.Phase)
}
