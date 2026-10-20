// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
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
	name := lifecycle.ApprovalName("app-v1", "prod", "oidc:alice@example.com")
	assert.Equal(t, "Recorded: oidc:alice@example.com approves app-v1 for prod (Approval "+name+")\n", out.String())
	list := approvals(t, c)
	require.Len(t, list, 1)
	a := list[0]
	assert.Equal(t, name, a.Name)
	assert.Equal(t, map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod", "kardinal.io/pipeline": "app"}, a.Labels)
	assert.Equal(t, v1alpha1.ApprovalSpec{Bundle: "app-v1", BundleUID: "uid-1", Environment: "prod", User: "oidc:alice@example.com",
		Groups: []string{"release-managers", "system:authenticated"}, Decision: "approve", Comment: "LGTM",
		// The verified actor (docs/actors.md): no agent index, so a person.
		Actor: &v1alpha1.Actor{Kind: "human", Principal: "oidc:alice@example.com"}}, a.Spec)
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
	t.Parallel()
	a := lifecycle.ApprovalName("app-v1", "prod", "alice")
	assert.Equal(t, a, lifecycle.ApprovalName("app-v1", "prod", "alice"), "stable")
	assert.NotEqual(t, a, lifecycle.ApprovalName("app-v1", "prod", "bob"), "one per user")
	assert.NotEqual(t, a, lifecycle.ApprovalName("app-v1", "test", "alice"), "one per environment")
	long := lifecycle.ApprovalName(string(bytes.Repeat([]byte("b"), 300)), "prod", "alice")
	assert.LessOrEqual(t, len(long), 253)
}

// TestApprove_SomeoneElsesApproval: an Approval with the caller's name that
// belongs to someone else is never counted as the caller's, replaced or
// revoked; one of an earlier Bundle with the same name is replaced.
func TestApprove_SomeoneElsesApproval(t *testing.T) {
	stubIdentity(t, Identity{Username: "alice"})
	ctx := context.Background()
	name := lifecycle.ApprovalName("app-v1", "prod", "alice")
	labels := map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod"}
	squat := &v1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels},
		Spec:       v1alpha1.ApprovalSpec{Bundle: "app-v1", BundleUID: "uid-1", Environment: "prod", User: "mallory", Decision: "reject"},
	}
	c := approveFixture(t, "Promoting")
	require.NoError(t, c.Create(ctx, squat))

	// Mallory's object under alice's name is neither counted as hers, nor
	// replaced, nor revoked: alice's Approval gets a generated name.
	err := approveFn(ctx, &bytes.Buffer{}, c, "default", "app-v1", approveOptions{env: "prod", decision: "approve", revoke: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alice has no Approval of app-v1 for prod to revoke")
	var out bytes.Buffer
	require.NoError(t, approveFn(ctx, &out, c, "default", "app-v1", approveOptions{env: "prod", decision: "approve"}))
	list := approvals(t, c)
	require.Len(t, list, 2)
	var alices []v1alpha1.Approval
	for _, a := range list {
		if a.Spec.User == "alice" {
			alices = append(alices, a)
		}
	}
	require.Len(t, alices, 1)
	assert.NotEqual(t, name, alices[0].Name, "the taken name is not reused")
	assert.True(t, strings.HasPrefix(alices[0].Name, name+"-"), alices[0].Name)
	assert.Contains(t, out.String(), "Recorded: alice approves app-v1 for prod (Approval "+alices[0].Name+")")

	// A second approve finds alice's by its labels and user.
	out.Reset()
	require.NoError(t, approveFn(ctx, &out, c, "default", "app-v1", approveOptions{env: "prod", decision: "approve"}))
	assert.Contains(t, out.String(), "Already recorded")
	require.NoError(t, approveFn(ctx, &bytes.Buffer{}, c, "default", "app-v1", approveOptions{env: "prod", decision: "approve", revoke: true}))
	list = approvals(t, c)
	require.Len(t, list, 1)
	assert.Equal(t, "mallory", list[0].Spec.User, "only alice's own Approval is revoked")

	// Alice's Approval of an earlier Bundle of the same name is replaced.
	require.NoError(t, c.Delete(ctx, &list[0]))
	old := squat.DeepCopy()
	old.ResourceVersion = ""
	old.Spec.User, old.Spec.BundleUID, old.Spec.Decision = "alice", "uid-of-an-earlier-app-v1", "approve"
	require.NoError(t, c.Create(ctx, old))
	require.NoError(t, approveFn(ctx, &bytes.Buffer{}, c, "default", "app-v1", approveOptions{env: "prod", decision: "approve"}))
	list = approvals(t, c)
	require.Len(t, list, 1)
	assert.Equal(t, "uid-1", list[0].Spec.BundleUID, "the earlier Bundle's Approval is replaced")
}

// TestApprove_FleetTarget (#1565 QA): a fleet target is an environment of its
// Pipeline, so kardinal approve --env <target> records the Approval for it;
// a name that is neither an environment nor a target is still refused.
func TestApprove_FleetTarget(t *testing.T) {
	stubIdentity(t, Identity{Username: "alice"})
	ctx := context.Background()
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default", UID: "uid-1"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod",
			Fleet: &v1alpha1.FleetSpec{Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}}}},
	}
	c := fake.NewClientBuilder().WithScheme(rejectScheme(t)).WithObjects(b, p).Build()
	require.NoError(t, approveFn(ctx, &bytes.Buffer{}, c, "default", "app-v1", approveOptions{env: "prod-eu", decision: "approve"}))
	list := approvals(t, c)
	require.Len(t, list, 1)
	assert.Equal(t, "prod-eu", list[0].Spec.Environment)
	err := approveFn(ctx, &bytes.Buffer{}, c, "default", "app-v1", approveOptions{env: "prod-ap", decision: "approve"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pipeline app has no environment "prod-ap"`)
}

// TestApproveTarget (C5): kardinal approve <pipeline> <env> resolves the
// Bundle waiting to enter env (status.environments' incoming Bundle, else
// the current Bundle with env still ahead); <bundle> --env keeps working,
// and an environment with nothing on its way says so.
//
// Covers CLI-VERBS-01.
func TestApproveTarget(t *testing.T) {
	ctx := context.Background()
	b1 := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "app"}, Status: v1alpha1.BundleStatus{Phase: "Verified"}}
	b2 := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "app"}, Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}}}
	p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{
		{Name: "test", Bundle: "app-v2", State: "Verified"},
		{Name: "prod", Bundle: "app-v1", IncomingBundle: "app-v2", State: "Pending", BlockedBy: []string{"cab"}},
	}
	c := fake.NewClientBuilder().WithScheme(rejectScheme(t)).WithObjects(b1, b2, p).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	approve, _, err := NewRootCmd().Find([]string{"approve"})
	require.NoError(t, err)

	bundle, env, err := approveTarget(ctx, c, "default", approve, []string{"app", "prod"}, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"app-v2", "prod"}, []string{bundle, env}, "the incoming Bundle")

	bundle, env, err = approveTarget(ctx, c, "default", approve, []string{"app-v1"}, "prod")
	require.NoError(t, err)
	assert.Equal(t, []string{"app-v1", "prod"}, []string{bundle, env}, "<bundle> --env names it")

	_, _, err = approveTarget(ctx, c, "default", approve, []string{"app", "test"}, "")
	assert.ErrorContains(t, err, "approve: no version waits for test of app: app-v2 runs there and no newer one is on its way")
	_, _, err = approveTarget(ctx, c, "default", approve, []string{"app-v1"}, "")
	assert.ErrorContains(t, err, "which environment? Give it with --env: kardinal approve app-v1 --env <env>")
	_, _, err = approveTarget(ctx, c, "default", approve, []string{"nope"}, "prod")
	assert.ErrorContains(t, err, "approve: bundle nope not found in namespace default (nor a pipeline of that name)")
	_, _, err = approveTarget(ctx, c, "default", approve, []string{"app", "staging"}, "")
	assert.ErrorContains(t, err, `pipeline app has no environment "staging"`)
	_, _, err = approveTarget(ctx, c, "default", approve, []string{"app", "prod"}, "test")
	assert.ErrorContains(t, err, "the environment is given twice")

	// Ahead of the gate: with nothing incoming in prod yet, the current
	// Bundle that will come there.
	p2 := p.DeepCopy()
	p2.ResourceVersion = ""
	p2.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: "app-v1", IncomingBundle: "app-v2", State: "Promoting"},
		{Name: "prod", Bundle: "app-v1", State: "Verified"}}
	c = fake.NewClientBuilder().WithScheme(rejectScheme(t)).WithObjects(b1, b2, p2).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	bundle, _, err = approveTarget(ctx, c, "default", approve, []string{"app", "prod"}, "")
	require.NoError(t, err)
	assert.Equal(t, "app-v2", bundle)
}

// TestApprove_Fields (spec 23 §8.4): --field values go with an approve; a
// required field missing is asked for at a terminal, with its description,
// and refused without one, naming the --field to give, before anything is
// written; a reject needs none; the values themselves are checked with the
// gate's own rule (lifecycle.RecordApproval).
func TestApprove_Fields(t *testing.T) {
	stubIdentity(t, Identity{Username: "alice"})
	ctx := context.Background()
	fixture := func(t *testing.T) sigs_client.Client {
		c := approveFixture(t, "Promoting")
		gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "change--app-v1-prod", Namespace: "default",
			Labels: map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod", "kardinal.io/gate-name": "change"}},
			Spec: v1alpha1.PolicyGateSpec{Expression: "true", Approval: &v1alpha1.GateApprovalPolicy{Required: 1, Fields: []v1alpha1.ApprovalField{
				{Name: "ticketID", Required: true, Pattern: `^CHG[0-9]{7}$`, Description: "ServiceNow change number"},
				{Name: "riskAcknowledged", Type: "bool", Required: true, MustBe: "true"},
				{Name: "rollbackPlan", Type: "url"}}}}}
		require.NoError(t, c.Create(ctx, gate))
		return c
	}

	t.Run("given with --field", func(t *testing.T) {
		c := fixture(t)
		o := approveOptions{env: "prod", decision: "approve", fields: []string{"ticketID=CHG0031337", "riskAcknowledged=true"}}
		require.NoError(t, approveChecked(ctx, &bytes.Buffer{}, c, "default", "app-v1", o, nil, nil))
		list := approvals(t, c)
		require.Len(t, list, 1)
		assert.Equal(t, map[string]string{"ticketID": "CHG0031337", "riskAcknowledged": "true"}, list[0].Spec.Fields)
	})
	t.Run("missing without a terminal: refused, naming the --field", func(t *testing.T) {
		c := fixture(t)
		o := approveOptions{env: "prod", decision: "approve", fields: []string{"ticketID=CHG0031337"}}
		err := approveChecked(ctx, &bytes.Buffer{}, c, "default", "app-v1", o, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gate change asks for field riskAcknowledged: give it with --field riskAcknowledged=true; nothing recorded")
		assert.Empty(t, approvals(t, c))
	})
	t.Run("missing at a terminal: asked for, with the description", func(t *testing.T) {
		c := fixture(t)
		var asked []string
		prompt := func(q string) (string, error) {
			asked = append(asked, q)
			if strings.HasPrefix(q, "ticketID") {
				return "CHG0000042", nil
			}
			return "true", nil
		}
		o := approveOptions{env: "prod", decision: "approve"}
		require.NoError(t, approveChecked(ctx, &bytes.Buffer{}, c, "default", "app-v1", o, nil, prompt))
		assert.Equal(t, []string{"ticketID (ServiceNow change number)", "riskAcknowledged, must be true"}, asked,
			"the required fields only, once each")
		assert.Equal(t, map[string]string{"ticketID": "CHG0000042", "riskAcknowledged": "true"}, approvals(t, c)[0].Spec.Fields)
	})
	t.Run("a value the gate does not accept", func(t *testing.T) {
		c := fixture(t)
		o := approveOptions{env: "prod", decision: "approve", fields: []string{"ticketID=CHG1", "riskAcknowledged=true", "rollbackPlan=notaurl"}}
		err := approveChecked(ctx, &bytes.Buffer{}, c, "default", "app-v1", o, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gate change: approval.fields: rollbackPlan must be an http or https URL with a host")
		assert.Contains(t, err.Error(), "ticketID must match ^CHG[0-9]{7}$")
		assert.Empty(t, approvals(t, c))
	})
	t.Run("a reject needs none", func(t *testing.T) {
		c := fixture(t)
		require.NoError(t, approveChecked(ctx, &bytes.Buffer{}, c, "default", "app-v1", approveOptions{env: "prod", decision: "reject"}, nil, nil))
		assert.Len(t, approvals(t, c), 1)
	})
	t.Run("malformed --field", func(t *testing.T) {
		for _, tc := range []struct {
			flags []string
			want  string
		}{
			{[]string{"ticketID"}, `--field "ticketID": give it as name=value`},
			{[]string{"=x"}, `--field "=x": give it as name=value`},
			{[]string{"a=1", "a=2"}, "--field a given twice"},
		} {
			_, err := parseFields(tc.flags)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		}
		got, err := parseFields([]string{"a=b=c", "empty="})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"a": "b=c", "empty": ""}, got, "the first = splits")
	})
}

// TestWhy_ApprovalFields: kardinal why lists the fields an approval still
// needs and gives the approve command with their --field flags.
func TestWhy_ApprovalFields(t *testing.T) {
	t.Parallel()
	fields := []v1alpha1.ApprovalField{
		{Name: "ticketID", Required: true, Description: "ServiceNow change number"},
		{Name: "riskAcknowledged", Type: "bool", Required: true, MustBe: "true"},
		{Name: "tier", Type: "enum", Values: []string{"low", "high"}},
	}
	assert.Equal(t, "ticketID (ServiceNow change number), riskAcknowledged true, [tier (low | high)]", fieldsNeeded(fields))
	assert.Equal(t, " --field ticketID=<ticketID> --field riskAcknowledged=true", fieldFlags(fields))
	assert.Empty(t, fieldsNeeded(nil))
	assert.Empty(t, fieldFlags(nil))
}
