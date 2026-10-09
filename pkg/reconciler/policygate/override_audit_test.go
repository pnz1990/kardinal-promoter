// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"errors"
	"fmt"
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

// policyBound is an IdentityPolicy that is (or is not) bound.
type policyBound bool

func (p policyBound) Active(context.Context) bool { return bool(p) }

func overrideAudits(t *testing.T, c client.Client) []kardinalv1alpha1.AuditEvent {
	t.Helper()
	var list kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.MatchingLabels{"kardinal.io/action": "GateOverridden"}))
	return list.Items
}

// TestPolicyGateReconciler_OverrideAudited writes one GateOverridden
// AuditEvent per override, naming who created it, and records the override
// in status.overrides (verified, audited); reconciling again writes nothing new
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
			r.IdentityPolicy = policyBound(true)
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
			require.Len(t, got.Status.Overrides, 1)
			rec := got.Status.Overrides[0]
			assert.True(t, rec.Verified && rec.Audited, "%+v", rec)
			assert.True(t, rec.FirstSeen.Equal(&metav1.Time{Time: now}))

			got.Spec.Overrides = append(got.Spec.Overrides, kardinalv1alpha1.PolicyGateOverride{
				Reason: "second", ExpiresAt: metav1.NewTime(now.Add(time.Hour)), CreatedBy: "bob", CreatedAt: &created})
			require.NoError(t, c.Update(context.Background(), &got))
			_, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			audits = overrideAudits(t, c)
			require.Len(t, audits, 2)
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Len(t, got.Status.Overrides, 2)
		})
	}
}

// TestPolicyGateReconciler_OverrideAuditRetried: an AuditEvent that cannot be
// written leaves the override recorded but not audited, so the next reconcile
// writes it with the same firstSeen; the gate is evaluated (and passes on the
// override) either way.
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
	require.Len(t, got.Status.Overrides, 1)
	assert.True(t, got.Status.Overrides[0].Audited, "the record is stored in the outbox (#1552)")
	require.Len(t, got.Status.PendingAuditEvents, 1, "and kept there while the create fails")
	assert.Equal(t, "GateOverridden", got.Status.PendingAuditEvents[0].Spec.Action)
	assert.True(t, got.Status.Ready, "a failed audit write does not block the override")
	assert.Empty(t, overrideAudits(t, c))

	fail = false
	r.NowFn = func() time.Time { return now.Add(time.Minute) }
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	audits := overrideAudits(t, c)
	require.Len(t, audits, 1)
	assert.True(t, audits[0].Spec.Timestamp.Equal(&metav1.Time{Time: now}), "stamped with the recorded firstSeen")
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	require.Len(t, got.Status.Overrides, 1)
	assert.True(t, got.Status.Overrides[0].Audited)
	assert.Empty(t, got.Status.PendingAuditEvents, "written: the outbox is empty")
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
	r.IdentityPolicy = policyBound(true)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "hold", Namespace: "default"}}
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	audits := overrideAudits(t, c)
	require.Len(t, audits, 1)
	assert.Contains(t, audits[0].Spec.Message, "laptop-user (unverified: the identity admission policy did not check it)")
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

// TestPolicyGateReconciler_OverrideUnverifiedWithoutPolicy (#1503): an
// override first seen while the identity admission policy is not bound is
// recorded unverified, in the record, the reason and the AuditEvent; it stays
// unverified when the policy appears later.
func TestPolicyGateReconciler_OverrideUnverifiedWithoutPolicy(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	gate := makeGateInstance("hold", "default", "nginx-demo-v1", "false", "5m")
	gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{{
		Reason: "hotfix", ExpiresAt: metav1.NewTime(now.Add(time.Hour)), CreatedBy: "alice"}}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Promoting"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return now }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "hold", Namespace: "default"}}
	for _, bound := range []bool{false, true} {
		r.IdentityPolicy = policyBound(bound)
		_, err = r.Reconcile(context.Background(), req)
		require.NoError(t, err)
	}
	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	require.Len(t, got.Status.Overrides, 1)
	assert.False(t, got.Status.Overrides[0].Verified)
	assert.False(t, policygate.OverrideVerified(&got, &got.Spec.Overrides[0]))
	assert.Contains(t, got.Status.Reason, "OVERRIDDEN by alice (unverified): hotfix")
	audits := overrideAudits(t, c)
	require.Len(t, audits, 1)
	assert.Contains(t, audits[0].Spec.Message, "alice (unverified")
}

// TestPolicyGateReconciler_OverrideCap: an override counts from when the
// controller first saw it, for at most the cap: the gate reason and the
// AuditEvent name the capped end; a createdAt dated more than 5 minutes after
// firstSeen is not counted (OverrideIgnored); removing an entry and adding it
// again keeps its record, so it cannot restart the cap.
//
// Covers GATE-OVERRIDE-CAP-01.
func TestPolicyGateReconciler_OverrideCap(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	gate := makeGateInstance("hold", "default", "nginx-demo-v1", "false", "5m")
	week := kardinalv1alpha1.PolicyGateOverride{Reason: "long", ExpiresAt: metav1.NewTime(now.Add(7 * 24 * time.Hour)), CreatedBy: "alice"}
	gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{week}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Promoting"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	clock := now
	r.NowFn = func() time.Time { return clock }
	r.MaxOverride = 2 * time.Hour
	r.IdentityPolicy = policyBound(true)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "hold", Namespace: "default"}}
	get := func() kardinalv1alpha1.PolicyGate {
		var g kardinalv1alpha1.PolicyGate
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &g))
		return g
	}
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	g := get()
	assert.True(t, g.Status.Ready)
	assert.Contains(t, g.Status.Reason, "(expires 2026-10-08T14:00Z)", "capped at firstSeen + 2h")
	audits := overrideAudits(t, c)
	require.Len(t, audits, 1)
	assert.Contains(t, audits[0].Spec.Message, "until 2026-10-08T14:00:00Z")

	// Removed and added again an hour later: same record, same end.
	g.Spec.Overrides = nil
	require.NoError(t, c.Update(context.Background(), &g))
	clock = now.Add(30 * time.Minute)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	g = get()
	require.Len(t, g.Status.Overrides, 1, "records are never dropped")
	g.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{week}
	require.NoError(t, c.Update(context.Background(), &g))
	clock = now.Add(time.Hour)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	g = get()
	assert.Contains(t, g.Status.Reason, "(expires 2026-10-08T14:00Z)")
	assert.Len(t, overrideAudits(t, c), 1, "not audited again")

	// Past the cap the gate evaluates normally.
	clock = now.Add(2*time.Hour + time.Second)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	g = get()
	assert.False(t, g.Status.Ready)
	assert.NotContains(t, g.Status.Reason, "OVERRIDDEN")

	// A future-dated entry is not counted.
	future := metav1.NewTime(clock.Add(time.Hour))
	g.Spec.Overrides = append(g.Spec.Overrides, kardinalv1alpha1.PolicyGateOverride{
		Reason: "chained", ExpiresAt: metav1.NewTime(clock.Add(3 * time.Hour)), CreatedBy: "alice", CreatedAt: &future})
	require.NoError(t, c.Update(context.Background(), &g))
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	g = get()
	assert.False(t, g.Status.Ready, "a future-dated override does not count")
	cond := findCondition(g.Status.Conditions, "OverrideIgnored")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Contains(t, cond.Message, "chained")
}

// TestPolicyGateReconciler_OverrideRecordLimit: past 200 records a new
// override is not recorded and not counted, so status.overrides stays within
// the CRD's maxItems.
func TestPolicyGateReconciler_OverrideRecordLimit(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	gate := makeGateInstance("hold", "default", "nginx-demo-v1", "false", "5m")
	for i := range 200 {
		gate.Status.Overrides = append(gate.Status.Overrides, kardinalv1alpha1.OverrideRecord{
			Key: fmt.Sprintf("old%03d", i), FirstSeen: metav1.NewTime(now.Add(-time.Hour)), Audited: true})
	}
	since := metav1.NewTime(now.Add(-time.Hour))
	gate.Status.OverridesVerifiedSince = &since
	gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{{
		Reason: "one more", ExpiresAt: metav1.NewTime(now.Add(time.Hour)), CreatedBy: "alice"}}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Promoting"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return now }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "hold", Namespace: "default"}}
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var g kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &g))
	assert.Len(t, g.Status.Overrides, 200)
	assert.False(t, g.Status.Ready)
	cond := findCondition(g.Status.Conditions, "OverrideIgnored")
	require.NotNil(t, cond)
	assert.Contains(t, cond.Message, "more than 200 overrides")
	assert.Empty(t, overrideAudits(t, c))
}

func findCondition(conds []metav1.Condition, typ string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == typ {
			return &conds[i]
		}
	}
	return nil
}
