// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

var crObserved = metav1.NewTime(time.Date(2026, 10, 14, 9, 5, 0, 0, time.UTC))

// changeRequestInstance is a ChangeRequest instance the Graph made from
// template for bundle (UID uid) in env.
func changeRequestInstance(name, template, bundle, uid, env string, st v1alpha1.ChangeRequestStatus) *v1alpha1.ChangeRequest {
	return &v1alpha1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: map[string]string{
			"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": bundle, "kardinal.io/environment": env,
			graph.LabelBundleUID: uid, graph.LabelChangeRequestTemplate: template}},
		Spec:   v1alpha1.ChangeRequestSpec{Provider: "servicenow"},
		Status: st,
	}
}

// changeRequestWorld is the ChangeRequests of a cluster around bundle-1's
// step in env: two of its own (an opened ServiceNow change and a Jira
// issue linked but not verified), and four that are not its own (another
// environment's, another Bundle's, an earlier Bundle of the same name, a
// template).
func changeRequestWorld(env string) []client.Object {
	no := false
	valid := metav1.NewTime(crObserved.Add(10 * time.Minute))
	approved := v1alpha1.ChangeRequestStatus{
		Ticket: &v1alpha1.ChangeRequestTicket{Key: "CHG0031337", ID: "sys1", URL: "https://acme.service-now.com/c/sys1"},
		State:  "approved", ProviderState: "Scheduled",
		Approvers:  []v1alpha1.ChangeRequestApprover{{Name: "Jane Smith", Decision: "approved"}},
		ObservedAt: &crObserved, ValidUntil: &valid,
	}
	linked := v1alpha1.ChangeRequestStatus{
		Ticket: &v1alpha1.ChangeRequestTicket{Key: "OPS-42", URL: "https://acme.atlassian.net/browse/OPS-42"},
		State:  "approved", ProviderState: "Approved", Verified: &no, VerifiedReason: "the issue was created before the version",
		ObservedAt: &crObserved,
	}
	// Named to sort after prod-change's instance: the order is the template's.
	jira := changeRequestInstance("z-jira-change-"+env+"--bundle-1", "jira-change", "bundle-1", "bundle-uid", env, linked)
	jira.Spec.Provider = "jira"
	return []client.Object{
		changeRequestInstance("prod-change-"+env+"--bundle-1", "prod-change", "bundle-1", "bundle-uid", env, approved),
		jira,
		changeRequestInstance("prod-change-other--bundle-1", "prod-change", "bundle-1", "bundle-uid", "other", approved),
		changeRequestInstance("prod-change-"+env+"--bundle-2", "prod-change", "bundle-2", "uid-2", env, approved),
		changeRequestInstance("prod-change-"+env+"--bundle-1-old", "prod-change", "bundle-1", "old-uid", env, approved),
		&v1alpha1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "prod-change", Namespace: "default",
			Labels: map[string]string{"kardinal.io/bundle": "bundle-1", "kardinal.io/environment": env}},
			Spec: v1alpha1.ChangeRequestSpec{PerPromotion: true, Provider: "servicenow"}},
	}
}

func createAll(t *testing.T, c client.Client, objs []client.Object) {
	t.Helper()
	for _, o := range objs {
		require.NoError(t, c.Create(context.Background(), o))
	}
}

// TestReceiptJourney_ChangeRequests (spec 23 section 9): the decision
// snapshot holds the step's own change request tickets (its Bundle, by
// UID, and environment; instances only), sorted by template, as they were
// when it started: a ticket closed afterwards is still approved in the
// receipt. A ticket kardinal opened is verified; a linked one says why not.
// The approval's fields reach the receipt.
//
// Covers CHANGE-REQUEST-11.
func TestReceiptJourney_ChangeRequests(t *testing.T) {
	f := newReceiptFixture(t, true, nil)
	createAll(t, f.c, changeRequestWorld("test"))
	var gate v1alpha1.PolicyGate
	require.NoError(t, f.c.Get(context.Background(), f.gate, &gate))
	gate.Status.Approvals[0].Fields = map[string]string{"ticketID": "CHG0031337"}
	require.NoError(t, f.c.Status().Update(context.Background(), &gate))

	s := f.reconcile(t)
	require.Equal(t, "Promoting", s.Status.State)
	require.NotNil(t, s.Status.Decision)
	valid := metav1.NewTime(crObserved.Add(10 * time.Minute))
	assert.Equal(t, []v1alpha1.DecisionChangeRequest{
		{Name: "jira-change", Instance: "z-jira-change-test--bundle-1", Provider: "jira",
			Ticket: &v1alpha1.ChangeRequestTicket{Key: "OPS-42", URL: "https://acme.atlassian.net/browse/OPS-42"},
			State:  "approved", ProviderState: "Approved", VerifiedReason: "the issue was created before the version", ObservedAt: &crObserved},
		{Name: "prod-change", Instance: "prod-change-test--bundle-1", Provider: "servicenow",
			Ticket: &v1alpha1.ChangeRequestTicket{Key: "CHG0031337", ID: "sys1", URL: "https://acme.service-now.com/c/sys1"},
			State:  "approved", ProviderState: "Scheduled", Verified: true,
			Approvers:  []v1alpha1.ChangeRequestApprover{{Name: "Jane Smith", Decision: "approved"}},
			ObservedAt: &crObserved, ValidUntil: &valid},
	}, normalizeTimes(s.Status.Decision.ChangeRequests))

	// Closed after the step started: the receipt keeps what the step started on.
	var cr v1alpha1.ChangeRequest
	require.NoError(t, f.c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "prod-change-test--bundle-1"}, &cr))
	cr.Status.State, cr.Status.ProviderState = "closed", "Closed"
	require.NoError(t, f.c.Update(context.Background(), &cr))

	f.toVerified(t)
	_, st := f.receipt(t)
	r := st.Predicate.Kardinal
	require.Len(t, r.ChangeRequests, 2)
	assert.Equal(t, receipt.ChangeRequest{Name: "prod-change", Instance: "prod-change-test--bundle-1", Provider: "servicenow",
		Ticket: "CHG0031337", URL: "https://acme.service-now.com/c/sys1", State: "approved", ProviderState: "Scheduled",
		Verified: true, Approvers: []string{"Jane Smith"}, ObservedAt: "2026-10-14T09:05:00Z", ValidUntil: "2026-10-14T09:15:00Z"},
		r.ChangeRequests[1])
	assert.False(t, r.ChangeRequests[0].Verified)
	assert.Equal(t, "the issue was created before the version", r.ChangeRequests[0].VerifiedReason)
	assert.Equal(t, map[string]string{"ticketID": "CHG0031337"}, r.Gates[0].Approvals[0].Fields)
}

// normalizeTimes drops the monotonic and location parts the fake client's
// JSON round trip changes, so times compare by instant.
func normalizeTimes(in []v1alpha1.DecisionChangeRequest) []v1alpha1.DecisionChangeRequest {
	for i := range in {
		for _, p := range []**metav1.Time{&in[i].ObservedAt, &in[i].ValidUntil} {
			if *p != nil {
				t := metav1.NewTime((*p).UTC())
				*p = &t
			}
		}
	}
	return in
}

// TestReceiptJourney_ChangeRequestsReadAtVerified: a step that started
// without a snapshot reads its change requests at Verified, as its gates.
//
// Covers CHANGE-REQUEST-11.
func TestReceiptJourney_ChangeRequestsReadAtVerified(t *testing.T) {
	f := newReceiptFixture(t, false, nil)
	createAll(t, f.c, changeRequestWorld("test"))
	s := f.reconcile(t)
	require.Equal(t, "Promoting", s.Status.State)
	assert.Nil(t, s.Status.Decision)
	var cr v1alpha1.ChangeRequest
	require.NoError(t, f.c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "prod-change-test--bundle-1"}, &cr))
	cr.Status.State = "closed"
	require.NoError(t, f.c.Update(context.Background(), &cr))
	f.r.Receipts = &promotionstep.ReceiptConfig{ClusterID: "cluster-1"}
	f.toVerified(t)
	_, st := f.receipt(t)
	r := st.Predicate.Kardinal
	assert.False(t, r.DecisionSnapshot)
	require.Len(t, r.ChangeRequests, 2)
	assert.Equal(t, []string{"jira-change", "prod-change"}, []string{r.ChangeRequests[0].Name, r.ChangeRequests[1].Name})
	assert.Equal(t, "closed", r.ChangeRequests[1].State, "read at Verified")
}

// TestReceiptJourney_ChangeRequestsListFails: a cluster without the
// ChangeRequest CRD has no change requests and the step starts as before;
// any other list error is retried: the step stays Pending rather than
// start without its tickets in the snapshot.
func TestReceiptJourney_ChangeRequestsListFails(t *testing.T) {
	for name, tc := range map[string]struct {
		err   error
		state string
	}{
		"no CRD":       {&meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "kardinal.io", Kind: "ChangeRequest"}}, "Promoting"},
		"a list error": {errors.New("cache not synced"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newReceiptFixture(t, true, nil)
			c := interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*v1alpha1.ChangeRequestList); ok {
						return tc.err
					}
					return c.List(ctx, list, opts...)
				}})
			f.r.Client = c
			_, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: f.step})
			var s v1alpha1.PromotionStep
			require.NoError(t, f.c.Get(context.Background(), f.step, &s))
			if tc.state == "Promoting" {
				require.NoError(t, err)
				assert.Equal(t, "Promoting", s.Status.State)
				require.NotNil(t, s.Status.Decision)
				assert.Empty(t, s.Status.Decision.ChangeRequests)
				return
			}
			require.ErrorContains(t, err, "list change requests: cache not synced")
			assert.Empty(t, s.Status.State, "still Pending, retried")
			assert.Nil(t, s.Status.Decision)
		})
	}
}

// TestPRBody_ChangeRequestsFromInstances: a pr-review step's PR body lists
// its own change request tickets, read from the instances when it opens the
// PR, and none of another environment's or version's.
//
// Covers CHANGE-REQUEST-11.
func TestPRBody_ChangeRequestsFromInstances(t *testing.T) {
	scm := &mockSCM{merged: true, prURL: "https://github.com/test/repo/pull/1", prNumber: 1}
	f := newReceiptFixture(t, false, func(_ *v1alpha1.Bundle, ps *v1alpha1.PromotionStep) {
		ps.Spec.Environment, ps.Spec.StepType = "prod", "pr-review"
		ps.Labels["kardinal.io/environment"] = "prod"
	})
	f.r.SCM = scm
	createAll(t, f.c, changeRequestWorld("prod"))
	for i := 0; i < 20 && scm.openCalled == 0; i++ {
		f.reconcile(t)
	}
	require.Len(t, scm.bodies, 1, "the step opened its PR")
	body := scm.bodies[0]
	assert.Contains(t, body, "### Change Requests\n\n| Change request | Ticket | Provider | State | Verified | Approvers | Read at |\n|---|---|---|---|---|---|---|\n"+
		"| jira-change | [OPS-42](https://acme.atlassian.net/browse/OPS-42) | jira | approved (Approved) | no: the issue was created before the version | — | 2026-10-14T09:05Z |\n"+
		"| prod-change | [CHG0031337](https://acme.service-now.com/c/sys1) | servicenow | approved (Scheduled) | yes | Jane Smith | 2026-10-14T09:05Z |\n")
	assert.Equal(t, 2, strings.Count(body, "| prod-change |")+strings.Count(body, "| jira-change |"), "only the step's own tickets")
}
