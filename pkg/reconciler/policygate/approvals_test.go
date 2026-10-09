// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
			bundle.Annotations = map[string]string{"kardinal.io/created-by": "ci-bot"}
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

// TestPolicyGateReconciler_ApprovalHeld: an approval gate blocks, whatever
// the approvals, when excludeAuthor cannot be enforced (the Bundle has no
// verified creator, or its creator is a kardinal component: a Subscription,
// the Bundle API's static token, the controller) or when it got more
// Approvals than it counts.
func TestPolicyGateReconciler_ApprovalHeld(t *testing.T) {
	many := make([]kardinalv1alpha1.GateApproval, 101)
	for i := range many {
		many[i] = decision(fmt.Sprintf("u%03d", i), "approve")
	}
	cases := []struct {
		name      string
		policy    *kardinalv1alpha1.GateApprovalPolicy
		creator   string
		approvals []kardinalv1alpha1.GateApproval
		want      string
		ready     bool
	}{
		{name: "excludeAuthor without a creator", policy: &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true},
			approvals: []kardinalv1alpha1.GateApproval{decision("alice", "approve")},
			want:      "excludeAuthor cannot be enforced: the Bundle has no verified creator (annotation kardinal.io/created-by)"},
		{name: "excludeAuthor on a Subscription's Bundle", policy: &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true},
			creator: "subscription:app", approvals: []kardinalv1alpha1.GateApproval{decision("alice", "approve")},
			want: `excludeAuthor cannot be enforced: the Bundle was created by the kardinal component "subscription:app", not a person`},
		{name: "excludeAuthor on a Bundle API Bundle", policy: &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true},
			creator: "bundle-api", approvals: []kardinalv1alpha1.GateApproval{decision("alice", "approve")},
			want: `created by the kardinal component "bundle-api"`},
		{name: "excludeAuthor on an automatic rollback", policy: &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true},
			creator: "kardinal-controller", approvals: []kardinalv1alpha1.GateApproval{decision("alice", "approve")},
			want: `created by the kardinal component "kardinal-controller"`},
		{name: "excludeAuthor with a creator", policy: &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}, creator: "bob",
			approvals: []kardinalv1alpha1.GateApproval{decision("alice", "approve")}, want: "approved by alice (1 of 1)", ready: true},
		{name: "without excludeAuthor no creator is needed", policy: &kardinalv1alpha1.GateApprovalPolicy{Required: 1},
			approvals: []kardinalv1alpha1.GateApproval{decision("alice", "approve")}, want: "approved by alice (1 of 1)", ready: true},
		{name: "too many approvals", policy: &kardinalv1alpha1.GateApprovalPolicy{Required: 1}, approvals: many,
			want: "more than 100 Approvals for this Bundle in prod"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := makeGateInstance("g", "default", "nginx-demo-v1", "true", "5m")
			gate.Spec.Approval, gate.Spec.Approvals = tc.policy, tc.approvals
			bundle := makeBundle("nginx-demo-v1", "default")
			bundle.Status.Phase = "Promoting"
			if tc.creator != "" {
				bundle.Annotations = map[string]string{"kardinal.io/created-by": tc.creator}
			}
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "g", Namespace: "default"}})
			require.NoError(t, err)
			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "g", Namespace: "default"}, &got))
			assert.Equal(t, tc.ready, got.Status.Ready)
			assert.Contains(t, got.Status.Reason, tc.want)
		})
	}
}

// TestPolicyGateReconciler_ApprovalAudits: a decision that appears in the
// gate's approvals writes ApprovalRecorded, one that leaves it (the Approval
// was deleted) ApprovalRevoked, each once.
func TestPolicyGateReconciler_ApprovalAudits(t *testing.T) {
	gate := makeGateInstance("g", "default", "nginx-demo-v1", "true", "5m")
	gate.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 2}
	gate.Spec.Approvals = []kardinalv1alpha1.GateApproval{decision("alice", "approve")}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Promoting"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "g", Namespace: "default"}}
	actions := func() []string {
		var list kardinalv1alpha1.AuditEventList
		require.NoError(t, c.List(context.Background(), &list))
		var out []string
		for _, a := range list.Items {
			if strings.HasPrefix(a.Spec.Action, "Approval") {
				out = append(out, a.Spec.Action+" "+a.Spec.Message)
			}
		}
		sort.Strings(out)
		return out
	}
	for range 2 {
		_, err = r.Reconcile(context.Background(), req)
		require.NoError(t, err)
	}
	assert.Equal(t, []string{"ApprovalRecorded approve by alice recorded on gate g (counted)"}, actions())

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	got.Spec.Approvals = nil
	require.NoError(t, c.Update(context.Background(), &got))
	for range 2 {
		_, err = r.Reconcile(context.Background(), req)
		require.NoError(t, err)
	}
	assert.Equal(t, []string{
		"ApprovalRecorded approve by alice recorded on gate g (counted)",
		"ApprovalRevoked approve by alice revoked on gate g (counted)",
	}, actions())
}

// TestPolicyGateReconciler_ApprovalAuditOutbox (#1552): an ApprovalRecorded
// AuditEvent whose create fails with an etcd timeout stays in the gate's
// status.pendingAuditEvents, stored with the approval it records, and is
// written by a later reconcile, once.
//
// Covers AUDIT-OUTBOX-01.
func TestPolicyGateReconciler_ApprovalAuditOutbox(t *testing.T) {
	gate := makeGateInstance("g", "default", "nginx-demo-v1", "true", "5m")
	gate.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 2}
	gate.Spec.Approvals = []kardinalv1alpha1.GateApproval{decision("alice", "approve")}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Promoting"
	fail := true
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if ae, ok := obj.(*kardinalv1alpha1.AuditEvent); ok && fail && ae.Spec.Action == "ApprovalRecorded" {
				return apierrors.NewInternalError(errors.New("etcdserver: request timed out"))
			}
			return cl.Create(ctx, obj, opts...)
		}}).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "g", Namespace: "default"}}
	approvals := func() (n int, pending []string) {
		var list kardinalv1alpha1.AuditEventList
		require.NoError(t, c.List(context.Background(), &list))
		for _, a := range list.Items {
			if a.Spec.Action == "ApprovalRecorded" {
				n++
			}
		}
		var got kardinalv1alpha1.PolicyGate
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		for _, p := range got.Status.PendingAuditEvents {
			pending = append(pending, p.Spec.Action)
		}
		return n, pending
	}

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	n, pending := approvals()
	assert.Zero(t, n)
	assert.Contains(t, pending, "ApprovalRecorded", "kept in the outbox")

	fail = false
	for range 2 {
		_, err = r.Reconcile(context.Background(), req)
		require.NoError(t, err)
	}
	n, pending = approvals()
	assert.Equal(t, 1, n, "written once")
	assert.NotContains(t, pending, "ApprovalRecorded")
}
