// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// evaluateGate reconciles gate (with the objects in objs) on Saturday
// 2026-04-11 and returns the gate as stored.
func evaluateGate(t *testing.T, gate *kardinalv1alpha1.PolicyGate, objs ...client.Object) kardinalv1alpha1.PolicyGate {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(append(objs, gate)...).WithStatusSubresource(gate).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC) }
	key := types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), key, &got))
	return got
}

// TestPolicyGateReconciler_BlockedReasonLeadsWithMessage (GATE-MESSAGE-01): a
// gate whose expression is false says why in its own words. status.reason and
// the Ready condition lead with spec.message; the evaluated version and CEL
// result follow in parentheses.
func TestPolicyGateReconciler_BlockedReasonLeadsWithMessage(t *testing.T) {
	const msg = "Production deployments are blocked on weekends"
	gate := makeGateInstance("no-weekend", "default", "app-v1", "!schedule.isWeekend", "5m")
	gate.Spec.Message = msg
	got := evaluateGate(t, gate, makeBundleWithImage("app-v1", "default", "ghcr.io/example/app", "1.29.0"))

	want := msg + " (bundle.version=1.29.0: !schedule.isWeekend = false)"
	assert.False(t, got.Status.Ready)
	assert.Equal(t, want, got.Status.Reason)
	if c := meta.FindStatusCondition(got.Status.Conditions, "Ready"); assert.NotNil(t, c) {
		assert.Equal(t, want, c.Message)
		assert.Equal(t, "Blocked", c.Reason)
	}
}

// TestPolicyGateReconciler_ReasonWithoutMessageUnchanged: the reasons that do
// not lead with the message are written as before: a gate with no message,
// one that passes, and the error and override reasons whose prefixes the CLI
// reads.
func TestPolicyGateReconciler_ReasonWithoutMessageUnchanged(t *testing.T) {
	const msg = "Production deployments are blocked on weekends"
	bundle := func() *kardinalv1alpha1.Bundle {
		return makeBundleWithImage("app-v1", "default", "ghcr.io/example/app", "1.29.0")
	}
	gate := func(expr, message string) *kardinalv1alpha1.PolicyGate {
		g := makeGateInstance("g", "default", "app-v1", expr, "5m")
		g.Spec.Message = message
		return g
	}

	got := evaluateGate(t, gate("!schedule.isWeekend", ""), bundle())
	assert.Equal(t, "bundle.version=1.29.0: !schedule.isWeekend = false", got.Status.Reason, "no message")

	got = evaluateGate(t, gate("schedule.isWeekend", msg), bundle())
	assert.True(t, got.Status.Ready)
	assert.Equal(t, "bundle.version=1.29.0: schedule.isWeekend = true", got.Status.Reason, "passing")

	got = evaluateGate(t, gate(`bundle.labels["missing"] == "x"`, msg), bundle())
	assert.False(t, got.Status.Ready)
	assert.True(t, strings.HasPrefix(got.Status.Reason, "bundle.version=1.29.0: CEL evaluation error: "), got.Status.Reason)
	assert.NotContains(t, got.Status.Reason, msg, "evaluation error")

	got = evaluateGate(t, gate("!schedule.isWeekend", msg))
	assert.True(t, strings.HasPrefix(got.Status.Reason, "context error: "), got.Status.Reason)
	assert.NotContains(t, got.Status.Reason, msg, "context error")

	overridden := gate("false", msg)
	overridden.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{{
		Reason: "hotfix", ExpiresAt: metav1.NewTime(time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)), CreatedBy: "alice",
	}}
	got = evaluateGate(t, overridden, bundle())
	assert.True(t, strings.HasPrefix(got.Status.Reason, "OVERRIDDEN by alice: hotfix"), got.Status.Reason)
}
