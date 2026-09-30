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

// TestPolicyGateReconciler_SettledBundleNotEvaluated verifies that a gate
// instance of a settled Bundle keeps the status it had: no evaluation, status
// write, audit record or requeue. Settled is Superseded (E2E-R20: before that
// fix a blocked soak gate of a Superseded Bundle turned ready later and let its
// Graph create a prod PromotionStep), or Verified with GraphReady True (#1301:
// before that fix every ScheduleClock tick re-evaluated and rewrote the gates
// of every finished Bundle). A Failed Bundle can recover, and a Verified
// Bundle's Graph is still read until GraphReady is True, so their gates are
// still evaluated.
func TestPolicyGateReconciler_SettledBundleNotEvaluated(t *testing.T) {
	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	lastEval := metav1.NewTime(tuesday.Add(-2 * time.Hour))
	const frozenReason = "bundle.version=main: !schedule.isWeekend = false"
	graphReady := func(status metav1.ConditionStatus) []metav1.Condition {
		return []metav1.Condition{{Type: "GraphReady", Status: status, Reason: "Test", LastTransitionTime: lastEval}}
	}

	cases := []struct {
		name       string
		phase      string
		conditions []metav1.Condition
		evaluated  bool
	}{
		{name: "Superseded", phase: "Superseded"},
		{name: "Verified, GraphReady True", phase: "Verified", conditions: graphReady(metav1.ConditionTrue)},
		{name: "Verified, GraphReady False", phase: "Verified", conditions: graphReady(metav1.ConditionFalse), evaluated: true},
		{name: "Verified, no GraphReady", phase: "Verified", evaluated: true},
		{name: "Failed", phase: "Failed", evaluated: true},
		{name: "Failed, GraphReady True", phase: "Failed", conditions: graphReady(metav1.ConditionTrue), evaluated: true},
		{name: "Promoting", phase: "Promoting", evaluated: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := makeGateInstance("no-weekend", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
			gate.Status = kardinalv1alpha1.PolicyGateStatus{Ready: false, Reason: frozenReason, LastEvaluatedAt: &lastEval}
			bundle := makeBundle("nginx-demo-v1", "default")
			bundle.Status.Phase = tc.phase
			bundle.Status.Conditions = tc.conditions
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
					assert.Equal(t, ctrl.Result{}, result, "a settled Bundle's gate is not requeued")
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
			assert.False(t, got.Status.Ready, "status stays as it was when the Bundle settled")
			assert.Equal(t, frozenReason, got.Status.Reason)
			require.NotNil(t, got.Status.LastEvaluatedAt)
			assert.True(t, got.Status.LastEvaluatedAt.Equal(&lastEval), "lastEvaluatedAt not moved")
			assert.Empty(t, audits.Items, "no GateEvaluated audit for a settled Bundle")
		})
	}
}
