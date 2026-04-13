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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	celpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/cel"
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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).
		Build()

	// Tuesday
	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return tuesday },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).
		Build()

	// Saturday
	saturday := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return saturday },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate). // bundle NOT added
		WithStatusSubresource(gate).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(template).
		WithStatusSubresource(template).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) },
	}

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
}

// TestPolicyGateReconciler_RequeueAfterRecheckInterval verifies RequeueAfter.
func TestPolicyGateReconciler_RequeueAfterRecheckInterval(t *testing.T) {
	gate := makeGateInstance("recheck-gate", "default", "nginx-demo-v1", "!schedule.isWeekend", "10m")
	bundle := makeBundle("nginx-demo-v1", "default")

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) },
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "recheck-gate", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Equal(t, 10*time.Minute, result.RequeueAfter, "RequeueAfter must match recheckInterval")
}

// TestPolicyGateReconciler_GateNotFound verifies no error when gate is deleted.
func TestPolicyGateReconciler_GateNotFound(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	r := &policygate.Reconciler{Client: c, Evaluator: eval, NowFn: time.Now}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).
		Build()

	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return tuesday },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "idem-gate", Namespace: "default"}}

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "idem-gate", Namespace: "default"}, &got))
	assert.True(t, got.Status.Ready)
}

// makeMetricCheck builds a MetricCheck with a given status result.
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
			LastValue: lastValue,
			Result:    result,
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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle, mc).
		WithStatusSubresource(gate, mc).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle, mc).
		WithStatusSubresource(gate, mc).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate, bundle).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return now },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate, bundle).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return now },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle, mcSameNS, mcOtherNS).
		WithStatusSubresource(gate, mcSameNS, mcOtherNS).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).
		Build()

	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return tuesday },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate, bundle).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) },
	}

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

// TestPolicyGateReconciler_BundleUpstreamSoakMinutes_Passes verifies that
// bundle.upstreamSoakMinutes is the max SoakMinutes across all upstream envs.
// This is the convenience shorthand documented in docs/policy-gates.md and used
// in examples/quickstart/policy-gates.yaml.
func TestPolicyGateReconciler_BundleUpstreamSoakMinutes_Passes(t *testing.T) {
	now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	healthCheckedAt := metav1.NewTime(now.Add(-50 * time.Minute))

	bundle := makeBundleWithEnvironments("nginx-demo-v1", "default", []kardinalv1alpha1.EnvironmentStatus{
		{Name: "test", Phase: "Verified", HealthCheckedAt: &healthCheckedAt, SoakMinutes: 50},
		{Name: "uat", Phase: "Verified", HealthCheckedAt: &healthCheckedAt, SoakMinutes: 45},
	})
	gate := makeGateInstance("soak-gate", "default", "nginx-demo-v1",
		`bundle.upstreamSoakMinutes >= 30`, "1m")

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate, bundle).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return now },
	}

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "soak-gate", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "soak-gate", Namespace: "default"}, &got))
	assert.True(t, got.Status.Ready,
		"gate must pass: bundle.upstreamSoakMinutes = max(50,45) = 50 >= 30")
}

// TestPolicyGateReconciler_BundleUpstreamSoakMinutes_Blocks verifies that
// bundle.upstreamSoakMinutes blocks when all upstream envs have soak < threshold.
func TestPolicyGateReconciler_BundleUpstreamSoakMinutes_Blocks(t *testing.T) {
	now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	healthCheckedAt := metav1.NewTime(now.Add(-10 * time.Minute))

	bundle := makeBundleWithEnvironments("nginx-demo-v1", "default", []kardinalv1alpha1.EnvironmentStatus{
		{Name: "test", Phase: "Verified", HealthCheckedAt: &healthCheckedAt, SoakMinutes: 5},
		{Name: "uat", Phase: "Verified", HealthCheckedAt: &healthCheckedAt, SoakMinutes: 10},
	})
	gate := makeGateInstance("soak-gate", "default", "nginx-demo-v1",
		`bundle.upstreamSoakMinutes >= 30`, "1m")

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate, bundle).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return now },
	}

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "soak-gate", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "soak-gate", Namespace: "default"}, &got))
	assert.False(t, got.Status.Ready,
		"gate must block: bundle.upstreamSoakMinutes = max(5,10) = 10 < 30")
}

// TestPolicyGateReconciler_InvalidCEL_SurfacesErrorInStatus verifies that a PolicyGate
// instance with syntactically invalid CEL does NOT apply silently — the reconciler sets
// status.ready=false and populates status.reason with the compilation error message.
// This is Option B from issue #315: operators can see the error via kubectl describe pg.
func TestPolicyGateReconciler_InvalidCEL_SurfacesErrorInStatus(t *testing.T) {
	gate := makeGateInstance("bad-cel-gate", "default", "nginx-demo-v1",
		`this is not valid CEL !!!`, "5m")
	bundle := makeBundle("nginx-demo-v1", "default")

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate, bundle).
		WithStatusSubresource(gate).
		Build()

	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return tuesday },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate).
		WithStatusSubresource(gate).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) },
	}

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

	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	s := newScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gate).
		WithStatusSubresource(gate).
		Build()

	r := &policygate.Reconciler{
		Client:    c,
		Evaluator: eval,
		NowFn:     func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) },
	}

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
		WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}).
		Build()

	env1, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval1 := celpkg.NewEvaluator(env1)
	r := &policygate.Reconciler{Client: c, Evaluator: eval1}
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
		WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}).
		Build()

	env2, err2 := celpkg.NewCELEnvironment()
	require.NoError(t, err2)
	eval2 := celpkg.NewEvaluator(env2)
	r := &policygate.Reconciler{Client: c, Evaluator: eval2}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cw-gate-ok", Namespace: "default"}}
	_, reconcileErr2 := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr2)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))

	assert.True(t, got.Status.Ready, "gate must be ready when no active change window")
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
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate, bundle).WithStatusSubresource(gate).Build()
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)

	r := &policygate.Reconciler{Client: c, Evaluator: celpkg.NewEvaluator(env), NowFn: time.Now}
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
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate, bundle).WithStatusSubresource(gate).Build()
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)

	r := &policygate.Reconciler{Client: c, Evaluator: celpkg.NewEvaluator(env), NowFn: time.Now}
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
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate, bundle).WithStatusSubresource(gate).Build()
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)

	r := &policygate.Reconciler{Client: c, Evaluator: celpkg.NewEvaluator(env), NowFn: time.Now}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}

	_, reconcileErr := r.Reconcile(context.Background(), req)
	require.NoError(t, reconcileErr)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.False(t, got.Status.Ready, "override for 'uat' must not affect 'prod' gate")
}
