// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
