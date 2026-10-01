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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
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

// --- Test: no PromotionStep found — wait for the PromotionStep watch ---

// TestReconciler_NoPromotionStep_WaitsForTheWatch: with no step of the Bundle
// yet, the policy is not polled (B49): the PromotionStep watch enqueues it
// when the step appears (TestPoliciesForStep).
func TestReconciler_NoPromotionStep_WaitsForTheWatch(t *testing.T) {
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
	assert.Equal(t, ctrl.Result{}, result, "no poll while no PromotionStep exists")
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
				assert.Equal(t, ctrl.Result{}, result, "the PromotionStep watch, not a poll, re-evaluates")
			}
		})
	}
}

// TestReconciler_NotTriggered_NoRequeue covers B49: a policy below its
// threshold is not polled. Its inputs are its own spec and the status of the
// Bundle's PromotionSteps, and both are watched (SetupWithManager), so a
// change re-evaluates it without a 30s requeue.
func TestReconciler_NotTriggered_NoRequeue(t *testing.T) {
	tests := []struct {
		name     string
		failures int
	}{
		{name: "healthy", failures: 0},
		{name: "below the threshold", failures: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated, result, err := reconcileOnce(t,
				makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3),
				makePromotionStep("step-1", "nginx-demo", "prod", tt.failures),
				makeBundle("bundle-1", "nginx-demo"))
			require.NoError(t, err)
			assert.False(t, updated.Status.ShouldRollback)
			assert.Equal(t, tt.failures, updated.Status.ConsecutiveFailures)
			assert.Equal(t, ctrl.Result{}, result, "the PromotionStep watch, not a poll, re-evaluates")
		})
	}
}

// TestReconciler_RollbackBundleError_Requeues: when the rollback Bundle
// cannot be listed or created, the policy is retried on a timer, since no
// watched object changes to re-enqueue it.
func TestReconciler_RollbackBundleError_Requeues(t *testing.T) {
	rp := makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3)
	step := makePromotionStep("step-1", "nginx-demo", "prod", 3)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(rp, step, makeBundle("bundle-1", "nginx-demo")).
		WithStatusSubresource(rp, step).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*v1alpha1.BundleList); ok {
					return errors.New("apiserver unavailable")
				}
				return cl.List(ctx, list, opts...)
			},
		}).Build()
	r := &rollbackpolicy.Reconciler{Client: c, NowFn: func() time.Time { return fixedNow }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-1", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, result.RequeueAfter)
}

// TestReconciler_ShouldRollbackFollowsTheThreshold covers B49: below the
// threshold status.shouldRollback is false again, unless a rollback Bundle
// exists, recorded (terminal) or not: an evaluation that created it and stopped
// before recording it left shouldRollback true, and it stays true and records
// the Bundle. Clearing it creates no Bundle, and a later crossing reuses the
// rollback Bundle that exists, so it cannot loop.
func TestReconciler_ShouldRollbackFollowsTheThreshold(t *testing.T) {
	existing := "bundle-1-rollback-policy"
	tests := []struct {
		name         string
		recorded     *string
		rollback     bool // the rollback Bundle exists
		failures     int
		wantRollback bool
		wantRecorded *string
	}{
		{name: "true, no rollback Bundle, failures drop: false", failures: 1},
		{name: "true, rollback Bundle created but not recorded, failures drop: stays true and records it",
			rollback: true, failures: 0, wantRollback: true, wantRecorded: &existing},
		{name: "true and nothing recorded, still at the threshold: true", rollback: true, failures: 3,
			wantRollback: true, wantRecorded: &existing},
		{name: "rollback Bundle recorded: terminal, stays true", recorded: &existing, rollback: true, failures: 0,
			wantRollback: true, wantRecorded: &existing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3)
			rp.Status.ShouldRollback = true
			rp.Status.RollbackBundleName = tt.recorded
			objs := []client.Object{rp, makePromotionStep("step-1", "nginx-demo", "prod", tt.failures),
				makeBundle("bundle-1", "nginx-demo")}
			if tt.rollback {
				objs = append(objs, policyRollbackBundle(existing))
			}

			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithObjects(objs...).WithStatusSubresource(objs[0], objs[1]).Build()
			r := &rollbackpolicy.Reconciler{Client: c, NowFn: func() time.Time { return fixedNow }}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-1", Namespace: "default"}}
			// Twice: the second reconcile must not flip the result back.
			for range 2 {
				_, err := r.Reconcile(context.Background(), req)
				require.NoError(t, err)
			}

			var updated v1alpha1.RollbackPolicy
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &updated))
			assert.Equal(t, tt.wantRollback, updated.Status.ShouldRollback)
			assert.Equal(t, tt.wantRecorded, updated.Status.RollbackBundleName)
			if tt.wantRecorded != nil && tt.recorded == nil {
				cond := meta.FindStatusCondition(updated.Status.Conditions, rollbackpolicy.ConditionRollbackRefused)
				require.NotNil(t, cond)
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, rollbackpolicy.ReasonRollbackCreated, cond.Reason)
			}
			var bundles v1alpha1.BundleList
			require.NoError(t, c.List(context.Background(), &bundles))
			assert.Len(t, bundles.Items, len(objs)-2, "no rollback Bundle is created")
		})
	}

	t.Run("cleared, then crossing again reuses the rollback Bundle", func(t *testing.T) {
		rp := makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3)
		rp.Status.ShouldRollback = true
		step := makePromotionStep("step-1", "nginx-demo", "prod", 1)
		c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
			WithObjects(rp, step, makeBundle("bundle-1", "nginx-demo")).
			WithStatusSubresource(rp, step).Build()
		r := &rollbackpolicy.Reconciler{Client: c, NowFn: func() time.Time { return fixedNow }}
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-1", Namespace: "default"}}
		get := func() v1alpha1.RollbackPolicy {
			var got v1alpha1.RollbackPolicy
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			return got
		}

		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		assert.False(t, get().Status.ShouldRollback)

		// onHealthFailure=rollback rolled the Bundle back meanwhile.
		require.NoError(t, c.Create(context.Background(), policyRollbackBundle(existing)))
		step.Status.ConsecutiveHealthFailures = 4
		require.NoError(t, c.Status().Update(context.Background(), step))
		_, err = r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		got := get()
		assert.True(t, got.Status.ShouldRollback)
		require.NotNil(t, got.Status.RollbackBundleName)
		assert.Equal(t, existing, *got.Status.RollbackBundleName)
		var bundles v1alpha1.BundleList
		require.NoError(t, c.List(context.Background(), &bundles))
		assert.Len(t, bundles.Items, 2, "the existing rollback Bundle is reused")
	})
}

// policyRollbackBundle is a rollback Bundle of bundle-1, as either automatic
// rollback path creates it.
func policyRollbackBundle(name string) *v1alpha1.Bundle {
	b := makeBundle(name, "nginx-demo")
	b.Labels[lifecycle.LabelRollback] = "true"
	b.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: "bundle-1"}
	return b
}

// TestReconciler_RefusalClearsBelowTheThreshold covers B49: RollbackRefused
// stayed True after the failures dropped below the threshold, because only a
// rollback attempt wrote it. It is False again, with reason BelowThreshold,
// whether shouldRollback is still true or was already cleared.
func TestReconciler_RefusalClearsBelowTheThreshold(t *testing.T) {
	for _, shouldRollback := range []bool{true, false} {
		t.Run(fmt.Sprintf("shouldRollback %v", shouldRollback), func(t *testing.T) {
			rp := makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3)
			rp.Status.ShouldRollback = shouldRollback
			rp.Status.Conditions = []metav1.Condition{{
				Type: rollbackpolicy.ConditionRollbackRefused, Status: metav1.ConditionTrue,
				Reason: rollbackpolicy.ReasonNoSafeTarget, Message: "nothing to roll back to",
				LastTransitionTime: metav1.NewTime(fixedNow.Add(-time.Hour)),
			}}
			updated, result, err := reconcileOnce(t, rp,
				makePromotionStep("step-1", "nginx-demo", "prod", 1), makeBundle("bundle-1", "nginx-demo"))
			require.NoError(t, err)
			assert.Zero(t, result.RequeueAfter)
			assert.False(t, updated.Status.ShouldRollback)
			assert.Nil(t, updated.Status.RollbackBundleName)
			cond := meta.FindStatusCondition(updated.Status.Conditions, rollbackpolicy.ConditionRollbackRefused)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, rollbackpolicy.ReasonBelowThreshold, cond.Reason)
			assert.Equal(t, "1 consecutive health failures, below the threshold of 3", cond.Message)
		})
	}
}
