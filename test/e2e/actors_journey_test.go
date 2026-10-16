// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package e2e

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
	pgrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// TestJourney8AgentActor: an admin registers agent fixer, sponsored by
// alice and scoped to the payments namespaces and test; the controller builds
// the index; the agent's version is stamped "agent fixer for alice", promotes
// through test, and every AuditEvent of it names the agent; an approval by
// bob is recorded with bob as its actor; the agent is refused outside its
// scope and once suspended (docs/actors.md).
func TestJourney8AgentActor(t *testing.T) {
	s := journeyScheme(t)
	ctx := context.Background()
	const ns, index = "payments-eu", "kardinal-promoter"
	fixerSA := "system:serviceaccount:ci:fixer"

	fixer := &v1alpha1.AgentIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1},
		Spec: v1alpha1.AgentIdentitySpec{
			Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"}},
			Sponsor:  v1alpha1.AgentSponsor{User: "alice"},
			Scope:    v1alpha1.AgentScope{Namespaces: []string{"payments-*"}, Environments: []string{"test"}},
			Autonomy: v1alpha1.AutonomyApprove,
		},
	}
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: ns},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "https://github.com/pnz1990/kardinal-demo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test", Approval: "auto"}, {Name: "prod", Approval: "auto"}}},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(fixer, pipeline).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{}, &v1alpha1.Bundle{}, &v1alpha1.PromotionStep{},
			&v1alpha1.PolicyGate{}).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()

	// The controller validates the registration and builds the index.
	_, err := (&agentidentity.IndexReconciler{Client: c, Name: index}).Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)
	var got v1alpha1.AgentIdentity
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "fixer"}, &got))
	require.True(t, got.Status.Ready, got.Status.Reason)

	// The agent's version is stamped from the index (what the CLI, the UI
	// and the Bundle API do).
	src := &actor.Source{Reader: c, Name: index}
	agent := actor.User{Username: fixerSA}
	r, err := src.Resolve(ctx, agent, actor.Where{Namespace: ns, Pipeline: "checkout"})
	require.NoError(t, err)
	stamp := actor.Stamp(r, &v1alpha1.AssertedActor{Model: "claude-opus-5"})
	assert.Equal(t, "agent fixer for alice", actor.Describe(*stamp))
	_, err = src.Resolve(ctx, agent, actor.Where{Namespace: "billing", Pipeline: "checkout"})
	assert.True(t, errors.Is(err, actor.ErrOutOfScope), "outside payments-*: %v", err)
	_, err = src.Resolve(ctx, agent, actor.Where{Namespace: ns, Environment: "prod"})
	assert.True(t, errors.Is(err, actor.ErrOutOfScope), "approve in prod: %v", err)

	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-v2", Namespace: ns, Labels: map[string]string{"kardinal.io/pipeline": "checkout"},
			Annotations: map[string]string{lifecycle.AnnotationCreatedBy: fixerSA}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "checkout", Actor: stamp},
	}
	require.NoError(t, c.Create(ctx, bundle))

	// It promotes through test; the promotion's AuditEvents name the agent.
	step := makeJourneyStep("step-test", "checkout", "checkout-v2", "test", "auto")
	step.Namespace = ns
	step.Labels["kardinal.io/bundle"], step.Labels["kardinal.io/environment"] = "checkout-v2", "test"
	require.NoError(t, c.Create(ctx, step))
	rec := &psrec.Reconciler{Client: c, SCM: &mockSCMForLoop{prURL: "https://github.com/pnz1990/kardinal-demo/pull/1", prNumber: 1},
		GitClient: &mockGitForLoop{}, WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	driveStepToVerified(t, ctx, rec, c, ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-test", Namespace: ns}}, "step-test")
	byAction := auditByAction(t, c, ns)
	for _, a := range []string{"PromotionStarted", "PromotionSucceeded"} {
		require.Contains(t, byAction, a)
		require.NotNil(t, byAction[a].Spec.Actor, a)
		assert.Equal(t, *stamp, *byAction[a].Spec.Actor, a)
	}

	// bob approves the version; the gate records bob as the decision's actor
	// and so does the ApprovalRecorded AuditEvent.
	bob := &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-approval", Namespace: ns, Labels: map[string]string{
			"kardinal.io/bundle": "checkout-v2", "kardinal.io/pipeline": "checkout", "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", RecheckInterval: "5m",
			Approval:  &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true},
			Approvals: []v1alpha1.GateApproval{{Bundle: "checkout-v2", Environment: "prod", User: "bob", Decision: "approve", Actor: bob}}},
	}
	require.NoError(t, c.Create(ctx, gate))
	pg, err := pgrec.NewReconciler(c)
	require.NoError(t, err)
	for range 2 {
		_, err = pg.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "prod-approval", Namespace: ns}})
		require.NoError(t, err)
	}
	var g v1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "prod-approval", Namespace: ns}, &g))
	require.Len(t, g.Status.Approvals, 1)
	assert.Equal(t, bob, g.Status.Approvals[0].Actor)
	assert.True(t, g.Status.Approvals[0].Counted)
	byAction = auditByAction(t, c, ns)
	require.Contains(t, byAction, "ApprovalRecorded")
	assert.Equal(t, bob, byAction["ApprovalRecorded"].Spec.Actor)

	// Suspending the agent blocks it within one index rebuild.
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "fixer"}, &got))
	got.Spec.Suspended = true
	require.NoError(t, c.Update(ctx, &got))
	_, err = (&agentidentity.IndexReconciler{Client: c, Name: index}).Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)
	_, err = src.Resolve(ctx, agent, actor.Where{Namespace: ns, Pipeline: "checkout"})
	assert.True(t, errors.Is(err, actor.ErrBlocked), "suspended: %v", err)
	// What it wrote before is unchanged.
	var b v1alpha1.Bundle
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(bundle), &b))
	assert.Equal(t, stamp, b.Spec.Actor)
}

// auditByAction is the namespace's AuditEvents by action (the last of each).
func auditByAction(t *testing.T, c client.Client, ns string) map[string]v1alpha1.AuditEvent {
	t.Helper()
	var list v1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace(ns)))
	out := map[string]v1alpha1.AuditEvent{}
	for _, e := range list.Items {
		out[e.Spec.Action] = e
	}
	return out
}
