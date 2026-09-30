// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestMetricCheckResultChanged covers C04-gates-36: the MetricCheck reconciler
// writes lastEvaluatedAt on every query, and only a change of what a gate can
// read (result, value) or of the spec re-evaluates the gates.
func TestMetricCheckResultChanged(t *testing.T) {
	base := func() *kardinalv1alpha1.MetricCheck {
		t0 := metav1.NewTime(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
		validUntil := metav1.NewTime(t0.Add(3 * time.Minute))
		return &kardinalv1alpha1.MetricCheck{
			ObjectMeta: metav1.ObjectMeta{Name: "error-rate", Namespace: "default", Generation: 1},
			Status: kardinalv1alpha1.MetricCheckStatus{
				Result: "Pass", LastValue: "0.01", LastEvaluatedAt: &t0, ValidUntil: &validUntil,
			},
		}
	}
	tests := []struct {
		name   string
		old    func(mc *kardinalv1alpha1.MetricCheck)
		mutate func(mc *kardinalv1alpha1.MetricCheck)
		want   bool
	}{
		{name: "only lastEvaluatedAt", want: false, mutate: func(mc *kardinalv1alpha1.MetricCheck) {
			t1 := metav1.NewTime(mc.Status.LastEvaluatedAt.Add(time.Minute))
			mc.Status.LastEvaluatedAt = &t1
		}},
		{name: "result", want: true, mutate: func(mc *kardinalv1alpha1.MetricCheck) { mc.Status.Result = "Fail" }},
		{name: "value", want: true, mutate: func(mc *kardinalv1alpha1.MetricCheck) { mc.Status.LastValue = "0.02" }},
		{name: "spec", want: true, mutate: func(mc *kardinalv1alpha1.MetricCheck) { mc.Generation = 2 }},
		// #1302: an evaluation that makes a stale result fresh passes, even
		// with the same result and value.
		{name: "stale to fresh", want: true, mutate: func(mc *kardinalv1alpha1.MetricCheck) {
			t1 := metav1.NewTime(mc.Status.LastEvaluatedAt.Add(4 * time.Minute))
			mc.Status.LastEvaluatedAt = &t1
		}},
		{name: "no validUntil before (upgrade)", want: true, old: func(mc *kardinalv1alpha1.MetricCheck) {
			mc.Status.ValidUntil = nil
		}, mutate: func(mc *kardinalv1alpha1.MetricCheck) {
			t1 := metav1.NewTime(mc.Status.LastEvaluatedAt.Add(time.Minute))
			mc.Status.LastEvaluatedAt = &t1
		}},
		{name: "fresh at validUntil", want: false, mutate: func(mc *kardinalv1alpha1.MetricCheck) {
			t1 := *mc.Status.ValidUntil
			mc.Status.LastEvaluatedAt = &t1
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldMC, newMC := base(), base()
			if tt.old != nil {
				tt.old(oldMC)
			}
			tt.mutate(newMC)
			assert.Equal(t, tt.want, metricCheckResultChanged.Update(event.UpdateEvent{ObjectOld: oldMC, ObjectNew: newMC}))
		})
	}
	assert.True(t, metricCheckResultChanged.Create(event.CreateEvent{Object: base()}))
	assert.True(t, metricCheckResultChanged.Delete(event.DeleteEvent{Object: base()}))
}

// TestMetricCheckRequests covers C04-gates-36: a MetricCheck change enqueues
// only the gate instances in its namespace that read metrics.
func TestMetricCheckRequests(t *testing.T) {
	gate := func(name, ns, expr string, instance bool) client.Object {
		g := &kardinalv1alpha1.PolicyGate{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: expr},
		}
		if instance {
			g.Labels = map[string]string{labelBundle: "app-v1"}
		}
		return g
	}
	scheme := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		gate("app-v1-prod-error-rate", "default", `metrics["error-rate"].result == "Pass"`, true),
		gate("app-v1-prod-no-weekend", "default", `!schedule.isWeekend`, true),
		gate("error-rate", "default", `metrics["error-rate"].result == "Pass"`, false),
		gate("app-v1-prod-error-rate", "other", `metrics["error-rate"].result == "Pass"`, true),
	).Build()
	r := &Reconciler{Client: c}

	mc := &kardinalv1alpha1.MetricCheck{ObjectMeta: metav1.ObjectMeta{Name: "error-rate", Namespace: "default"}}
	reqs := r.metricCheckRequests(context.Background(), mc)
	require.Len(t, reqs, 1)
	assert.Equal(t, "default", reqs[0].Namespace)
	assert.Equal(t, "app-v1-prod-error-rate", reqs[0].Name)
}

// TestExprRefersToMetric covers the stale-metric note in status.reason (#1302):
// it names a metric only when the expression refers to it.
func TestExprRefersToMetric(t *testing.T) {
	tests := []struct {
		expr, name string
		want       bool
	}{
		{expr: `metrics["error-rate"].result == "Pass"`, name: "error-rate", want: true},
		{expr: `metrics['error-rate'].result == "Pass"`, name: "error-rate", want: true},
		{expr: `metrics.latency.result == "Pass"`, name: "latency", want: true},
		{expr: `metrics.latency_p99.result == "Pass"`, name: "latency", want: false},
		{expr: `metrics.latency_p99.result == "Pass" && metrics.latency.stale`, name: "latency", want: true},
		{expr: `metrics["error-rate"].result == "Pass"`, name: "rate", want: false},
		{expr: `!schedule.isWeekend`, name: "error-rate", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.expr+"/"+tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, exprRefersToMetric(tt.expr, tt.name))
		})
	}
}
