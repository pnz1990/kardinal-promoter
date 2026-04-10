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

// TestPolicyGateReconciler_TemplateIgnored verifies templates (no bundle label) are skipped.
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
	assert.Equal(t, ctrl.Result{}, result, "template must return empty result (no requeue)")
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
