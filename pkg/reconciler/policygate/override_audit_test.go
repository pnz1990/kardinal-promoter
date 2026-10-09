// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"errors"
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
			assert.True(t, ae.Spec.Timestamp.Equal(&created), "the record carries the override's createdAt")
			assert.Equal(t, "gate no-weekend overridden by oidc:alice@example.com for prod until 2026-10-08T13:00:00Z: INC-1 hotfix",
				ae.Spec.Message)
			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			require.Len(t, got.Status.ObservedOverrides, 1)

			got.Spec.Overrides = append(got.Spec.Overrides, kardinalv1alpha1.PolicyGateOverride{
				Reason: "second", ExpiresAt: metav1.NewTime(now.Add(time.Hour)), CreatedBy: "bob"})
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
