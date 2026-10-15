// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// The agents registered in every scenario cluster, and their sponsor.
const (
	Sponsor    = "bob@corp.example.com"
	agentNS    = "ci"
	fixerSA    = "fixer"
	triageUser = "oidc:triage-1"
	triageGrp  = "agents:triage"
	dualUser   = "oidc:dual"
)

func saUser(name string) string { return actor.ServiceAccountPrefix + agentNS + ":" + name }

// ActorIdentities are who creates a scenario's versions, by DimActor value
// (and the refused actors of ActorRefusals).
var ActorIdentities = map[string]Identity{
	"human":       {Username: "dev@corp.example.com", Groups: []string{"developers", "system:authenticated"}},
	"bot":         {Username: saUser("builder"), Groups: []string{"system:serviceaccounts", "system:authenticated"}},
	"agent":       {Username: saUser(fixerSA), Groups: []string{"system:serviceaccounts", "system:authenticated"}, Asserted: v1alpha1.AssertedActor{Model: "model-x", Session: "run-42", Client: "kardinal/scenario"}},
	"agent-group": {Username: triageUser, Groups: []string{triageGrp, "system:authenticated"}},
	"suspended":   {Username: saUser("parked"), Groups: []string{"system:serviceaccounts", "system:authenticated"}},
	"outofscope":  {Username: saUser("elsewhere"), Groups: []string{"system:serviceaccounts", "system:authenticated"}},
	"conflict":    {Username: saUser("second"), Groups: []string{"system:serviceaccounts", "system:authenticated"}},
	"nowhere":     {Username: saUser("nowhere"), Groups: []string{"system:serviceaccounts", "system:authenticated"}},
	// A user that is agent dual and, by group, agent triage.
	"two-agents": {Username: dualUser, Groups: []string{triageGrp, "system:authenticated"}},
}

// wantStamp is the stamp a version created by ActorIdentities[a] carries,
// written out from docs/actors.md (not derived by pkg/actor).
func wantStamp(a string) v1alpha1.Actor {
	id := ActorIdentities[a]
	switch a {
	case "human":
		return v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: id.Username}
	case "bot":
		return v1alpha1.Actor{Kind: v1alpha1.ActorBot, Principal: id.Username}
	case "agent":
		asserted := id.Asserted
		return v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: id.Username, Agent: "fixer", Sponsor: Sponsor, Asserted: &asserted}
	case "agent-group":
		return v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: id.Username, Agent: "triage", Sponsor: Sponsor}
	}
	panic("no stamp for " + a)
}

// wantCreatedBy is the PR evidence line of a version created by
// ActorIdentities[a] (docs/pr-evidence.md, Created by).
func wantCreatedBy(a string) string {
	switch a {
	case "human":
		return "Created by: dev@corp.example.com"
	case "bot":
		return "Created by: bot system:serviceaccount:ci:builder"
	case "agent":
		return "Created by: agent fixer for bob@corp.example.com; model model-x, session run-42, client kardinal/scenario (asserted)"
	case "agent-group":
		return "Created by: agent triage for bob@corp.example.com"
	}
	panic("no line for " + a)
}

// qaIdentity is QAAgent, the verify gates' agent, sponsored by QASponsor.
// Its runs vote as their own ServiceAccounts; its subject is the
// ServiceAccount it verifies with from outside the cluster.
func qaIdentity(ns string) *v1alpha1.AgentIdentity {
	a := AgentIdentityOf(QAAgent, []v1alpha1.AgentSubject{saSubject(QAAgent)}, []string{ns}, false)
	a.Spec.Sponsor.User = QASponsor
	return a
}

// AgentIdentityOf is a registered agent.
func AgentIdentityOf(name string, subjects []v1alpha1.AgentSubject, namespaces []string, suspended bool) *v1alpha1.AgentIdentity {
	return &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: subjects, Sponsor: v1alpha1.AgentSponsor{User: Sponsor},
			Scope: v1alpha1.AgentScope{Namespaces: namespaces}, Suspended: suspended}}
}

func saSubject(name string) v1alpha1.AgentSubject {
	return v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: agentNS, Name: name}
}

// agentFixture are the AgentIdentities of a scenario cluster in tenant ns,
// oldest first: fixer and triage may act in ns; parked is suspended;
// elsewhere may act only in another namespace; first and second both list
// ci/shared, so second (the newer) is not ready and its own subject ci/second
// is blocked; nowhere is scoped to no namespace; dual is the user oidc:dual,
// a member of triage's group. Each has autonomy auto: the scenarios are
// about the stamp and admission, not the approval the default autonomy
// (approve) asks of an agent's version (docs/design/18-actor-policy.md
// section 7, covered by the actor policy journeys).
func agentFixture(ns string) []client.Object {
	objs := agentIdentities(ns)
	for _, o := range objs {
		o.(*v1alpha1.AgentIdentity).Spec.Autonomy = v1alpha1.AutonomyAuto
	}
	return objs
}

func agentIdentities(ns string) []client.Object {
	return []client.Object{
		AgentIdentityOf("first", []v1alpha1.AgentSubject{saSubject("shared")}, []string{ns}, false),
		AgentIdentityOf("fixer", []v1alpha1.AgentSubject{saSubject(fixerSA)}, []string{ns}, false),
		AgentIdentityOf("triage", []v1alpha1.AgentSubject{{Kind: "Group", Name: triageGrp}}, []string{"team-*"}, false),
		AgentIdentityOf("parked", []v1alpha1.AgentSubject{saSubject("parked")}, []string{ns}, true),
		AgentIdentityOf("elsewhere", []v1alpha1.AgentSubject{saSubject("elsewhere")}, []string{"other-team"}, false),
		AgentIdentityOf("second", []v1alpha1.AgentSubject{saSubject("shared"), saSubject("second")}, []string{ns}, false),
		AgentIdentityOf("nowhere", []v1alpha1.AgentSubject{saSubject("nowhere")}, nil, false),
		AgentIdentityOf("dual", []v1alpha1.AgentSubject{{Kind: "User", Name: dualUser}}, []string{ns}, false),
		qaIdentity(ns),
	}
}

// ActorRefusal is a write the actor policy (and, where it can tell, the
// CLI) refuses, and how the cluster is put right. Rows of the matrix with
// Actor set to one of these run RunActorRefusal.
type ActorRefusal struct {
	Actor string
	// Message is part of the API server's refusal.
	Message string
	// CLI is the CLI's own refusal (errors.Is) before it writes, or nil
	// when the CLI writes and the API server refuses.
	CLI error
	// Index is true when the refusal is the agent index's state: the
	// version is a person's, and versions admitted before keep promoting.
	Index bool
	// Stalls is true when versions admitted before wait for the fix too: a
	// missing index fails the actor policy's binding (paramRef
	// parameterNotFoundAction Deny) before its controller and kro
	// exemptions are evaluated, so kro cannot write gate instances (#1929).
	Stalls bool
	// Write makes the refused write as kubectl would: id is who writes,
	// the stamp what it claims.
	Write func(w *World, p *Plan, b *v1alpha1.Bundle) (Identity, *v1alpha1.Actor)
	// Break puts the cluster in the refusing state; Fix puts it right, and
	// Shipper is who ships the version once it is.
	Break, Fix func(w *World, p *Plan) error
	Shipper    string
}

func agentStamp(id Identity, agent string) *v1alpha1.Actor {
	return &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: id.Username, Agent: agent, Sponsor: Sponsor}
}

// ActorRefusals are the writes the actor policy refuses: a forged stamp, an
// agent posing as a person, a suspended agent, an agent out of its scope, an
// agent that conflicts with an older one, and the agent index missing, not
// built yet and over its size bound (fail closed).
var ActorRefusals = []ActorRefusal{
	{Actor: "forged", Message: "actor must be {kind: human, principal: dev@corp.example.com}",
		Write: func(w *World, p *Plan, b *v1alpha1.Bundle) (Identity, *v1alpha1.Actor) {
			return ActorIdentities["human"], &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: Approver}
		}, Shipper: "human"},
	{Actor: "agent-as-human", Message: "actor must be {kind: agent, principal: system:serviceaccount:ci:fixer, agent: fixer, sponsor: bob@corp.example.com}",
		Write: func(w *World, p *Plan, b *v1alpha1.Bundle) (Identity, *v1alpha1.Actor) {
			id := ActorIdentities["agent"]
			return id, &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: id.Username}
		}, Shipper: "agent"},
	{Actor: "suspended", Message: "agent parked may not act: suspended", CLI: actor.ErrBlocked,
		Write: func(w *World, p *Plan, b *v1alpha1.Bundle) (Identity, *v1alpha1.Actor) {
			id := ActorIdentities["suspended"]
			return id, agentStamp(id, "parked")
		},
		Fix: func(w *World, p *Plan) error {
			return w.editAgent("parked", func(a *v1alpha1.AgentIdentity) { a.Spec.Suspended = false })
		}, Shipper: "suspended"},
	{Actor: "outofscope", Message: "agent elsewhere may not act here (namespace team-a, pipeline app)", CLI: actor.ErrOutOfScope,
		Write: func(w *World, p *Plan, b *v1alpha1.Bundle) (Identity, *v1alpha1.Actor) {
			id := ActorIdentities["outofscope"]
			return id, agentStamp(id, "elsewhere")
		},
		Fix: func(w *World, p *Plan) error {
			return w.editAgent("elsewhere", func(a *v1alpha1.AgentIdentity) {
				a.Spec.Scope.Namespaces = append(a.Spec.Scope.Namespaces, p.NS)
			})
		}, Shipper: "outofscope"},
	{Actor: "conflict", Message: "agent second may not act: not ready: SubjectConflict", CLI: actor.ErrBlocked,
		Write: func(w *World, p *Plan, b *v1alpha1.Bundle) (Identity, *v1alpha1.Actor) {
			id := ActorIdentities["conflict"]
			return id, agentStamp(id, "second")
		},
		// The older AgentIdentity is retired the documented way: deleting
		// it is refused until it is suspended.
		Fix: func(w *World, p *Plan) error {
			first := &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "first"}}
			err := w.Client.Delete(AsUser(w.Ctx, AdminUser, AdminGroups...), first)
			if err == nil || !strings.Contains(err.Error(), "suspend agent first before deleting it") {
				return fmt.Errorf("deleting an AgentIdentity that is not suspended: want the agent-identities policy's refusal, got %v", err)
			}
			if err := w.editAgent("first", func(a *v1alpha1.AgentIdentity) { a.Spec.Suspended = true }); err != nil {
				return err
			}
			return w.Client.Delete(AsUser(w.Ctx, AdminUser, AdminGroups...), first)
		}, Shipper: "conflict"},
	// The index omits an empty scope list: the policy must still refuse
	// with the scope, not an expression error.
	{Actor: "nowhere", Message: "agent nowhere may not act here (namespace team-a, pipeline app)", CLI: actor.ErrOutOfScope,
		Write: func(w *World, p *Plan, b *v1alpha1.Bundle) (Identity, *v1alpha1.Actor) {
			id := ActorIdentities["nowhere"]
			return id, agentStamp(id, "nowhere")
		},
		Fix: func(w *World, p *Plan) error {
			return w.editAgent("nowhere", func(a *v1alpha1.AgentIdentity) { a.Spec.Scope.Namespaces = []string{p.NS} })
		}, Shipper: "nowhere"},
	// A user that matches two agents (its username and a group) is refused,
	// whichever of them it stamps, until one registration goes.
	{Actor: "two-agents", Message: "you (oidc:dual) match more than one AgentIdentity (dual, triage)", CLI: actor.ErrConflict,
		Write: func(w *World, p *Plan, b *v1alpha1.Bundle) (Identity, *v1alpha1.Actor) {
			id := ActorIdentities["two-agents"]
			return id, agentStamp(id, "dual")
		},
		Fix: func(w *World, p *Plan) error {
			if err := w.editAgent("dual", func(a *v1alpha1.AgentIdentity) { a.Spec.Suspended = true }); err != nil {
				return err
			}
			return w.Client.Delete(AsUser(w.Ctx, AdminUser, AdminGroups...), &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "dual"}})
		}, Shipper: "two-agents"},
	{Actor: "index-missing", Message: "no params found for policy binding with `Deny` parameterNotFoundAction", Index: true, Stalls: true,
		Break: func(w *World, p *Plan) error {
			w.PauseIndex(true)
			return w.Client.Delete(w.Ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: Release}})
		},
		Fix: func(w *World, p *Plan) error { w.PauseIndex(false); return nil }, Shipper: "human"},
	{Actor: "index-notready", Message: "kardinal's agent index is not ready yet", Index: true,
		// The index as the controller first creates it, before it builds it.
		Break: func(w *World, p *Plan) error {
			w.PauseIndex(true)
			if err := w.Client.Delete(w.Ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: Release}}); err != nil {
				return err
			}
			return w.Client.Create(w.Ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: Release}})
		},
		Fix: func(w *World, p *Plan) error { w.PauseIndex(false); return nil }, Shipper: "human"},
	{Actor: "index-overflow", Message: "kardinal's agent index is not ready yet", Index: true,
		// Suspended AgentIdentities count against the bound too; being
		// suspended, they can be deleted to put it right.
		Break: func(w *World, p *Plan) error {
			for i := 0; i < overflowAgents; i++ {
				a := AgentIdentityOf(fmt.Sprintf("overflow-%02d", i), []v1alpha1.AgentSubject{{Kind: "User",
					Name: fmt.Sprintf("oidc:%s-%02d", strings.Repeat("x", 200), i)}}, []string{p.NS}, true)
				if _, err := w.Apply(a); err != nil {
					return err
				}
			}
			return nil
		},
		Fix: func(w *World, p *Plan) error {
			for i := 0; i < overflowAgents; i++ {
				err := w.Client.Delete(AsUser(w.Ctx, AdminUser, AdminGroups...),
					&v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("overflow-%02d", i)}})
				if err != nil {
					return err
				}
			}
			return nil
		}, Shipper: "human"},
}

// The matrix's index bound, and the AgentIdentities that overflow it: the
// fixture fits, the fixture and overflowAgents subjects of 200 bytes do not.
const (
	matrixIndexBytes = 4 << 10
	overflowAgents   = 16
)

// ActorRefusalOf returns the refusal of DimActor value a, or nil.
func ActorRefusalOf(a string) *ActorRefusal {
	for i := range ActorRefusals {
		if ActorRefusals[i].Actor == a {
			return &ActorRefusals[i]
		}
	}
	return nil
}

// editAgent changes AgentIdentity name as the cluster admin.
func (w *World) editAgent(name string, edit func(*v1alpha1.AgentIdentity)) error {
	var a v1alpha1.AgentIdentity
	if err := w.Client.Get(w.Ctx, types.NamespacedName{Name: name}, &a); err != nil {
		return fmt.Errorf("get agent %s: %w", name, err)
	}
	edit(&a)
	if err := w.Client.Update(AsUser(w.Ctx, AdminUser, AdminGroups...), &a); err != nil {
		return fmt.Errorf("update agent %s: %w", name, err)
	}
	return nil
}

// IndexReady reports whether the agent index is built (Ready True).
func (w *World) IndexReady() bool {
	var idx v1alpha1.AgentIndex
	if err := w.Client.Get(w.Ctx, types.NamespacedName{Name: Release}, &idx); err != nil {
		return false
	}
	return meta.IsStatusConditionTrue(idx.Status.Conditions, "Ready")
}

// Ship creates b as the plan's actor, as kardinal create bundle does, and
// fails the test when it is refused.
func (p *Plan) Ship(t *testing.T, w *World, b *v1alpha1.Bundle) {
	t.Helper()
	ship(t, w, p.Actor, b)
}

func ship(t *testing.T, w *World, who string, b *v1alpha1.Bundle) {
	t.Helper()
	cliErr, refused, err := w.CreateBundle(ActorIdentities[who], b)
	require.NoError(t, err)
	require.NoError(t, cliErr, "the CLI refused %s's version", who)
	require.Empty(t, refused, "the API server refused %s's version", who)
}

// RunActorRefusal runs the row of a refused actor: the version is refused
// (by the CLI before it writes where it can tell, and always by the API
// server), nothing is created or promoted for it, a version admitted before
// an index outage keeps promoting through it (the controller's own writes
// are not judged), and once the cluster is put right the same version is
// admitted, stamped, and promotes.
func RunActorRefusal(t *testing.T, w *World, p *Plan, r *ActorRefusal, fail func(string, ...interface{})) {
	t.Helper()
	ns := p.NS
	var before *v1alpha1.Bundle
	if r.Index {
		before = ImageBundle(ns, PipelineName, "1.1.0")
		ship(t, w, "human", before)
		w.Round()
	}
	if r.Stalls {
		w.Kro.Expect(r.Message)
	}
	if r.Break != nil {
		require.NoError(t, r.Break(w, p))
	}
	if r.Actor == "index-overflow" {
		if !w.RunUntil(blockRounds, func() bool { return !w.IndexReady() }) {
			fail("the agent index stayed Ready past its bound")
		}
		var idx v1alpha1.AgentIndex
		require.NoError(t, w.Client.Get(w.Ctx, types.NamespacedName{Name: Release}, &idx))
		c := meta.FindStatusCondition(idx.Status.Conditions, "Ready")
		require.NotNil(t, c)
		assert.Equal(t, actor.ReasonIndexFull, c.Reason)
		assert.Empty(t, idx.Status.Users, "a full index is written empty")
	}
	v := ImageBundle(ns, PipelineName, "1.2.0")
	refusedBy := func(id Identity, stamp *v1alpha1.Actor) {
		t.Helper()
		b := v.DeepCopy()
		b.Annotations = map[string]string{"kardinal.io/created-by": id.Username}
		b.Spec.Actor = stamp
		reasons, err := w.ApplyAs(w.CtxOf(id), b)
		require.NoError(t, err)
		require.NotEmpty(t, reasons, "the API server admitted %s's version", r.Actor)
		assert.Contains(t, strings.Join(reasons, "; "), r.Message)
		assert.NotContains(t, strings.Join(reasons, "; "), "failed to evaluate", "a refusal names its reason")
		assert.NotContains(t, strings.Join(reasons, "; "), "resulted in error", "a refusal names its reason")
	}
	if r.Write != nil {
		refusedBy(r.Write(w, p, v))
	}
	// What the CLI does: refuse before it writes where it can tell, else
	// write what the API server refuses (the index states). A forged stamp
	// is not the CLI's: it stamps what the index says.
	if r.CLI != nil || r.Index {
		cliErr, reasons, err := w.CreateBundle(ActorIdentities[r.Shipper], v.DeepCopy())
		require.NoError(t, err)
		if r.CLI != nil {
			assert.ErrorIs(t, cliErr, r.CLI, "the CLI refuses %s's version before it writes", r.Actor)
		} else {
			require.NoError(t, cliErr)
			require.NotEmpty(t, reasons, "the API server admitted %s's version", r.Actor)
			assert.Contains(t, strings.Join(reasons, "; "), r.Message)
		}
	}
	for i := 0; i < blockRounds; i++ {
		w.Round()
	}
	var got v1alpha1.Bundle
	err := w.Client.Get(w.Ctx, client.ObjectKeyFromObject(v), &got)
	require.True(t, apierrors.IsNotFound(err), "%s's refused version exists: %v", r.Actor, err)
	if before != nil {
		// The controller and kro are not judged: the version admitted
		// before the outage promotes through it.
		if !r.Stalls && !w.RunUntil(maxRounds, func() bool { return w.BundlePhase(ns)[before.Name] == "Verified" }) {
			fail("%s: bundle %s admitted before the outage not Verified during it", r.Actor, before.Name)
		}
		// Still refused while the index is out.
		cliErr, reasons, err := w.CreateBundle(ActorIdentities["human"], v.DeepCopy())
		require.NoError(t, err)
		require.NoError(t, cliErr)
		assert.NotEmpty(t, reasons, "%s: a version was admitted during the outage", r.Actor)
	}
	if r.Fix != nil {
		require.NoError(t, r.Fix(w, p))
	}
	if !w.RunUntil(blockRounds, w.IndexReady) {
		fail("%s: the agent index is not Ready once fixed", r.Actor)
	}
	w.Round()
	if before != nil && !w.RunUntil(maxRounds, func() bool { return w.BundlePhase(ns)[before.Name] == "Verified" }) {
		fail("%s: bundle %s admitted before the outage not Verified after it", r.Actor, before.Name)
	}
	ship(t, w, r.Shipper, v)
	if !w.RunUntil(maxRounds, func() bool { return w.BundlePhase(ns)[v.Name] == "Verified" }) {
		fail("%s: bundle %s not Verified once the cluster was put right", r.Actor, v.Name)
	}
	stored := w.Bundle(ns, v.Name)
	require.NotNil(t, stored.Spec.Actor)
	if a := stored.Spec.Actor; r.Shipper == "suspended" || r.Shipper == "outofscope" || r.Shipper == "conflict" ||
		r.Shipper == "nowhere" || r.Shipper == "two-agents" {
		assert.Equal(t, v1alpha1.ActorAgent, a.Kind)
		assert.Equal(t, ActorIdentities[r.Shipper].Username, a.Principal)
	} else {
		assert.Equal(t, wantStamp(r.Shipper), *a)
	}
	p.expectRunning(t, w, "1.2.0", p.Envs...)
}

// checkActors checks that every version carries its creator's stamp, the
// controller's for a rollback; every promotion AuditEvent names the actor of
// its version; every PR of a version says who created it.
func (p *Plan) checkActors(t *testing.T, w *World) {
	t.Helper()
	var bundles v1alpha1.BundleList
	require.NoError(t, w.Client.List(w.Ctx, &bundles, client.InNamespace(p.NS)))
	stamps := map[string]v1alpha1.Actor{}
	for _, b := range bundles.Items {
		require.NotNil(t, b.Spec.Actor, "Bundle %s has no actor stamp", b.Name)
		if b.Spec.Actor.Kind == v1alpha1.ActorController {
			assert.Equal(t, ControllerUser, b.Spec.Actor.Principal, "Bundle %s", b.Name)
			assert.Equal(t, v1alpha1.ViaAutoRollback, b.Spec.Actor.Via, "Bundle %s", b.Name)
		} else {
			assert.Equal(t, wantStamp(p.Actor), *b.Spec.Actor, "Bundle %s", b.Name)
		}
		stamps[b.Name] = *b.Spec.Actor
	}
	var events v1alpha1.AuditEventList
	require.NoError(t, w.Client.List(w.Ctx, &events, client.InNamespace(p.NS)))
	promotion := map[string]bool{psrec.AuditActionPromotionStarted: true, psrec.AuditActionPromotionSucceeded: true,
		psrec.AuditActionPromotionFailed: true, psrec.AuditActionPromotionSuperseded: true, psrec.AuditActionPromotionRejected: true,
		psrec.AuditActionRollbackStarted: true, psrec.AuditActionRollbackSucceeded: true}
	n := 0
	for _, e := range events.Items {
		want, ok := stamps[e.Spec.BundleName]
		if !promotion[e.Spec.Action] || !ok {
			continue
		}
		n++
		if assert.NotNil(t, e.Spec.Actor, "AuditEvent %s (%s) names no actor", e.Name, e.Spec.Action) {
			assert.Equal(t, want, *e.Spec.Actor, "AuditEvent %s (%s)", e.Name, e.Spec.Action)
		}
	}
	assert.Positive(t, n, "no promotion AuditEvent")
	if p.Delivery != "pr-review" {
		return
	}
	for _, b := range bundles.Items {
		for env, s := range w.Steps(p.NS, b.Name) {
			if s.Status.PRURL == "" {
				continue
			}
			for _, pr := range w.SCM.PRs() {
				if !strings.HasSuffix(s.Status.PRURL, fmt.Sprintf("/%d", pr.Number)) {
					continue
				}
				if stamps[b.Name].Kind == v1alpha1.ActorController {
					continue
				}
				assert.Contains(t, pr.Body, wantCreatedBy(p.Actor), "%s/%s: PR #%d evidence", b.Name, env, pr.Number)
			}
		}
	}
}
