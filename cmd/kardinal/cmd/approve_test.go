// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func approveFixture(t *testing.T, phase string) sigs_client.Client {
	t.Helper()
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default", UID: "uid-1"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
		Status:     v1alpha1.BundleStatus{Phase: phase},
	}
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec:       v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}},
	}
	return fake.NewClientBuilder().WithScheme(rejectScheme(t)).WithObjects(b, p).Build()
}

func approvals(t *testing.T, c sigs_client.Client) []v1alpha1.Approval {
	t.Helper()
	var list v1alpha1.ApprovalList
	require.NoError(t, c.List(context.Background(), &list))
	return list.Items
}

// TestApprove records an Approval in the caller's name, with the labels the
// Graph selects on and an ownerReference to the Bundle; a repeat is a no-op,
// another decision replaces it, and --revoke deletes it.
func TestApprove(t *testing.T) {
	stubIdentity(t, Identity{Username: "oidc:alice@example.com", Groups: []string{"release-managers", "system:authenticated"}})
	c := approveFixture(t, "Promoting")
	ctx := context.Background()
	var out bytes.Buffer

	require.NoError(t, approveFn(ctx, &out, c, "default", "app-v1", approveOptions{env: "prod", decision: "approve", comment: "LGTM"}))
	name := approvalName("app-v1", "prod", "oidc:alice@example.com")
	assert.Equal(t, "Recorded: oidc:alice@example.com approves app-v1 for prod (Approval "+name+")\n", out.String())
	list := approvals(t, c)
	require.Len(t, list, 1)
	a := list[0]
	assert.Equal(t, name, a.Name)
	assert.Equal(t, map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod", "kardinal.io/pipeline": "app"}, a.Labels)
	assert.Equal(t, v1alpha1.ApprovalSpec{Bundle: "app-v1", BundleUID: "uid-1", Environment: "prod", User: "oidc:alice@example.com",
		Groups: []string{"release-managers", "system:authenticated"}, Decision: "approve", Comment: "LGTM"}, a.Spec)
	require.Len(t, a.OwnerReferences, 1)
	assert.Equal(t, "Bundle", a.OwnerReferences[0].Kind)
	assert.Equal(t, types.UID("uid-1"), a.OwnerReferences[0].UID)

	out.Reset()
	require.NoError(t, approveFn(ctx, &out, c, "default", "app-v1", approveOptions{env: "prod", decision: "approve", comment: "LGTM"}))
	assert.Contains(t, out.String(), "Already recorded")
	assert.Len(t, approvals(t, c), 1)

	out.Reset()
	require.NoError(t, approveFn(ctx, &out, c, "default", "app-v1", approveOptions{env: "prod", decision: "reject", comment: "not yet"}))
	list = approvals(t, c)
	require.Len(t, list, 1)
	assert.Equal(t, "reject", list[0].Spec.Decision, "a new decision replaces the old one")

	out.Reset()
	require.NoError(t, approveFn(ctx, &out, c, "default", "app-v1", approveOptions{env: "prod", decision: "approve", revoke: true}))
	assert.Contains(t, out.String(), "Revoked: oidc:alice@example.com no longer rejects app-v1 for prod")
	assert.Empty(t, approvals(t, c))
}

func TestApprove_Refusals(t *testing.T) {
	stubIdentity(t, Identity{Username: "alice"})
	cases := []struct {
		name  string
		phase string
		bnd   string
		o     approveOptions
		want  string
	}{
		{name: "bad decision", phase: "Promoting", bnd: "app-v1", o: approveOptions{env: "prod", decision: "maybe"}, want: "--decision must be approve or reject"},
		{name: "unknown bundle", phase: "Promoting", bnd: "nope", o: approveOptions{env: "prod", decision: "approve"}, want: "bundle nope not found"},
		{name: "unknown environment", phase: "Promoting", bnd: "app-v1", o: approveOptions{env: "stage", decision: "approve"}, want: `pipeline app has no environment "stage"`},
		{name: "rejected bundle", phase: "Rejected", bnd: "app-v1", o: approveOptions{env: "prod", decision: "approve"}, want: "bundle app-v1 is Rejected and never promotes again"},
		{name: "superseded bundle", phase: "Superseded", bnd: "app-v1", o: approveOptions{env: "prod", decision: "approve"}, want: "is Superseded"},
		{name: "nothing to revoke", phase: "Promoting", bnd: "app-v1", o: approveOptions{env: "prod", decision: "approve", revoke: true}, want: "alice has no Approval of app-v1 for prod to revoke"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := approveFixture(t, tc.phase)
			err := approveFn(context.Background(), &bytes.Buffer{}, c, "default", tc.bnd, tc.o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Empty(t, approvals(t, c))
		})
	}
}

func TestApprovalName(t *testing.T) {
	a := approvalName("app-v1", "prod", "alice")
	assert.Equal(t, a, approvalName("app-v1", "prod", "alice"), "stable")
	assert.NotEqual(t, a, approvalName("app-v1", "prod", "bob"), "one per user")
	assert.NotEqual(t, a, approvalName("app-v1", "test", "alice"), "one per environment")
	long := approvalName(string(bytes.Repeat([]byte("b"), 300)), "prod", "alice")
	assert.LessOrEqual(t, len(long), 253)
}

// TestApprove_SomeoneElsesApproval: an Approval with the caller's name that
// belongs to someone else is never counted as the caller's, replaced or
// revoked; one of an earlier Bundle with the same name is replaced.
func TestApprove_SomeoneElsesApproval(t *testing.T) {
	stubIdentity(t, Identity{Username: "alice"})
	ctx := context.Background()
	name := approvalName("app-v1", "prod", "alice")
	squat := &v1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.ApprovalSpec{Bundle: "app-v1", BundleUID: "uid-1", Environment: "prod", User: "mallory", Decision: "reject"},
	}
	c := approveFixture(t, "Promoting")
	require.NoError(t, c.Create(ctx, squat))
	for _, o := range []approveOptions{{env: "prod", decision: "reject"}, {env: "prod", decision: "approve"}, {env: "prod", decision: "approve", revoke: true}} {
		err := approveFn(ctx, &bytes.Buffer{}, c, "default", "app-v1", o)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "belongs to mallory, not to you (alice)")
	}
	require.Len(t, approvals(t, c), 1)
	assert.Equal(t, "mallory", approvals(t, c)[0].Spec.User)

	stale := approvals(t, c)[0]
	require.NoError(t, c.Delete(ctx, &stale))
	old := squat.DeepCopy()
	old.ResourceVersion = ""
	old.Spec.User, old.Spec.BundleUID, old.Spec.Decision = "alice", "uid-of-an-earlier-app-v1", "approve"
	require.NoError(t, c.Create(ctx, old))
	require.NoError(t, approveFn(ctx, &bytes.Buffer{}, c, "default", "app-v1", approveOptions{env: "prod", decision: "approve"}))
	list := approvals(t, c)
	require.Len(t, list, 1)
	assert.Equal(t, "uid-1", list[0].Spec.BundleUID, "the earlier Bundle's Approval is replaced")
}
