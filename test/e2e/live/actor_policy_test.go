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
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// Live tests of actor-aware policy and freezes (docs/agentic/policy.md,
// docs/agentic/freezes.md; docs/design/18-actor-policy.md).

// policyAgent registers agent <ns>-<name> for ns: ServiceAccount sa bound
// to the promoter and approver roles there, sponsored by sponsor, with
// autonomy, and returns its CLI (a token of the ServiceAccount).
func policyAgent(t *testing.T, e *framework.Env, ns, name, sponsor, autonomy string) *framework.CLI {
	t.Helper()
	ctx := context.Background()
	agent, sa := ns+"-"+name, name
	_, err := e.Kube.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa}}, metav1.CreateOptions{})
	require.NoError(t, err)
	for _, role := range []string{"promoter", "approver"} {
		_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-" + role, Namespace: ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: framework.ControllerDeployment + "-" + role},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: ns}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		waitBoundNamed(t, e, ns, name+"-"+role)
	}
	// Approving needs the chart's approvals role too (create Approvals).
	_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-approvals", Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: approverRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: ns}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	waitBoundNamed(t, e, ns, name+"-approvals")
	id := &v1alpha1.AgentIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: agent},
		Spec: v1alpha1.AgentIdentitySpec{
			Description: "e2e agent of " + t.Name(),
			Subjects:    []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: ns, Name: sa}},
			Sponsor:     v1alpha1.AgentSponsor{User: sponsor},
			Scope:       v1alpha1.AgentScope{Namespaces: []string{ns}},
			Autonomy:    autonomy,
		},
	}
	require.NoError(t, e.Client.Create(ctx, id))
	t.Cleanup(func() {
		var cur v1alpha1.AgentIdentity
		if err := e.Client.Get(context.Background(), types.NamespacedName{Name: agent}, &cur); err != nil {
			return
		}
		cur.Spec.Suspended = true
		_ = e.Client.Update(context.Background(), &cur)
		_ = e.Client.Delete(context.Background(), &cur)
	})
	user := "system:serviceaccount:" + ns + ":" + sa
	waitIndexed(t, e, user, func(en v1alpha1.AgentIndexEntry) bool {
		return en.Agent == agent && en.Blocked == "" && en.Autonomy == autonomy
	})
	tr, err := e.Kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, sa, &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](3600)}}, metav1.CreateOptions{})
	require.NoError(t, err)
	cli := e.CLI(t)
	cli.Kubeconfig = tokenKubeconfig(t, e, tr.Status.Token)
	return cli
}

// agentShips creates a version as the agent with its CLI (stamped agent).
func agentShips(t *testing.T, cli *framework.CLI, ns, image string) string {
	t.Helper()
	out := cli.Exec(framework.CLIOptions{}, cli.Args(ns, "create", "bundle", pipelineName, "--image", image)...)
	require.Zero(t, out.Code, out.Output())
	return bundleCreatedName(t, out.Stdout)
}

// approvalRecord is the gate's status.approvals entry of user.
func approvalRecord(t *testing.T, e *framework.Env, ns, bundle, env, gate, user string) v1alpha1.GateApprovalStatus {
	t.Helper()
	var rec v1alpha1.GateApprovalStatus
	e.WaitGate(t, ns, bundle, env, gate, gateTimeout, "records "+user, func(g *v1alpha1.PolicyGate) bool {
		for _, r := range g.Status.Approvals {
			if r.User == user {
				rec = r
				return true
			}
		}
		return false
	})
	return rec
}

// TestGate_AgentAutonomy: with agents: auto on test and agents: approve on
// prod, an agent (AgentIdentity autonomy auto) ships a version that promotes
// through test on its own and waits at prod's kardinal-agents-approve gate.
// The agent's sponsor approves: recorded, not counted (the one-party rule).
// Another person approves: prod promotes.
//
// Covers POLICY-AGENTS-01, POLICY-AGENTS-02, POLICY-PARTY-01.
func TestGate_AgentAutonomy(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "prod")
	p := a.pipeline(nil)
	envSpec(t, p, "test").Agents = v1alpha1.AutonomyAuto
	envSpec(t, p, "prod").Agents = v1alpha1.AutonomyApprove
	a.apply(t, p)
	sponsor := "sponsor-" + a.ns + "@example.com"
	agent := policyAgent(t, e, a.ns, "fixer", sponsor, v1alpha1.AutonomyAuto)

	bundle := agentShips(t, agent, a.ns, imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "kardinal-agents-approve", false, "waiting for approvals: 0 of 1", gateTimeout)
	a.noStep(t, bundle, "prod", 10*time.Second)

	as := func(path string, args ...string) framework.CLIResult {
		return c.Exec(framework.CLIOptions{Kubeconfig: path}, c.Args(a.ns, args...)...)
	}
	r := as(userKubeconfig(t, e, a.ns, sponsor), "approve", bundle, "--env", "prod")
	require.Zero(t, r.Code, r.Output())
	rec := approvalRecord(t, e, a.ns, bundle, "prod", "kardinal-agents-approve", sponsor)
	assert.False(t, rec.Counted)
	assert.Contains(t, rec.Reason, "sponsor of the version's agent")
	e.WaitGateReady(t, a.ns, bundle, "prod", "kardinal-agents-approve", false, "waiting for approvals: 0 of 1", gateTimeout)

	r = as(userKubeconfig(t, e, a.ns, "carol@example.com"), "approve", bundle, "--env", "prod")
	require.Zero(t, r.Code, r.Output())
	e.WaitGateReady(t, a.ns, bundle, "prod", "kardinal-agents-approve", true, "approved by carol@example.com", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_AgentsDenied: agents: deny on prod keeps an agent's version out
// of prod; a person's version gets no agent gate and promotes.
//
// Covers POLICY-AGENTS-03.
func TestGate_AgentsDenied(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	p := a.pipeline(nil)
	envSpec(t, p, "test").Agents = v1alpha1.AutonomyAuto
	envSpec(t, p, "prod").Agents = v1alpha1.AutonomyDeny
	a.apply(t, p)
	agent := policyAgent(t, e, a.ns, "fixer", "sponsor-"+a.ns+"@example.com", v1alpha1.AutonomyAuto)

	bundle := agentShips(t, agent, a.ns, imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "kardinal-agents-deny", false, "does not admit versions an agent created", gateTimeout)
	a.noStep(t, bundle, "prod", 15*time.Second)

	person := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitStepState(t, a.ns, pipelineName, person, "prod", "Verified", 2*promoteTimeout)
	gates, err := e.GateInstances(context.Background(), a.ns, person, "prod", "kardinal-agents-deny")
	require.NoError(t, err)
	assert.Empty(t, gates, "a person's version has no agent gate")
}

// TestGate_AgentApprovals: an agent's approval counts only on a gate that
// opts in (allowAgents with the agent in allowedAgents), and never on its
// own version, even on such a gate; a people-only gate waits for a person.
//
// Covers POLICY-AGENT-APPROVALS-01, POLICY-PARTY-02.
func TestGate_AgentApprovals(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "prod")
	fixer := policyAgent(t, e, a.ns, "fixer", "alice-"+a.ns+"@example.com", v1alpha1.AutonomyAuto)
	reviewer := policyAgent(t, e, a.ns, "reviewer", "bob-"+a.ns+"@example.com", v1alpha1.AutonomyAuto)
	withAgents := framework.Gate(a.ns, "agent-review", "prod", "true", recheck)
	withAgents.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true, AllowAgents: true,
		AllowedAgents: []string{a.ns + "-fixer", a.ns + "-reviewer"}}
	e.CreateGate(t, withAgents)
	people := framework.Gate(a.ns, "people-review", "prod", "true", recheck)
	people.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}
	e.CreateGate(t, people)
	a.apply(t, a.pipeline(nil))

	bundle := agentShips(t, fixer, a.ns, imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "agent-review", false, "waiting for approvals: 0 of 1", gateTimeout)

	// The version's own agent: listed, but never counts on its own work.
	r := fixer.Exec(framework.CLIOptions{}, fixer.Args(a.ns, "approve", bundle, "--env", "prod")...)
	require.Zero(t, r.Code, r.Output())
	own := approvalRecord(t, e, a.ns, bundle, "prod", "agent-review", "system:serviceaccount:"+a.ns+":fixer")
	assert.False(t, own.Counted)
	assert.Contains(t, own.Reason, "own party")

	// Another party's agent counts where agents are allowed, not elsewhere.
	r = reviewer.Exec(framework.CLIOptions{}, reviewer.Args(a.ns, "approve", bundle, "--env", "prod")...)
	require.Zero(t, r.Code, r.Output())
	e.WaitGateReady(t, a.ns, bundle, "prod", "agent-review", true, "approved by system:serviceaccount:"+a.ns+":reviewer", gateTimeout)
	rec := approvalRecord(t, e, a.ns, bundle, "prod", "people-review", "system:serviceaccount:"+a.ns+":reviewer")
	assert.False(t, rec.Counted, "agent approvals are opt-in")
	assert.Contains(t, rec.Reason, "approval.allowAgents")
	a.noStep(t, bundle, "prod", 10*time.Second)

	r = c.Exec(framework.CLIOptions{Kubeconfig: userKubeconfig(t, e, a.ns, "carol@example.com")}, c.Args(a.ns, "approve", bundle, "--env", "prod")...)
	require.Zero(t, r.Code, r.Output())
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestGate_FreezeAgentsAndStopTheLine: kardinal freeze --agents (limited to
// the test's namespace) holds an agent's version and lets a person's
// through; kardinal unfreeze releases it. kardinal freeze --everyone holds a
// person's version too, until kardinal override --freeze lets the
// environment through: the step goes on and writes one FreezeExempted
// AuditEvent naming who let it through. The freezes admission policy refuses
// a freeze in someone else's name.
//
// Covers FREEZE-AGENTS-01, FREEZE-STOP-01, FREEZE-EXEMPT-01, FREEZE-ADMIT-01.
func TestGate_FreezeAgentsAndStopTheLine(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	agent := policyAgent(t, e, a.ns, "fixer", "sponsor-"+a.ns+"@example.com", v1alpha1.AutonomyAuto)
	me := whoAmI(t, e)

	// The admission policy: a freeze names who froze, as the API server
	// authenticated them.
	forged := &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "forged-" + a.ns},
		Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.Now(), Reason: "x",
			Freeze: &v1alpha1.ChangeWindowFreeze{Actors: v1alpha1.FreezeEveryone, Namespaces: []string{a.ns}, CreatedBy: "mallory@example.com"}}}
	err := e.Client.Create(ctx, forged)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `spec.freeze.createdBy must be your own username "`+me+`"`)

	agents := "kardinal-freeze-agents-" + a.ns
	everyone := "kardinal-freeze-everyone-" + a.ns
	t.Cleanup(func() {
		for _, n := range []string{agents, everyone} {
			_ = e.Client.Delete(context.Background(), &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: n}})
		}
	})
	out := c.Must(a.ns, "freeze", "--agents", "--reason", "INC-4211 agents paused", "--yes")
	assert.Contains(t, out, "Frozen: "+agents+" (agents, namespaces "+a.ns+")")
	cw := e.WaitChangeWindow(t, agents, gateTimeout, "active", func(w *v1alpha1.ChangeWindow) bool { return w.Status.Active })
	assert.Equal(t, me, cw.Spec.Freeze.CreatedBy)

	person := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, person, "test", "Verified", promoteTimeout)

	agentV := agentShips(t, agent, a.ns, fixtures.Image+":"+fixtures.V3)
	e.WaitStepMessage(t, a.ns, pipelineName, agentV, "test", "Pending", "frozen by "+agents, promoteTimeout)
	e.StepHeld(t, a.ns, pipelineName, agentV, "test", "INC-4211 agents paused", 15*time.Second)
	// The step records the freeze (#1974), the Pipeline projects it, and the
	// CLI shows the environment frozen.
	e.WaitStep(t, a.ns, pipelineName, agentV, "test", gateTimeout, "status.frozenBy names the freeze",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			f := ps.Status.FrozenBy
			if f == nil {
				return false, "frozenBy unset"
			}
			return f.Name == agents && f.Since != nil, fmt.Sprintf("%+v", *f)
		})
	e.WaitPipeline(t, a.ns, pipelineName, gateTimeout, "environment test frozen by "+agents, func(p *v1alpha1.Pipeline) bool {
		for _, env := range p.Status.Environments {
			if env.Name == "test" {
				return env.FrozenBy == agents
			}
		}
		return false
	})
	assert.Contains(t, c.Must(a.ns, "get", "pipelines"), "Frozen by "+agents)
	listed := c.Must("", "freeze")
	assert.Contains(t, listed, agents)
	assert.Contains(t, c.Must(a.ns, "unfreeze", agents), "Freeze "+agents+" lifted")
	released := e.WaitStepState(t, a.ns, pipelineName, agentV, "test", "Verified", promoteTimeout)
	assert.Nil(t, released.Status.FrozenBy, "the lifted freeze is cleared from the step")

	// Stop the line: everyone's version holds.
	c.Must(a.ns, "freeze", "--everyone", "--reason", "INC-4212 stop", "--yes")
	e.WaitChangeWindow(t, everyone, gateTimeout, "active", func(w *v1alpha1.ChangeWindow) bool { return w.Status.Active })
	held := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V1)
	e.WaitStepMessage(t, a.ns, pipelineName, held, "test", "Pending", "frozen by "+everyone, promoteTimeout)
	e.StepHeld(t, a.ns, pipelineName, held, "test", "frozen by "+everyone, 15*time.Second)

	// Break glass: an exemption for test lets it through, audited once.
	out = c.Must(a.ns, "override", pipelineName, "test", "--freeze", "--reason", "hotfix for INC-4212", "--expires-in", "30m", "--yes")
	assert.Contains(t, out, "Exemption added to "+everyone)
	e.WaitStepState(t, a.ns, pipelineName, held, "test", "Verified", promoteTimeout)
	framework.Eventually(t, gateTimeout, "a FreezeExempted AuditEvent", func(ctx context.Context) (bool, string) {
		var list v1alpha1.AuditEventList
		if err := e.Client.List(ctx, &list, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/bundle": held}); err != nil {
			return false, err.Error()
		}
		var msgs []string
		for _, ev := range list.Items {
			if ev.Spec.Action == "FreezeExempted" {
				msgs = append(msgs, ev.Spec.Message)
			}
		}
		return len(msgs) == 1 && strings.Contains(msgs[0], "by "+me) && strings.Contains(msgs[0], "hotfix for INC-4212"),
			fmt.Sprintf("%q", msgs)
	})
	assert.Contains(t, c.Must(a.ns, "unfreeze", "--everyone"), "Freeze "+everyone+" lifted")
}
