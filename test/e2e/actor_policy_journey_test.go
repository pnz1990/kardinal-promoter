// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
	pgrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// The actor-policy journeys (docs/design/18-actor-policy.md, #1843): the
// real Graph builder, the PolicyGate, ChangeWindow, PromotionStep and agent
// registry reconcilers on a fake client. Gate instances are what the Graph
// renders (BuildResult.GateInstances), created as kro would.

const (
	apNS    = "payments"
	apIndex = "kardinal-promoter"
	fixerSA = "system:serviceaccount:ci:fixer"
)

var (
	fixerActor    = &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: fixerSA, Agent: "fixer", Sponsor: "alice"}
	reviewerActor = &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:reviewer", Agent: "reviewer", Sponsor: "bob"}
	aliceActor    = &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "alice"}
	carolActor    = &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "carol"}
)

type apWorld struct {
	t   *testing.T
	ctx context.Context
	c   client.Client
	pg  *pgrec.Reconciler
	now time.Time
}

// newAPWorld registers agents fixer (sponsor alice, autonomy fixerAutonomy)
// and reviewer (sponsor bob, auto) and builds the index, as the controller
// does.
func newAPWorld(t *testing.T, fixerAutonomy string, objs ...client.Object) *apWorld {
	t.Helper()
	ctx := context.Background()
	ident := func(name, sponsor, autonomy string) *v1alpha1.AgentIdentity {
		return &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
			Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: name}},
				Sponsor: v1alpha1.AgentSponsor{User: sponsor}, Scope: v1alpha1.AgentScope{Namespaces: []string{"*"},
					Pipelines: []string{"*"}, Environments: []string{"*"}}, Autonomy: autonomy}}
	}
	c := fake.NewClientBuilder().WithScheme(journeyScheme(t)).
		WithObjects(append(objs, ident("fixer", "alice", fixerAutonomy), ident("reviewer", "bob", v1alpha1.AutonomyAuto))...).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{}, &v1alpha1.Bundle{}, &v1alpha1.PromotionStep{},
			&v1alpha1.PolicyGate{}, &v1alpha1.ChangeWindow{}).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	w := &apWorld{t: t, ctx: ctx, c: c, now: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)}
	w.rebuildRegistry()
	pg, err := pgrec.NewReconciler(c)
	require.NoError(t, err)
	pg.AgentIndexName = apIndex
	pg.NowFn = func() time.Time { return w.now }
	w.pg = pg
	return w
}

// rebuildRegistry reconciles every AgentIdentity and the index.
func (w *apWorld) rebuildRegistry() {
	w.t.Helper()
	for _, n := range []string{"fixer", "reviewer"} {
		_, err := (&agentidentity.Reconciler{Client: w.c}).Reconcile(w.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: n}})
		require.NoError(w.t, err)
	}
	_, err := (&agentidentity.IndexReconciler{Client: w.c, Name: apIndex}).Reconcile(w.ctx, ctrl.Request{})
	require.NoError(w.t, err)
}

func apPipeline(agents map[string]string) *v1alpha1.Pipeline {
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: apNS},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "https://github.com/pnz1990/kardinal-demo", Branch: "main"}}}
	for _, e := range []string{"test", "prod"} {
		p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{Name: e, Approval: "auto", Agents: agents[e]})
	}
	return p
}

func apBundle(name string, by *v1alpha1.Actor) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: apNS, UID: types.UID("uid-" + name),
		Labels:      map[string]string{"kardinal.io/pipeline": "checkout"},
		Annotations: map[string]string{lifecycle.AnnotationCreatedBy: by.Principal}},
		Spec:   v1alpha1.BundleSpec{Type: "image", Pipeline: "checkout", Actor: by, Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/pnz1990/kardinal-test-app", Tag: "sha-1a2b3c4"}}},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
}

// instances builds bundle's Graph and creates its gate instances for env (as
// kro would), returning their names by template.
func (w *apWorld) instances(p *v1alpha1.Pipeline, b *v1alpha1.Bundle, templates []v1alpha1.PolicyGate, env string) map[string]string {
	w.t.Helper()
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b, PolicyGates: templates})
	require.NoError(w.t, err)
	out := map[string]string{}
	for _, g := range res.GateInstances {
		if g.Labels["kardinal.io/environment"] != env {
			continue
		}
		g := g
		g.Namespace = apNS
		require.NoError(w.t, w.c.Create(w.ctx, &g))
		out[g.Labels["kardinal.io/gate-template"]] = g.Name
	}
	return out
}

// approve copies an Approval decision of by into the gate instance, as the
// Graph does.
func (w *apWorld) approve(gate, bundle, env string, by *v1alpha1.Actor) {
	w.t.Helper()
	var g v1alpha1.PolicyGate
	require.NoError(w.t, w.c.Get(w.ctx, types.NamespacedName{Namespace: apNS, Name: gate}, &g))
	g.Spec.Approvals = append(g.Spec.Approvals, v1alpha1.GateApproval{Bundle: bundle, BundleUID: "uid-" + bundle, Environment: env,
		User: by.Principal, Groups: []string{}, Decision: "approve", Actor: by})
	require.NoError(w.t, w.c.Update(w.ctx, &g))
}

// eval reconciles gate twice (idempotent) and returns it.
func (w *apWorld) eval(gate string) v1alpha1.PolicyGate {
	w.t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: apNS, Name: gate}}
	var g v1alpha1.PolicyGate
	for range 2 {
		_, err := w.pg.Reconcile(w.ctx, req)
		require.NoError(w.t, err)
	}
	require.NoError(w.t, w.c.Get(w.ctx, req.NamespacedName, &g))
	return g
}

// TestJourneyActorPolicy_AgentAutoToTest: with agents: auto on test and an
// AgentIdentity autonomy of auto, an agent's version passes its agent gate
// in test with no approval; suspending the agent holds it at once.
func TestJourneyActorPolicy_AgentAutoToTest(t *testing.T) {
	p := apPipeline(map[string]string{"test": "auto", "prod": "approve"})
	b := apBundle("checkout-v2", fixerActor)
	w := newAPWorld(t, v1alpha1.AutonomyAuto, p, b)
	gates := w.instances(p, b, nil, "test")
	require.Contains(t, gates, "kardinal-agents-auto")
	g := w.eval(gates["kardinal-agents-auto"])
	assert.True(t, g.Status.Ready, g.Status.Reason)
	assert.Contains(t, g.Status.Reason, "approval not required")

	var id v1alpha1.AgentIdentity
	require.NoError(t, w.c.Get(w.ctx, types.NamespacedName{Name: "fixer"}, &id))
	id.Spec.Suspended = true
	require.NoError(t, w.c.Update(w.ctx, &id))
	w.rebuildRegistry()
	g = w.eval(gates["kardinal-agents-auto"])
	assert.False(t, g.Status.Ready, "a suspended agent's in-flight version holds")

	// A person's version gets no agent gate at all.
	human := apBundle("checkout-v3", carolActor)
	require.NoError(t, w.c.Create(w.ctx, human))
	assert.Empty(t, w.instances(p, human, nil, "test"))
}

// TestJourneyActorPolicy_AgentNeedsApprovalInProd: agents: approve on prod
// holds an agent's version for an approval. The agent's sponsor cannot give
// it, nor the agent itself; a person of another party can.
func TestJourneyActorPolicy_AgentNeedsApprovalInProd(t *testing.T) {
	p := apPipeline(map[string]string{"test": "auto", "prod": "approve"})
	b := apBundle("checkout-v2", fixerActor)
	w := newAPWorld(t, v1alpha1.AutonomyAuto, p, b)
	gate := w.instances(p, b, nil, "prod")["kardinal-agents-approve"]
	require.NotEmpty(t, gate)

	g := w.eval(gate)
	assert.False(t, g.Status.Ready)
	assert.Contains(t, g.Status.Reason, "waiting for approvals: 0 of 1")
	require.NotNil(t, g.Status.ApprovalRequired)
	assert.True(t, *g.Status.ApprovalRequired)

	w.approve(gate, b.Name, "prod", aliceActor) // the sponsor
	w.approve(gate, b.Name, "prod", fixerActor) // the agent itself
	g = w.eval(gate)
	assert.False(t, g.Status.Ready, g.Status.Reason)
	for _, rec := range g.Status.Approvals {
		assert.False(t, rec.Counted, rec.User)
	}

	w.approve(gate, b.Name, "prod", carolActor)
	g = w.eval(gate)
	assert.True(t, g.Status.Ready, g.Status.Reason)
	assert.Contains(t, g.Status.Reason, "approved by carol")
}

// TestJourneyActorPolicy_AgentsDeniedInProd: agents: deny keeps an agent's
// version out of prod whatever approvals come; a person's version is not
// affected; an AgentIdentity autonomy of deny holds it everywhere.
func TestJourneyActorPolicy_AgentsDeniedInProd(t *testing.T) {
	p := apPipeline(map[string]string{"test": "auto", "prod": "deny"})
	b := apBundle("checkout-v2", fixerActor)
	w := newAPWorld(t, v1alpha1.AutonomyAuto, p, b)
	gate := w.instances(p, b, nil, "prod")["kardinal-agents-deny"]
	require.NotEmpty(t, gate)
	g := w.eval(gate)
	assert.False(t, g.Status.Ready)
	assert.Contains(t, g.Status.Reason, "does not admit versions an agent created")

	// The admin's ceiling: autonomy deny holds it in test (agents: auto) too.
	var id v1alpha1.AgentIdentity
	require.NoError(t, w.c.Get(w.ctx, types.NamespacedName{Name: "fixer"}, &id))
	id.Spec.Autonomy = v1alpha1.AutonomyDeny
	require.NoError(t, w.c.Update(w.ctx, &id))
	w.rebuildRegistry()
	test := w.instances(p, b, nil, "test")["kardinal-agents-auto"]
	assert.False(t, w.eval(test).Status.Ready, "the stricter ceiling wins")
}

// TestJourneyActorPolicy_AgentApprovalOptIn: an agent's approval counts only
// on a gate that sets allowAgents and lists the agent.
func TestJourneyActorPolicy_AgentApprovalOptIn(t *testing.T) {
	p := apPipeline(nil)
	b := apBundle("checkout-v2", carolActor)
	tmpl := func(name string, policy *v1alpha1.GateApprovalPolicy) v1alpha1.PolicyGate {
		return v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: apNS,
			Labels: map[string]string{"kardinal.io/applies-to": "prod"}},
			Spec: v1alpha1.PolicyGateSpec{Expression: "true", RecheckInterval: "5m", Approval: policy}}
	}
	off := tmpl("security-review", &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true})
	on := tmpl("security-review-agents", &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true,
		AllowAgents: true, AllowedAgents: []string{"reviewer"}})
	w := newAPWorld(t, v1alpha1.AutonomyAuto, p, b)
	gates := w.instances(p, b, []v1alpha1.PolicyGate{off, on}, "prod")
	for _, name := range []string{"security-review", "security-review-agents"} {
		w.approve(gates[name], b.Name, "prod", reviewerActor)
	}
	g := w.eval(gates["security-review"])
	assert.False(t, g.Status.Ready, "agent approvals are opt-in")
	require.Len(t, g.Status.Approvals, 1)
	assert.Contains(t, g.Status.Approvals[0].Reason, "approval.allowAgents")
	g = w.eval(gates["security-review-agents"])
	assert.True(t, g.Status.Ready, g.Status.Reason)
}

// TestJourneyActorPolicy_NoSelfOrSponsorApproval: an agent cannot approve its
// own version, nor its sponsor's, even on a gate that admits it.
func TestJourneyActorPolicy_NoSelfOrSponsorApproval(t *testing.T) {
	p := apPipeline(nil)
	policy := &v1alpha1.GateApprovalPolicy{Required: 1, AllowAgents: true, AllowedAgents: []string{"fixer"}}
	tmpl := v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "review", Namespace: apNS,
		Labels: map[string]string{"kardinal.io/applies-to": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", RecheckInterval: "5m", Approval: policy}}
	own := apBundle("checkout-v2", fixerActor)
	sponsors := apBundle("checkout-v3", aliceActor)
	w := newAPWorld(t, v1alpha1.AutonomyAuto, p, own, sponsors)
	for _, b := range []*v1alpha1.Bundle{own, sponsors} {
		gate := w.instances(p, b, []v1alpha1.PolicyGate{tmpl}, "prod")["review"]
		w.approve(gate, b.Name, "prod", fixerActor)
		g := w.eval(gate)
		assert.False(t, g.Status.Ready, "%s: %s", b.Name, g.Status.Reason)
		require.Len(t, g.Status.Approvals, 1)
		assert.Contains(t, g.Status.Approvals[0].Reason, "own party", b.Name)
	}
}

// freezeStep creates a Pending step of bundle in env and returns a
// reconciler and its request.
func (w *apWorld) freezeStep(name, bundle, env string) (*psrec.Reconciler, ctrl.Request) {
	w.t.Helper()
	step := makeJourneyStep(name, "checkout", bundle, env, "auto")
	step.Namespace = apNS
	step.Labels["kardinal.io/bundle"], step.Labels["kardinal.io/environment"] = bundle, env
	require.NoError(w.t, w.c.Create(w.ctx, step))
	rec := &psrec.Reconciler{Client: w.c, SCM: &mockSCMForLoop{prURL: "https://github.com/pnz1990/kardinal-demo/pull/1", prNumber: 1},
		GitClient: &mockGitForLoop{}, WorkDirFn: func(_, _ string) string { return w.t.TempDir() },
		NowFn: func() time.Time { return w.now }}
	return rec, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: apNS, Name: name}}
}

func (w *apWorld) step(req ctrl.Request) v1alpha1.PromotionStep {
	w.t.Helper()
	var s v1alpha1.PromotionStep
	require.NoError(w.t, w.c.Get(w.ctx, req.NamespacedName, &s))
	return s
}

// freeze writes a freeze with lifecycle.Freeze (kardinal freeze) and
// reconciles its window.
func (w *apWorld) freeze(actors string) string {
	w.t.Helper()
	cw, _, err := lifecycle.Freeze(w.ctx, w.c, lifecycle.FreezeRequest{Actors: actors, Reason: "INC-4211", CreatedBy: "alice", Now: w.now})
	require.NoError(w.t, err)
	w.reconcileWindow(cw.Name)
	return cw.Name
}

func (w *apWorld) reconcileWindow(name string) {
	w.t.Helper()
	// The fake client does not set a generation: give it the API server's.
	var cw v1alpha1.ChangeWindow
	require.NoError(w.t, w.c.Get(w.ctx, types.NamespacedName{Name: name}, &cw))
	if cw.Generation == 0 {
		cw.Generation = 1
		require.NoError(w.t, w.c.Update(w.ctx, &cw))
	}
	_, err := (&changewindow.Reconciler{Client: w.c, NowFn: func() time.Time { return w.now }}).Reconcile(w.ctx,
		ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	require.NoError(w.t, err)
}

// TestJourneyActorPolicy_FreezeAgentsNotHumans: an agent freeze holds an
// agent's step and lets a person's step start; unfreezing releases it.
func TestJourneyActorPolicy_FreezeAgentsNotHumans(t *testing.T) {
	p := apPipeline(nil)
	agentV, humanV := apBundle("checkout-v2", fixerActor), apBundle("checkout-v3", carolActor)
	w := newAPWorld(t, v1alpha1.AutonomyAuto, p, agentV, humanV)
	name := w.freeze(v1alpha1.FreezeAgents)
	ar, areq := w.freezeStep("step-agent", agentV.Name, "test")
	hr, hreq := w.freezeStep("step-human", humanV.Name, "test")
	_, err := ar.Reconcile(w.ctx, areq)
	require.NoError(t, err)
	_, err = hr.Reconcile(w.ctx, hreq)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(w.step(areq).Status.Message, "frozen by "+name), w.step(areq).Status.Message)
	assert.Equal(t, "", w.step(areq).Status.State)
	assert.Equal(t, psrec.StatePromoting, w.step(hreq).Status.State, "a person's version is not held")

	require.NoError(t, lifecycle.Unfreeze(w.ctx, w.c, name))
	_, err = ar.Reconcile(w.ctx, areq)
	require.NoError(t, err)
	assert.Equal(t, psrec.StatePromoting, w.step(areq).Status.State)
}

// TestJourneyActorPolicy_StopTheLine: stop-the-line holds everyone's step.
func TestJourneyActorPolicy_StopTheLine(t *testing.T) {
	p := apPipeline(nil)
	agentV, humanV := apBundle("checkout-v2", fixerActor), apBundle("checkout-v3", carolActor)
	w := newAPWorld(t, v1alpha1.AutonomyAuto, p, agentV, humanV)
	name := w.freeze(v1alpha1.FreezeEveryone)
	for _, b := range []string{agentV.Name, humanV.Name} {
		r, req := w.freezeStep("step-"+b, b, "test")
		_, err := r.Reconcile(w.ctx, req)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(w.step(req).Status.Message, "frozen by "+name), b)
		assert.Equal(t, "", w.step(req).Status.State, b)
	}
}

// TestJourneyActorPolicy_FreezeExemption: during a stop-the-line, an
// exemption (kardinal override --freeze) lets one pipeline environment
// through once the window's status lists it, and the step writes one
// FreezeExempted AuditEvent naming who let it through.
func TestJourneyActorPolicy_FreezeExemption(t *testing.T) {
	p := apPipeline(nil)
	b := apBundle("checkout-v3", carolActor)
	w := newAPWorld(t, v1alpha1.AutonomyAuto, p, b)
	name := w.freeze(v1alpha1.FreezeEveryone)
	r, req := w.freezeStep("step-prod", b.Name, "prod")
	_, err := r.Reconcile(w.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "", w.step(req).Status.State)

	names, err := lifecycle.ExemptFromFreezes(w.ctx, w.c, lifecycle.ExemptRequest{Namespace: apNS, Pipeline: "checkout",
		Environment: "prod", Reason: "hotfix for INC-4211", CreatedBy: "dave", Now: w.now})
	require.NoError(t, err)
	assert.Equal(t, []string{name}, names)
	// Until the window reconciler lists the exemption, the step stays held.
	var cw v1alpha1.ChangeWindow
	require.NoError(t, w.c.Get(w.ctx, types.NamespacedName{Name: name}, &cw))
	cw.Generation++
	require.NoError(t, w.c.Update(w.ctx, &cw))
	_, err = r.Reconcile(w.ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "", w.step(req).Status.State, "an exemption not yet reconciled does not count")

	w.reconcileWindow(name)
	for range 3 {
		_, err = r.Reconcile(w.ctx, req)
		require.NoError(t, err)
	}
	assert.Equal(t, psrec.StatePromoting, w.step(req).Status.State)
	byAction := auditByAction(t, w.c, apNS)
	require.Contains(t, byAction, psrec.AuditActionFreezeExempted)
	msg := byAction[psrec.AuditActionFreezeExempted].Spec.Message
	assert.Contains(t, msg, "by dave")
	assert.Contains(t, msg, "hotfix for INC-4211")
	var list v1alpha1.AuditEventList
	require.NoError(t, w.c.List(w.ctx, &list, client.InNamespace(apNS)))
	n := 0
	for _, e := range list.Items {
		if e.Spec.Action == psrec.AuditActionFreezeExempted {
			n++
		}
	}
	assert.Equal(t, 1, n, "audited once")
}
