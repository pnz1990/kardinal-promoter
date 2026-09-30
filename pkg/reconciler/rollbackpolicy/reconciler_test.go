// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rollbackpolicy_test

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

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/rollbackpolicy"
)

// fixedNow is the reference time used throughout tests.
var fixedNow = time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)

// buildScheme creates a scheme with kardinal types registered.
func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// makeRollbackPolicy creates a RollbackPolicy for testing.
func makeRollbackPolicy(name, pipelineName, environment, bundleRef string, threshold int) *v1alpha1.RollbackPolicy {
	return &v1alpha1.RollbackPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    pipelineName,
				"kardinal.io/environment": environment,
			},
		},
		Spec: v1alpha1.RollbackPolicySpec{
			PipelineName:     pipelineName,
			Environment:      environment,
			BundleRef:        bundleRef,
			FailureThreshold: threshold,
		},
	}
}

// makePromotionStep creates a PromotionStep of bundle-1 with the given health failure count.
func makePromotionStep(name, pipelineName, environment string, failures int) *v1alpha1.PromotionStep {
	return makeBundleStep(name, pipelineName, environment, "bundle-1", failures)
}

// makeBundleStep creates a PromotionStep for the given Bundle, labelled the way
// the Graph builder labels it (pipeline, bundle, environment).
func makeBundleStep(name, pipelineName, environment, bundleName string, failures int) *v1alpha1.PromotionStep {
	return &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    pipelineName,
				"kardinal.io/environment": environment,
				"kardinal.io/bundle":      bundleName,
			},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: pipelineName,
			BundleName:   bundleName,
			Environment:  environment,
			StepType:     "health-check",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:                     "HealthChecking",
			ConsecutiveHealthFailures: failures,
		},
	}
}

// unlabelledBundleStep removes the kardinal.io/bundle label, as on a step
// created outside the Graph, leaving spec.bundleName as the only reference.
func unlabelledBundleStep(st *v1alpha1.PromotionStep) *v1alpha1.PromotionStep {
	delete(st.Labels, "kardinal.io/bundle")
	return st
}

// makeBundle creates a Bundle for testing.
func makeBundle(name, pipeline string) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline": pipeline,
			},
		},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: pipeline,
			Images: []v1alpha1.ImageRef{
				{Repository: "nginx", Tag: "1.25.0"},
			},
		},
	}
}

func reconcileOnce(t *testing.T, objs ...interface{ DeepCopyObject() runtime.Object }) (*v1alpha1.RollbackPolicy, ctrl.Result, error) {
	t.Helper()
	s := buildScheme(t)
	builder := fake.NewClientBuilder().WithScheme(s)
	for _, o := range objs {
		switch obj := o.(type) {
		case *v1alpha1.RollbackPolicy:
			builder = builder.WithObjects(obj).WithStatusSubresource(obj)
		case *v1alpha1.PromotionStep:
			builder = builder.WithObjects(obj).WithStatusSubresource(obj)
		case *v1alpha1.Bundle:
			builder = builder.WithObjects(obj).WithStatusSubresource(obj)
		}
	}
	c := builder.Build()

	r := &rollbackpolicy.Reconciler{
		Client: c,
		NowFn:  func() time.Time { return fixedNow },
	}

	// Find the RollbackPolicy to reconcile
	var rp *v1alpha1.RollbackPolicy
	for _, o := range objs {
		if p, ok := o.(*v1alpha1.RollbackPolicy); ok {
			rp = p
			break
		}
	}
	require.NotNil(t, rp, "must include a RollbackPolicy in test objects")

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: rp.Name, Namespace: rp.Namespace}}
	result, err := r.Reconcile(context.Background(), req)

	var updated v1alpha1.RollbackPolicy
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &updated))
	return &updated, result, err
}

// --- Test: below threshold — shouldRollback stays false ---

func TestReconciler_BelowThreshold_NoRollback(t *testing.T) {
	rp := makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3)
	step := makePromotionStep("step-1", "nginx-demo", "prod", 2) // 2 failures < 3 threshold
	bundle := makeBundle("bundle-1", "nginx-demo")

	updated, _, err := reconcileOnce(t, rp, step, bundle)
	require.NoError(t, err)

	assert.Equal(t, 2, updated.Status.ConsecutiveFailures)
	assert.False(t, updated.Status.ShouldRollback, "should NOT trigger rollback below threshold")
	assert.Nil(t, updated.Status.RollbackBundleName, "no rollback bundle created")
	assert.NotNil(t, updated.Status.LastEvaluatedAt, "lastEvaluatedAt must be set")
}

// --- Test: at threshold — shouldRollback becomes true and Bundle created ---

func TestReconciler_AtThreshold_TriggersRollback(t *testing.T) {
	rp := makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3)
	step := makePromotionStep("step-1", "nginx-demo", "prod", 3) // 3 failures >= 3 threshold
	bundle := rtBundle("bundle-1", "1.25.0", 30)
	// bundle-0 was Verified in prod before bundle-1: the rollback target.
	good, goodStep := rtBundle("bundle-0", "1.24.0", 0), rtVerifiedStep("bundle-0", 5)

	s := buildScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(rp, step, bundle, rtPipeline(), good, goodStep).
		WithStatusSubresource(rp, step, bundle).
		Build()

	r := &rollbackpolicy.Reconciler{
		Client: c,
		NowFn:  func() time.Time { return fixedNow },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-1", Namespace: "default"}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var updated v1alpha1.RollbackPolicy
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &updated))

	assert.Equal(t, 3, updated.Status.ConsecutiveFailures)
	assert.True(t, updated.Status.ShouldRollback, "should trigger rollback at threshold")
	require.NotNil(t, updated.Status.RollbackBundleName, "rollback bundle name must be set")
	assert.NotEmpty(t, *updated.Status.RollbackBundleName, "rollback bundle name must not be empty")

	// Verify rollback Bundle was created
	var bundleList v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	var rollbackFound bool
	for _, b := range bundleList.Items {
		if b.Labels["kardinal.io/rollback"] == "true" {
			rollbackFound = true
			assert.Equal(t, "bundle-0", b.Spec.Provenance.RollbackOf,
				"rollback bundle must reference the bundle it restores")
			assert.Equal(t, "bundle-1", b.Annotations["kardinal.io/rollback-from"],
				"rollback bundle must reference the failing bundle")
		}
	}
	assert.True(t, rollbackFound, "rollback Bundle must be created")
}

// --- Test: idempotent — reconcile twice when already triggered is a no-op ---

func TestReconciler_AlreadyTriggered_IsNoOp(t *testing.T) {
	rollbackBundleName := "nginx-demo-rollback-12345"
	rp := makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3)
	rp.Status.ShouldRollback = true
	rp.Status.ConsecutiveFailures = 3
	now := metav1.NewTime(fixedNow)
	rp.Status.LastEvaluatedAt = &now
	rp.Status.RollbackBundleName = &rollbackBundleName

	step := makePromotionStep("step-1", "nginx-demo", "prod", 5) // More failures, but already triggered
	bundle := makeBundle("bundle-1", "nginx-demo")
	// Pre-existing rollback bundle
	existingRB := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rollbackBundleName,
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/rollback": "true"},
		},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
			Provenance: &v1alpha1.BundleProvenance{
				RollbackOf: "bundle-1",
			},
		},
	}

	s := buildScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(rp, step, bundle, existingRB).
		WithStatusSubresource(rp, step, bundle, existingRB).
		Build()

	r := &rollbackpolicy.Reconciler{
		Client: c,
		NowFn:  func() time.Time { return fixedNow },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-1", Namespace: "default"}}

	// First reconcile — should be no-op
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	// Count bundles — should be 2 (original + existing rollback), not 3
	var bundleList v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	assert.Len(t, bundleList.Items, 2, "no new rollback bundle should be created when already triggered")
}

// --- Test: no PromotionStep found — requeue gracefully ---

func TestReconciler_NoPromotionStep_RequeuesGracefully(t *testing.T) {
	rp := makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3)
	bundle := makeBundle("bundle-1", "nginx-demo")
	// No PromotionStep created

	s := buildScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(rp, bundle).
		WithStatusSubresource(rp, bundle).
		Build()

	r := &rollbackpolicy.Reconciler{
		Client: c,
		NowFn:  func() time.Time { return fixedNow },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-1", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter.Milliseconds(), int64(0), "should requeue when no PromotionStep found")
}

// --- Test: not-found RollbackPolicy is a no-op ---

func TestReconciler_NotFound_NoOp(t *testing.T) {
	s := buildScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &rollbackpolicy.Reconciler{
		Client: c,
		NowFn:  func() time.Time { return fixedNow },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "missing", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// --- Test: default threshold of 3 when spec.failureThreshold <= 0 ---

func TestReconciler_DefaultThreshold(t *testing.T) {
	rp := makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 0) // 0 = use default (3)
	step := makePromotionStep("step-1", "nginx-demo", "prod", 3)          // 3 failures = default threshold
	bundle := makeBundle("bundle-1", "nginx-demo")

	s := buildScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(rp, step, bundle).
		WithStatusSubresource(rp, step, bundle).
		Build()

	r := &rollbackpolicy.Reconciler{
		Client: c,
		NowFn:  func() time.Time { return fixedNow },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-1", Namespace: "default"}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var updated v1alpha1.RollbackPolicy
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &updated))
	assert.True(t, updated.Status.ShouldRollback, "default threshold of 3 should trigger at 3 failures")
}

// TestReconciler_ReadsStepsOfBundleRef verifies that the policy reads only the
// PromotionSteps of spec.bundleRef (C04-gates-05). Before the fix it used
// Items[0] of every step in the pipeline and environment, so an older Bundle's
// failing step could roll back a healthy Bundle, or hide a failing one.
func TestReconciler_ReadsStepsOfBundleRef(t *testing.T) {
	tests := []struct {
		name         string
		steps        []*v1alpha1.PromotionStep
		wantFailures int
		wantRollback bool
		wantRequeue  bool
	}{
		{
			name: "old bundle failing, bundleRef healthy: no rollback",
			steps: []*v1alpha1.PromotionStep{
				makeBundleStep("a-old", "nginx-demo", "prod", "bundle-0", 5),
				makeBundleStep("b-new", "nginx-demo", "prod", "bundle-1", 0),
			},
			wantFailures: 0,
		},
		{
			name: "old bundle healthy, bundleRef failing: rollback",
			steps: []*v1alpha1.PromotionStep{
				makeBundleStep("a-old", "nginx-demo", "prod", "bundle-0", 0),
				makeBundleStep("b-new", "nginx-demo", "prod", "bundle-1", 3),
			},
			wantFailures: 3,
			wantRollback: true,
		},
		{
			name: "multi-region: the worst region counts",
			steps: []*v1alpha1.PromotionStep{
				makeBundleStep("b-new-eu", "nginx-demo", "prod", "bundle-1", 1),
				makeBundleStep("b-new-us", "nginx-demo", "prod", "bundle-1", 4),
			},
			wantFailures: 4,
			wantRollback: true,
		},
		{
			name: "step without the bundle label is matched on spec.bundleName",
			steps: []*v1alpha1.PromotionStep{
				unlabelledBundleStep(makeBundleStep("a-old", "nginx-demo", "prod", "bundle-0", 9)),
				unlabelledBundleStep(makeBundleStep("b-new", "nginx-demo", "prod", "bundle-1", 3)),
			},
			wantFailures: 3,
			wantRollback: true,
		},
		{
			name: "spec.bundleName wins over a disagreeing label",
			steps: []*v1alpha1.PromotionStep{
				func() *v1alpha1.PromotionStep {
					st := makeBundleStep("a-old", "nginx-demo", "prod", "bundle-0", 9)
					st.Labels["kardinal.io/bundle"] = "bundle-1"
					return st
				}(),
				makeBundleStep("b-new", "nginx-demo", "prod", "bundle-1", 0),
			},
			wantFailures: 0,
		},
		{
			name: "only another bundle's step exists: wait",
			steps: []*v1alpha1.PromotionStep{
				makeBundleStep("a-old", "nginx-demo", "prod", "bundle-0", 9),
			},
			wantRequeue: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []interface{ DeepCopyObject() runtime.Object }{
				makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3),
				makeBundle("bundle-1", "nginx-demo"),
			}
			for _, st := range tt.steps {
				objs = append(objs, st)
			}
			updated, result, err := reconcileOnce(t, objs...)
			require.NoError(t, err)
			assert.Equal(t, tt.wantFailures, updated.Status.ConsecutiveFailures)
			assert.Equal(t, tt.wantRollback, updated.Status.ShouldRollback)
			if tt.wantRequeue {
				assert.Nil(t, updated.Status.LastEvaluatedAt, "no status is written before the bundle's step exists")
				assert.Equal(t, 30*time.Second, result.RequeueAfter)
			}
		})
	}
}
