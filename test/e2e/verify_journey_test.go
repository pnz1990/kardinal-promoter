// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentrun"
	pgrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// verifyWorld is a namespace with three registered agents (qa-explorer for
// bob, fixer and alice-qa for alice), a Pipeline uat → prod, a version
// created by agent fixer that passed its uat health check, and a verify gate
// on prod built by the Graph builder from policy.
type verifyWorld struct {
	t       *testing.T
	ctx     context.Context
	c       client.Client
	bundle  *v1alpha1.Bundle
	gate    types.NamespacedName
	now     time.Time
	agents  *actor.Source
	pg      *pgrec.Reconciler
	created int
}

const verifyNS = "payments"

func newVerifyWorld(t *testing.T, policy v1alpha1.GateVerifyPolicy) *verifyWorld {
	t.Helper()
	ctx := context.Background()
	s := journeyScheme(t)
	require.NoError(t, batchv1.AddToScheme(s))
	require.NoError(t, networkingv1.AddToScheme(s))
	require.NoError(t, discoveryv1.AddToScheme(s))
	require.NoError(t, rbacv1.AddToScheme(s))
	agent := func(name, sponsor string) *v1alpha1.AgentIdentity {
		return &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
			Spec: v1alpha1.AgentIdentitySpec{
				Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: verifyNS, Name: name}},
				Sponsor:  v1alpha1.AgentSponsor{User: sponsor},
				Scope:    v1alpha1.AgentScope{Namespaces: []string{verifyNS}, Pipelines: []string{"*"}, Environments: []string{"*"}},
				Autonomy: v1alpha1.AutonomyApprove}}
	}
	pipeline := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: verifyNS},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "uat"}, {Name: "prod"}}}}
	health := metav1.NewTime(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC))
	fixer := &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: saUser("fixer"), Agent: "fixer", Sponsor: "alice"}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-v2", Namespace: verifyNS, UID: "uid-v2",
			Labels: map[string]string{"kardinal.io/pipeline": "checkout"}, Annotations: map[string]string{lifecycle.AnnotationCreatedBy: saUser("fixer")}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "checkout", Actor: fixer,
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/pnz1990/kardinal-test-app", Tag: "sha-1a2b3c4"}}},
		Status: v1alpha1.BundleStatus{Phase: "Promoting", Environments: []v1alpha1.EnvironmentStatus{
			{Name: "uat", Phase: "Verified", HealthCheckedAt: &health}}},
	}
	// The Graph builder makes the prod gate instance from the org policy:
	// it resolves the verified environment and plans the AgentRuns.
	tmpl := v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "prod-needs-qa", Namespace: "platform-policies",
		Labels: map[string]string{"kardinal.io/applies-to": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", RecheckInterval: "5m", Verify: &policy}}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle, PolicyGates: []v1alpha1.PolicyGate{tmpl}})
	require.NoError(t, err)
	// The version is an agent's, so it also gets the kardinal-agents-*
	// gates of the agents shorthand (docs/design/18-actor-policy.md
	// section 7); the verify gate is the org policy's one instance.
	var verifyGates []v1alpha1.PolicyGate
	for _, g := range res.GateInstances {
		if g.Labels["kardinal.io/gate-template"] == tmpl.Name {
			verifyGates = append(verifyGates, g)
		}
	}
	require.Len(t, verifyGates, 1)
	gate := verifyGates[0]
	gate.Namespace = verifyNS
	require.Equal(t, "uat", gate.Spec.Verify.Environment, "the Graph resolves the verified environment")

	uids := 0
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(agent("qa-explorer", "bob"), agent("fixer", "alice"), agent("alice-qa", "alice"), pipeline, bundle, &gate,
			&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"},
				AddressType: discoveryv1.AddressTypeIPv4, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"172.18.0.2"}}}},
			&v1alpha1.StepTemplate{ObjectMeta: metav1.ObjectMeta{Name: "qa-explorer"},
				Spec: v1alpha1.StepTemplateSpec{Image: "kardinal-test-agent:e2e", Agent: "qa-explorer", Timeout: "10m"}}).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{}, &v1alpha1.Bundle{}, &v1alpha1.PolicyGate{},
			&v1alpha1.AgentRun{}, &batchv1.Job{}).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetUID() == "" { // the API server sets it; the fake client does not
				uids++
				obj.SetUID(types.UID(fmt.Sprintf("uid-%d", uids)))
			}
			if ts := obj.GetCreationTimestamp(); ts.IsZero() {
				obj.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 10, 14, 9, uids, 0, 0, time.UTC)))
			}
			return cl.Create(ctx, obj, opts...)
		}}).Build()
	_, err = (&agentidentity.IndexReconciler{Client: c, Name: "kardinal-promoter"}).Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)
	pg, err := pgrec.NewReconciler(c)
	require.NoError(t, err)
	w := &verifyWorld{t: t, ctx: ctx, c: c, bundle: bundle, gate: client.ObjectKeyFromObject(&gate),
		now: time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC), agents: &actor.Source{Reader: c, Name: "kardinal-promoter"}, pg: pg}
	pg.NowFn = func() time.Time { return w.now }
	return w
}

func saUser(name string) string { return "system:serviceaccount:" + verifyNS + ":" + name }

// verdict writes a Verdict as the agent (or person) by would, with the stamp
// the index gives it; run > 0 binds it to an AgentRun of agent by, and the
// run's own ServiceAccount writes it (the Graph names agent by as its actor,
// from the AgentRun).
func (w *verifyWorld) verdict(by, result string, run, attempt int, mutate ...func(*v1alpha1.Verdict)) {
	w.t.Helper()
	r, err := w.agents.Resolve(w.ctx, actor.User{Username: by}, actor.Where{Namespace: verifyNS, Pipeline: "checkout", Environment: "uat"})
	require.NoError(w.t, err)
	w.created++
	v := &v1alpha1.Verdict{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("v%02d", w.created), Namespace: verifyNS,
		Labels: map[string]string{"kardinal.io/bundle": w.bundle.Name, "kardinal.io/environment": "uat", "kardinal.io/check": "explore"}},
		Spec: v1alpha1.VerdictSpec{Pipeline: "checkout", Bundle: w.bundle.Name, BundleUID: string(w.bundle.UID), Environment: "uat",
			Check: "explore", Result: result, Summary: fmt.Sprintf("%s by %s", result, by), By: by, Actor: actor.Stamp(r, nil)}}
	if run > 0 {
		v.Name = graph.AgentRunName("checkout", w.bundle.Name, "uat", "explore", run, attempt)
		v.Spec.Run = &v1alpha1.VerdictRun{Name: v.Name, Index: run, Attempt: attempt}
		v.Spec.By = graph.RunServiceAccountUser(verifyNS, v.Name)
		agent := *actor.Stamp(r, nil)
		agent.Principal = v.Spec.By
		v.Spec.Actor = &agent // what the Graph copies: the AgentRun's agent
	}
	for _, m := range mutate {
		m(v)
	}
	require.NoError(w.t, w.c.Create(w.ctx, v))
}

// copyVerdicts does what the Graph's gate collection does (pkg/graph
// verdictsOfItem, tested against the CEL in pkg/graph): copy the Bundle's
// Verdicts for this UID, Pipeline, environment and check from allowed
// writers into the gate instance's spec.verdicts.
func (w *verifyWorld) copyVerdicts() {
	w.t.Helper()
	var g v1alpha1.PolicyGate
	require.NoError(w.t, w.c.Get(w.ctx, w.gate, &g))
	list, err := lifecycle.VerdictsOf(w.ctx, w.c, verifyNS, w.bundle.Name, "", "")
	require.NoError(w.t, err)
	p := g.Spec.Verify
	g.Spec.Verdicts = nil
	for _, v := range list {
		agent := v.Spec.Actor != nil && v.Spec.Actor.Kind == v1alpha1.ActorAgent
		allowed := (agent && contains(p.AllowedAgents, v.Spec.Actor.Agent)) ||
			(!agent && (len(p.AllowedUsers) == 0 && len(p.AllowedGroups) == 0 || contains(p.AllowedUsers, v.Spec.By)))
		// The Bundle UID is not filtered here: the tally refuses another
		// version's Verdict on its own (the Graph filters it too).
		if v.Spec.Pipeline != "checkout" || v.Spec.Environment != p.Environment ||
			v.Spec.Check != p.Check || !allowed {
			continue
		}
		ts := v.CreationTimestamp
		g.Spec.Verdicts = append(g.Spec.Verdicts, v1alpha1.GateVerdict{Name: v.Name, CreatedAt: &ts, Bundle: v.Spec.Bundle,
			BundleUID: v.Spec.BundleUID, Environment: v.Spec.Environment, Check: v.Spec.Check, Result: v.Spec.Result,
			Summary: v.Spec.Summary, By: v.Spec.By, Actor: v.Spec.Actor, Run: v.Spec.Run})
	}
	require.NoError(w.t, w.c.Update(w.ctx, &g))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// evaluate copies the Verdicts and reconciles the gate twice (idempotent).
func (w *verifyWorld) evaluate() v1alpha1.PolicyGate {
	w.t.Helper()
	w.copyVerdicts()
	for range 2 {
		_, err := w.pg.Reconcile(w.ctx, ctrl.Request{NamespacedName: w.gate})
		require.NoError(w.t, err)
	}
	var g v1alpha1.PolicyGate
	require.NoError(w.t, w.c.Get(w.ctx, w.gate, &g))
	return g
}

// TestJourney9AgentVerification (docs/agentic/verify-with-agents.md): agents
// verify a version created by agent fixer in uat before it may enter prod.
func TestJourney9AgentVerification(t *testing.T) {
	qa := v1alpha1.GateVerifyPolicy{Check: "explore", AllowedAgents: []string{"qa-explorer", "fixer", "alice-qa"}}

	t.Run("2 of 3 runs, one fail tolerated; the evidence is in the PR", func(t *testing.T) {
		p := qa
		p.Required, p.MaxFailures = 2, 1
		p.Run = &v1alpha1.GateVerifyRun{Template: "qa-explorer", Count: 3}
		w := newVerifyWorld(t, p)
		w.verdict(saUser("qa-explorer"), "pass", 1, 1)
		g := w.evaluate()
		assert.False(t, g.Status.Ready)
		assert.Contains(t, g.Status.Reason, "waiting for verdicts: 1 of 2 (qa-explorer)")
		w.verdict(saUser("qa-explorer"), "fail", 2, 1)
		w.verdict(saUser("qa-explorer"), "pass", 3, 1)
		g = w.evaluate()
		assert.True(t, g.Status.Ready, g.Status.Reason)
		assert.Contains(t, g.Status.Reason, "verified: 2 of 2 (qa-explorer ×2)")
		body, err := scm.RenderPRBody(scm.PRBody{PipelineName: "checkout", Environment: "prod", BundleName: w.bundle.Name,
			Bundle: w.bundle.Spec, Verifications: []scm.Verification{{Gate: "prod-needs-qa", Check: "explore", Environment: "uat",
				Reason: g.Status.Reason, Verdicts: g.Status.Verdicts}}})
		require.NoError(t, err)
		assert.Contains(t, body, "### Verification: explore in uat")
		assert.Equal(t, 3, strings.Count(body, "| agent qa-explorer for bob |"))
	})

	t.Run("inconclusive, then a retried attempt passes", func(t *testing.T) {
		p := qa
		p.Required, p.OnInconclusive, p.Retries = 1, "retry", 1
		p.Run = &v1alpha1.GateVerifyRun{Template: "qa-explorer", Count: 1}
		w := newVerifyWorld(t, p)
		w.verdict(saUser("qa-explorer"), "inconclusive", 1, 1)
		g := w.evaluate()
		assert.False(t, g.Status.Ready)
		assert.Contains(t, g.Status.Reason, "1 inconclusive, retrying")
		w.verdict(saUser("qa-explorer"), "pass", 1, 2)
		g = w.evaluate()
		assert.True(t, g.Status.Ready, g.Status.Reason)
	})

	t.Run("the author agent's own verdict is refused", func(t *testing.T) {
		w := newVerifyWorld(t, qa)
		w.verdict(saUser("fixer"), "pass", 0, 0)
		g := w.evaluate()
		assert.False(t, g.Status.Ready)
		require.Len(t, g.Status.Verdicts, 1)
		assert.Contains(t, g.Status.Verdicts[0].Reason, "the author's party (agent fixer for alice)")
	})

	t.Run("the sponsor's own agent's verdict is refused", func(t *testing.T) {
		w := newVerifyWorld(t, qa)
		w.verdict(saUser("alice-qa"), "pass", 0, 0)
		g := w.evaluate()
		assert.False(t, g.Status.Ready)
		assert.Contains(t, g.Status.Verdicts[0].Reason, "the author's party")
	})

	t.Run("a verdict on another version's UID is ignored", func(t *testing.T) {
		w := newVerifyWorld(t, qa)
		w.verdict(saUser("qa-explorer"), "pass", 0, 0, func(v *v1alpha1.Verdict) { v.Spec.BundleUID = "uid-of-an-earlier-checkout-v2" })
		g := w.evaluate()
		assert.False(t, g.Status.Ready)
		require.Len(t, g.Status.Verdicts, 1)
		assert.False(t, g.Status.Verdicts[0].Counted)
		assert.Contains(t, g.Status.Verdicts[0].Reason, "for another version")
	})

	t.Run("a fail blocks", func(t *testing.T) {
		w := newVerifyWorld(t, qa)
		w.verdict(saUser("qa-explorer"), "fail", 0, 0)
		g := w.evaluate()
		assert.False(t, g.Status.Ready)
		assert.Contains(t, g.Status.Reason, "verification failed: qa-explorer: fail by "+saUser("qa-explorer"))
		byAction := auditByAction(t, w.c, verifyNS)
		require.Contains(t, byAction, "VerdictRecorded")
		assert.Equal(t, "qa-explorer", byAction["VerdictRecorded"].Spec.Actor.Agent)
	})

	for _, onTimeout := range []string{"fail", "allow"} {
		t.Run("a timeout "+onTimeout+"s as configured", func(t *testing.T) {
			p := qa
			p.Timeout, p.OnTimeout = "45m", onTimeout
			w := newVerifyWorld(t, p)
			g := w.evaluate()
			assert.False(t, g.Status.Ready, "before the deadline it waits")
			assert.Contains(t, g.Status.Reason, "waiting for verdicts: 0 of 1")
			w.now = w.now.Add(time.Hour) // past uat's health check + 45m
			g = w.evaluate()
			assert.Equal(t, onTimeout == "allow", g.Status.Ready, g.Status.Reason)
			assert.Contains(t, g.Status.Reason, "verification timed out after 45m")
		})
	}
}

// TestJourney9AgentRun: the gate's AgentRun runs the registered agent
// bounded: its Job runs as the run's own ServiceAccount, bound only to the
// chart's AgentRun roles, with the run's context and a NetworkPolicy, and
// the run's access is gone once it finished; a suspended agent is refused.
func TestJourney9AgentRun(t *testing.T) {
	p := v1alpha1.GateVerifyPolicy{Check: "explore", AllowedAgents: []string{"qa-explorer"},
		Run: &v1alpha1.GateVerifyRun{Template: "qa-explorer", Count: 1}}
	for _, suspended := range []bool{false, true} {
		w := newVerifyWorld(t, p)
		if suspended {
			var id v1alpha1.AgentIdentity
			require.NoError(t, w.c.Get(w.ctx, client.ObjectKey{Name: "qa-explorer"}, &id))
			id.Spec.Suspended = true
			require.NoError(t, w.c.Update(w.ctx, &id))
		}
		name := graph.AgentRunName("checkout", w.bundle.Name, "uat", "explore", 1, 1)
		run := &v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: verifyNS,
			Labels: map[string]string{graph.LabelKRONodeID: graph.NodeAgentRuns, graph.LabelBundleUID: string(w.bundle.UID)}},
			Spec: v1alpha1.AgentRunSpec{PipelineName: "checkout", BundleName: w.bundle.Name, BundleUID: string(w.bundle.UID),
				Environment: "uat", TargetNamespace: graph.TargetNamespace(&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
					Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "uat"}}}}, "uat"),
				Check: "explore", Template: "qa-explorer", Run: 1, Attempt: 1}}
		require.NoError(t, w.c.Create(w.ctx, run))
		r := &agentrun.Reconciler{Client: w.c, WriterClusterRole: "w", ReaderClusterRole: "r", NowFn: func() time.Time { return w.now }}
		for range 4 {
			_, err := r.Reconcile(w.ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
			require.NoError(t, err)
		}
		var got v1alpha1.AgentRun
		require.NoError(t, w.c.Get(w.ctx, client.ObjectKeyFromObject(run), &got))
		var job batchv1.Job
		jobErr := w.c.Get(w.ctx, client.ObjectKeyFromObject(run), &job)
		if suspended {
			assert.Equal(t, v1alpha1.AgentRunFailed, got.Status.Phase)
			assert.Contains(t, got.Status.Message, "agent qa-explorer is suspended")
			assert.Error(t, jobErr, "no Job for a suspended agent")
			continue
		}
		require.Equal(t, v1alpha1.AgentRunRunning, got.Status.Phase, got.Status.Message)
		require.NoError(t, jobErr)
		assert.Equal(t, "kr-"+name, job.Spec.Template.Spec.ServiceAccountName)
		assert.Equal(t, "bob", got.Status.Sponsor)
		var np networkingv1.NetworkPolicy
		require.NoError(t, w.c.Get(w.ctx, client.ObjectKeyFromObject(run), &np))
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
		require.NoError(t, w.c.Status().Update(w.ctx, &job))
		for range 2 {
			_, err := r.Reconcile(w.ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
			require.NoError(t, err)
		}
		require.NoError(t, w.c.Get(w.ctx, client.ObjectKeyFromObject(run), &got))
		assert.Equal(t, v1alpha1.AgentRunSucceeded, got.Status.Phase)
		var sa corev1.ServiceAccount
		assert.Error(t, w.c.Get(w.ctx, client.ObjectKey{Namespace: verifyNS, Name: "kr-" + name}, &sa), "the run's identity is deleted")
	}
}
