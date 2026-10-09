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
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

func decision(user, d string, groups ...string) kardinalv1alpha1.GateApproval {
	if groups == nil {
		groups = []string{}
	}
	return kardinalv1alpha1.GateApproval{Bundle: "nginx-demo-v1", Environment: "prod", User: user, Groups: groups, Decision: d}
}

// TestPolicyGateReconciler_Approvals evaluates approval gates: the quorum of
// distinct allowed approvers, allowed users and groups, excludeAuthor, a
// counted reject blocking, decisions for another environment ignored, the
// approvals.* CEL context with and without a policy, and status.approvals.
// Each case is reconciled twice (idempotent).
func TestPolicyGateReconciler_Approvals(t *testing.T) {
	policy := &kardinalv1alpha1.GateApprovalPolicy{Required: 2, AllowedUsers: []string{"carol"},
		AllowedGroups: []string{"release-managers"}, ExcludeAuthor: true}
	other := decision("dave", "approve", "release-managers")
	other.Environment = "test"
	cases := []struct {
		name       string
		expr       string
		policy     *kardinalv1alpha1.GateApprovalPolicy
		approvals  []kardinalv1alpha1.GateApproval
		wantReady  bool
		wantReason string
		counted    map[string]bool
	}{
		{name: "no approvals yet", expr: "true", policy: policy,
			wantReason: "waiting for approvals: 0 of 2"},
		{name: "one of two", expr: "true", policy: policy, approvals: []kardinalv1alpha1.GateApproval{decision("alice", "approve", "release-managers")},
			wantReason: "waiting for approvals: 1 of 2 (alice)", counted: map[string]bool{"alice": true}},
		{name: "quorum by group and user", expr: "true", policy: policy,
			approvals: []kardinalv1alpha1.GateApproval{decision("alice", "approve", "release-managers"), decision("carol", "approve")},
			wantReady: true, wantReason: "approved by alice, carol (2 of 2)", counted: map[string]bool{"alice": true, "carol": true}},
		{name: "not allowed, author, other environment and a duplicate do not count", expr: "true", policy: policy,
			approvals: []kardinalv1alpha1.GateApproval{decision("alice", "approve", "release-managers"), decision("alice", "approve", "release-managers"),
				decision("mallory", "approve", "devs"), decision("ci-bot", "approve", "release-managers"), other},
			wantReason: "waiting for approvals: 1 of 2 (alice)",
			counted:    map[string]bool{"alice": true, "mallory": false, "ci-bot": false, "dave": false}},
		{name: "a counted reject blocks", expr: "true", policy: policy,
			approvals: []kardinalv1alpha1.GateApproval{decision("alice", "approve", "release-managers"), decision("carol", "approve"),
				{Bundle: "nginx-demo-v1", Environment: "prod", User: "bob", Groups: []string{"release-managers"}, Decision: "reject", Comment: "breaks the API"}},
			wantReason: "rejected by bob (breaks the API)", counted: map[string]bool{"alice": true, "carol": true, "bob": true}},
		{name: "the expression still decides", expr: "false", policy: policy,
			approvals:  []kardinalv1alpha1.GateApproval{decision("alice", "approve", "release-managers"), decision("carol", "approve")},
			wantReason: "= false"},
		{name: "approvals.count without a policy counts everyone", expr: "approvals.count >= 1 && !approvals.rejected",
			approvals: []kardinalv1alpha1.GateApproval{decision("anyone", "approve")}, wantReady: true, wantReason: "= true"},
		{name: "approvals.count is 0 with none", expr: "approvals.count >= 1", wantReason: "= false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := makeGateInstance("two-approvers", "default", "nginx-demo-v1", tc.expr, "5m")
			gate.Spec.Approval = tc.policy
			gate.Spec.Approvals = tc.approvals
			bundle := makeBundle("nginx-demo-v1", "default")
			bundle.Status.Phase = "Promoting"
			bundle.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{Author: "ci-bot"}
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC) }
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "two-approvers", Namespace: "default"}}
			var first []kardinalv1alpha1.GateApprovalStatus
			for i := range 2 {
				_, err = r.Reconcile(context.Background(), req)
				require.NoError(t, err)
				var got kardinalv1alpha1.PolicyGate
				require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
				assert.Equal(t, tc.wantReady, got.Status.Ready)
				assert.Contains(t, got.Status.Reason, tc.wantReason)
				if i == 0 {
					first = got.Status.Approvals
					continue
				}
				assert.Equal(t, first, got.Status.Approvals, "status.approvals is stable across reconciles")
				counted := map[string]bool{}
				for _, rec := range got.Status.Approvals {
					counted[rec.User] = rec.Counted
					assert.NotNil(t, rec.FirstSeenAt)
					if !rec.Counted {
						assert.NotEmpty(t, rec.Reason, "%s: why it does not count", rec.User)
					}
				}
				if tc.counted != nil {
					assert.Equal(t, tc.counted, counted)
				}
			}
		})
	}
}

// TestPolicyGateReconciler_ApprovalFirstSeenKept: status.approvals keeps the
// time a decision was first seen when another decision arrives.
func TestPolicyGateReconciler_ApprovalFirstSeenKept(t *testing.T) {
	gate := makeGateInstance("ok", "default", "nginx-demo-v1", "true", "5m")
	gate.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 2}
	gate.Spec.Approvals = []kardinalv1alpha1.GateApproval{decision("alice", "approve")}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Promoting"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	t0 := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	r.NowFn = func() time.Time { return t0 }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "ok", Namespace: "default"}}
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	got.Spec.Approvals = append(got.Spec.Approvals, decision("bob", "approve"))
	require.NoError(t, c.Update(context.Background(), &got))
	r.NowFn = func() time.Time { return t0.Add(time.Hour) }
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	require.Len(t, got.Status.Approvals, 2)
	assert.True(t, got.Status.Approvals[0].FirstSeenAt.Equal(&metav1.Time{Time: t0}), "alice first seen at t0")
	assert.True(t, got.Status.Approvals[1].FirstSeenAt.Equal(&metav1.Time{Time: t0.Add(time.Hour)}), "bob first seen an hour later")
	assert.True(t, got.Status.Ready)
}
