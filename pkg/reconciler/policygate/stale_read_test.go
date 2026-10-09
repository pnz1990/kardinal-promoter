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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// TestPolicyGateReconciler_StaleReadWritesNoDuplicateAudit (#1513): a
// reconcile that reads the gate from a stale cache, from before the flip a
// newer reconcile already wrote, would detect that flip again and audit it a
// second time milliseconds later. The status patch carries the
// resourceVersion it read, so the stale reconcile gets a Conflict, writes
// nothing and is requeued; the next reconcile on the current gate writes
// nothing either (unchanged result).
//
// Covers GATE-AUDIT-03.
func TestPolicyGateReconciler_StaleReadWritesNoDuplicateAudit(t *testing.T) {
	tue := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	gate := makeGateInstance("prod-no-weekend", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
	base := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(gate, makeBundle("nginx-demo-v1", "default")).
		WithStatusSubresource(gate).Build()
	key := types.NamespacedName{Namespace: "default", Name: gate.Name}

	var stale *kardinalv1alpha1.PolicyGate
	c := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if g, ok := obj.(*kardinalv1alpha1.PolicyGate); ok && stale != nil && k == key {
				stale.DeepCopyInto(g)
				return nil
			}
			return cl.Get(ctx, k, obj, opts...)
		},
	})
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	now := tue
	r.NowFn = func() time.Time { return now }

	var before kardinalv1alpha1.PolicyGate
	require.NoError(t, base.Get(context.Background(), key, &before))
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	require.Len(t, auditEvents(t, base), 1, "the first evaluation is audited")

	// The cache still holds the gate from before the first evaluation.
	stale = before.DeepCopy()
	now = tue.Add(20 * time.Millisecond)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err, "a Conflict is requeued, not returned")
	assert.Positive(t, res.RequeueAfter, "the stale reconcile is requeued")
	assert.Len(t, auditEvents(t, base), 1, "the stale reconcile writes no second record")

	stale = nil
	now = tue.Add(40 * time.Millisecond)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Len(t, auditEvents(t, base), 1, "the current gate has the result already")
}

// TestPolicyGateReconciler_StaleReadWritesNoDuplicateApprovalAudit (#1513,
// #1449): a reconcile that reads the gate from before a newer reconcile
// recorded an approval would see the approval as new and audit it again
// (under another first-seen time). The approvals patch carries the
// resourceVersion it read and the audit follows the write, so the stale
// reconcile writes no second ApprovalRecorded.
func TestPolicyGateReconciler_StaleReadWritesNoDuplicateApprovalAudit(t *testing.T) {
	gate := makeGateInstance("g", "default", "nginx-demo-v1", "true", "5m")
	gate.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 2}
	gate.Spec.Approvals = []kardinalv1alpha1.GateApproval{{Bundle: "nginx-demo-v1", Environment: "prod",
		User: "alice", Groups: []string{}, Decision: "approve"}}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Promoting"
	base := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).
		WithStatusSubresource(gate, bundle).Build()
	key := types.NamespacedName{Namespace: "default", Name: gate.Name}
	var stale *kardinalv1alpha1.PolicyGate
	c := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if g, ok := obj.(*kardinalv1alpha1.PolicyGate); ok && stale != nil && k == key {
				stale.DeepCopyInto(g)
				return nil
			}
			return cl.Get(ctx, k, obj, opts...)
		},
	})
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	r.NowFn = func() time.Time { return now }
	approvalAudits := func() int {
		n := 0
		for _, a := range auditEvents(t, base) {
			if a.Spec.Action == "ApprovalRecorded" {
				n++
			}
		}
		return n
	}

	var before kardinalv1alpha1.PolicyGate
	require.NoError(t, base.Get(context.Background(), key, &before))
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	require.Equal(t, 1, approvalAudits())

	stale = before.DeepCopy()
	now = now.Add(time.Second)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err, "a Conflict is requeued, not returned")
	assert.Positive(t, res.RequeueAfter)
	assert.Equal(t, 1, approvalAudits(), "the stale reconcile writes no second record")

	stale = nil
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Equal(t, 1, approvalAudits())
}

// TestPolicyGateReconciler_ApprovalsConflictRequeued (QA #1510): a Conflict
// on the approvals status patch is not an error: the reconcile is requeued
// and writes no audit record; the next one records and audits the approval.
func TestPolicyGateReconciler_ApprovalsConflictRequeued(t *testing.T) {
	gate := makeGateInstance("g", "default", "nginx-demo-v1", "true", "5m")
	gate.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	gate.Spec.Approvals = []kardinalv1alpha1.GateApproval{{Bundle: "nginx-demo-v1", Environment: "prod",
		User: "alice", Groups: []string{}, Decision: "approve"}}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Promoting"
	conflicts := 1
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, cl client.Client, sub string,
			obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if g, ok := obj.(*kardinalv1alpha1.PolicyGate); ok && conflicts > 0 && len(g.Status.Approvals) > 0 {
				conflicts--
				return apierrors.NewConflict(schema.GroupResource{Group: "kardinal.io", Resource: "policygates"}, g.Name,
					errors.New("the object has been modified"))
			}
			return cl.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}}).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	key := types.NamespacedName{Namespace: "default", Name: "g"}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err, "a Conflict is requeued, not returned")
	assert.Positive(t, res.RequeueAfter)
	var audits kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &audits))
	for _, a := range audits.Items {
		assert.NotEqual(t, "ApprovalRecorded", a.Spec.Action, "nothing audited before the write lands")
	}
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), key, &got))
	require.Len(t, got.Status.Approvals, 1)
	assert.True(t, got.Status.Ready)
}
