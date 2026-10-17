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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// TestPolicyGateReconciler_ChangeRequests (spec 23 §6): a gate reads a
// per-promotion ChangeRequest template's name as the instance for its own
// Bundle and environment, from the instance's status only. No instance, a
// reading past validUntil (state "unknown"), another Bundle's or
// environment's instance, two claimants, and a name with no template all
// block; the reason names the ticket.
func TestPolicyGateReconciler_ChangeRequests(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	fresh, past := metav1.NewTime(now.Add(time.Minute)), metav1.NewTime(now.Add(-time.Second))
	instance := func(name, bundle, env, state string, until metav1.Time, mut ...func(*kardinalv1alpha1.ChangeRequest)) client.Object {
		cr := &kardinalv1alpha1.ChangeRequest{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: map[string]string{
				graph.LabelChangeRequestTemplate: "prod-change", "kardinal.io/bundle": bundle, "kardinal.io/environment": env}},
			Spec: kardinalv1alpha1.ChangeRequestSpec{Provider: "servicenow"},
			Status: kardinalv1alpha1.ChangeRequestStatus{State: state, ProviderState: "Scheduled / " + state, ValidUntil: &until,
				Ticket:    &kardinalv1alpha1.ChangeRequestTicket{Key: "CHG0031337", URL: "https://snow/x"},
				Approvers: []kardinalv1alpha1.ChangeRequestApprover{{Name: "jane"}}},
		}
		for _, m := range mut {
			m(cr)
		}
		return cr
	}
	template := &kardinalv1alpha1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "prod-change", Namespace: "default"},
		Spec: kardinalv1alpha1.ChangeRequestSpec{PerPromotion: true, Provider: "servicenow"}}
	const approved = `changeRequests["prod-change"].state == "approved"`
	unverified := func(cr *kardinalv1alpha1.ChangeRequest) { v := false; cr.Status.Verified = &v }
	timedOut := func(cr *kardinalv1alpha1.ChangeRequest) { cr.Status.TimedOut = true }

	tests := []struct {
		name       string
		expr       string
		objs       []client.Object
		wantReady  bool
		wantReason string
	}{
		{"approved and fresh passes", approved + ` && !changeRequests["prod-change"].stale`,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "approved", fresh)}, true, ""},
		{"pending blocks and the reason names the ticket", approved,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "pending", fresh)}, false,
			"change request prod-change: ticket CHG0031337 is pending (Scheduled / pending)"},
		{"no instance yet blocks", approved, []client.Object{template}, false, "change request prod-change has no ticket yet"},
		{"no instance is stale", `changeRequests["prod-change"].stale && changeRequests["prod-change"].state == ""`,
			[]client.Object{template}, true, ""},
		{"a stale approval blocks state == approved", approved,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "approved", past)}, false,
			"ticket CHG0031337 was not read since its validUntil (stale)"},
		{"validUntil now is stale", approved,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "approved", metav1.NewTime(now))}, false, "(stale)"},
		{"stale reads unknown", `changeRequests["prod-change"].state == "unknown" && changeRequests["prod-change"].ticket == "CHG0031337"`,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "approved", past)}, true, ""},
		{"another Bundle's instance does not count", approved,
			[]client.Object{template, instance("i2", "nginx-demo-v0", "prod", "approved", fresh)}, false, "has no ticket yet"},
		{"another environment's instance does not count", approved,
			[]client.Object{template, instance("i3", "nginx-demo-v1", "uat", "approved", fresh)}, false, "has no ticket yet"},
		{"two claimants block", approved, []client.Object{template, instance("i1", "nginx-demo-v1", "prod", "approved", fresh),
			instance("i4", "nginx-demo-v1", "prod", "approved", fresh)}, false, "has no ticket yet"},
		{"a name with no template blocks", `changeRequests["nope"].state == "approved"`,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "approved", fresh)}, false, ""},
		{"an instance is not exposed by its own name", `!("i1" in changeRequests)`,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "approved", fresh)}, true, ""},
		{"an unverified link says so", approved + ` && changeRequests["prod-change"].verified`,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "approved", fresh, unverified)}, false, ", not verified"},
		{"verified, approvers, url, timedOut", `changeRequests["prod-change"].verified && changeRequests["prod-change"].approvers == ["jane"] && ` +
			`changeRequests["prod-change"].url == "https://snow/x" && changeRequests["prod-change"].timedOut`,
			[]client.Object{template, instance("i1", "nginx-demo-v1", "prod", "approved", fresh, timedOut)}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := makeGateInstance("cab-gate", "default", "nginx-demo-v1", tt.expr, "1m")
			objs := append([]client.Object{gate, makeBundle("nginx-demo-v1", "default")}, tt.objs...)
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objs...).WithStatusSubresource(gate).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return now }
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cab-gate", Namespace: "default"}}
			_, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tt.wantReady, got.Status.Ready, got.Status.Reason)
			assert.Contains(t, got.Status.Reason, tt.wantReason)
		})
	}
}
