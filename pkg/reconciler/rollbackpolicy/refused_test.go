// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package rollbackpolicy_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/rollbackpolicy"
)

// drain returns the Events the fake recorder holds.
func drain(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// TestRollbackPolicy_RefusalIsVisible covers #1314: when the failure threshold
// is reached but the planner refuses the rollback, the policy gets
// RollbackRefused=True with the planner's reason and message, and one Warning
// Event; reconciling again emits no second Event. A later successful plan
// sets RollbackRefused=False.
func TestRollbackPolicy_RefusalIsVisible(t *testing.T) {
	tests := []struct {
		name              string
		noPipeline        bool
		failingIsRollback bool
		history           []client.Object
		wantStatus        metav1.ConditionStatus
		wantReason        string
		wantMessage       string
		wantEvents        int
	}{
		{name: "nothing verified before the failing bundle",
			wantStatus: metav1.ConditionTrue, wantReason: rollbackpolicy.ReasonNoSafeTarget,
			wantMessage: "no earlier Bundle", wantEvents: 1},
		{name: "the failing bundle is itself a rollback",
			history:           []client.Object{rtBundle("bundle-0", "1.24.0", 0), rtVerifiedStep("bundle-0", 5)},
			failingIsRollback: true,
			wantStatus:        metav1.ConditionTrue, wantReason: rollbackpolicy.ReasonNoSafeTarget,
			wantMessage: "is itself a rollback", wantEvents: 1},
		{name: "the pipeline does not exist", noPipeline: true,
			wantStatus: metav1.ConditionTrue, wantReason: rollbackpolicy.ReasonInvalidPolicy,
			wantMessage: "pipeline default/nginx-demo", wantEvents: 1},
		{name: "a rollback bundle is created",
			history:    []client.Object{rtBundle("bundle-0", "1.24.0", 0), rtVerifiedStep("bundle-0", 5)},
			wantStatus: metav1.ConditionFalse, wantReason: rollbackpolicy.ReasonRollbackCreated,
			wantMessage: lifecycle.AutoRollbackName("bundle-1", "policy")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			failing := makePromotionStep("a-bundle-1-prod", "nginx-demo", "prod", 3)
			failing.Labels["kardinal.io/bundle"] = "bundle-1"
			failingBundle := rtBundle("bundle-1", "1.25.0", 30)
			if tc.failingIsRollback {
				failingBundle.Labels[lifecycle.LabelRollback] = "true"
				failingBundle.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: "bundle-2"}
				failingBundle.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod"}
			}
			objs := []client.Object{makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3), failing, failingBundle}
			if !tc.noPipeline {
				objs = append(objs, rtPipeline())
			}
			objs = append(objs, tc.history...)
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(objs...).
				WithStatusSubresource(&v1alpha1.RollbackPolicy{}, &v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).Build()
			rec := events.NewFakeRecorder(10)
			r := &rollbackpolicy.Reconciler{Client: c, Recorder: rec, NowFn: func() time.Time { return fixedNow }}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-1", Namespace: "default"}}

			_, err := r.Reconcile(ctx, req)
			require.NoError(t, err)
			var got v1alpha1.RollbackPolicy
			require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
			cond := meta.FindStatusCondition(got.Status.Conditions, rollbackpolicy.ConditionRollbackRefused)
			require.NotNil(t, cond, "RollbackRefused condition")
			assert.Equal(t, tc.wantStatus, cond.Status)
			assert.Equal(t, tc.wantReason, cond.Reason)
			assert.Contains(t, cond.Message, tc.wantMessage)
			assert.NotContains(t, cond.Message, "--to", "no CLI hint on an automatic rollback")

			evs := drain(rec)
			require.Len(t, evs, tc.wantEvents)
			for _, e := range evs {
				assert.Contains(t, e, "Warning RollbackRefused env prod:")
				assert.Contains(t, e, tc.wantMessage)
			}

			// A refused policy is re-evaluated on every PromotionStep event;
			// that must not emit an Event each time.
			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			assert.Empty(t, drain(rec), "one Event per refusal, not per reconcile")
			var again v1alpha1.RollbackPolicy
			require.NoError(t, c.Get(ctx, req.NamespacedName, &again))
			assert.Equal(t, got.Status.Conditions, again.Status.Conditions)
		})
	}
}

// TestRollbackPolicy_RefusalClearsOnSuccess covers the other half of #1314: a
// refused policy whose later plan succeeds gets RollbackRefused=False, the
// rollback Bundle name, and no further Warning Event.
func TestRollbackPolicy_RefusalClearsOnSuccess(t *testing.T) {
	ctx := context.Background()
	failing := makePromotionStep("a-bundle-1-prod", "nginx-demo", "prod", 3)
	failing.Labels["kardinal.io/bundle"] = "bundle-1"
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3), failing,
			rtBundle("bundle-1", "1.25.0", 30), rtPipeline()).
		WithStatusSubresource(&v1alpha1.RollbackPolicy{}, &v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).Build()
	rec := events.NewFakeRecorder(10)
	r := &rollbackpolicy.Reconciler{Client: c, Recorder: rec, NowFn: func() time.Time { return fixedNow }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-1", Namespace: "default"}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, drain(rec), 1, "the refusal emits one Warning Event")

	// An earlier Bundle turns out to have been Verified in prod.
	require.NoError(t, c.Create(ctx, rtBundle("bundle-0", "1.24.0", 0)))
	verified := rtVerifiedStep("bundle-0", 5)
	status := verified.Status
	require.NoError(t, c.Create(ctx, verified))
	verified.Status = status
	require.NoError(t, c.Status().Update(ctx, verified))

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	var got v1alpha1.RollbackPolicy
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	require.NotNil(t, got.Status.RollbackBundleName)
	assert.Equal(t, lifecycle.AutoRollbackName("bundle-1", "policy"), *got.Status.RollbackBundleName)
	cond := meta.FindStatusCondition(got.Status.Conditions, rollbackpolicy.ConditionRollbackRefused)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, rollbackpolicy.ReasonRollbackCreated, cond.Reason)
	assert.Empty(t, drain(rec), "a successful rollback emits no RollbackRefused Event")
}
