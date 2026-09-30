// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// TestPolicyGateReconciler_SupersededBundleNotEvaluated verifies that a gate
// instance of a Superseded Bundle keeps the status it had when the Bundle was
// superseded: no evaluation, status write, audit record or requeue (E2E-R20).
// Before the fix a blocked soak gate of a Superseded Bundle turned ready later
// and let its Graph create a prod PromotionStep. A Failed Bundle can recover,
// so its gates are still evaluated.
func TestPolicyGateReconciler_SupersededBundleNotEvaluated(t *testing.T) {
	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	lastEval := metav1.NewTime(tuesday.Add(-2 * time.Hour))
	const frozenReason = "bundle.version=main: !schedule.isWeekend = false"

	cases := []struct {
		phase     string
		evaluated bool
	}{
		{phase: "Superseded"},
		{phase: "Failed", evaluated: true},
		{phase: "Promoting", evaluated: true},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			gate := makeGateInstance("no-weekend", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
			gate.Status = kardinalv1alpha1.PolicyGateStatus{Ready: false, Reason: frozenReason, LastEvaluatedAt: &lastEval}
			bundle := makeBundle("nginx-demo-v1", "default")
			bundle.Status.Phase = tc.phase
			c := fake.NewClientBuilder().
				WithScheme(newScheme()).
				WithObjects(gate, bundle).
				WithStatusSubresource(gate, bundle).
				Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return tuesday }

			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "no-weekend", Namespace: "default"}}
			// Twice: the reconciler must be idempotent in both branches.
			for i := 0; i < 2; i++ {
				result, err := r.Reconcile(context.Background(), req)
				require.NoError(t, err)
				if tc.evaluated {
					assert.Greater(t, result.RequeueAfter, time.Duration(0), "an evaluated gate is rechecked")
				} else {
					assert.Equal(t, ctrl.Result{}, result, "a Superseded Bundle's gate is not requeued")
				}
			}

			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			var audits kardinalv1alpha1.AuditEventList
			require.NoError(t, c.List(context.Background(), &audits))
			if tc.evaluated {
				assert.True(t, got.Status.Ready, "weekday gate passes when evaluated")
				assert.True(t, got.Status.LastEvaluatedAt.After(lastEval.Time))
				assert.Len(t, audits.Items, 1, "the ready flip is audited once")
				return
			}
			assert.False(t, got.Status.Ready, "status stays as it was at supersession")
			assert.Equal(t, frozenReason, got.Status.Reason)
			require.NotNil(t, got.Status.LastEvaluatedAt)
			assert.True(t, got.Status.LastEvaluatedAt.Equal(&lastEval), "lastEvaluatedAt not moved")
			assert.Empty(t, audits.Items, "no GateEvaluated audit for a Superseded Bundle")
		})
	}
}
