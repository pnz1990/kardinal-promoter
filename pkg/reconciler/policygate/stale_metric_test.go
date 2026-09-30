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

// TestPolicyGateReconciler_StaleMetricFailsClosed covers #1302: a MetricCheck
// result whose status.validUntil is unset or has passed at the gate's
// evaluation time is exposed as result "Stale", an empty value and stale true,
// and the gate reason names the stale metric. Before the fix a Pass from before
// an outage passed the gate indefinitely.
func TestPolicyGateReconciler_StaleMetricFailsClosed(t *testing.T) {
	now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *metav1.Time {
		ts := metav1.NewTime(now.Add(d))
		return &ts
	}
	const staleNote = `metric "error-rate" result is stale`

	tests := []struct {
		name       string
		expr       string
		validUntil *metav1.Time
		wantReady  bool
		wantNote   bool
	}{
		{name: "fresh Pass passes", expr: `metrics["error-rate"].result == "Pass"`, validUntil: at(time.Minute), wantReady: true},
		{name: "validUntil now is still fresh", expr: `metrics["error-rate"].result == "Pass"`, validUntil: at(0), wantReady: true},
		{name: "past validUntil blocks", expr: `metrics["error-rate"].result == "Pass"`, validUntil: at(-time.Second), wantNote: true},
		{name: "unset validUntil blocks", expr: `metrics["error-rate"].result == "Pass"`, wantNote: true},
		{name: "stale value fails closed", expr: `double(metrics["error-rate"].value) < 0.01`, validUntil: at(-time.Minute), wantNote: true},
		{name: "fresh value passes", expr: `double(metrics["error-rate"].value) < 0.01`, validUntil: at(time.Minute), wantReady: true},
		{name: "stale is exposed", expr: `metrics["error-rate"].stale && metrics["error-rate"].result == "Stale"`, validUntil: at(-time.Minute), wantReady: true},
		{name: "fresh is not stale", expr: `!metrics["error-rate"].stale`, validUntil: at(time.Minute), wantReady: true},
		{name: "unreferenced stale metric is not named", expr: `bundle.type == "config"`, validUntil: at(-time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := makeMetricCheck("error-rate", "default", "0.005", "Pass")
			mc.Status.ValidUntil = tt.validUntil
			gate := makeGateInstance("metric-gate", "default", "nginx-demo-v1", tt.expr, "1m")
			c := fake.NewClientBuilder().
				WithScheme(newScheme()).
				WithObjects(gate, makeBundle("nginx-demo-v1", "default"), mc).
				WithStatusSubresource(gate, mc).
				Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return now }

			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "metric-gate", Namespace: "default"}}
			_, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)

			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tt.wantReady, got.Status.Ready, got.Status.Reason)
			if tt.wantNote {
				assert.Contains(t, got.Status.Reason, staleNote)
			} else {
				assert.NotContains(t, got.Status.Reason, staleNote)
			}
		})
	}
}

// TestPolicyGateReconciler_MetricsNowFn covers the simulate clock: with
// MetricsNowFn set, staleness is judged at that time, not at the evaluation
// time, so a simulated future time does not make a fresh result stale.
func TestPolicyGateReconciler_MetricsNowFn(t *testing.T) {
	now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	nextWeek := now.Add(7 * 24 * time.Hour)
	tests := []struct {
		name       string
		metricsNow func() time.Time
		wantReady  bool
	}{
		{name: "evaluation time", wantReady: false},
		{name: "metrics clock", metricsNow: func() time.Time { return now }, wantReady: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := makeMetricCheck("error-rate", "default", "0.005", "Pass")
			gate := makeGateInstance("metric-gate", "default", "nginx-demo-v1", `metrics["error-rate"].result == "Pass"`, "1m")
			c := fake.NewClientBuilder().
				WithScheme(newScheme()).
				WithObjects(gate, makeBundle("nginx-demo-v1", "default"), mc).
				WithStatusSubresource(gate, mc).
				Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return nextWeek }
			r.MetricsNowFn = tt.metricsNow

			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "metric-gate", Namespace: "default"}}
			_, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)

			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tt.wantReady, got.Status.Ready, got.Status.Reason)
		})
	}
}
