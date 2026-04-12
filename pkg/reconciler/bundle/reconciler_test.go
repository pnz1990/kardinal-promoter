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
// new config Bundle does NOT supersede an in-flight image Bundle for the same Pipeline.
func TestBundleReconciler_ConfigBundleDoesNotSupersedeImageBundle(t *testing.T) {
	// Image bundle actively promoting.
	imagBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}
	// New config bundle.
	configBundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-config-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "config", Pipeline: "nginx-demo"},
	}

	sch := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(imagBundle, configBundle).
		WithStatusSubresource(imagBundle, configBundle).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-config-v1", Namespace: "default"},
	})
	require.NoError(t, err)

	// Image bundle must remain Promoting (different type — not superseded).
	var gotImage kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"}, &gotImage))
	assert.Equal(t, "Promoting", gotImage.Status.Phase, "image bundle must not be superseded by config bundle")

	// Config bundle should be Available.
	var gotConfig kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-config-v1", Namespace: "default"}, &gotConfig))
	assert.Equal(t, "Available", gotConfig.Status.Phase)
}

// TestBundleReconciler_ConfigBundleSupersededByNewConfigBundle verifies that a
// new config Bundle does supersede an older config Bundle for the same Pipeline.
func TestBundleReconciler_ConfigBundleSupersededByNewConfigBundle(t *testing.T) {
	oldConfig := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-config-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "config", Pipeline: "nginx-demo"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
	}
	newConfig := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-config-v2", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "config", Pipeline: "nginx-demo"},
	}

	sch := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(oldConfig, newConfig).
		WithStatusSubresource(oldConfig, newConfig).
		Build()

	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo-config-v2", Namespace: "default"},
	})
	require.NoError(t, err)

	// Old config should be superseded.
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
		WithObjects(b, ps).
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
		WithObjects(b, ps).
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
