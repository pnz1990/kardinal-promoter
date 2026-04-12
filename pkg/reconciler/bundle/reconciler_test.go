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
	// RequeueAfter > 0 means immediate requeue to advance to Promoting.
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

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
	assert.Zero(t, result.RequeueAfter, "no requeue after advancing to Promoting")

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
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(pipeline, b).WithStatusSubresource(b).Build()

	r := &bundle.Reconciler{Client: c} // no Translator
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)

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

// TestBundleReconciler_SelfSupersession verifies the BU-1 fix: self-supersession.
// When an older Available bundle detects a newer sibling, it marks itself Superseded.
// This replaces the old cross-CRD write where the new bundle superseded its siblings.
func TestBundleReconciler_SelfSupersession(t *testing.T) {
	// Old bundle is Available for the same pipeline (older creation timestamp).
	oldBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-v1",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}
	// New bundle is Available and was created later.
	newBundleObj := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-v2",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(oldBundle, newBundleObj).
		WithStatusSubresource(oldBundle, newBundleObj).
		Build()

	r := &bundle.Reconciler{Client: c}

	// Reconcile old bundle — it detects the newer sibling and marks itself Superseded.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	// Old bundle should be Superseded (self-supersession).
	var gotOld kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"}, &gotOld))
	assert.Equal(t, "Superseded", gotOld.Status.Phase)

	// New bundle is unaffected.
	var gotNew kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v2", Namespace: "default"}, &gotNew))
	assert.Equal(t, "Available", gotNew.Status.Phase)
}

// TestBundleReconciler_NewBundleBecomesAvailableWithoutSupersedingSiblings documents
// the BU-1 fix: reconciling the new bundle no longer supersedes siblings.
// The old bundle remains Available until it reconciles itself (self-supersession).
func TestBundleReconciler_NewBundleBecomesAvailableWithoutSupersedingSiblings(t *testing.T) {
	// Old bundle is Promoting.
	oldBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-v1",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}
	// New bundle just created (no status).
	newBundleObj := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-v2",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
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

	// Old bundle should still be Promoting — it has not reconciled itself yet.
	// (Self-supersession happens when the old bundle's own reconcile runs.)
	var gotOld kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"}, &gotOld))
	assert.Equal(t, "Promoting", gotOld.Status.Phase,
		"old bundle stays Promoting until it reconciles itself (BU-1 self-supersession)")
}

// TestBundleReconciler_Supersession_DifferentPipeline verifies that bundles for
// different pipelines are NOT superseded (self-supersession check).
func TestBundleReconciler_Supersession_DifferentPipeline(t *testing.T) {
	// Both pipelines must exist so bundles are not self-deleted (#270).
	otherPipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "other-pipeline", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}},
	}
	nginxPipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}},
	}
	otherPipelineBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "other-pipeline-v1",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "other-pipeline"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}
	newerNginxBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-v2",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(otherPipeline, nginxPipeline, otherPipelineBundle, newerNginxBundle).
		WithStatusSubresource(otherPipelineBundle, newerNginxBundle).
		Build()

	r := &bundle.Reconciler{Client: c}
	// Reconcile other-pipeline bundle — nginx bundle is for a different pipeline.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "other-pipeline-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	// Other pipeline bundle must remain Available (different pipeline — no self-supersession).
	var gotOther kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "other-pipeline-v1", Namespace: "default"}, &gotOther))
	assert.Equal(t, "Available", gotOther.Status.Phase)
}

// TestBundleReconciler_SelfSupersession_AlreadySupersededSkipped verifies that
// a bundle that is already Superseded is not touched again (idempotent).
func TestBundleReconciler_SelfSupersession_AlreadySupersededSkipped(t *testing.T) {
	alreadySuperseded := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-v0",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Superseded"},
	}
	// A new bundle (does not matter — superseded bundles reconcile to terminal state).
	newBundleObj := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-v2",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(alreadySuperseded, newBundleObj).
		WithStatusSubresource(alreadySuperseded, newBundleObj).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v0", Namespace: "default"},
	})
	require.NoError(t, err)

	// Still Superseded (evidence sync falls through — terminal state not re-patched).
	var gotOld kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v0", Namespace: "default"}, &gotOld))
	assert.Equal(t, "Superseded", gotOld.Status.Phase)
}

// TestStartupReconciliation_IsNoOp verifies that Start() is a no-op in the
// PRStatus CRD architecture. WaitingForMerge re-check is now handled by
// the PRStatusReconciler polling GitHub and the Graph Watch node propagating
// when status.merged == true. No SCM polling occurs at startup.
func TestStartupReconciliation_IsNoOp(t *testing.T) {
	ps := &kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-wfm", Namespace: "default"},
		Spec: kardinalv1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "prod",
			StepType:     "pr-review",
		},
		Status: kardinalv1alpha1.PromotionStepStatus{
			State: "WaitingForMerge",
			PRURL: "https://github.com/owner/repo/pull/42",
			Outputs: map[string]string{
				"prNumber": "42",
			},
		},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ps).
		WithStatusSubresource(ps).
		Build()

	// Start() is now a no-op — PRStatus CRD architecture handles polling.
	r := &bundle.Reconciler{Client: c}

	err := r.Start(context.Background())
	require.NoError(t, err)

	// State must be unchanged — Start() no longer mutates PromotionStep status.
	var updated kardinalv1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-wfm", Namespace: "default"}, &updated))
	assert.Equal(t, "WaitingForMerge", updated.Status.State,
		"Start() must not mutate PromotionStep state — PRStatusReconciler handles polling")
}

// TestStartupReconciliation_SkipsCompletedBundles verifies that PromotionSteps
// not in WaitingForMerge state are unaffected by startup.
func TestStartupReconciliation_SkipsCompletedBundles(t *testing.T) {
	// Step already succeeded — should NOT be touched.
	psSucceeded := &kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-done", Namespace: "default"},
		Status: kardinalv1alpha1.PromotionStepStatus{
			State:   "Succeeded",
			PRURL:   "https://github.com/owner/repo/pull/10",
			Outputs: map[string]string{"prNumber": "10"},
		},
	}
	// Step in HealthChecking — should NOT be touched.
	psHealthChecking := &kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-hc", Namespace: "default"},
		Status: kardinalv1alpha1.PromotionStepStatus{
			State:   "HealthChecking",
			PRURL:   "https://github.com/owner/repo/pull/11",
			Outputs: map[string]string{"prNumber": "11"},
		},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(psSucceeded, psHealthChecking).
		WithStatusSubresource(psSucceeded, psHealthChecking).
		Build()

	// Start() is a no-op; states must be unchanged.
	r := &bundle.Reconciler{Client: c}

	err := r.Start(context.Background())
	require.NoError(t, err)

	// States must be unchanged.
	var gotDone kardinalv1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-done", Namespace: "default"}, &gotDone))
	assert.Equal(t, "Succeeded", gotDone.Status.State)

	var gotHC kardinalv1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-hc", Namespace: "default"}, &gotHC))
	assert.Equal(t, "HealthChecking", gotHC.Status.State)
}

// TestStartupReconciliation_NoSCMProvider verifies that Start() is a no-op
// when SCMProvider is nil (preserved for backward compatibility).
func TestStartupReconciliation_NoSCMProvider(t *testing.T) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &bundle.Reconciler{Client: c} // no SCMProvider needed
	err := r.Start(context.Background())
	require.NoError(t, err)
}

// TestBundleReconciler_ConfigBundleDoesNotSupersedeImageBundle verifies that a
// config Bundle does NOT self-supersede when a newer image Bundle exists (different type).
func TestBundleReconciler_ConfigBundleDoesNotSupersedeImageBundle(t *testing.T) {
	// Config bundle that's older — should NOT be superseded by the newer image bundle.
	configBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-config-v1",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "config", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}
	// New image bundle (newer, but different type).
	imagBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-v2",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	sch := newScheme()
	nginxPipeline2 := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}},
	}
	c := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(nginxPipeline2, imagBundle, configBundle).
		WithStatusSubresource(imagBundle, configBundle).
		Build()

	r := &bundle.Reconciler{Client: c}
	// Reconcile the config bundle — the image bundle is a different type, no self-supersession.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-config-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	// Config bundle must remain Available (different type — no self-supersession).
	var gotConfig kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-config-v1", Namespace: "default"}, &gotConfig))
	assert.Equal(t, "Available", gotConfig.Status.Phase, "config bundle must not be superseded by image bundle (different type)")
}

// TestBundleReconciler_ConfigBundleSupersededByNewConfigBundle verifies that a
// config Bundle self-supersedes when a newer config Bundle for the same Pipeline exists.
func TestBundleReconciler_ConfigBundleSupersededByNewConfigBundle(t *testing.T) {
	oldConfig := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-config-v1",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "config", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}
	newConfig := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-config-v2",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "config", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	sch := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(oldConfig, newConfig).
		WithStatusSubresource(oldConfig, newConfig).
		Build()

	r := &bundle.Reconciler{Client: c}

	// Reconcile the OLD config bundle — it detects the newer sibling and self-supersedes.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-config-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	// Old config should be superseded (self-supersession).
	var gotOld kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-config-v1", Namespace: "default"}, &gotOld))
	assert.Equal(t, "Superseded", gotOld.Status.Phase)

	// New config should be Available.
	var gotNew kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-config-v2", Namespace: "default"}, &gotNew))
	assert.Equal(t, "Available", gotNew.Status.Phase)
}

// TestBundleReconciler_PausedPipelineNoLongerBlocksInReconciler verifies that after
// the PS-2/BU-2 fix, Pipeline.Spec.Paused is no longer enforced by the BundleReconciler.
// Pause enforcement is now done via the freeze PolicyGate (Graph-visible) created by
// `kardinal pause`. The reconciler allows the bundle to advance even if Spec.Paused=true.
func TestBundleReconciler_PausedPipelineNoLongerBlocksInReconciler(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Paused: true, // Note: reconciler no longer checks this field (PS-2 fix)
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
			},
		},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}
	translator := &mockTranslator{graphName: "graph-1"}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	r := &bundle.Reconciler{Client: c, Translator: translator}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	// After the PS-2/BU-2 fix: translator IS called even when Spec.Paused=true.
	// The Graph-level freeze gate (created by `kardinal pause`) enforces the pause.
	assert.True(t, translator.called, "translator must be called — Spec.Paused no longer blocks in reconciler (PS-2/BU-2 fix)")

	// Bundle advances to Promoting — the freeze gate in the Graph will block further progress.
	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"}, &got))
	assert.Equal(t, "Promoting", got.Status.Phase, "bundle advances to Promoting; freeze gate blocks further Graph progress")
}

// TestBundleReconciler_ResumedPipeline verifies that a non-paused Pipeline allows
// an Available Bundle to advance to Promoting.
func TestBundleReconciler_ResumedPipeline(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Paused: false,
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
			},
		},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}
	translator := &mockTranslator{graphName: "graph-1"}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	r := &bundle.Reconciler{Client: c, Translator: translator}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	// Translator MUST be called when pipeline is not paused.
	assert.True(t, translator.called, "translator must be called when pipeline is not paused")

	// Bundle must advance to Promoting.
	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"}, &got))
	assert.Equal(t, "Promoting", got.Status.Phase, "bundle must advance to Promoting when pipeline is not paused")
}

// TestBundleReconciler_SyncEvidenceFromPromotionStep verifies that when a Promoting
// Bundle is reconciled and a PromotionStep with the matching bundle label exists,
// the PromotionStep's status is copied into Bundle.status.environments.
// This replaces the cross-CRD write that was previously in the PromotionStep reconciler (PS-9).
func TestBundleReconciler_SyncEvidenceFromPromotionStep(t *testing.T) {
	s := newScheme()

	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}

	// PromotionStep for the "test" environment, Verified state.
	prURL := "https://github.com/test/repo/pull/42"
	ps := &kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "step-test",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/bundle": "nginx-demo-v1"},
		},
		Spec: kardinalv1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "nginx-demo-v1",
			Environment:  "test",
			StepType:     "auto",
		},
		Status: kardinalv1alpha1.PromotionStepStatus{
			State: "Verified",
			PRURL: prURL,
			Outputs: map[string]string{
				"prURL": prURL,
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(&kardinalv1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
			Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}},
		}, b, ps).
		WithStatusSubresource(b).
		Build()

	r := &bundle.Reconciler{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"}, &updated))

	require.Len(t, updated.Status.Environments, 1, "should have one environment status")
	env := updated.Status.Environments[0]
	assert.Equal(t, "test", env.Name)
	assert.Equal(t, "Verified", env.Phase)
	assert.Equal(t, prURL, env.PRURL)
	require.NotNil(t, env.HealthCheckedAt, "HealthCheckedAt must be set when state is Verified")
}

// TestBundleReconciler_SyncEvidence_Idempotent verifies that syncing evidence twice
// does not change the HealthCheckedAt timestamp (idempotent).
func TestBundleReconciler_SyncEvidence_Idempotent(t *testing.T) {
	s := newScheme()

	fixedTime := metav1.NewTime(time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC))
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{
			Phase: "Verified",
			Environments: []kardinalv1alpha1.EnvironmentStatus{
				{Name: "test", Phase: "Verified", PRURL: "https://github.com/test/repo/pull/42", HealthCheckedAt: &fixedTime},
			},
		},
	}
	ps := &kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "step-test",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/bundle": "nginx-demo-v1"},
		},
		Spec: kardinalv1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "nginx-demo-v1",
			Environment:  "test",
			StepType:     "auto",
		},
		Status: kardinalv1alpha1.PromotionStepStatus{
			State: "Verified",
			PRURL: "https://github.com/test/repo/pull/42",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(&kardinalv1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
			Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}},
		}, b, ps).
		WithStatusSubresource(b).
		Build()

	r := &bundle.Reconciler{Client: c}

	// Reconcile twice — HealthCheckedAt must remain the fixed value, not be updated.
	for i := 0; i < 2; i++ {
		_, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"},
		})
		require.NoError(t, err)
	}

	var updated kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"}, &updated))

	require.Len(t, updated.Status.Environments, 1)
	env := updated.Status.Environments[0]
	require.NotNil(t, env.HealthCheckedAt)
	assert.Equal(t, fixedTime.UTC(), env.HealthCheckedAt.UTC(),
		"HealthCheckedAt must not be overwritten on subsequent syncs")
}

// TestBundleReconciler_OrphanGuard_SelfDeletesWhenPipelineGone verifies that when the
// parent Pipeline no longer exists, the Bundle self-deletes to avoid orphaned resources (#270).
// This mirrors the PromotionStep orphan guard (#248).
func TestBundleReconciler_OrphanGuard_SelfDeletesWhenPipelineGone(t *testing.T) {
	// Bundle references a pipeline that does not exist.
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-bundle", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "deleted-pipeline",
		},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(b).
		WithStatusSubresource(b).
		Build()

	r := &bundle.Reconciler{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-bundle", Namespace: "default"},
	})
	require.NoError(t, err, "orphan guard must not return an error")

	// Bundle must have been deleted.
	var got kardinalv1alpha1.Bundle
	err = c.Get(context.Background(),
		types.NamespacedName{Name: "my-bundle", Namespace: "default"}, &got)
	require.Error(t, err, "orphaned Bundle must be self-deleted")
}

// TestBundleReconciler_OrphanGuard_NoDeleteWhenPipelineExists verifies that when the
// parent Pipeline exists, the reconciler proceeds normally.
func TestBundleReconciler_OrphanGuard_NoDeleteWhenPipelineExists(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pipeline", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-bundle", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "my-pipeline",
		},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	r := &bundle.Reconciler{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-bundle", Namespace: "default"},
	})
	require.NoError(t, err)

	// Bundle must still exist (no self-deletion).
	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "my-bundle", Namespace: "default"}, &got),
		"Bundle must not be deleted when Pipeline exists")
}

// TestBundleReconciler_PromotingBundleSupersededByNewerPromoting verifies that a
// bundle in Promoting phase is superseded when a newer bundle is also Promoting (#281).
func TestBundleReconciler_PromotingBundleSupersededByNewerPromoting(t *testing.T) {
	s := newScheme()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "prod"}}},
	}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newT := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	oldBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-old",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: old},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}
	newBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-new",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: newT},
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, oldBundle, newBundle).
		WithStatusSubresource(oldBundle, newBundle).
		Build()

	r := &bundle.Reconciler{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-old", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "nginx-demo-old", Namespace: "default"}, &updated))
	assert.Equal(t, "Superseded", updated.Status.Phase,
		"old Promoting bundle must be superseded when newer Promoting bundle exists")
}

// TestBundleReconciler_SameSecondSupersession verifies same-second tiebreaker (#289).
func TestBundleReconciler_SameSecondSupersession(t *testing.T) {
	s := newScheme()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "prod"}}},
	}
	sameTime := metav1.Time{Time: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	bundleAaa := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-aaa",
			Namespace:         "default",
			CreationTimestamp: sameTime,
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}
	bundleZzz := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-zzz",
			Namespace:         "default",
			CreationTimestamp: sameTime,
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, bundleAaa, bundleZzz).
		WithStatusSubresource(bundleAaa, bundleZzz).
		Build()

	r := &bundle.Reconciler{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-aaa", Namespace: "default"},
	})
	require.NoError(t, err)

	var aaa kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "nginx-demo-aaa", Namespace: "default"}, &aaa))
	assert.Equal(t, "Superseded", aaa.Status.Phase,
		"lexicographically smaller name superseded when timestamps equal")
}
