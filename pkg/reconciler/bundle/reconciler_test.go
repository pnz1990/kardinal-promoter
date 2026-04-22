// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	// RequeueAfter must be >= 500ms — guards against the hot-loop regression
	// where RequeueAfter was mistakenly set to time.Millisecond (1ms), bypassing
	// controller-runtime rate limiting and pressuring the API server/etcd under
	// concurrent Bundle load. See: docs/design/15-production-readiness.md Lens 7,
	// pkg/reconciler/bundle/reconciler.go handleNew.
	assert.GreaterOrEqual(t, result.RequeueAfter, 500*time.Millisecond,
		"handleNew RequeueAfter must be >= 500ms to avoid hot-loop regression")

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

// TestBundleReconciler_ConcurrentBundleSupersession verifies that when N bundles
// are created in rapid succession, all older bundles are superseded and only
// the newest one is promoted. This is the regression test for issue #405.
//
// Scenario: 5 bundles created ~1 second apart. Each bundle reconciles itself.
// Expected: bundles 1–4 become Superseded; bundle 5 becomes Available/Promoting.
func TestBundleReconciler_ConcurrentBundleSupersession(t *testing.T) {
	s := newScheme()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git:          kardinalv1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test", Approval: "auto"}},
		},
	}

	// Create 5 bundles with increasing timestamps to simulate rapid successive creation.
	baseTime := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	bundles := make([]*kardinalv1alpha1.Bundle, 5)
	for i := 0; i < 5; i++ {
		bundles[i] = &kardinalv1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{
				Name:              fmt.Sprintf("nginx-demo-v%d", i+1),
				Namespace:         "default",
				CreationTimestamp: metav1.Time{Time: baseTime.Add(time.Duration(i) * time.Second)},
			},
			Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
			Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
		}
	}

	builder := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline)
	statusSubresources := make([]client.Object, 5)
	for i, b := range bundles {
		builder = builder.WithObjects(b)
		statusSubresources[i] = b
	}
	c := builder.WithStatusSubresource(statusSubresources...).Build()

	r := &bundle.Reconciler{Client: c}

	// Reconcile all bundles. Each bundle should check for newer siblings
	// and supersede itself if a newer one exists.
	for i := 0; i < 5; i++ {
		_, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      fmt.Sprintf("nginx-demo-v%d", i+1),
				Namespace: "default",
			},
		})
		require.NoError(t, err, "bundle %d reconcile must not error", i+1)
	}

	// Check results: bundles 1–4 must be Superseded; bundle 5 must NOT be Superseded.
	for i := 0; i < 4; i++ {
		var b kardinalv1alpha1.Bundle
		require.NoError(t, c.Get(context.Background(),
			types.NamespacedName{Name: fmt.Sprintf("nginx-demo-v%d", i+1), Namespace: "default"}, &b))
		assert.Equal(t, "Superseded", b.Status.Phase,
			"bundle %d (v%d) must be Superseded — it has 1+ newer siblings", i+1, i+1)
	}

	// Bundle 5 (newest) must not be Superseded — it has no newer siblings.
	var newestBundle kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "nginx-demo-v5", Namespace: "default"}, &newestBundle))
	assert.NotEqual(t, "Superseded", newestBundle.Status.Phase,
		"newest bundle (v5) must NOT be Superseded — it is the active bundle")
	t.Logf("concurrent supersession: bundles 1–4 Superseded, bundle 5 phase=%s ✅", newestBundle.Status.Phase)
}

// ─── K-05: Deployment metrics ────────────────────────────────────────────────

// TestBundleReconciler_MetricsComputedOnVerified verifies that when a Bundle
// has all environments Verified, the reconciler computes Bundle.status.metrics
// including commitToProductionMinutes (K-05).
func TestBundleReconciler_MetricsComputedOnVerified(t *testing.T) {
	scheme := newScheme()

	// Bundle created 2 hours ago
	createdAt := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	verifiedAt := metav1.NewTime(time.Now().Add(-10 * time.Minute))

	myPipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod"},
			},
		},
	}
	myBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-app-v1",
			Namespace:         "default",
			CreationTimestamp: createdAt,
		},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "my-app",
		},
		Status: kardinalv1alpha1.BundleStatus{
			Phase: "Promoting",
			Environments: []kardinalv1alpha1.EnvironmentStatus{
				{Name: "test", Phase: "Verified", HealthCheckedAt: &verifiedAt},
				{Name: "prod", Phase: "Verified", HealthCheckedAt: &verifiedAt},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(myPipeline, myBundle).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}).
		Build()

	r := &bundle.Reconciler{Client: c}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))

	// commitToProductionMinutes must be set (>0 since bundle was created 2h ago)
	assert.NotNil(t, got.Status.Metrics, "metrics must be set when all environments are Verified")
	if got.Status.Metrics != nil {
		assert.Greater(t, got.Status.Metrics.CommitToProductionMinutes, int64(0),
			"commitToProductionMinutes must be >0 (bundle was created 2h ago)")
		assert.Less(t, got.Status.Metrics.CommitToProductionMinutes, int64(200),
			"commitToProductionMinutes must be < 200 (should be ~120-130m)")
	}
}

// --- GraphChecker mock ---

// mockGraphChecker is a test double for bundle.GraphChecker.
type mockGraphChecker struct {
	exists       bool
	err          error
	callCount    int
	deleteCalled bool
}

func (m *mockGraphChecker) GraphExists(_ context.Context, _, _ string) (bool, error) {
	m.callCount++
	return m.exists, m.err
}

func (m *mockGraphChecker) DeleteGraph(_ context.Context, _, _ string) error {
	m.deleteCalled = true
	return nil
}

// TestBundleReconciler_GraphRefStoredOnPromotion verifies that Bundle.status.graphRef
// is set when the bundle transitions to Promoting (fixes #490 prerequisite).
func TestBundleReconciler_GraphRefStoredOnPromotion(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	c := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	translator := &mockTranslator{graphName: "my-app-my-app-v1"}
	r := &bundle.Reconciler{Client: c, Translator: translator}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"},
	})
	require.NoError(t, err)
	require.True(t, translator.called, "translator must be called")

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "my-app-v1", Namespace: "default"}, &got))
	assert.Equal(t, "Promoting", got.Status.Phase)
	assert.Equal(t, "my-app-my-app-v1", got.Status.GraphRef,
		"GraphRef must be set in Bundle.status when advancing to Promoting")
}

// TestBundleReconciler_GraphRecreatedAfterDeletion verifies that when a Promoting
// Bundle's Graph is deleted externally, the reconciler detects this via GraphChecker
// and calls Translate to recreate it (#490).
func TestBundleReconciler_GraphRecreatedAfterDeletion(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
		Status: kardinalv1alpha1.BundleStatus{
			Phase:    "Promoting",
			GraphRef: "my-app-my-app-v1",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	// Graph does NOT exist — simulates manual kubectl delete graph.
	checker := &mockGraphChecker{exists: false}
	translator := &mockTranslator{graphName: "my-app-my-app-v1"}
	r := &bundle.Reconciler{Client: c, Translator: translator, GraphChecker: checker}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	assert.Equal(t, 1, checker.callCount, "GraphChecker.GraphExists must be called once")
	assert.True(t, translator.called,
		"Translator.Translate must be called when graph is missing")
}

// TestBundleReconciler_GraphNotRecreatedWhenPresent verifies that when the Graph
// exists, the translator is NOT called again (idempotency).
func TestBundleReconciler_GraphNotRecreatedWhenPresent(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
		Status: kardinalv1alpha1.BundleStatus{
			Phase:    "Promoting",
			GraphRef: "my-app-my-app-v1",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	// Graph EXISTS — normal operating condition.
	checker := &mockGraphChecker{exists: true}
	translator := &mockTranslator{graphName: "my-app-my-app-v1"}
	r := &bundle.Reconciler{Client: c, Translator: translator, GraphChecker: checker}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	assert.Equal(t, 1, checker.callCount, "GraphChecker must be called once")
	assert.False(t, translator.called,
		"Translator must NOT be called when graph already exists")
}

// TestBundleReconciler_GraphCheckerErrorIsNonFatal verifies that a GraphChecker
// error does not fail the reconcile — evidence sync still proceeds.
func TestBundleReconciler_GraphCheckerErrorIsNonFatal(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
		Status: kardinalv1alpha1.BundleStatus{
			Phase:    "Promoting",
			GraphRef: "my-app-my-app-v1",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	// GraphChecker returns an error — must not propagate.
	checker := &mockGraphChecker{exists: false, err: fmt.Errorf("connection refused")}
	r := &bundle.Reconciler{Client: c, GraphChecker: checker}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"},
	})
	// Error should NOT propagate — GraphChecker failures are non-fatal.
	require.NoError(t, err,
		"GraphChecker error must be non-fatal — reconcile should not return error")
}

// --- Pipeline spec change detection tests (#626) ---

// mockGraphCheckerV2 extends the GraphChecker interface with DeleteGraph support.
// Used to test pipeline spec change detection.
type mockGraphCheckerV2 struct {
	exists      bool
	existsErr   error
	existsCount int
	deleteCount int
	deleteErr   error
}

func (m *mockGraphCheckerV2) GraphExists(_ context.Context, _, _ string) (bool, error) {
	m.existsCount++
	return m.exists, m.existsErr
}

func (m *mockGraphCheckerV2) DeleteGraph(_ context.Context, _, _ string) error {
	m.deleteCount++
	return m.deleteErr
}

// TestBundleReconciler_PipelineSpecChange_DeletesGraph verifies that when a Pipeline
// spec changes (different from the stored PipelineSpecHash), the reconciler deletes
// the existing Graph so it is regenerated with the updated spec (#626).
func TestBundleReconciler_PipelineSpecChange_DeletesGraph(t *testing.T) {
	scheme := newScheme()

	// Pipeline with NEW spec (two environments)
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod"},
			},
		},
	}

	// Bundle that was created with OLD spec (one environment)
	// The stored hash will differ from the current pipeline spec hash.
	bndl := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{
			Pipeline: "my-app",
			Type:     "image",
		},
		Status: kardinalv1alpha1.BundleStatus{
			Phase:            "Promoting",
			GraphRef:         "my-app-my-app-v1",
			PipelineSpecHash: "stale-hash-from-old-spec", // deliberately stale
		},
	}

	translator := &mockTranslator{graphName: "my-app-my-app-v1"}
	checker := &mockGraphCheckerV2{exists: true} // graph exists but spec is stale

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pipeline, bndl).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}).
		Build()

	r := &bundle.Reconciler{
		Client:       c,
		Translator:   translator,
		GraphChecker: checker,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	// Graph must have been deleted
	assert.Equal(t, 1, checker.deleteCount,
		"Graph must be deleted when Pipeline spec hash changes")

	// Bundle status must have updated hash
	var updated kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "my-app-v1", Namespace: "default"}, &updated))
	assert.NotEqual(t, "stale-hash-from-old-spec", updated.Status.PipelineSpecHash,
		"PipelineSpecHash must be updated to current spec hash")
	assert.NotEmpty(t, updated.Status.PipelineSpecHash,
		"PipelineSpecHash must not be empty after update")
}

// TestBundleReconciler_PipelineSpecUnchanged_NoDelete verifies that when the Pipeline
// spec hash matches the stored hash, no Graph deletion occurs.
func TestBundleReconciler_PipelineSpecUnchanged_NoDelete(t *testing.T) {
	scheme := newScheme()

	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}

	// Store the correct hash for the current Pipeline spec
	specBytes, _ := json.Marshal(pipeline.Spec)
	hashSum := sha256.Sum256(specBytes)
	currentHash := hex.EncodeToString(hashSum[:])

	bndl := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{
			Pipeline: "my-app",
			Type:     "image",
		},
		Status: kardinalv1alpha1.BundleStatus{
			Phase:            "Promoting",
			GraphRef:         "my-app-my-app-v1",
			PipelineSpecHash: currentHash, // already up to date
		},
	}

	checker := &mockGraphCheckerV2{exists: true}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pipeline, bndl).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}).
		Build()

	r := &bundle.Reconciler{
		Client:       c,
		GraphChecker: checker,
		Translator:   &mockTranslator{graphName: "my-app-my-app-v1"},
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	assert.Equal(t, 0, checker.deleteCount,
		"Graph must NOT be deleted when Pipeline spec is unchanged")
}

// TestBundleReconciler_PipelineSpecHashStoredOnPromotion verifies that when a Bundle
// transitions from Available to Promoting, the PipelineSpecHash is stored (#626).
func TestBundleReconciler_PipelineSpecHashStoredOnPromotion(t *testing.T) {
	scheme := newScheme()

	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}

	bndl := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{
			Pipeline: "my-app",
			Type:     "image",
		},
		Status: kardinalv1alpha1.BundleStatus{
			Phase: "Available", // will transition to Promoting
		},
	}

	translator := &mockTranslator{graphName: "my-app-my-app-v1"}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pipeline, bndl).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}).
		Build()

	r := &bundle.Reconciler{
		Client:     c,
		Translator: translator,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "my-app-v1", Namespace: "default"}, &updated))

	assert.Equal(t, "Promoting", updated.Status.Phase)
	assert.NotEmpty(t, updated.Status.PipelineSpecHash,
		"PipelineSpecHash must be stored when bundle transitions to Promoting")
}

// TestBundleReconciler_EmptyPipelineSpecHash_NoGraphDeletion verifies that
// ensurePipelineSpecCurrent does NOT delete the Graph when PipelineSpecHash is
// empty (uninitialised). An empty stored hash must be treated as "not yet
// observed", not "spec has changed". Regression guard for #789.
func TestBundleReconciler_EmptyPipelineSpecHash_NoGraphDeletion(t *testing.T) {
	scheme := newScheme()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	// Bundle is Promoting with an empty PipelineSpecHash — simulates a bundle
	// that was promoted before PipelineSpecHash was introduced (pre-#634).
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
		Status: kardinalv1alpha1.BundleStatus{
			Phase:            "Promoting",
			GraphRef:         "my-app-my-app-v1",
			PipelineSpecHash: "", // empty — key test condition
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	gc := &mockGraphChecker{exists: true}
	translator := &mockTranslator{graphName: "my-app-my-app-v1"}
	r := &bundle.Reconciler{
		Client:       c,
		Translator:   translator,
		GraphChecker: gc,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	// Graph must NOT be deleted when PipelineSpecHash is empty.
	assert.False(t, gc.deleteCalled,
		"Graph must NOT be deleted when PipelineSpecHash is empty — empty means uninitialized, not changed (#789)")

	// The stored hash MUST be updated so subsequent reconciles don't trigger deletion.
	var updated kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "my-app-v1", Namespace: "default"}, &updated))
	assert.NotEmpty(t, updated.Status.PipelineSpecHash,
		"PipelineSpecHash must be saved after first reconcile to prevent future spurious deletions")
}

// TestBundleReconciler_HistoryGC_DeletesOldestTerminal verifies that when a new Bundle
// is created and there are more terminal Bundles than historyLimit, the oldest terminal
// Bundles are deleted first (spec #910 O1, O3).
func TestBundleReconciler_HistoryGC_DeletesOldestTerminal(t *testing.T) {
	const limit = 3
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			HistoryLimit: limit,
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}

	// Create 4 terminal bundles — one more than the limit.
	// oldest → newest: v1, v2, v3, v4. v1 should be deleted.
	bundles := []*kardinalv1alpha1.Bundle{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-app-v1",
				Namespace:         "default",
				CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			},
			Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
			Status: kardinalv1alpha1.BundleStatus{Phase: "Verified"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-app-v2",
				Namespace:         "default",
				CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)),
			},
			Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
			Status: kardinalv1alpha1.BundleStatus{Phase: "Superseded"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-app-v3",
				Namespace:         "default",
				CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)),
			},
			Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
			Status: kardinalv1alpha1.BundleStatus{Phase: "Failed"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-app-v4",
				Namespace:         "default",
				CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)),
			},
			Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
			Status: kardinalv1alpha1.BundleStatus{Phase: "Verified"},
		},
	}

	// The new bundle (v5) — no phase yet — triggers GC.
	newBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-app-v5",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)),
		},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
	}

	s := newScheme()
	objs := []client.Object{pipeline, newBundle}
	for _, b := range bundles {
		objs = append(objs, b)
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(newBundle).
		WithIndex(&kardinalv1alpha1.Bundle{}, "spec.pipeline", func(obj client.Object) []string {
			b, ok := obj.(*kardinalv1alpha1.Bundle)
			if !ok || b.Spec.Pipeline == "" {
				return nil
			}
			return []string{b.Spec.Pipeline}
		}).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-v5", Namespace: "default"},
	})
	require.NoError(t, err)

	// v1 (oldest) must be deleted.
	var v1 kardinalv1alpha1.Bundle
	err = c.Get(context.Background(), types.NamespacedName{Name: "my-app-v1", Namespace: "default"}, &v1)
	assert.True(t, apierrors.IsNotFound(err), "oldest terminal bundle (v1) must have been deleted by GC")

	// v2, v3, v4 must still exist (they are within the limit after deleting v1).
	for _, name := range []string{"my-app-v2", "my-app-v3", "my-app-v4"} {
		var b kardinalv1alpha1.Bundle
		require.NoError(t, c.Get(context.Background(),
			types.NamespacedName{Name: name, Namespace: "default"}, &b),
			"bundle %s must still exist", name)
	}
}

// TestBundleReconciler_HistoryGC_DefaultLimit verifies that when Pipeline.spec.historyLimit
// is unset (zero), the default limit of 50 is applied (spec #910 O2).
func TestBundleReconciler_HistoryGC_DefaultLimit(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			// HistoryLimit intentionally unset (zero value → use default 50)
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}

	// Create 51 terminal bundles — one more than the default limit of 50.
	objs := []client.Object{pipeline}
	statusObjs := []client.Object{}
	for i := range 51 {
		b := &kardinalv1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{
				Name:              fmt.Sprintf("my-app-old-%03d", i),
				Namespace:         "default",
				CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC)),
			},
			Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
			Status: kardinalv1alpha1.BundleStatus{Phase: "Verified"},
		}
		objs = append(objs, b)
		statusObjs = append(statusObjs, b)
	}
	// The new bundle that triggers GC.
	newBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-app-new",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
		},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
	}
	objs = append(objs, newBundle)
	statusObjs = append(statusObjs, newBundle)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(statusObjs...).
		WithIndex(&kardinalv1alpha1.Bundle{}, "spec.pipeline", func(obj client.Object) []string {
			b, ok := obj.(*kardinalv1alpha1.Bundle)
			if !ok || b.Spec.Pipeline == "" {
				return nil
			}
			return []string{b.Spec.Pipeline}
		}).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-new", Namespace: "default"},
	})
	require.NoError(t, err)

	// After GC: the oldest bundle (my-app-old-000) must be deleted.
	var oldest kardinalv1alpha1.Bundle
	err = c.Get(context.Background(),
		types.NamespacedName{Name: "my-app-old-000", Namespace: "default"}, &oldest)
	assert.True(t, apierrors.IsNotFound(err),
		"oldest terminal bundle must be deleted when historyLimit=50 (default) and 51 exist")

	// Exactly 50 old bundles should remain (my-app-old-001 through my-app-old-050).
	var remaining kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &remaining,
		client.InNamespace("default"),
		client.MatchingFields{"spec.pipeline": "my-app"},
	))
	// 50 terminal + 1 new (Available) = 51 total remaining
	assert.LessOrEqual(t, len(remaining.Items), 51,
		"total bundles must be at most 51 (50 terminal + 1 new Available)")
}

// TestBundleReconciler_HistoryGC_NonTerminalNotDeleted verifies that non-terminal
// Bundles (Available, Promoting) are never deleted by history GC (spec #910 O4).
func TestBundleReconciler_HistoryGC_NonTerminalNotDeleted(t *testing.T) {
	const limit = 1
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			HistoryLimit: limit,
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}

	// One terminal bundle (Verified) and one non-terminal (Promoting).
	// With historyLimit=1, after adding the new bundle we'd have 1 terminal — no GC needed.
	// But a Promoting bundle must never be deleted.
	terminalBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-app-old",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Verified"},
	}
	promotingBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-app-promoting",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)),
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}
	// New bundle with historyLimit=1 — after GC there should be exactly 1 terminal.
	// The terminal bundle (my-app-old) is at the limit, no excess to delete.
	newBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-app-new",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)),
		},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, terminalBundle, promotingBundle, newBundle).
		WithStatusSubresource(newBundle).
		WithIndex(&kardinalv1alpha1.Bundle{}, "spec.pipeline", func(obj client.Object) []string {
			b, ok := obj.(*kardinalv1alpha1.Bundle)
			if !ok || b.Spec.Pipeline == "" {
				return nil
			}
			return []string{b.Spec.Pipeline}
		}).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-app-new", Namespace: "default"},
	})
	require.NoError(t, err)

	// The Promoting bundle must never be deleted, regardless of historyLimit.
	var promoting kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "my-app-promoting", Namespace: "default"}, &promoting),
		"Promoting bundle must NOT be deleted by history GC")

	// The terminal bundle is at the limit (1 terminal = 1 allowed) — must not be deleted either.
	var terminal kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "my-app-old", Namespace: "default"}, &terminal),
		"terminal bundle within limit must not be deleted")
}

// findCondition returns the condition with the given type from a slice, or nil.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// TestBundleConditions_Available verifies that a newly created Bundle has
// Ready=False/Available set after the first reconcile.
func TestBundleConditions_Available(t *testing.T) {
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "cond-test", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "test-pipe"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(b).
		WithStatusSubresource(b).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cond-test", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cond-test", Namespace: "default"}, &got))

	assert.Equal(t, "Available", got.Status.Phase)
	ready := findCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready, "Ready condition must be present after Available phase")
	assert.Equal(t, metav1.ConditionFalse, ready.Status, "Ready must be False when Available")
	assert.Equal(t, "Available", ready.Reason, "Ready.Reason must be Available")
}

// TestBundleConditions_Promoting verifies that an Available Bundle has
// Ready=False/Promoting after the translator advances it to Promoting.
func TestBundleConditions_Promoting(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pipe", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "cond-prom", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "test-pipe"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	r := &bundle.Reconciler{Client: c, Translator: &mockTranslator{graphName: "g"}}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cond-prom", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cond-prom", Namespace: "default"}, &got))

	assert.Equal(t, "Promoting", got.Status.Phase)
	ready := findCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready, "Ready condition must be present after Promoting phase")
	assert.Equal(t, metav1.ConditionFalse, ready.Status, "Ready must be False when Promoting")
	assert.Equal(t, "Promoting", ready.Reason, "Ready.Reason must be Promoting")
}

// TestBundleConditions_Failed verifies that a Bundle has Ready=False/Failed and
// Failed=True when translation fails.
func TestBundleConditions_Failed(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pipe", Namespace: "default"},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "cond-fail", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "test-pipe"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	r := &bundle.Reconciler{Client: c, Translator: &mockTranslator{err: fmt.Errorf("simulated translation failure")}}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cond-fail", Namespace: "default"},
	})
	// Reconcile returns the translation error.
	assert.Error(t, err)

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cond-fail", Namespace: "default"}, &got))

	assert.Equal(t, "Failed", got.Status.Phase)

	ready := findCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready, "Ready condition must be present after Failed phase")
	assert.Equal(t, metav1.ConditionFalse, ready.Status, "Ready must be False when Failed")
	assert.Equal(t, "Failed", ready.Reason)

	failed := findCondition(got.Status.Conditions, "Failed")
	require.NotNil(t, failed, "Failed condition must be present after translation error")
	assert.Equal(t, metav1.ConditionTrue, failed.Status)
	assert.Equal(t, "TranslationError", failed.Reason)
}

// TestBundleConditions_Superseded verifies that Ready=False/Superseded is set
// when a Bundle is self-superseded.
func TestBundleConditions_Superseded(t *testing.T) {
	// newer bundle exists for the same pipeline+type
	older := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cond-super-old",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "test-pipe"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}
	newer := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cond-super-new",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now()),
		},
		Spec:   kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "test-pipe"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(older, newer).
		WithStatusSubresource(older, newer).
		WithIndex(&kardinalv1alpha1.Bundle{}, "spec.pipeline", func(o client.Object) []string {
			b := o.(*kardinalv1alpha1.Bundle)
			if b.Spec.Pipeline != "" {
				return []string{b.Spec.Pipeline}
			}
			return nil
		}).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cond-super-old", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cond-super-old", Namespace: "default"}, &got))

	assert.Equal(t, "Superseded", got.Status.Phase)
	ready := findCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready, "Ready condition must be present after Superseded phase")
	assert.Equal(t, metav1.ConditionFalse, ready.Status, "Ready must be False when Superseded")
	assert.Equal(t, "Superseded", ready.Reason)
}

// TestBundleConditions_NoDuplicates verifies that reconciling the same Bundle
// multiple times does not create duplicate condition entries.
func TestBundleConditions_NoDuplicates(t *testing.T) {
	// Start from Available to avoid the requeue path (which creates a new status object).
	// Include the Pipeline so the orphan guard doesn't delete the Bundle.
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pipe", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "cond-dedup", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "test-pipe"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b).
		WithStatusSubresource(b).
		Build()

	// Reconciler with a translator that advances Available → Promoting on first reconcile.
	r := &bundle.Reconciler{Client: c, Translator: &mockTranslator{graphName: "g"}}

	// First reconcile: Available → Promoting (sets Ready=False/Promoting).
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cond-dedup", Namespace: "default"},
	})
	require.NoError(t, err)

	// Second reconcile on the same bundle (now Promoting): handles sync evidence.
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cond-dedup", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cond-dedup", Namespace: "default"}, &got))

	// Count how many Ready conditions exist — must be exactly one (no duplicates).
	readyCount := 0
	for _, cond := range got.Status.Conditions {
		if cond.Type == "Ready" {
			readyCount++
		}
	}
	assert.Equal(t, 1, readyCount, "exactly one Ready condition must exist after multiple reconciles")
}

// TestBundleReconciler_MaxConcurrentPromotions_CapEnforced verifies that when
// maxConcurrentPromotions is set on the Pipeline and the cap is already reached,
// an Available Bundle is requeued rather than advanced to Promoting.
func TestBundleReconciler_MaxConcurrentPromotions_CapEnforced(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments:            []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
			MaxConcurrentPromotions: 1, // cap = 1
		},
	}
	// First bundle is already Promoting — this fills the cap.
	b1 := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}
	// Second bundle is Available — it should be requeued (cap reached).
	b2 := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v2", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b1, b2).
		WithStatusSubresource(b1, b2).
		Build()

	translator := &mockTranslator{graphName: "nginx-demo-v2-graph"}
	r := &bundle.Reconciler{Client: c, Translator: translator}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v2", Namespace: "default"},
	})

	require.NoError(t, err)
	assert.False(t, translator.called, "Translator.Translate must NOT be called when cap is reached")
	assert.Equal(t, 30*time.Second, result.RequeueAfter, "bundle must be requeued with 30s delay")

	// Verify the bundle stays in Available phase (not advanced to Promoting).
	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "nginx-demo-v2", Namespace: "default",
	}, &got))
	assert.Equal(t, "Available", got.Status.Phase, "bundle must remain Available when cap is reached")
}

// TestBundleReconciler_MaxConcurrentPromotions_ZeroIsUnlimited verifies that
// maxConcurrentPromotions=0 (the default) does not block any promotion.
func TestBundleReconciler_MaxConcurrentPromotions_ZeroIsUnlimited(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments:            []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
			MaxConcurrentPromotions: 0, // 0 = unlimited
		},
	}
	// Multiple bundles already Promoting.
	for i := 1; i <= 5; i++ {
		_ = &kardinalv1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("nginx-demo-v%d", i), Namespace: "default"},
			Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
			Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
		}
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v6", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}
	// Include 5 Promoting siblings + our Available bundle.
	promotingSiblings := make([]kardinalv1alpha1.Bundle, 5)
	for i := range promotingSiblings {
		promotingSiblings[i] = kardinalv1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("nginx-demo-v%d", i+1), Namespace: "default"},
			Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
			Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
		}
	}

	s := newScheme()
	builder := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b).
		WithStatusSubresource(b)
	for i := range promotingSiblings {
		builder = builder.WithObjects(&promotingSiblings[i]).WithStatusSubresource(&promotingSiblings[i])
	}
	c := builder.Build()

	translator := &mockTranslator{graphName: "nginx-demo-v6-graph"}
	r := &bundle.Reconciler{Client: c, Translator: translator}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v6", Namespace: "default"},
	})

	require.NoError(t, err)
	assert.True(t, translator.called, "Translator must be called when cap is 0 (unlimited)")
	assert.Zero(t, result.RequeueAfter, "no requeue delay when cap is 0")
}

// TestBundleReconciler_MaxConcurrentPromotions_CapNotReached verifies that when
// cap is set but not yet reached, promotion proceeds normally.
func TestBundleReconciler_MaxConcurrentPromotions_CapNotReached(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments:            []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
			MaxConcurrentPromotions: 2, // cap = 2; only 1 Promoting exists → allow
		},
	}
	b1 := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}
	b2 := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v2", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, b1, b2).
		WithStatusSubresource(b1, b2).
		Build()

	translator := &mockTranslator{graphName: "nginx-demo-v2-graph"}
	r := &bundle.Reconciler{Client: c, Translator: translator}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-v2", Namespace: "default"},
	})

	require.NoError(t, err)
	assert.True(t, translator.called, "Translator must be called when cap is not yet reached")
	assert.Zero(t, result.RequeueAfter, "no requeue delay when cap is not reached")

	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "nginx-demo-v2", Namespace: "default",
	}, &got))
	assert.Equal(t, "Promoting", got.Status.Phase)
}
