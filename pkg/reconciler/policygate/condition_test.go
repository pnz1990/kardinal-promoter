// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// gateClock evaluates one weekday gate at chosen times.
type gateClock struct {
	t   *testing.T
	c   client.Client
	r   *policygate.Reconciler
	now time.Time
}

func newGateClock(t *testing.T) *gateClock {
	t.Helper()
	gate := makeGateInstance("no-weekend", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
	gate.CreationTimestamp = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(gate, makeBundle("nginx-demo-v1", "default")).
		WithStatusSubresource(gate).Build()
	gc := &gateClock{t: t, c: c}
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return gc.now }
	gc.r = r
	return gc
}

func (gc *gateClock) eval(at time.Time) kardinalv1alpha1.PolicyGate {
	gc.t.Helper()
	gc.now = at
	key := types.NamespacedName{Name: "no-weekend", Namespace: "default"}
	_, err := gc.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(gc.t, err)
	var g kardinalv1alpha1.PolicyGate
	require.NoError(gc.t, gc.c.Get(context.Background(), key, &g))
	return g
}

// TestPolicyGateReconciler_ReadyConditionMarksTransitions: the Ready
// condition's lastTransitionTime moves only when the gate flips, so it
// identifies one blocking episode (C04-gates-09, C04-gates-10), while
// lastEvaluatedAt moves on every recheck.
func TestPolicyGateReconciler_ReadyConditionMarksTransitions(t *testing.T) {
	sat := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	mon := time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC)
	gc := newGateClock(t)

	steps := []struct {
		at         time.Time
		wantStatus metav1.ConditionStatus
		wantReason string
		wantLTT    time.Time
	}{
		{at: sat, wantStatus: metav1.ConditionFalse, wantReason: "Blocked", wantLTT: sat},
		{at: sat.Add(5 * time.Minute), wantStatus: metav1.ConditionFalse, wantReason: "Blocked", wantLTT: sat},
		{at: sat.Add(10 * time.Minute), wantStatus: metav1.ConditionFalse, wantReason: "Blocked", wantLTT: sat},
		{at: mon, wantStatus: metav1.ConditionTrue, wantReason: "Allowed", wantLTT: mon},
		{at: mon.Add(5 * time.Minute), wantStatus: metav1.ConditionTrue, wantReason: "Allowed", wantLTT: mon},
		{at: sat.Add(7 * 24 * time.Hour), wantStatus: metav1.ConditionFalse, wantReason: "Blocked", wantLTT: sat.Add(7 * 24 * time.Hour)},
	}
	for _, s := range steps {
		g := gc.eval(s.at)
		cond := meta.FindStatusCondition(g.Status.Conditions, "Ready")
		require.NotNil(t, cond, "at %s", s.at)
		assert.Equal(t, s.wantStatus, cond.Status, "at %s", s.at)
		assert.Equal(t, s.wantReason, cond.Reason, "at %s", s.at)
		assert.True(t, s.wantLTT.Equal(cond.LastTransitionTime.Time), "at %s: lastTransitionTime %s, want %s",
			s.at, cond.LastTransitionTime.Time, s.wantLTT)
		assert.Equal(t, g.Status.Reason, cond.Message)
		require.NotNil(t, g.Status.LastEvaluatedAt)
		assert.True(t, s.at.Equal(g.Status.LastEvaluatedAt.Time), "lastEvaluatedAt moves on every evaluation")
	}
}

// blockingHistogram returns the sample count and sum of the gate blocking
// duration histogram.
func blockingHistogram(t *testing.T) (uint64, float64) {
	t.Helper()
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(observability.GateBlockingDurationSeconds))
	mfs, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, mfs, 1)
	h := mfs[0].GetMetric()[0].GetHistogram()
	return h.GetSampleCount(), h.GetSampleSum()
}

// TestPolicyGateReconciler_BlockingDurationMetric covers C04-gates-32: the
// blocking duration is measured from when the gate started blocking, not from
// its creation, and a gate that passes on its first evaluation is not
// recorded as blocked.
func TestPolicyGateReconciler_BlockingDurationMetric(t *testing.T) {
	sat := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	mon := time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC)

	t.Run("passes on first evaluation", func(t *testing.T) {
		count0, _ := blockingHistogram(t)
		newGateClock(t).eval(mon)
		count1, _ := blockingHistogram(t)
		assert.Equal(t, count0, count1, "a gate that was never blocked records no blocking duration")
	})

	t.Run("blocked then allowed", func(t *testing.T) {
		gc := newGateClock(t)
		gc.eval(sat)
		gc.eval(sat.Add(30 * time.Minute))
		count0, sum0 := blockingHistogram(t)
		gc.eval(mon)
		count1, sum1 := blockingHistogram(t)
		assert.Equal(t, count0+1, count1)
		assert.InDelta(t, mon.Sub(sat).Seconds(), sum1-sum0, 0.001, "measured from the start of the block")

		gc.eval(mon.Add(5 * time.Minute))
		count2, _ := blockingHistogram(t)
		assert.Equal(t, count1, count2, "an allowed recheck is not a transition")
	})
}
