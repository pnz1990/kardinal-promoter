// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

func overrideAudits(t *testing.T, c client.Client) []kardinalv1alpha1.AuditEvent {
	t.Helper()
	var list kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.MatchingLabels{"kardinal.io/action": "GateOverridden"}))
	return list.Items
}

// TestPolicyGateReconciler_OverrideAudited writes one GateOverridden
// AuditEvent per override, naming who created it, and records the override
// in status.observedOverrides; reconciling again writes nothing new
// (idempotent), and a second override gets its own record. It is written for
// a settled Bundle too, whose gate is otherwise left alone.
func TestPolicyGateReconciler_OverrideAudited(t *testing.T) {
	for _, phase := range []string{"Promoting", "Superseded"} {
		t.Run(phase, func(t *testing.T) {
			now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
			created := metav1.NewTime(now.Add(-time.Minute))
			gate := makeGateInstance("no-weekend", "default", "nginx-demo-v1", "false", "5m")
			since := metav1.NewTime(now.Add(-time.Hour))
			gate.Status.OverridesVerifiedSince = &since
			gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{{
				Reason: "INC-1 hotfix", Stage: "prod", ExpiresAt: metav1.NewTime(now.Add(time.Hour)),
				CreatedAt: &created, CreatedBy: "oidc:alice@example.com",
			}}
			bundle := makeBundle("nginx-demo-v1", "default")
			bundle.Status.Phase = phase
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).
				WithStatusSubresource(gate, bundle).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return now }
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "no-weekend", Namespace: "default"}}

			for range 2 {
				_, err = r.Reconcile(context.Background(), req)
				require.NoError(t, err)
			}
			audits := overrideAudits(t, c)
			require.Len(t, audits, 1)
			ae := audits[0]
			assert.Equal(t, "GateOverridden", ae.Spec.Action)
			assert.Equal(t, "Success", ae.Spec.Outcome)
			assert.Equal(t, "nginx-demo-v1", ae.Spec.BundleName)
			assert.Equal(t, "nginx-demo", ae.Spec.PipelineName)
			assert.Equal(t, "prod", ae.Spec.Environment)
			assert.True(t, ae.Spec.Timestamp.Equal(&metav1.Time{Time: now}), "the record carries when the controller first saw it, not the writer's createdAt")
			assert.Equal(t, "gate no-weekend overridden by oidc:alice@example.com for prod until 2026-10-08T13:00:00Z: INC-1 hotfix",
				ae.Spec.Message)
			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			require.Len(t, got.Status.ObservedOverrides, 1)

			got.Spec.Overrides = append(got.Spec.Overrides, kardinalv1alpha1.PolicyGateOverride{
				Reason: "second", ExpiresAt: metav1.NewTime(now.Add(time.Hour)), CreatedBy: "bob", CreatedAt: &created})
			require.NoError(t, c.Update(context.Background(), &got))
			_, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			audits = overrideAudits(t, c)
			require.Len(t, audits, 2)
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Len(t, got.Status.ObservedOverrides, 2)
		})
	}
}

// TestPolicyGateReconciler_OverrideAuditRetried: an AuditEvent that cannot be
// written is not recorded as observed, so the next reconcile writes it; the
// gate is evaluated (and passes on the override) either way.
func TestPolicyGateReconciler_OverrideAuditRetried(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	gate := makeGateInstance("hold", "default", "nginx-demo-v1", "false", "5m")
	gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{{
		Reason: "hotfix", ExpiresAt: metav1.NewTime(now.Add(time.Hour)), CreatedBy: "alice"}}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Promoting"
	fail := true
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if ae, ok := obj.(*kardinalv1alpha1.AuditEvent); ok && fail && ae.Spec.Action == "GateOverridden" {
				return errors.New("quota exceeded")
			}
			return cl.Create(ctx, obj, opts...)
		}}).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return now }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "hold", Namespace: "default"}}

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Empty(t, got.Status.ObservedOverrides)
	assert.True(t, got.Status.Ready, "a failed audit write does not block the override")
	assert.Empty(t, overrideAudits(t, c))

	fail = false
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Len(t, overrideAudits(t, c), 1)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Len(t, got.Status.ObservedOverrides, 1)
}

// TestPolicyGateReconciler_OverrideUnverifiedOnUpgrade: overrides already on a
// gate the controller sees for the first time with identity checks (an
// upgrade from a release without them) are audited and shown as unverified,
// and the gate gets status.overridesVerifiedSince; an override created after
// that is verified.
func TestPolicyGateReconciler_OverrideUnverifiedOnUpgrade(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	old := metav1.NewTime(now.Add(-24 * time.Hour))
	gate := makeGateInstance("hold", "default", "nginx-demo-v1", "false", "5m")
	gate.Status.LastEvaluatedAt = &old // evaluated by the release before the upgrade
	gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{{
		Reason: "old", ExpiresAt: metav1.NewTime(now.Add(time.Hour)), CreatedBy: "laptop-user", CreatedAt: &old}}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Promoting"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return now }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "hold", Namespace: "default"}}
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	audits := overrideAudits(t, c)
	require.Len(t, audits, 1)
	assert.Contains(t, audits[0].Spec.Message, "laptop-user (unverified: recorded before kardinal checked override identity)")
	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	require.NotNil(t, got.Status.OverridesVerifiedSince)
	assert.True(t, got.Status.OverridesVerifiedSince.Equal(&metav1.Time{Time: now}))
	assert.Contains(t, got.Status.Reason, "OVERRIDDEN by laptop-user (unverified): old")

	later := metav1.NewTime(now.Add(time.Minute))
	got.Spec.Overrides = append(got.Spec.Overrides, kardinalv1alpha1.PolicyGateOverride{
		Reason: "new", ExpiresAt: metav1.NewTime(now.Add(2 * time.Hour)), CreatedBy: "oidc:alice", CreatedAt: &later})
	require.NoError(t, c.Update(context.Background(), &got))
	r.NowFn = func() time.Time { return now.Add(2 * time.Minute) }
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	audits = overrideAudits(t, c)
	require.Len(t, audits, 2)
	var verified []string
	for _, a := range audits {
		if !strings.Contains(a.Spec.Message, "unverified") {
			verified = append(verified, a.Spec.Message)
		}
	}
	require.Len(t, verified, 1)
	assert.Contains(t, verified[0], "overridden by oidc:alice for every stage")
}
