// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestChangeRequestChanged: a poll that changes nothing a gate reads
// (observedAt, validUntil of a fresh reading) passes nothing; a new state,
// ticket, timedOut, verified, approvers or provider state, a spec change,
// and a reading that makes a stale one fresh pass.
func TestChangeRequestChanged(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *metav1.Time { v := metav1.NewTime(t0.Add(d)); return &v }
	base := func() *kardinalv1alpha1.ChangeRequest {
		return &kardinalv1alpha1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Generation: 1},
			Status: kardinalv1alpha1.ChangeRequestStatus{State: "pending", ProviderState: "Assess",
				Ticket:     &kardinalv1alpha1.ChangeRequestTicket{Key: "CHG1", URL: "u"},
				ObservedAt: at(0), ValidUntil: at(2 * time.Minute)}}
	}
	yes := true
	for name, tc := range map[string]struct {
		mut  func(*kardinalv1alpha1.ChangeRequest)
		want bool
	}{
		"a poll within validUntil": {func(n *kardinalv1alpha1.ChangeRequest) {
			n.Status.ObservedAt, n.Status.ValidUntil = at(time.Minute), at(3*time.Minute)
		}, false},
		"a poll after validUntil (stale to fresh)": {func(n *kardinalv1alpha1.ChangeRequest) {
			n.Status.ObservedAt, n.Status.ValidUntil = at(2*time.Minute), at(4*time.Minute)
		}, true},
		"state":          {func(n *kardinalv1alpha1.ChangeRequest) { n.Status.State = "approved" }, true},
		"provider state": {func(n *kardinalv1alpha1.ChangeRequest) { n.Status.ProviderState = "Authorize" }, true},
		"ticket": {func(n *kardinalv1alpha1.ChangeRequest) {
			n.Status.Ticket = &kardinalv1alpha1.ChangeRequestTicket{Key: "CHG2"}
		}, true},
		"timedOut": {func(n *kardinalv1alpha1.ChangeRequest) { n.Status.TimedOut = true }, true},
		"verified": {func(n *kardinalv1alpha1.ChangeRequest) { n.Status.Verified = &yes }, true},
		"approvers": {func(n *kardinalv1alpha1.ChangeRequest) {
			n.Status.Approvers = []kardinalv1alpha1.ChangeRequestApprover{{Name: "jane"}}
		}, true},
		"generation": {func(n *kardinalv1alpha1.ChangeRequest) { n.Generation = 2 }, true},
		"conditions only": {func(n *kardinalv1alpha1.ChangeRequest) {
			n.Status.Conditions = []metav1.Condition{{Type: "ProviderError", Status: "True"}}
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			o, n := base(), base()
			tc.mut(n)
			assert.Equal(t, tc.want, changeRequestChanged.Update(event.UpdateEvent{ObjectOld: o, ObjectNew: n}))
		})
	}
	assert.True(t, changeRequestChanged.Create(event.CreateEvent{Object: base()}))
	assert.True(t, changeRequestChanged.Delete(event.DeleteEvent{Object: base()}))
}

// TestChangeRequestRequests: a ChangeRequest event enqueues the gate
// instances of its namespace that read changeRequests, and no others.
func TestChangeRequestRequests(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(scheme))
	gate := func(name, ns, expr string) *kardinalv1alpha1.PolicyGate {
		return &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			Labels: map[string]string{labelBundle: "b1"}}, Spec: kardinalv1alpha1.PolicyGateSpec{Expression: expr}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		gate("cab", "team-a", `changeRequests["prod-change"].state == "approved"`),
		gate("weekend", "team-a", `!schedule.isWeekend`),
		gate("cab-b", "team-b", `changeRequests["prod-change"].state == "approved"`),
	).Build()
	r := &Reconciler{Client: c}
	got := r.changeRequestRequests(context.Background(), &kardinalv1alpha1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-change-prod--b1", Namespace: "team-a"}})
	assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "cab"}}}, got)
}
