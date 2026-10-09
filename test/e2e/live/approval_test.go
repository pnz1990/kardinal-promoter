//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// approverRole is the chart's approver ClusterRole (approver-role.yaml) of the
// e2e release.
const approverRole = framework.ControllerDeployment + "-approvals"

// userKubeconfig writes a kubeconfig that reaches the kind cluster as user in
// groups (client-go impersonation on top of the harness credentials), so the
// CLI runs as that person: SelfSubjectReview answers with user and groups,
// RBAC and the admission policies judge them. It binds user to the chart's
// approver ClusterRole in ns.
func userKubeconfig(t *testing.T, e *framework.Env, ns, user string, groups ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig-"+strings.NewReplacer("@", "-", ":", "-").Replace(user))
	e.WriteKubeconfig(t, path, map[string]string{e.Context: ns}, e.Context)
	cfg, err := clientcmd.LoadFromFile(path)
	require.NoError(t, err)
	for _, ai := range cfg.AuthInfos {
		ai.Impersonate = user
		ai.ImpersonateGroups = groups
	}
	require.NoError(t, clientcmd.WriteToFile(*cfg, path))
	_, err = e.Kube.RbacV1().RoleBindings(ns).Create(context.Background(), &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "approver-" + strings.NewReplacer("@", "-", ":", "-", ".", "-").Replace(user), Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: approverRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return path
}

// TestGate_ApprovalQuorum puts a two-approver gate (allowed group
// release-managers, excludeAuthor) in front of an approval: auto prod and
// approves with the CLI as several users. prod has no step and the gate reads
// "waiting for approvals: 0 of 2" until two allowed people approved: an
// approval from someone outside the group is not copied into the gate, one
// in another user's name is refused by the admission policy, a reject from
// an allowed approver blocks until revoked, and the second allowed approval
// lets prod promote. Each decision is an Approval object in the approver's
// own name, and status.approvals says which ones count. An Approval with a
// group the approver does not have is refused, someone else cannot delete
// (revoke) an Approval, every decision writes an ApprovalRecorded or
// ApprovalRevoked AuditEvent, and the Bundle names its verified creator.
//
// Covers GATE-APPROVAL-01, GATE-APPROVAL-02, GATE-APPROVAL-03, CLI-APPROVE-01.
func TestGate_ApprovalQuorum(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	g := framework.Gate(a.ns, "two-approvers", "prod", "true", recheck)
	g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 2, AllowedGroups: []string{"release-managers"}, ExcludeAuthor: true}
	e.CreateGate(t, g)
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "two-approvers", false, "waiting for approvals: 0 of 2", gateTimeout)

	as := func(path string, args ...string) framework.CLIResult {
		return c.Exec(framework.CLIOptions{Kubeconfig: path}, c.Args(a.ns, args...)...)
	}
	alice := userKubeconfig(t, e, a.ns, "alice@example.com", "release-managers")
	bob := userKubeconfig(t, e, a.ns, "bob@example.com", "release-managers")
	mallory := userKubeconfig(t, e, a.ns, "mallory@example.com", "devs")

	// Outside the allowed group: recorded, not counted.
	r := as(mallory, "approve", bundle, "--env", "prod", "--comment", "looks fine to me")
	require.Equal(t, 0, r.Code, r.Output())
	assert.Contains(t, r.Stdout, "Recorded: mallory@example.com approves "+bundle+" for prod")
	// The Graph copies only allowed approvers' Approvals into the gate
	// (before the 101 cap), so mallory's never reaches it.
	framework.Consistently(t, 15*time.Second, "mallory's Approval is not copied into the gate", func(ctx context.Context) (bool, string) {
		gate, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", "two-approvers")
		if err != nil || !ok {
			return false, fmt.Sprintf("no gate instance (%v)", err)
		}
		return len(gate.Spec.Approvals) == 0 && !gate.Status.Ready, fmt.Sprintf("%d approvals, ready=%v", len(gate.Spec.Approvals), gate.Status.Ready)
	})

	// An Approval in someone else's name is refused (identity policy).
	forger := impersonating(t, e, a.ns, "mallory@example.com", []string{"devs"}, []string{"approvals"}, "create", "delete")
	b := getBundle(t, e, a.ns, bundle)
	forged := func(name, user string, groups ...string) *v1alpha1.Approval {
		return &v1alpha1.Approval{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.ns,
				Labels: map[string]string{"kardinal.io/bundle": bundle, "kardinal.io/environment": "prod"}},
			Spec: v1alpha1.ApprovalSpec{Bundle: bundle, BundleUID: string(b.UID), Environment: "prod", User: user,
				Groups: groups, Decision: "approve"},
		}
	}
	err := forger.Create(ctx, forged("forged-user", "alice@example.com", "release-managers"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `spec.user must be your own username "mallory@example.com"`)
	err = forger.Create(ctx, forged("forged-group", "mallory@example.com", "release-managers"))
	require.Error(t, err, "a group the approver does not have is refused")
	assert.Contains(t, err.Error(), "spec.groups may only list your own groups")

	// One allowed approval of two.
	r = as(alice, "approve", bundle, "--env", "prod")
	require.Equal(t, 0, r.Code, r.Output())
	e.WaitGateReady(t, a.ns, bundle, "prod", "two-approvers", false, "waiting for approvals: 1 of 2 (alice@example.com)", gateTimeout)
	a.noStep(t, bundle, "prod", 10*time.Second)

	// Only alice can revoke her Approval.
	words := strings.Fields(r.Stdout) // ... (Approval <name>)
	var alices v1alpha1.Approval
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: strings.TrimSuffix(words[len(words)-1], ")")}, &alices))
	assert.Equal(t, string(b.UID), alices.Spec.BundleUID)
	err = forger.Delete(ctx, &alices)
	require.Error(t, err, "someone else cannot revoke alice's approval")
	assert.Contains(t, err.Error(), "only alice@example.com can revoke this Approval")

	// An allowed reject blocks, whatever the approvals.
	r = as(bob, "approve", bundle, "--env", "prod", "--decision", "reject", "--comment", "wait for the DB migration")
	require.Equal(t, 0, r.Code, r.Output())
	e.WaitGateReady(t, a.ns, bundle, "prod", "two-approvers", false, "rejected by bob@example.com (wait for the DB migration)", gateTimeout)

	// bob changes his mind: the second allowed approval opens the gate.
	r = as(bob, "approve", bundle, "--env", "prod")
	require.Equal(t, 0, r.Code, r.Output())
	e.WaitGateReady(t, a.ns, bundle, "prod", "two-approvers", true, "approved by alice@example.com, bob@example.com (2 of 2)", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)

	var list v1alpha1.ApprovalList
	require.NoError(t, e.Client.List(ctx, &list, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/bundle": bundle}))
	users := map[string]string{}
	for _, ap := range list.Items {
		users[ap.Spec.User] = ap.Spec.Decision
		require.Len(t, ap.OwnerReferences, 1, "%s is owned by its Bundle", ap.Name)
		assert.Equal(t, bundle, ap.OwnerReferences[0].Name)
	}
	assert.Equal(t, map[string]string{"alice@example.com": "approve", "bob@example.com": "approve", "mallory@example.com": "approve"}, users)
	// mallory's Approval exists but is not an allowed approver's: the Graph
	// does not copy it into the gate.
	final := e.WaitGate(t, a.ns, bundle, "prod", "two-approvers", gateTimeout, "two decisions recorded", func(g *v1alpha1.PolicyGate) bool {
		return len(g.Status.Approvals) == 2
	})
	counted := map[string]bool{}
	for _, rec := range final.Status.Approvals {
		counted[rec.User] = rec.Counted
	}
	assert.Equal(t, map[string]bool{"alice@example.com": true, "bob@example.com": true}, counted,
		fmt.Sprintf("status.approvals: %+v", final.Status.Approvals))

	// Every decision is audited: bob's reject recorded and revoked by his
	// approve, which is recorded too.
	var audits v1alpha1.AuditEventList
	require.NoError(t, e.Client.List(ctx, &audits, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/bundle": bundle}))
	var decisions []string
	for _, ae := range audits.Items {
		if strings.HasPrefix(ae.Spec.Action, "Approval") {
			decisions = append(decisions, ae.Spec.Action+" "+strings.Fields(ae.Spec.Message)[0]+" "+strings.Fields(ae.Spec.Message)[2])
		}
	}
	assert.ElementsMatch(t, []string{
		"ApprovalRecorded approve alice@example.com",
		"ApprovalRecorded reject bob@example.com", "ApprovalRevoked reject bob@example.com",
		"ApprovalRecorded approve bob@example.com",
	}, decisions)
	// The Bundle the CLI created names its creator, which excludeAuthor reads.
	assert.Equal(t, whoAmI(t, e), b.Annotations["kardinal.io/created-by"])
}
