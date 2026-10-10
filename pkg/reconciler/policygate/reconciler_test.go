// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone/objectgonetest"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kardinalv1alpha1.AddToScheme(s)
	return s
}

// makeGateInstance creates a PolicyGate instance (with bundle label) for testing.
// nowFn is used to override the clock for schedule-based tests.
func makeGateInstance(name, ns, bundleName, expression, recheckInterval string) *kardinalv1alpha1.PolicyGate {
	return &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"kardinal.io/bundle":      bundleName,
				"kardinal.io/pipeline":    "nginx-demo",
				"kardinal.io/environment": "prod",
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{
			Expression:      expression,
			Message:         "test gate",
			RecheckInterval: recheckInterval,
		},
	}
}

func makeBundle(name, ns string) *kardinalv1alpha1.Bundle {
	return &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
			Provenance: &kardinalv1alpha1.BundleProvenance{
				Author:    "alice",
				CommitSHA: "abc123",
			},
		},
	}
}

// TestPolicyGateReconciler_WeekdayGatePasses verifies !schedule.isWeekend passes on a weekday.
func TestPolicyGateReconciler_WeekdayGatePasses(t *testing.T) {
	gate := makeGateInstance("no-weekend", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
	bundle := makeBundle("nginx-demo-v1", "default")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	// Tuesday
	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return tuesday }

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-weekend", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter.Seconds(), float64(0), "must requeue after recheckInterval")

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "no-weekend", Namespace: "default"}, &got))
	assert.True(t, got.Status.Ready, "gate must be ready on weekday")
	assert.NotNil(t, got.Status.LastEvaluatedAt, "lastEvaluatedAt must be set")
}

// TestPolicyGateReconciler_WeekendGateBlocks verifies !schedule.isWeekend blocks on weekend.
func TestPolicyGateReconciler_WeekendGateBlocks(t *testing.T) {
	gate := makeGateInstance("no-weekend", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
	bundle := makeBundle("nginx-demo-v1", "default")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	// Saturday
	saturday := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return saturday }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-weekend", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "no-weekend", Namespace: "default"}, &got))
	assert.False(t, got.Status.Ready, "gate must NOT be ready on Saturday")
}

// TestPolicyGateReconciler_BundleNotFound verifies fail-closed when bundle is missing.
func TestPolicyGateReconciler_BundleNotFound(t *testing.T) {
	gate := makeGateInstance("no-weekend", "default", "missing-bundle", "!schedule.isWeekend", "5m")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate). // bundle NOT added
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-weekend", Namespace: "default"},
	})
	require.NoError(t, err, "bundle-not-found must not return error (requeue only)")

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "no-weekend", Namespace: "default"}, &got))
	assert.False(t, got.Status.Ready, "gate must be blocked when bundle is missing")
}

// TestPolicyGateReconciler_TemplateIgnored verifies templates (no bundle label) are processed
// for CEL validation but do NOT get requeued (no recheckInterval requeue for templates).
// Since the template has valid CEL, status.reason should indicate "valid CEL syntax".
func TestPolicyGateReconciler_TemplateIgnored(t *testing.T) {
	// Gate template: no kardinal.io/bundle label
	template := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-template",
			Namespace: "platform-policies",
			// No kardinal.io/bundle label
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(template).
		WithStatusSubresource(template).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-weekend-template", Namespace: "platform-policies"},
	})
	require.NoError(t, err)
	// Templates return empty result (no requeue interval — they have no recheckInterval loop)
	assert.Equal(t, ctrl.Result{}, result, "template must return empty result (no requeue)")

	// #315: template CEL should be validated and status updated
	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "no-weekend-template", Namespace: "platform-policies"}, &got))
	assert.Contains(t, got.Status.Reason, "valid CEL syntax",
		"valid template must indicate valid syntax in status.reason")
	// A template is not evaluated against a bundle (C04-gates-11).
	assert.Nil(t, got.Status.LastEvaluatedAt, "template must not look evaluated")
	assert.Empty(t, got.Status.Conditions, "template must not carry a Ready condition")
}

// TestPolicyGateReconciler_RequeueAfterRecheckInterval verifies RequeueAfter.
func TestPolicyGateReconciler_RequeueAfterRecheckInterval(t *testing.T) {
	gate := makeGateInstance("recheck-gate", "default", "nginx-demo-v1", "!schedule.isWeekend", "10m")
	bundle := makeBundle("nginx-demo-v1", "default")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "recheck-gate", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Equal(t, 10*time.Minute, result.RequeueAfter, "RequeueAfter must match recheckInterval")
}

// TestPolicyGateReconciler_GateNotFound verifies no error when gate is deleted.
func TestPolicyGateReconciler_GateNotFound(t *testing.T) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "gone", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// TestPolicyGateReconciler_Idempotent verifies reconciling the same gate twice is safe.
func TestPolicyGateReconciler_Idempotent(t *testing.T) {
	gate := makeGateInstance("idem-gate", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
	bundle := makeBundle("nginx-demo-v1", "default")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return tuesday }

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "idem-gate", Namespace: "default"}}

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "idem-gate", Namespace: "default"}, &got))
	assert.True(t, got.Status.Ready)
}

// metricValidUntil keeps makeMetricCheck results fresh at the 2026-04-07
// 10:00 UTC clock the metric tests use.
var metricValidUntil = metav1.NewTime(time.Date(2026, 4, 7, 10, 3, 0, 0, time.UTC))

// makeMetricCheck builds a MetricCheck with a given status result, fresh until
// metricValidUntil.
func makeMetricCheck(name, ns, lastValue, result string) *kardinalv1alpha1.MetricCheck {
	return &kardinalv1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kardinalv1alpha1.MetricCheckSpec{
			Provider:      "prometheus",
			PrometheusURL: "http://prometheus:9090",
			Query:         `error_rate`,
			Threshold:     kardinalv1alpha1.MetricThreshold{Value: 0.01, Operator: "lt"},
		},
		Status: kardinalv1alpha1.MetricCheckStatus{
			LastValue:  lastValue,
			Result:     result,
			ValidUntil: &metricValidUntil,
		},
	}
}

// makeBundleWithEnvironments builds a Bundle with environment statuses for soak-time tests.
func makeBundleWithEnvironments(name, ns string, envs []kardinalv1alpha1.EnvironmentStatus) *kardinalv1alpha1.Bundle {
	b := makeBundle(name, ns)
	b.Status.Environments = envs
	return b
}

// TestPolicyGateReconciler_MetricsContext_PassWhenMetricPasses verifies that
// metrics.<name>.result == "Pass" makes a gate expression pass.
func TestPolicyGateReconciler_MetricsContext_PassWhenMetricPasses(t *testing.T) {
	mc := makeMetricCheck("error-rate", "default", "0.005", "Pass")
	gate := makeGateInstance("metric-gate", "default", "nginx-demo-v1",
		`metrics["error-rate"].result == "Pass"`, "1m")
	bundle := makeBundle("nginx-demo-v1", "default")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle, mc).
		WithStatusSubresource(gate, mc).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "metric-gate", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "metric-gate", Namespace: "default"}, &got))
	assert.True(t, got.Status.Ready, "gate must be ready when MetricCheck result is Pass")
}

// TestPolicyGateReconciler_MetricsContext_BlockWhenMetricFails verifies that
// metrics.<name>.result == "Fail" causes the gate expression to fail.
func TestPolicyGateReconciler_MetricsContext_BlockWhenMetricFails(t *testing.T) {
	mc := makeMetricCheck("error-rate", "default", "0.05", "Fail")
	gate := makeGateInstance("metric-gate", "default", "nginx-demo-v1",
		`metrics["error-rate"].result == "Pass"`, "1m")
	bundle := makeBundle("nginx-demo-v1", "default")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle, mc).
		WithStatusSubresource(gate, mc).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "metric-gate", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "metric-gate", Namespace: "default"}, &got))
	assert.False(t, got.Status.Ready, "gate must be blocked when MetricCheck result is Fail")
}

// TestPolicyGateReconciler_UpstreamSoakContext_Passes verifies that
// upstream["uat"].soakMinutes >= 30 passes when bundle.status.environments[uat].soakMinutes >= 30.
// After the PG-3 fix, soakMinutes is read from Bundle.status (written by BundleReconciler),
// not computed via time.Since() in the PolicyGate reconciler.
func TestPolicyGateReconciler_UpstreamSoakContext_Passes(t *testing.T) {
	now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	healthCheckedAt := metav1.NewTime(now.Add(-45 * time.Minute)) // 45 minutes ago

	bundle := makeBundleWithEnvironments("nginx-demo-v1", "default", []kardinalv1alpha1.EnvironmentStatus{
		// SoakMinutes=45 is written by BundleReconciler; PolicyGate reads it (PG-3 fix).
		{Name: "uat", Phase: "Verified", HealthCheckedAt: &healthCheckedAt, SoakMinutes: 45},
	})
	gate := makeGateInstance("soak-gate", "default", "nginx-demo-v1",
		`upstream["uat"].soakMinutes >= 30`, "2m")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate, bundle).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return now }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "soak-gate", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "soak-gate", Namespace: "default"}, &got))
	assert.True(t, got.Status.Ready, "gate must pass when uat has soaked for 45 min (>= 30 threshold)")
}

// TestPolicyGateReconciler_UpstreamSoakContext_Blocks verifies that
// upstream["uat"].soakMinutes >= 30 blocks when bundle.status.environments[uat].soakMinutes < 30.
func TestPolicyGateReconciler_UpstreamSoakContext_Blocks(t *testing.T) {
	now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	healthCheckedAt := metav1.NewTime(now.Add(-10 * time.Minute)) // only 10 minutes ago

	bundle := makeBundleWithEnvironments("nginx-demo-v1", "default", []kardinalv1alpha1.EnvironmentStatus{
		// SoakMinutes=10 written by BundleReconciler; PolicyGate reads it (PG-3 fix).
		{Name: "uat", Phase: "Verified", HealthCheckedAt: &healthCheckedAt, SoakMinutes: 10},
	})
	gate := makeGateInstance("soak-gate", "default", "nginx-demo-v1",
		`upstream["uat"].soakMinutes >= 30`, "2m")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate, bundle).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return now }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "soak-gate", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "soak-gate", Namespace: "default"}, &got))
	assert.False(t, got.Status.Ready, "gate must block when uat has only soaked for 10 min (< 30 threshold)")
}

// TestPolicyGateReconciler_MetricsContext_NamespaceIsolation verifies that
// buildMetricsContext only includes MetricChecks from the gate's own namespace.
// A MetricCheck in a different namespace must not appear in the metrics context.
func TestPolicyGateReconciler_MetricsContext_NamespaceIsolation(t *testing.T) {
	mcSameNS := makeMetricCheck("error-rate", "default", "0.005", "Pass")
	// MetricCheck in a different namespace — must NOT appear in metrics context
	mcOtherNS := makeMetricCheck("error-rate", "other-ns", "0.999", "Fail")
	gate := makeGateInstance("metric-gate", "default", "nginx-demo-v1",
		`metrics["error-rate"].result == "Pass"`, "1m")
	bundle := makeBundle("nginx-demo-v1", "default")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle, mcSameNS, mcOtherNS).
		WithStatusSubresource(gate, mcSameNS, mcOtherNS).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "metric-gate", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "metric-gate", Namespace: "default"}, &got))
	// Must pass — the "default" namespace MetricCheck has result=Pass
	// If it had picked up the "other-ns" one (Fail), this would fail
	assert.True(t, got.Status.Ready, "gate must pass using only same-namespace MetricChecks")
}

// TestPolicyGateReconciler_MetricsContext_EmptyWhenNoMetricChecks verifies that
// a gate not referencing metrics can still evaluate when no MetricChecks exist.
func TestPolicyGateReconciler_MetricsContext_EmptyWhenNoMetricChecks(t *testing.T) {
	gate := makeGateInstance("no-metric-gate", "default", "nginx-demo-v1",
		`!schedule.isWeekend`, "5m")
	bundle := makeBundle("nginx-demo-v1", "default")
	// No MetricCheck objects created
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return tuesday }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-metric-gate", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "no-metric-gate", Namespace: "default"}, &got))
	assert.True(t, got.Status.Ready, "schedule gate must pass on weekday even with empty metrics context")
}

// TestPolicyGateReconciler_UpstreamSoakContext_ZeroWhenNotHealthChecked verifies
// soakMinutes is 0 when HealthCheckedAt is nil.
func TestPolicyGateReconciler_UpstreamSoakContext_ZeroWhenNotHealthChecked(t *testing.T) {
	bundle := makeBundleWithEnvironments("nginx-demo-v1", "default", []kardinalv1alpha1.EnvironmentStatus{
		{Name: "uat", Phase: "Promoting"}, // no HealthCheckedAt
	})
	gate := makeGateInstance("soak-gate", "default", "nginx-demo-v1",
		`upstream["uat"].soakMinutes >= 30`, "2m")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate, bundle).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "soak-gate", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "soak-gate", Namespace: "default"}, &got))
	assert.False(t, got.Status.Ready, "gate must block when uat has not been health-checked (soakMinutes=0)")
}

// makeBundleWithImage builds a Bundle with an image ref (for version extraction tests).
func makeBundleWithImage(name, ns, repo, tag string) *kardinalv1alpha1.Bundle {
	b := makeBundle(name, ns)
	b.Spec.Images = []kardinalv1alpha1.ImageRef{{Repository: repo, Tag: tag}}
	return b
}

func makePipeline(name, ns string, envs ...kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
}

// TestPolicyGateReconciler_BundleUpstreamSoakMinutes verifies that
// bundle.upstreamSoakMinutes is the soak of the environment(s) directly upstream
// of the gated environment in the Pipeline DAG (C04-gates-01, C12-examples-demo-04),
// not the maximum over every environment. With fan-in it is the minimum across
// the direct upstreams; an upstream that is not Verified counts as 0.
func TestPolicyGateReconciler_BundleUpstreamSoakMinutes(t *testing.T) {
	sequential := makePipeline("nginx-demo", "default",
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "uat"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod"},
	)
	fanIn := makePipeline("nginx-demo", "default",
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod-eu", DependsOn: []string{"test"}},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod-us", DependsOn: []string{"test"}},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", DependsOn: []string{"prod-eu", "prod-us"}},
	)

	tests := []struct {
		name       string
		pipeline   *kardinalv1alpha1.Pipeline
		envs       []kardinalv1alpha1.EnvironmentStatus
		intent     *kardinalv1alpha1.BundleIntent
		wantReady  bool
		wantReason string
	}{
		{
			name:     "passes when direct upstream uat soaked long enough",
			pipeline: sequential,
			envs: []kardinalv1alpha1.EnvironmentStatus{
				{Name: "test", Phase: "Verified", SoakMinutes: 50},
				{Name: "uat", Phase: "Verified", SoakMinutes: 45},
			},
			wantReady: true,
		},
		{
			name:     "blocks when direct upstream uat soaked 1m even though test soaked 120m",
			pipeline: sequential,
			envs: []kardinalv1alpha1.EnvironmentStatus{
				{Name: "test", Phase: "Verified", SoakMinutes: 120},
				{Name: "uat", Phase: "Verified", SoakMinutes: 1},
			},
			wantReady: false,
		},
		{
			name:     "blocks when direct upstream is not Verified",
			pipeline: sequential,
			envs: []kardinalv1alpha1.EnvironmentStatus{
				{Name: "test", Phase: "Verified", SoakMinutes: 120},
				{Name: "uat", Phase: "HealthChecking", SoakMinutes: 90},
			},
			wantReady: false,
		},
		{
			name:     "skipped uat is bridged to test",
			pipeline: sequential,
			intent:   &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"uat"}},
			envs: []kardinalv1alpha1.EnvironmentStatus{
				{Name: "test", Phase: "Verified", SoakMinutes: 40},
			},
			wantReady: true,
		},
		{
			name:     "fan-in uses the minimum of the direct upstreams",
			pipeline: fanIn,
			envs: []kardinalv1alpha1.EnvironmentStatus{
				{Name: "test", Phase: "Verified", SoakMinutes: 300},
				{Name: "prod-eu", Phase: "Verified", SoakMinutes: 60},
				{Name: "prod-us", Phase: "Verified", SoakMinutes: 5},
			},
			wantReady: false,
		},
		{
			name:       "missing pipeline fails closed",
			pipeline:   nil,
			envs:       []kardinalv1alpha1.EnvironmentStatus{{Name: "uat", Phase: "Verified", SoakMinutes: 90}},
			wantReady:  false,
			wantReason: "context error: bundle.upstreamSoakMinutes: load pipeline nginx-demo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
			bundle := makeBundleWithEnvironments("nginx-demo-v1", "default", tt.envs)
			bundle.Spec.Intent = tt.intent
			gate := makeGateInstance("soak-gate", "default", "nginx-demo-v1",
				`bundle.upstreamSoakMinutes >= 30`, "1m")
			objs := []client.Object{gate, bundle}
			if tt.pipeline != nil {
				objs = append(objs, tt.pipeline.DeepCopy())
			}
			c := fake.NewClientBuilder().
				WithScheme(newScheme()).
				WithObjects(objs...).
				WithStatusSubresource(gate, bundle).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
				Build()

			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return now }

			_, err = r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "soak-gate", Namespace: "default"},
			})
			require.NoError(t, err)

			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "soak-gate", Namespace: "default"}, &got))
			assert.Equal(t, tt.wantReady, got.Status.Ready, "reason: %s", got.Status.Reason)
			if tt.wantReason != "" {
				assert.Contains(t, got.Status.Reason, tt.wantReason)
			}
		})
	}
}

// TestPolicyGateReconciler_NoSoakReference_NoPipelineNeeded verifies that a gate
// that does not use bundle.upstreamSoakMinutes is not affected by a missing Pipeline.
func TestPolicyGateReconciler_NoSoakReference_NoPipelineNeeded(t *testing.T) {
	gate := makeGateInstance("plain", "default", "nginx-demo-v1", `bundle.type == "image"`, "1m")
	bundle := makeBundle("nginx-demo-v1", "default")
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "plain", Namespace: "default"},
	})
	require.NoError(t, err)
	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "plain", Namespace: "default"}, &got))
	assert.True(t, got.Status.Ready, "reason: %s", got.Status.Reason)
}

// TestPolicyGateReconciler_InvalidCEL_SurfacesErrorInStatus verifies that a PolicyGate
// instance with syntactically invalid CEL does NOT apply silently — the reconciler sets
// status.ready=false and populates status.reason with the compilation error message.
// This is Option B from issue #315: operators can see the error via kubectl describe pg.
func TestPolicyGateReconciler_InvalidCEL_SurfacesErrorInStatus(t *testing.T) {
	gate := makeGateInstance("bad-cel-gate", "default", "nginx-demo-v1",
		`this is not valid CEL !!!`, "5m")
	bundle := makeBundle("nginx-demo-v1", "default")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "bad-cel-gate", Namespace: "default"},
	})
	require.NoError(t, err, "invalid CEL must not crash the reconciler (fail-closed, not panic)")

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "bad-cel-gate", Namespace: "default"}, &got))

	// #315: gate must be ready=false (fail-closed)
	assert.False(t, got.Status.Ready,
		"gate with invalid CEL must NOT be ready — fail-closed")

	// #315: status.reason must contain the CEL compilation error so operators can diagnose it
	// via `kubectl get pg bad-cel-gate -o wide` or `kubectl describe pg bad-cel-gate`
	assert.Contains(t, got.Status.Reason, "CEL compile error",
		"status.reason must contain the compilation error for operator visibility")
}

// TestPolicyGateReconciler_StatusReasonContainsVersion verifies that when a bundle
// has an image tag, the evaluated status.reason includes the bundle version.
// This makes the routing info (bundle version used for evaluation) CRD-observable
// rather than staying in Go memory only (PG-6 in docs/design/11-graph-purity-tech-debt.md).
func TestPolicyGateReconciler_StatusReasonContainsVersion(t *testing.T) {
	bundle := makeBundleWithImage("nginx-demo-v1", "default", "ghcr.io/nginx/nginx", "1.29.0")
	gate := makeGateInstance("version-gate", "default", "nginx-demo-v1",
		`!schedule.isWeekend`, "5m")
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return tuesday }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "version-gate", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "version-gate", Namespace: "default"}, &got))
	assert.True(t, got.Status.Ready, "gate must pass on weekday")
	// PG-6: bundle version must be visible in status.reason so Graph and operators can observe it
	assert.Contains(t, got.Status.Reason, "1.29.0",
		"status.reason must include bundle version to make routing info CRD-observable")
}

// makeGateTemplate creates a template PolicyGate (no bundle label) for testing.
func makeGateTemplate(name, ns, expression string) *kardinalv1alpha1.PolicyGate {
	return &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"kardinal.io/scope":      "org",
				"kardinal.io/applies-to": "prod",
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{
			Expression:      expression,
			Message:         "template gate message",
			RecheckInterval: "5m",
		},
	}
}

// TestPolicyGateReconciler_Template_InvalidCEL_SurfacesError verifies that a template
// PolicyGate (no bundle label) with invalid CEL has the compilation error surfaced in
// status.reason — so platform engineers can see the error via kubectl describe pg.
// This is issue #315 for template gates (the existing test covers instances).
func TestPolicyGateReconciler_Template_InvalidCEL_SurfacesError(t *testing.T) {
	gate := makeGateTemplate("bad-template", "platform-policies", `this is not valid CEL !!!`)
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate).
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "bad-template", Namespace: "platform-policies"},
	})
	require.NoError(t, err, "invalid CEL in template must not crash the reconciler")

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "bad-template", Namespace: "platform-policies"}, &got))

	// #315: status must surface the CEL error so platform engineers can see it.
	assert.False(t, got.Status.Ready,
		"template gate with invalid CEL must NOT be ready — fail-closed")
	assert.Contains(t, got.Status.Reason, "CEL syntax error",
		"status.reason must contain the compilation error message for operator visibility")
}

// TestPolicyGateReconciler_Template_ValidCEL_StatusShowsValid verifies that a template
// PolicyGate with valid CEL sets status.reason to indicate valid syntax.
func TestPolicyGateReconciler_Template_ValidCEL_StatusShowsValid(t *testing.T) {
	gate := makeGateTemplate("valid-template", "platform-policies", `!schedule.isWeekend`)
	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate).
		WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "valid-template", Namespace: "platform-policies"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "valid-template", Namespace: "platform-policies"}, &got))

	// Template gates are always ready=false (not evaluated against a real bundle yet)
	assert.False(t, got.Status.Ready,
		"template gate is never ready=true — it's a spec, not an evaluation")
	assert.Contains(t, got.Status.Reason, "valid CEL syntax",
		"status.reason must confirm valid CEL syntax for operator visibility")
}

// ─── K-04: ChangeWindow CRD ───────────────────────────────────────────────────

// TestPolicyGateReconciler_ChangeWindowBlocked verifies that a PolicyGate using
// changewindow.isBlocked() returns ready=false when a blackout ChangeWindow is active.
func TestPolicyGateReconciler_ChangeWindowBlocked(t *testing.T) {
	scheme := newScheme()

	// Active blackout window (starts before now, ends after now)
	cw := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "holiday-freeze",
			Namespace: "kardinal-system",
		},
		Spec: kardinalv1alpha1.ChangeWindowSpec{
			Type:   "blackout",
			Start:  metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			End:    metav1.NewTime(time.Now().Add(1 * time.Hour)),
			Reason: "Q4 holiday freeze",
		},
	}

	gate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cw-gate",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/bundle":      "bundle-1",
				"kardinal.io/pipeline":    "my-app",
				"kardinal.io/environment": "test",
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{
			Expression: `!changewindow["holiday-freeze"]`,
			Message:    "blocked by change window",
		},
	}
	bundle := makeBundle("bundle-1", "default")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gate, bundle, cw).
		WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()
	r, err1 := policygate.NewReconciler(c)
	require.NoError(t, err1)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cw-gate", Namespace: "default"}}
	_, reconcileErr1 := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr1)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))

	assert.False(t, got.Status.Ready, "gate must be not-ready when change window is active")
	assert.NotEmpty(t, got.Status.Reason, "reason must be set when gate blocks")
}

// TestPolicyGateReconciler_ChangeWindowAllowed verifies that a PolicyGate using
// changewindow.isBlocked() returns ready=true when no active blackout window exists.
func TestPolicyGateReconciler_ChangeWindowAllowed(t *testing.T) {
	scheme := newScheme()

	// Past window — ended before now (no longer blocking)
	cw := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "past-freeze",
			Namespace: "kardinal-system",
		},
		Spec: kardinalv1alpha1.ChangeWindowSpec{
			Type:  "blackout",
			Start: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
			End:   metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
	}

	gate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cw-gate-ok",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/bundle":      "bundle-1",
				"kardinal.io/pipeline":    "my-app",
				"kardinal.io/environment": "test",
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{
			Expression: `!changewindow["past-freeze"]`,
		},
	}
	bundle := makeBundle("bundle-1", "default")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gate, bundle, cw).
		WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()
	r, err2 := policygate.NewReconciler(c)
	require.NoError(t, err2)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cw-gate-ok", Namespace: "default"}}
	_, reconcileErr2 := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr2)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))

	assert.True(t, got.Status.Ready, "gate must be ready when no active change window")
}

// TestPolicyGateReconciler_ChangeWindowIsBlocked_MethodSyntax verifies that
// changewindow.isBlocked("name") returns true when the window is active (#506).
func TestPolicyGateReconciler_ChangeWindowIsBlocked_MethodSyntax(t *testing.T) {
	scheme := newScheme()

	cw := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "q4-freeze", Namespace: "kardinal-system"},
		Spec: kardinalv1alpha1.ChangeWindowSpec{
			Type:  "blackout",
			Start: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			End:   metav1.NewTime(time.Now().Add(1 * time.Hour)),
		},
	}
	gate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cw-method-blocked", Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/bundle":      "bundle-1",
				"kardinal.io/pipeline":    "my-app",
				"kardinal.io/environment": "prod",
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: `!changewindow.isBlocked("q4-freeze")`},
	}
	bundle := makeBundle("bundle-1", "default")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gate, bundle, cw).WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cw-method-blocked", Namespace: "default"}}
	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var gotBlocked kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &gotBlocked))
	assert.False(t, gotBlocked.Status.Ready,
		"gate must block when isBlocked returns true for active window")
}

// TestPolicyGateReconciler_ChangeWindowIsAllowed_MethodSyntax verifies that
// changewindow.isAllowed("name") returns true when the window is NOT active (#506).
func TestPolicyGateReconciler_ChangeWindowIsAllowed_MethodSyntax(t *testing.T) {
	scheme := newScheme()

	cw := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "business-hours", Namespace: "kardinal-system"},
		Spec: kardinalv1alpha1.ChangeWindowSpec{
			Type:  "blackout",
			Start: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
			End:   metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
	}
	gate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cw-method-allowed", Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/bundle":      "bundle-1",
				"kardinal.io/pipeline":    "my-app",
				"kardinal.io/environment": "prod",
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: `changewindow.isAllowed("business-hours")`},
	}
	bundle := makeBundle("bundle-1", "default")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gate, bundle, cw).WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cw-method-allowed", Namespace: "default"}}
	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var gotAllowed kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &gotAllowed))
	assert.True(t, gotAllowed.Status.Ready,
		"gate must pass when isAllowed returns true for inactive window")
}

// TestPolicyGateReconciler_ChangeWindowBothSyntaxesEquivalent verifies that
// map-access and method-call syntax are semantically equivalent for the same ChangeWindow (#506).
func TestPolicyGateReconciler_ChangeWindowBothSyntaxesEquivalent(t *testing.T) {
	// Build context with one active window.
	cwCtx := map[string]interface{}{"my-freeze": true}
	ctx := map[string]interface{}{
		"bundle":       map[string]interface{}{},
		"schedule":     map[string]interface{}{"isWeekend": false, "hour": 10, "dayOfWeek": "Tuesday"},
		"environment":  map[string]interface{}{"name": "prod"},
		"metrics":      map[string]interface{}{},
		"upstream":     map[string]interface{}{},
		"changewindow": cwCtx,
	}

	tableTests := []struct {
		name       string
		expression string
		wantPass   bool
		wantErr    bool
	}{
		// Map-access (legacy syntax — still works)
		{"map-blocked", `changewindow["my-freeze"]`, true, false},
		{"map-not-blocked", `!changewindow["my-freeze"]`, false, false},
		// Method syntax (new in #506)
		{"isBlocked-true", `changewindow.isBlocked("my-freeze")`, true, false},
		{"not-isBlocked", `!changewindow.isBlocked("my-freeze")`, false, false},
		{"isAllowed-false", `changewindow.isAllowed("my-freeze")`, false, false},
		{"not-isAllowed", `!changewindow.isAllowed("my-freeze")`, true, false},
		// A missing window is an error in every syntax, so the gate fails closed
		// (C04-gates-31): a typo must not silently allow a promotion.
		{"missing-map", `!changewindow["nonexistent"]`, false, true},
		{"missing-isBlocked", `!changewindow.isBlocked("nonexistent")`, false, true},
		{"missing-isAllowed", `changewindow.isAllowed("nonexistent")`, false, true},
	}

	for _, tt := range tableTests {
		t.Run(tt.name, func(t *testing.T) {
			pass, _, err := policygate.EvaluateForTest(tt.expression, ctx)
			if tt.wantErr {
				require.Error(t, err, "expression must fail closed: %s", tt.expression)
				assert.False(t, pass)
				return
			}
			require.NoError(t, err, "expression must evaluate without error: %s", tt.expression)
			assert.Equal(t, tt.wantPass, pass, "expression %q result mismatch", tt.expression)
		})
	}
}

// TestPolicyGateReconciler_ChangeWindowFailsClosed verifies that a gate that
// references a ChangeWindow blocks when the windows cannot be listed
// (C04-gates-02) or when the named window does not exist (C04-gates-31), and
// that a List failure does not affect gates that do not reference a window.
func TestPolicyGateReconciler_ChangeWindowFailsClosed(t *testing.T) {
	listErr := fmt.Errorf("changewindows.kardinal.io is forbidden")
	tests := []struct {
		name       string
		expression string
		failList   bool
		wantReady  bool
		wantReason string
	}{
		{
			name:       "list error blocks a map-syntax gate",
			expression: `!changewindow["holiday-freeze"]`,
			failList:   true,
			wantReason: "context error: changewindow: list ChangeWindows",
		},
		{
			name:       "list error blocks an isAllowed gate",
			expression: `changewindow.isAllowed("business-hours")`,
			failList:   true,
			wantReason: "forbidden",
		},
		{
			name:       "list error does not affect a gate without changewindow",
			expression: `bundle.type == "image"`,
			failList:   true,
			wantReady:  true,
		},
		{
			name:       "unknown window name blocks",
			expression: `!changewindow.isBlocked("holiday-frezee")`,
			wantReason: `unknown ChangeWindow "holiday-frezee"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := makeGateInstance("cw-gate", "default", "bundle-1", tt.expression, "5m")
			bundle := makeBundle("bundle-1", "default")
			b := fake.NewClientBuilder().WithScheme(newScheme()).
				WithObjects(gate, bundle).
				WithStatusSubresource(&kardinalv1alpha1.PolicyGate{})
			if tt.failList {
				b = b.WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, ok := list.(*kardinalv1alpha1.ChangeWindowList); ok {
							return listErr
						}
						return c.List(ctx, list, opts...)
					},
				})
			}
			c := b.WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cw-gate", Namespace: "default"}}
			_, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)

			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tt.wantReady, got.Status.Ready, "reason: %s", got.Status.Reason)
			if tt.wantReason != "" {
				assert.Contains(t, got.Status.Reason, tt.wantReason)
			}
		})
	}
}

// TestPolicyGateReconciler_RecurringChangeWindow verifies that a recurring
// ChangeWindow is evaluated from its schedule at the gate's evaluation time
// (C04-gates-03): before this fix a recurring window was never active. Every
// gate evaluation sees the invalid window; the ChangeWindow reconciler reports
// it, so the evaluation logs it at debug only.
func TestPolicyGateReconciler_RecurringChangeWindow(t *testing.T) {
	businessHours := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "business-hours"},
		Spec: kardinalv1alpha1.ChangeWindowSpec{
			Type: "recurring",
			Schedule: &kardinalv1alpha1.ChangeWindowSchedule{
				Timezone:     "UTC",
				AllowedDays:  []string{"Mon", "Tue", "Wed", "Thu", "Fri"},
				AllowedHours: "09:00-17:00",
			},
		},
	}
	broken := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "broken"},
		Spec: kardinalv1alpha1.ChangeWindowSpec{
			Type:     "recurring",
			Schedule: &kardinalv1alpha1.ChangeWindowSchedule{AllowedHours: "9am-5pm"},
		},
	}
	tests := []struct {
		name       string
		now        time.Time
		expression string
		wantReady  bool
	}{
		{"friday afternoon is allowed", time.Date(2026, 1, 2, 16, 30, 0, 0, time.UTC),
			`changewindow.isAllowed("business-hours")`, true},
		{"friday evening is blocked", time.Date(2026, 1, 2, 17, 0, 0, 0, time.UTC),
			`changewindow.isAllowed("business-hours")`, false},
		{"saturday is blocked (map syntax)", time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC),
			`!changewindow["business-hours"]`, false},
		{"monday morning is allowed (isBlocked)", time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
			`!changewindow.isBlocked("business-hours")`, true},
		{"invalid window blocks", time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC),
			`changewindow.isAllowed("broken")`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := makeGateInstance("cw-gate", "default", "bundle-1", tt.expression, "5m")
			bundle := makeBundle("bundle-1", "default")
			c := fake.NewClientBuilder().WithScheme(newScheme()).
				WithObjects(gate, bundle, businessHours.DeepCopy(), broken.DeepCopy()).
				WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
				Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return tt.now }
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cw-gate", Namespace: "default"}}
			var logs bytes.Buffer
			_, err = r.Reconcile(objectgonetest.Context(&logs), req)
			require.NoError(t, err)

			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tt.wantReady, got.Status.Ready, "reason: %s", got.Status.Reason)
			reported := 0
			for _, line := range strings.Split(logs.String(), "\n") {
				if strings.Contains(line, "invalid ChangeWindow") {
					reported++
					assert.Contains(t, line, `"level":"debug"`)
				}
			}
			assert.Equal(t, 1, reported, "the broken window is evaluated once")
		})
	}
}

// TestReconciler_OverrideActive verifies that a non-expired override causes
// the gate to pass immediately without evaluating CEL (K-09).
func TestReconciler_OverrideActive(t *testing.T) {
	bundle := makeBundle("app-v1", "default")
	future := metav1.NewTime(time.Now().Add(1 * time.Hour))
	gate := makeGateInstance("no-weekend-deploy", "default", "app-v1", "false", "5m")
	gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{
		{Reason: "P0 hotfix — incident #4521", Stage: "prod", ExpiresAt: future, CreatedBy: "alice"},
	}

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate, bundle).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.True(t, got.Status.Ready, "gate must pass when non-expired override exists")
	assert.Contains(t, got.Status.Reason, "OVERRIDDEN")
	assert.Contains(t, got.Status.Reason, "P0 hotfix")
}

// TestReconciler_OverrideExpired verifies that an expired override is ignored (K-09).
func TestReconciler_OverrideExpired(t *testing.T) {
	bundle := makeBundle("app-v1", "default")
	past := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	gate := makeGateInstance("no-weekend-deploy", "default", "app-v1", "false", "5m")
	gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{
		{Reason: "expired", Stage: "prod", ExpiresAt: past},
	}

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate, bundle).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.False(t, got.Status.Ready, "gate must block when override is expired")
}

// TestReconciler_OverrideWrongStage verifies that a stage-specific override
// does not affect other stages (K-09).
func TestReconciler_OverrideWrongStage(t *testing.T) {
	bundle := makeBundle("app-v1", "default")
	future := metav1.NewTime(time.Now().Add(1 * time.Hour))
	gate := makeGateInstance("no-weekend-deploy", "default", "app-v1", "false", "5m")
	gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{
		{Reason: "for uat", Stage: "uat", ExpiresAt: future},
	}

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate, bundle).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.False(t, got.Status.Ready, "override for 'uat' must not affect 'prod' gate")
}

// makeBundleWithHistory creates a Bundle with per-environment phase set, for history testing.
// pipelineName is set so the history lookup can find it.
func makeBundleWithHistory(name, ns, pipelineName string, envPhases map[string]string, createdAt metav1.Time) *kardinalv1alpha1.Bundle {
	envStatuses := make([]kardinalv1alpha1.EnvironmentStatus, 0, len(envPhases))
	for envName, phase := range envPhases {
		es := kardinalv1alpha1.EnvironmentStatus{Name: envName, Phase: phase}
		if phase == "Verified" {
			t := metav1.NewTime(createdAt.Time)
			es.HealthCheckedAt = &t
		}
		envStatuses = append(envStatuses, es)
	}
	return &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			CreationTimestamp: createdAt,
		},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: pipelineName,
		},
		Status: kardinalv1alpha1.BundleStatus{
			Environments: envStatuses,
		},
	}
}

// TestPolicyGateReconciler_CrossStageHistory_RecentSuccessCount verifies that
// upstream.staging.recentSuccessCount counts Verified bundles correctly (K-11).
func TestPolicyGateReconciler_CrossStageHistory_RecentSuccessCount(t *testing.T) {
	tests := []struct {
		name      string
		expr      string
		bundles   []map[string]string // per-bundle envPhase maps
		wantReady bool
	}{
		{
			name:      "3 successes required, 3 verified (current + 2 hist) — passes",
			expr:      `upstream.staging.recentSuccessCount >= 3`,
			bundles:   []map[string]string{{"staging": "Verified"}, {"staging": "Verified"}},
			wantReady: true,
		},
		{
			name:      "3 successes required, only current verified + 1 hist failed — blocks",
			expr:      `upstream.staging.recentSuccessCount >= 3`,
			bundles:   []map[string]string{{"staging": "Failed"}, {"staging": "Failed"}},
			wantReady: false,
		},
		{
			name:      "no historical bundles — current bundle counts as 1 success — blocks for >= 2",
			expr:      `upstream.staging.recentSuccessCount >= 2`,
			bundles:   nil,
			wantReady: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			allObjects := []client.Object{}

			// Current bundle being promoted
			currentBundle := makeBundleWithHistory("current", "default", "test-pipeline",
				map[string]string{"staging": "Verified"}, metav1.NewTime(now))
			currentBundle.Status.Environments = []kardinalv1alpha1.EnvironmentStatus{
				{Name: "staging", Phase: "Verified", SoakMinutes: 10},
			}
			allObjects = append(allObjects, currentBundle)

			// Historical bundles
			for i, phases := range tt.bundles {
				b := makeBundleWithHistory(
					fmt.Sprintf("hist-%d", i), "default", "test-pipeline",
					phases, metav1.NewTime(now.Add(-time.Duration(i+1)*time.Hour)),
				)
				allObjects = append(allObjects, b)
			}

			gate := makeGateInstance("history-gate", "default", "current", tt.expr, "5m")
			gate.Labels["kardinal.io/pipeline"] = "test-pipeline"
			allObjects = append(allObjects, gate)

			s := newScheme()
			c := fake.NewClientBuilder().WithScheme(s).
				WithObjects(allObjects...).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = time.Now
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

			_, reconcileErr := r.Reconcile(context.Background(), req)
			require.NoError(t, reconcileErr)

			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tt.wantReady, got.Status.Ready, tt.name)
		})
	}
}

// TestPolicyGateReconciler_CrossStageHistory_RecentFailureCount verifies that
// upstream.staging.recentFailureCount is correctly populated (K-11).
func TestPolicyGateReconciler_CrossStageHistory_RecentFailureCount(t *testing.T) {
	now := time.Now()
	currentBundle := makeBundleWithHistory("current", "default", "my-pipeline",
		map[string]string{}, metav1.NewTime(now))
	hist1 := makeBundleWithHistory("hist-1", "default", "my-pipeline",
		map[string]string{"staging": "Failed"}, metav1.NewTime(now.Add(-1*time.Hour)))
	hist2 := makeBundleWithHistory("hist-2", "default", "my-pipeline",
		map[string]string{"staging": "Failed"}, metav1.NewTime(now.Add(-2*time.Hour)))

	// Gate blocks if more than 1 recent failure
	gate := makeGateInstance("failure-gate", "default", "current",
		`upstream.staging.recentFailureCount < 2`, "5m")
	gate.Labels["kardinal.io/pipeline"] = "my-pipeline"

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(gate, currentBundle, hist1, hist2).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.False(t, got.Status.Ready, "gate must block when recentFailureCount >= 2")
}

// TestPolicyGateReconciler_CrossStageHistory_LastPromotedAt verifies that
// upstream.staging.lastPromotedAt is non-empty after a successful promotion (K-11).
func TestPolicyGateReconciler_CrossStageHistory_LastPromotedAt(t *testing.T) {
	now := time.Now()
	currentBundle := makeBundleWithHistory("current", "default", "my-pipeline",
		map[string]string{"staging": "Verified"}, metav1.NewTime(now))
	currentBundle.Status.Environments = []kardinalv1alpha1.EnvironmentStatus{
		{Name: "staging", Phase: "Verified", SoakMinutes: 5,
			HealthCheckedAt: func() *metav1.Time { t := metav1.NewTime(now); return &t }()},
	}

	// Gate passes only if staging was ever promoted
	gate := makeGateInstance("history-gate", "default", "current",
		`upstream.staging.lastPromotedAt != ""`, "5m")
	gate.Labels["kardinal.io/pipeline"] = "my-pipeline"

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(gate, currentBundle).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.True(t, got.Status.Ready, "gate must pass when staging was promoted")
}

// TestPolicyGateReconciler_CrossStageHistory_NoPipelineLabel verifies that when
// the gate has no pipeline label, the reconciler falls back gracefully (K-11).
func TestPolicyGateReconciler_CrossStageHistory_NoPipelineLabel(t *testing.T) {
	bundle := makeBundle("app-v1", "default")
	// Gate without pipeline label — should not crash
	gate := makeGateInstance("soak-gate", "default", "app-v1", `upstream.staging.soakMinutes >= 0`, "5m")
	delete(gate.Labels, "kardinal.io/pipeline")

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(gate, bundle).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr, "missing pipeline label must not crash reconciler")
}

// makePRStatus creates a PRStatus with the given bundle, environment and approval state.
func makePRStatus(name, ns, bundleName, envName string, approved bool, approvalCount int) *kardinalv1alpha1.PRStatus {
	return &kardinalv1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"kardinal.io/bundle":      bundleName,
				"kardinal.io/environment": envName,
			},
		},
		Spec: kardinalv1alpha1.PRStatusSpec{
			PRNumber: 42, Repo: "owner/repo",
			PRURL: "https://github.com/owner/repo/pull/42",
		},
		Status: kardinalv1alpha1.PRStatusStatus{
			Approved: approved, ApprovalCount: approvalCount, Open: true,
		},
	}
}

// TestPolicyGateReconciler_PRReviewGate_Approved verifies that bundle.pr["prod"].isApproved
// evaluates to true when the PRStatus CRD has status.approved=true (K-08).
func TestPolicyGateReconciler_PRReviewGate_Approved(t *testing.T) {
	bundle := makeBundle("app-v1", "default")
	prs := makePRStatus("prstatus-app-v1-prod", "default", "app-v1", "prod", true, 1)
	gate := makeGateInstance("pr-review-gate", "default", "app-v1",
		`bundle.pr["prod"].isApproved`, "5m")

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(gate, bundle, prs).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.True(t, got.Status.Ready, "gate must pass when PR is approved")
}

// TestPolicyGateReconciler_PRReviewGate_NotApproved verifies that bundle.pr["prod"].isApproved
// evaluates to false when the PRStatus has status.approved=false (K-08).
func TestPolicyGateReconciler_PRReviewGate_NotApproved(t *testing.T) {
	bundle := makeBundle("app-v1", "default")
	prs := makePRStatus("prstatus-app-v1-prod", "default", "app-v1", "prod", false, 0)
	gate := makeGateInstance("pr-review-gate", "default", "app-v1",
		`bundle.pr["prod"].isApproved`, "5m")

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(gate, bundle, prs).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.False(t, got.Status.Ready, "gate must block when PR is not approved")
}

// TestPolicyGateReconciler_PRReviewGate_OldPR covers B72: a PRStatus whose
// spec was pointed at a new PR (a recreated step) keeps the old PR's reviews
// until the PRStatus reconciler clears them; the gate does not count them.
func TestPolicyGateReconciler_PRReviewGate_OldPR(t *testing.T) {
	bundle := makeBundle("app-v1", "default")
	prs := makePRStatus("prstatus-app-v1-prod", "default", "app-v1", "prod", true, 2)
	prs.Generation, prs.Status.ObservedGeneration = 2, 1
	gate := makeGateInstance("pr-review-gate", "default", "app-v1",
		`bundle.pr["prod"].isApproved || bundle.pr["prod"].approvalCount > 0`, "5m")

	c := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(gate, bundle, prs).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.False(t, got.Status.Ready, "the old PR's approvals must not pass the gate")
}

// TestPolicyGateReconciler_PRReviewGate_MinReviewers verifies that
// bundle.pr["prod"].approvalCount >= 2 works correctly (K-08).
func TestPolicyGateReconciler_PRReviewGate_MinReviewers(t *testing.T) {
	tests := []struct {
		name          string
		approvalCount int
		wantReady     bool
	}{
		{"1 approval (need 2) — blocks", 1, false},
		{"2 approvals (need 2) — passes", 2, true},
		{"3 approvals (need 2) — passes", 3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle := makeBundle("app-v1", "default")
			prs := makePRStatus("prstatus-app-v1-prod", "default", "app-v1", "prod", tt.approvalCount >= 1, tt.approvalCount)
			gate := makeGateInstance("min-reviewers-gate", "default", "app-v1",
				`bundle.pr["prod"].approvalCount >= 2`, "5m")

			s := newScheme()
			c := fake.NewClientBuilder().WithScheme(s).
				WithObjects(gate, bundle, prs).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = time.Now
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

			_, reconcileErr := r.Reconcile(context.Background(), req)
			require.NoError(t, reconcileErr)

			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tt.wantReady, got.Status.Ready, tt.name)
		})
	}
}

// TestPolicyGateReconciler_PRReviewGate_NoPRStatus verifies that when no PRStatus
// exists for the stage, bundle.pr["prod"].isApproved defaults to false (K-08).
func TestPolicyGateReconciler_PRReviewGate_NoPRStatus(t *testing.T) {
	bundle := makeBundle("app-v1", "default")
	// No PRStatus objects — gate must block (fail-closed).
	gate := makeGateInstance("pr-review-gate", "default", "app-v1",
		`bundle.pr["prod"].isApproved`, "5m")

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(gate, bundle).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.False(t, got.Status.Ready, "gate must block when no PRStatus exists (fail-closed)")
}

// TestPolicyGateReconciler_EmitsBlockedEvent verifies that a Kubernetes Event is
// emitted when a gate first transitions to blocked (ready=false).
func TestPolicyGateReconciler_EmitsBlockedEvent(t *testing.T) {
	// Gate that will always block: expression evaluates to false (false literal).
	gate := makeGateInstance("always-block-gate", "default", "app-v1", "false", "5m")
	bundle := makeBundle("app-v1", "default")

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(gate, bundle).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()

	fakeRecorder := events.NewFakeRecorder(10)
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	r.Recorder = fakeRecorder

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}
	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	// Expect a Blocked warning event.
	select {
	case ev := <-fakeRecorder.Events:
		assert.Contains(t, ev, "Blocked")
		assert.Contains(t, ev, "Warning")
	default:
		t.Fatal("expected Blocked event to be emitted, got none")
	}
}

// TestPolicyGateReconciler_NoRecorderNoPanic verifies that a nil Recorder does not
// cause a panic.
func TestPolicyGateReconciler_NoRecorderNoPanic(t *testing.T) {
	gate := makeGateInstance("no-recorder-gate", "default", "app-v1", "true", "5m")
	bundle := makeBundle("app-v1", "default")

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(gate, bundle).WithStatusSubresource(gate).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()

	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = time.Now
	// Recorder is nil — must not panic.

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}
	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)
}

// TestPolicyGateReconciler_DeletedBeforeWrite: a PolicyGate deleted between
// its read and the reconciler's write (its status after an evaluation, the
// status of a template, or spec.generated on a long-named instance) ends the
// reconcile with no error, requeue or warn or error log.
func TestPolicyGateReconciler_DeletedBeforeWrite(t *testing.T) {
	long := makeGateInstance("app-v1-prod-"+strings.Repeat("p", 60), "default", "app-v1", "true", "5m")
	long.Labels["kardinal.io/gate-template"] = "t"
	template := makeGateInstance("no-weekend", "default", "", "!schedule.isWeekend", "")
	delete(template.Labels, "kardinal.io/bundle")
	tests := []struct {
		name string
		gate *kardinalv1alpha1.PolicyGate
	}{
		{name: "instance status", gate: makeGateInstance("app-v1-prod-ok", "default", "app-v1", "true", "5m")},
		{name: "template status", gate: template},
		{name: "spec.generated", gate: long},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isGate := func(obj client.Object) bool { _, ok := obj.(*kardinalv1alpha1.PolicyGate); return ok }
			c := fake.NewClientBuilder().WithScheme(newScheme()).
				WithObjects(tt.gate, makeBundle("app-v1", "default")).WithStatusSubresource(tt.gate).
				WithInterceptorFuncs(objectgonetest.DeleteOnWrite(t, isGate)).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			var logs bytes.Buffer
			res, err := r.Reconcile(objectgonetest.Context(&logs), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: tt.gate.Name, Namespace: "default"},
			})
			objectgonetest.AssertQuiet(t, res, err, &logs)
		})
	}
}

// TestReconciler_ChainedOverridesStopAtTheCap: a gate with 30 overrides
// chained a cap apart (createdAt T, T+24h, ...) passes for one cap only. The
// reconciler records when it first saw each entry in status.overrides, sets
// OverrideIgnored for the 29 dated after that, and blocks once the cap
// is over, though every entry passed admission.
func TestReconciler_ChainedOverridesStopAtTheCap(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	bundle := makeBundle("app-v1", "default")
	gate := makeGateInstance("no-weekend-deploy", "default", "app-v1", "false", "5m")
	for i := 0; i < 30; i++ {
		created := metav1.NewTime(t0.Add(time.Duration(i) * 24 * time.Hour))
		gate.Spec.Overrides = append(gate.Spec.Overrides, kardinalv1alpha1.PolicyGateOverride{
			Reason: fmt.Sprintf("chain %d", i), Stage: "prod", CreatedBy: "mallory",
			CreatedAt: &created, ExpiresAt: metav1.NewTime(created.Add(24 * time.Hour)),
		})
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.MaxOverride = 24 * time.Hour
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}
	get := func() kardinalv1alpha1.PolicyGate {
		var g kardinalv1alpha1.PolicyGate
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &g))
		return g
	}

	for _, step := range []struct {
		after time.Duration
		ready bool
	}{
		{0, true}, {12 * time.Hour, true}, {24*time.Hour + time.Minute, false},
		{48*time.Hour + time.Minute, false}, {29*24*time.Hour + time.Hour, false},
	} {
		now := t0.Add(step.after)
		r.NowFn = func() time.Time { return now }
		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		g := get()
		assert.Equal(t, step.ready, g.Status.Ready, "after %s: %s", step.after, g.Status.Reason)
		require.Len(t, g.Status.Overrides, 30)
		for _, o := range g.Status.Overrides {
			assert.True(t, o.FirstSeen.Time.Equal(t0), "firstSeen stays the first reconcile")
		}
		cond := meta.FindStatusCondition(g.Status.Conditions, "OverrideIgnored")
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Contains(t, cond.Message, "29 override(s) not counted")
	}
}
