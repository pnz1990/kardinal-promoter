// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// TestPolicyGateReconciler_PerPromotionMetric: a gate reads a per-promotion
// MetricCheck template's name as the instance the Graph made for its own
// Bundle and environment. Without that instance, or with two claiming to be
// it, the metric is stale and the gate blocks; an instance of another Bundle
// or environment never decides it, and instances never show up under their
// own names.
func TestPolicyGateReconciler_PerPromotionMetric(t *testing.T) {
	instance := func(name, bundle, env, result string) *kardinalv1alpha1.MetricCheck {
		mc := makeMetricCheck(name, "default", "0.001", result)
		mc.Labels = map[string]string{graph.LabelMetricTemplate: "error-rate",
			"kardinal.io/bundle": bundle, "kardinal.io/environment": env}
		return mc
	}
	template := makeMetricCheck("error-rate", "default", "", "")
	template.Spec.PerPromotion = true
	template.Status = kardinalv1alpha1.MetricCheckStatus{}

	tests := []struct {
		name      string
		expr      string
		objs      []client.Object
		wantReady bool
	}{
		{"own instance passes", `metrics["error-rate"].result == "Pass"`,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "Pass")}, true},
		{"own instance fails", `metrics["error-rate"].result == "Pass"`,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "Fail")}, false},
		{"no instance is stale", `metrics["error-rate"].stale`, []client.Object{template}, true},
		{"no instance blocks", `metrics["error-rate"].result == "Pass"`, []client.Object{template}, false},
		{"other bundle's instance does not count", `metrics["error-rate"].result == "Pass"`,
			[]client.Object{template, instance("i2", "nginx-demo-v0", "prod", "Pass")}, false},
		{"other environment's instance does not count", `metrics["error-rate"].result == "Pass"`,
			[]client.Object{template, instance("i3", "nginx-demo-v1", "uat", "Pass")}, false},
		{"two claimants are stale", `metrics["error-rate"].result == "Pass"`,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "Pass"), instance("i4", "nginx-demo-v1", "prod", "Pass")}, false},
		{"instances are not exposed by name", `!("i1" in metrics)`,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "Pass")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := makeGateInstance("metric-gate", "default", "nginx-demo-v1", tt.expr, "1m")
			objs := append([]client.Object{gate, makeBundle("nginx-demo-v1", "default")}, tt.objs...)
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objs...).WithStatusSubresource(gate).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return metricValidUntil.Add(-time.Minute) }
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "metric-gate", Namespace: "default"}}
			_, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tt.wantReady, got.Status.Ready, got.Status.Reason)
		})
	}
}
