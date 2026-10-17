// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// approvalsOf lists the Approvals of bundle in ns, by spec.user.
func approvalsOf(t *testing.T, c client.Client, bundle string) map[string]v1alpha1.Approval {
	t.Helper()
	var list v1alpha1.ApprovalList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace(ns),
		client.MatchingLabels{"kardinal.io/bundle": bundle}))
	out := map[string]v1alpha1.Approval{}
	for _, a := range list.Items {
		out[a.Spec.User] = a
	}
	return out
}

// TestRecordApproval_OthersApprovalsAreNotTheirs (#1593 QA): RecordApproval
// only ever replaces or revokes the requesting user's own Approval. Bob's
// revoke of Alice's approval is refused (there is nothing of his to revoke)
// and leaves hers; his approve and reject add his own decision and leave
// hers; an object that takes the name Alice's Approval would get, but is
// someone else's, is not hers to revoke either. kardinal approve and the UI
// (POST /api/v1/ui/approvals) both go through RecordApproval.
func TestRecordApproval_OthersApprovalsAreNotTheirs(t *testing.T) {
	ctx := context.Background()
	p := pipeline("app", "test", "prod")
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: ns, UID: "uid-app-v2"},
		Spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "image"}, Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	c := newClient(t, p, b)
	req := func(user, decision string, revoke bool) lifecycle.ApprovalRequest {
		return lifecycle.ApprovalRequest{Namespace: ns, Bundle: "app-v2", Environment: "prod", User: user,
			Decision: decision, Revoke: revoke}
	}

	_, alice, err := lifecycle.RecordApproval(ctx, c, req("alice", lifecycle.DecisionApprove, false))
	require.NoError(t, err)

	_, _, err = lifecycle.RecordApproval(ctx, c, req("bob", "", true))
	require.Error(t, err)
	assert.True(t, errors.Is(err, lifecycle.ErrNotFound), err)
	assert.Contains(t, err.Error(), "bob has no Approval of app-v2 for prod to revoke")
	got := approvalsOf(t, c, "app-v2")
	require.Contains(t, got, "alice", "bob's revoke leaves alice's approval")
	assert.Equal(t, alice.Name, got["alice"].Name)
	assert.Len(t, got, 1)

	for _, d := range []string{lifecycle.DecisionApprove, lifecycle.DecisionReject} {
		_, _, err = lifecycle.RecordApproval(ctx, c, req("bob", d, false))
		require.NoError(t, err)
		got = approvalsOf(t, c, "app-v2")
		assert.Equal(t, lifecycle.DecisionApprove, got["alice"].Spec.Decision, "bob's %s leaves alice's decision", d)
		assert.Equal(t, alice.Name, got["alice"].Name)
		assert.Equal(t, d, got["bob"].Spec.Decision)
		assert.Len(t, got, 2)
	}

	// Mallory's object under the name carol's Approval would get is not
	// carol's: her revoke finds nothing and leaves it, and her approve
	// records her own under another name.
	squat := &v1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{Name: lifecycle.ApprovalName("app-v2", "prod", "carol"), Namespace: ns,
			Labels: map[string]string{"kardinal.io/bundle": "app-v2", "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.ApprovalSpec{Bundle: "app-v2", BundleUID: string(b.UID), Environment: "prod", User: "mallory",
			Groups: []string{}, Decision: lifecycle.DecisionReject},
	}
	require.NoError(t, c.Create(ctx, squat))
	_, _, err = lifecycle.RecordApproval(ctx, c, req("carol", "", true))
	assert.True(t, errors.Is(err, lifecycle.ErrNotFound), err)
	assert.Contains(t, approvalsOf(t, c, "app-v2"), "mallory")
	_, carol, err := lifecycle.RecordApproval(ctx, c, req("carol", lifecycle.DecisionApprove, false))
	require.NoError(t, err)
	assert.NotEqual(t, squat.Name, carol.Name)
	got = approvalsOf(t, c, "app-v2")
	assert.Equal(t, lifecycle.DecisionReject, got["mallory"].Spec.Decision, "carol's approve leaves the other object")
	assert.Equal(t, lifecycle.DecisionApprove, got["carol"].Spec.Decision)

	// Each user's revoke removes only their own.
	_, _, err = lifecycle.RecordApproval(ctx, c, req("bob", "", true))
	require.NoError(t, err)
	got = approvalsOf(t, c, "app-v2")
	assert.NotContains(t, got, "bob")
	assert.Contains(t, got, "alice")
	assert.Contains(t, got, "carol")
	assert.Contains(t, got, "mallory")
}

// TestRecordApproval_Fields (spec 23 §8.2 (d)): an approve is checked against
// the approval.fields of the Bundle's approval gates in the environment
// before anything is written, with the tally's own rule (ErrInvalid naming the
// gate and the field); a reject needs none; the fields are written to the
// Approval, and the same decision with other fields replaces it. A caller
// who may not read gates is not checked here (the tally applies the rule).
func TestRecordApproval_Fields(t *testing.T) {
	ctx := context.Background()
	p := pipeline("app", "test", "prod")
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: ns, UID: "uid-app-v2"},
		Spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "image"}, Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "change--app-v2-prod", Namespace: ns,
		Labels: map[string]string{"kardinal.io/bundle": "app-v2", "kardinal.io/environment": "prod", "kardinal.io/gate-name": "change"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Approval: &v1alpha1.GateApprovalPolicy{Required: 1, Fields: []v1alpha1.ApprovalField{
			{Name: "ticketID", Required: true, Pattern: `^CHG[0-9]{7}$`}}}}}
	other := gate.DeepCopy()
	other.Name, other.Labels = "other--app-v2-test", map[string]string{"kardinal.io/bundle": "app-v2", "kardinal.io/environment": "test"}
	c := newClient(t, p, b, gate, other)
	req := func(user, decision string, fields map[string]string) lifecycle.ApprovalRequest {
		return lifecycle.ApprovalRequest{Namespace: ns, Bundle: "app-v2", Environment: "prod", User: user, Decision: decision, Fields: fields}
	}

	_, _, err := lifecycle.RecordApproval(ctx, c, req("alice", lifecycle.DecisionApprove, nil))
	require.Error(t, err)
	assert.True(t, errors.Is(err, lifecycle.ErrInvalid), err)
	assert.Contains(t, err.Error(), "gate change: approval.fields: ticketID is required")
	_, _, err = lifecycle.RecordApproval(ctx, c, req("alice", lifecycle.DecisionApprove, map[string]string{"ticketID": "CHG1"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ticketID must match")
	_, _, err = lifecycle.RecordApproval(ctx, c, req("alice", lifecycle.DecisionApprove, map[string]string{"bad-name": "x"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `field name "bad-name"`)
	assert.Empty(t, approvalsOf(t, c, "app-v2"), "nothing written")

	outcome, a, err := lifecycle.RecordApproval(ctx, c, req("alice", lifecycle.DecisionApprove, map[string]string{"ticketID": "CHG0031337"}))
	require.NoError(t, err)
	assert.Equal(t, lifecycle.ApprovalRecorded, outcome)
	assert.Equal(t, map[string]string{"ticketID": "CHG0031337"}, a.Spec.Fields)
	outcome, _, err = lifecycle.RecordApproval(ctx, c, req("alice", lifecycle.DecisionApprove, map[string]string{"ticketID": "CHG0031337"}))
	require.NoError(t, err)
	assert.Equal(t, lifecycle.ApprovalUnchanged, outcome)
	outcome, a, err = lifecycle.RecordApproval(ctx, c, req("alice", lifecycle.DecisionApprove, map[string]string{"ticketID": "CHG0000042"}))
	require.NoError(t, err)
	assert.Equal(t, lifecycle.ApprovalRecorded, outcome, "other fields replace the decision")
	assert.Equal(t, "CHG0000042", approvalsOf(t, c, "app-v2")["alice"].Spec.Fields["ticketID"])
	assert.Equal(t, a.Name, approvalsOf(t, c, "app-v2")["alice"].Name)

	_, _, err = lifecycle.RecordApproval(ctx, c, req("bob", lifecycle.DecisionReject, nil))
	require.NoError(t, err, "a reject needs no fields")

	// The test environment's gate is another environment's: it does not apply.
	_, _, err = lifecycle.RecordApproval(ctx, c, lifecycle.ApprovalRequest{Namespace: ns, Bundle: "app-v2", Environment: "test",
		User: "carol", Decision: lifecycle.DecisionApprove, Fields: map[string]string{"ticketID": "CHG0031337"}})
	require.NoError(t, err)
	_, _, err = lifecycle.RecordApproval(ctx, c, lifecycle.ApprovalRequest{Namespace: ns, Bundle: "app-v2", Environment: "test",
		User: "dave", Decision: lifecycle.DecisionApprove})
	require.Error(t, err, "test's own gate asks for the field too")
	assert.Contains(t, err.Error(), "gate other--app-v2-test: approval.fields: ticketID is required")
}

// TestRecordApproval_FieldsUnreadableGates: an approver who may not list
// PolicyGates is not refused by the pre-check: the gate's tally decides.
func TestRecordApproval_FieldsUnreadableGates(t *testing.T) {
	ctx := context.Background()
	p := pipeline("app", "test", "prod")
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: ns, UID: "uid-app-v2"},
		Spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "image"}, Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	c := interceptor.NewClient(newClient(t, p, b).(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*v1alpha1.PolicyGateList); ok {
				return apierrors.NewForbidden(v1alpha1.GroupVersion.WithResource("policygates").GroupResource(), "", errors.New("no list"))
			}
			return cl.List(ctx, list, opts...)
		}})
	_, a, err := lifecycle.RecordApproval(ctx, c, lifecycle.ApprovalRequest{Namespace: ns, Bundle: "app-v2", Environment: "prod",
		User: "alice", Decision: lifecycle.DecisionApprove, Fields: map[string]string{"ticketID": "x"}})
	require.NoError(t, err)
	assert.Equal(t, "x", a.Spec.Fields["ticketID"])
}
