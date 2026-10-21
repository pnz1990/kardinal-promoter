//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// indexFullIdentities is how many AgentIdentities fill the release's agent
// index past its 1 MiB bound: 16 subjects each, every subject's entry
// carrying 64 Pipeline patterns of 253 characters (about 16 KiB).
const indexFullIdentities = 5

// TestAudit_IndexFullNeverRefusesAPerson (#2451): on a cluster whose agent
// index is full (IndexFull), stopping is always allowed and going needs
// verification. A person is never refused: kardinal create bundle,
// kardinal freeze and kardinal pause go through, stamped unverified. The
// unverified version counts as an agent's: its kardinal-agents-auto gate
// holds it and says why, and the person's override then is recorded but not
// honoured. A raw version or Approval must carry the unverified stamp
// (refused unstamped, with the fix in the message), and a ServiceAccount's
// version is refused saying the index is full. Once the AgentIdentities are
// removed the index is Ready again: a person's override opens the held
// gate, and a person is stamped human. Not parallel: the index is the release's, read by every
// test's actor checks, so it runs before Go releases the parallel tests, and
// its cleanup waits for the index to be Ready again.
//
// Covers ACTOR-INDEX-07.
func TestAudit_IndexFullNeverRefusesAPerson(t *testing.T) {
	e := framework.New(t)
	cli := e.CLI(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	barePipeline(t, e, ns, pipelineName)
	waitPipelineValid(t, e, ns, pipelineName)

	// A ServiceAccount allowed to ship versions in ns.
	sa := "system:serviceaccount:" + ns + ":ci"
	_, err := e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "ci-promoter", Namespace: ns},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: framework.ControllerDeployment + "-promoter"},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "ci", Namespace: ns}}}, metav1.CreateOptions{})
	require.NoError(t, err)
	waitBoundNamed(t, e, ns, "ci-promoter")
	cfg := rest.CopyConfig(e.Config)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: sa}
	ci, err := client.New(cfg, client.Options{Scheme: e.Client.Scheme()})
	require.NoError(t, err)

	indexReady := func(want metav1.ConditionStatus, reason string) {
		t.Helper()
		framework.Eventually(t, 2*time.Minute, fmt.Sprintf("the agent index Ready=%s %s", want, reason), func(ctx context.Context) (bool, string) {
			var idx v1alpha1.AgentIndex
			if err := e.Client.Get(ctx, types.NamespacedName{Name: agentIndexName}, &idx); err != nil {
				return false, err.Error()
			}
			c := meta.FindStatusCondition(idx.Status.Conditions, "Ready")
			return c != nil && c.Status == want && (reason == "" || c.Reason == reason), fmt.Sprint(idx.Status.Conditions)
		})
	}
	// Fill the index. Suspended, so they need no suspension before their
	// deletion (the agent-identities policy); blocked entries count too.
	long := func(i int) string { return fmt.Sprintf("%03d-%s", i, strings.Repeat("p", 249)) }
	patterns := make([]string, 64)
	for i := range patterns {
		patterns[i] = long(i)
	}
	names := make([]string, indexFullIdentities)
	for i := range names {
		names[i] = fmt.Sprintf("e2e-full-%s-%d", ns, i)
		subjects := make([]v1alpha1.AgentSubject, 16)
		for j := range subjects {
			subjects[j] = v1alpha1.AgentSubject{Kind: "User", Name: fmt.Sprintf("full-%s-%d-%d@example.com", ns, i, j)}
		}
		require.NoError(t, e.Client.Create(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: names[i]},
			Spec: v1alpha1.AgentIdentitySpec{Description: "fills the agent index: " + t.Name(), Subjects: subjects,
				Sponsor: v1alpha1.AgentSponsor{User: "sponsor@example.com"}, Suspended: true,
				Scope: v1alpha1.AgentScope{Namespaces: []string{ns}, Pipelines: patterns}}}))
	}
	removeAll := func() {
		for _, n := range names {
			_ = e.Client.Delete(context.Background(), &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: n}})
		}
	}
	t.Cleanup(func() {
		removeAll()
		indexReady(metav1.ConditionTrue, "")
	})
	indexReady(metav1.ConditionFalse, "IndexFull")

	newest := func() *v1alpha1.Bundle {
		t.Helper()
		var list v1alpha1.BundleList
		require.NoError(t, e.Client.List(ctx, &list, client.InNamespace(ns)))
		require.NotEmpty(t, list.Items)
		b := list.Items[0]
		for _, x := range list.Items[1:] {
			if x.CreationTimestamp.After(b.CreationTimestamp.Time) || (x.CreationTimestamp.Equal(&b.CreationTimestamp) && x.Name > b.Name) {
				b = x
			}
		}
		return &b
	}
	// A person ships, freezes and pauses: never refused for the index.
	cli.Must(ns, "create", "bundle", pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	b := newest()
	require.NotNil(t, b.Spec.Actor, "kardinal stamps the version")
	assert.Equal(t, v1alpha1.ActorUnverified, b.Spec.Actor.Kind, "a person is stamped unverified while the index is full")
	cli.Must(ns, "freeze", "--everyone", "--reason", "INC-2451", "--for", "5m")
	t.Cleanup(func() { cli.Run(ns, "unfreeze", "--everyone") })
	cli.Must(ns, "pause", pipelineName)
	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipelineName}, &p))
	require.NotNil(t, p.Spec.LastChange)
	require.NotNil(t, p.Spec.LastChange.Actor, "kardinal pause stamps the change")
	assert.Equal(t, v1alpha1.ActorUnverified, p.Spec.LastChange.Actor.Kind)
	cli.Must(ns, "resume", pipelineName)
	cli.Must(ns, "unfreeze", "--everyone")

	// Going needs verification: the unverified version is held like an
	// agent's, and a break-glass override waits for the index.
	e.WaitGateReady(t, ns, b.Name, "test", "kardinal-agents-auto", false, graph.UnverifiedAgentGateMessage, 2*gateTimeout)
	cli.Must(ns, "override", pipelineName, "test", "--gate", "kardinal-agents-auto", "--reason", "during IndexFull")
	e.WaitGate(t, ns, b.Name, "test", "kardinal-agents-auto", gateTimeout, "an unverified override not honoured",
		func(g *v1alpha1.PolicyGate) bool {
			c := meta.FindStatusCondition(g.Status.Conditions, "OverrideIgnored")
			return !g.Status.Ready && c != nil && c.Status == metav1.ConditionTrue &&
				strings.Contains(c.Message, "(unverified): written while kardinal's agent index was not Ready")
		})
	err = e.Client.Create(ctx, actorBundle(ns, nil))
	require.Error(t, err, "a raw unstamped version while the index is full")
	assert.Contains(t, err.Error(), "a version needs the unverified actor stamp now")

	// A raw Approval must carry the unverified stamp: it decides a gate.
	ssr, err := e.Kube.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	require.NoError(t, err)
	me := ssr.Status.UserInfo.Username
	approval := func(name string, a *v1alpha1.Actor) *v1alpha1.Approval {
		return &v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			Labels: map[string]string{"kardinal.io/bundle": b.Name, "kardinal.io/environment": "test"}},
			Spec: v1alpha1.ApprovalSpec{Bundle: b.Name, BundleUID: string(b.UID), Environment: "test", User: me, Decision: "approve", Actor: a}}
	}
	err = e.Client.Create(ctx, approval("raw", nil))
	require.Error(t, err, "a person's unstamped Approval while the index is full")
	assert.Contains(t, err.Error(), "an Approval needs the unverified actor stamp now. Use kardinal approve or the UI, which set it, "+
		"or set spec.actor: {kind: unverified, principal: "+me+"} yourself")
	require.NoError(t, e.Client.Create(ctx, approval("stamped", &v1alpha1.Actor{Kind: v1alpha1.ActorUnverified, Principal: me})),
		"a person's Approval stamped unverified")

	// A ServiceAccount waits for the index.
	err = ci.Create(ctx, actorBundle(ns, nil))
	require.Error(t, err, "a ServiceAccount's version while the index is full")
	assert.Contains(t, err.Error(), "agent index is full (IndexFull")
	assert.Contains(t, err.Error(), "remove AgentIdentities or subjects")

	// Room again: a person's override opens the held gate, and a person is
	// human.
	removeAll()
	indexReady(metav1.ConditionTrue, "")
	cli.Must(ns, "override", pipelineName, "test", "--gate", "kardinal-agents-auto", "--reason", "index Ready again")
	e.WaitGateReady(t, ns, b.Name, "test", "kardinal-agents-auto", true, "OVERRIDDEN", gateTimeout)
	framework.Eventually(t, time.Minute, "a person is stamped human again", func(context.Context) (bool, string) {
		r := cli.Run(ns, "create", "bundle", pipelineName, "--image", fixtures.Image+":"+fixtures.V1)
		if r.Code != 0 {
			return false, r.Output()
		}
		b := newest()
		return b.Spec.Actor != nil && b.Spec.Actor.Kind == v1alpha1.ActorHuman, fmt.Sprint(b.Spec.Actor)
	})
}
