// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package policycases is the actor-aware policy scenarios
// (test/scenario/policy/part1, part2).
package policycases

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// The people and agents of the policy scenarios, besides ActorIdentities:
// bob sponsors fixer (the agent shipper) and bob-qa; alice sponsors
// alice-bot; carol is nobody's sponsor; ic is an incident commander.
var (
	bob      = scenario.Identity{Username: scenario.Sponsor, Groups: []string{"system:authenticated"}}
	alice    = scenario.Identity{Username: "alice@corp.example.com", Groups: []string{"system:authenticated"}}
	carol    = scenario.Identity{Username: "carol-reviewer@corp.example.com", Groups: []string{"system:authenticated"}}
	aliceBot = scenario.Identity{Username: scenario.SAUser("alice-bot"), Groups: []string{"system:serviceaccounts", "system:authenticated"}}
	ic       = scenario.Identity{Username: "ic@corp.example.com", Groups: []string{"incident-commanders", "system:authenticated"}}
	teamAdm  = scenario.Identity{Username: "team-a-admin@corp.example.com", Groups: []string{"system:authenticated"}}
)

// policyWorld is a linear Pipeline dev → staging → prod in team-a, with the
// matrix's agents registered (autonomy auto unless set) and bob-qa and
// alice-bot, a passing CEL gate on prod and the Plan's own extra objects.
type policyWorld struct {
	*scenario.World
	p *scenario.Plan
}

func startPolicy(t *testing.T, edit func(p *scenario.Plan), agents ...func(*v1alpha1.AgentIdentity)) *policyWorld {
	t.Helper()
	ns := "team-a"
	c := scenario.Combo{Update: "kustomize", Health: "deployment", Gate: "cel", Topology: "linear", Delivery: "direct", SCM: scenario.SCMGitHub,
		Outcome: "success", Actor: "human"}
	w := scenario.NewWorld(t, scenario.Options{SCM: c.SCM, PolicyNamespaces: []string{scenario.PolicyNS}, Actors: true})
	p := scenario.BuildPlan(c, ns, w.Now(), false)
	if edit != nil {
		edit(p)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("after %d rounds:\n%s", w.Rounds(), w.Describe(ns))
		}
	})
	require.NoError(t, p.Setup(w))
	bobQA := scenario.AgentIdentityOf("bob-qa", []v1alpha1.AgentSubject{scenario.SASubject("bob-qa")}, []string{ns}, false)
	bot := scenario.AgentIdentityOf("alice-bot", []v1alpha1.AgentSubject{scenario.SASubject("alice-bot")}, []string{ns}, false)
	bot.Spec.Sponsor.User = alice.Username
	objs := append(scenario.AgentFixture(ns), bobQA, bot)
	for _, o := range objs {
		a := o.(*v1alpha1.AgentIdentity)
		a.Spec.Autonomy = v1alpha1.AutonomyAuto
		for _, e := range agents {
			e(a)
		}
	}
	w.MustApply(objs...)
	w.MustApply(p.Pipeline)
	w.Round()
	w.Round()
	return &policyWorld{World: w, p: p}
}

// approvalGate is a PolicyGate on prod named name with approval a.
func approvalGate(ns, name string, a v1alpha1.GateApprovalPolicy) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name,
		Labels: map[string]string{"kardinal.io/scope": "team", "kardinal.io/applies-to": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Message: name, Approval: &a}}
}

// approve records id's approval of bundle in env as kardinal approve does
// (its actor stamp), through admission. It returns the CLI's or the API
// server's refusal.
func (pw *policyWorld) approve(id scenario.Identity, bundle, env string) error {
	ns := pw.p.NS
	stamp, err := pw.StampFor(id, actor.Where{Namespace: ns, Environment: env})
	if err != nil {
		return err
	}
	_, _, err = lifecycle.RecordApproval(pw.CtxOf(id), pw.Client, lifecycle.ApprovalRequest{Namespace: ns, Bundle: bundle,
		Environment: env, User: id.Username, Groups: id.Groups, Decision: lifecycle.DecisionApprove, Comment: "scenario", Actor: stamp})
	pw.Round()
	pw.Round()
	return err
}

// gate is bundle's instance of gate template name.
func (pw *policyWorld) gate(t *testing.T, bundle, name string) v1alpha1.PolicyGate {
	t.Helper()
	for _, g := range pw.GateInstances(pw.p.NS, bundle) {
		if g.Labels["kardinal.io/gate-name"] == name && g.Labels["kardinal.io/environment"] == "prod" {
			return g
		}
	}
	t.Fatalf("no gate %s for %s in prod", name, bundle)
	return v1alpha1.PolicyGate{}
}

// counted reports how gate counted user's approval: found, counted, why not.
func counted(g v1alpha1.PolicyGate, user string) (bool, bool, string) {
	for _, a := range g.Status.Approvals {
		if a.User == user {
			return true, a.Counted, a.Reason
		}
	}
	return false, false, ""
}

// shipToProdGate ships a version as who and runs it to the prod gates.
func (pw *policyWorld) shipToProdGate(t *testing.T, who string, tag string) *v1alpha1.Bundle {
	t.Helper()
	b := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, tag)
	scenario.ShipAs(t, pw.World, who, b)
	require.True(t, pw.RunUntil(scenario.MaxRounds, func() bool {
		for _, e := range pw.Verified(pw.p.NS, b.Name) {
			if e == "staging" {
				return true
			}
		}
		return false
	}), "%s's version reaches the prod gates\n%s", who, pw.Describe(pw.p.NS))
	pw.Round()
	return b
}

func (pw *policyWorld) expectHeld(t *testing.T, b *v1alpha1.Bundle, env string) {
	t.Helper()
	for i := 0; i < scenario.BlockRounds; i++ {
		pw.Round()
	}
	assert.NotContains(t, pw.Verified(pw.p.NS, b.Name), env, "%s is held out of %s", b.Name, env)
}

func (pw *policyWorld) expectVerified(t *testing.T, b *v1alpha1.Bundle) {
	t.Helper()
	require.True(t, pw.RunUntil(scenario.MaxRounds, func() bool { return pw.BundlePhase(pw.p.NS)[b.Name] == "Verified" }),
		"%s not Verified\n%s", b.Name, pw.Describe(pw.p.NS))
}

// OneParty: an agent and its sponsor are one party, and a party
// has one vote.
func OneParty(t *testing.T) {
	t.Parallel()
	t.Run("the sponsor of the version's agent cannot approve it", func(t *testing.T) {
		t.Parallel()
		pw := startPolicy(t, func(p *scenario.Plan) {
			p.Extra = append(p.Extra, approvalGate(p.NS, "release", v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true,
				AllowedUsers: []string{bob.Username, alice.Username}}))
		})
		b := pw.shipToProdGate(t, "agent", "1.1.0")
		require.NoError(t, pw.approve(bob, b.Name, "prod"))
		found, ok, why := counted(pw.gate(t, b.Name, "release"), bob.Username)
		assert.True(t, found && !ok, "bob's approval is recorded, not counted")
		assert.NotEmpty(t, why)
		pw.expectHeld(t, b, "prod")
		require.NoError(t, pw.approve(alice, b.Name, "prod"))
		pw.expectVerified(t, b)
	})
	t.Run("neither the author agent nor its sponsor's other agent counts, even when listed", func(t *testing.T) {
		t.Parallel()
		pw := startPolicy(t, func(p *scenario.Plan) {
			p.Extra = append(p.Extra, approvalGate(p.NS, "release", v1alpha1.GateApprovalPolicy{Required: 1,
				AllowAgents: true, AllowedAgents: []string{"fixer", "bob-qa"}, AllowedUsers: []string{alice.Username}}))
		})
		b := pw.shipToProdGate(t, "agent", "1.1.0")
		for _, id := range []scenario.Identity{scenario.ActorIdentities["agent"], {Username: scenario.SAUser("bob-qa"), Groups: []string{"system:authenticated"}}} {
			if err := pw.approve(id, b.Name, "prod"); err != nil {
				t.Logf("%s refused up front: %v", id.Username, err)
				continue
			}
			found, ok, _ := counted(pw.gate(t, b.Name, "release"), id.Username)
			assert.True(t, found && !ok, "%s's approval is recorded, not counted (without excludeAuthor too)", id.Username)
		}
		pw.expectHeld(t, b, "prod")
		require.NoError(t, pw.approve(alice, b.Name, "prod"))
		pw.expectVerified(t, b)
	})
	t.Run("a person and their agent are one approval", func(t *testing.T) {
		t.Parallel()
		pw := startPolicy(t, func(p *scenario.Plan) {
			p.Extra = append(p.Extra, approvalGate(p.NS, "release", v1alpha1.GateApprovalPolicy{Required: 2, ExcludeAuthor: true,
				AllowAgents: true, AllowedAgents: []string{"alice-bot"}, AllowedUsers: []string{alice.Username, carol.Username}}))
		})
		b := pw.shipToProdGate(t, "human", "1.1.0")
		require.NoError(t, pw.approve(alice, b.Name, "prod"))
		require.NoError(t, pw.approve(aliceBot, b.Name, "prod"))
		g := pw.gate(t, b.Name, "release")
		assert.False(t, g.Status.Ready, "alice and alice-bot are one party: %s", g.Status.Reason)
		pw.expectHeld(t, b, "prod")
		require.NoError(t, pw.approve(carol, b.Name, "prod"))
		pw.expectVerified(t, b)
	})
}

// AgentApprovals: agents' approvals count only where a gate
// opts in, by name.
func AgentApprovals(t *testing.T) {
	t.Parallel()
	qa := scenario.Identity{Username: scenario.SAUser(scenario.QAAgent), Groups: []string{"system:serviceaccounts", "system:authenticated"}}
	t.Run("naming agents without opting in is refused", func(t *testing.T) {
		t.Parallel()
		w := scenario.NewWorld(t, scenario.Options{Actors: true})
		reasons, err := w.Apply(approvalGate("team-a", "release", v1alpha1.GateApprovalPolicy{Required: 1, AllowedAgents: []string{scenario.QAAgent}}))
		require.NoError(t, err)
		assert.Contains(t, strings.Join(reasons, "; "), "approval.allowedAgents needs approval.allowAgents: true")
	})
	for _, tc := range []struct {
		name   string
		policy v1alpha1.GateApprovalPolicy
		counts bool
	}{
		{"without allowAgents", v1alpha1.GateApprovalPolicy{Required: 1}, false},
		{"allowAgents, another agent listed", v1alpha1.GateApprovalPolicy{Required: 1, AllowAgents: true, AllowedAgents: []string{"triage"}}, false},
		{"allowAgents with the agent listed", v1alpha1.GateApprovalPolicy{Required: 1, AllowAgents: true, AllowedAgents: []string{scenario.QAAgent}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pol := tc.policy
			pol.AllowedUsers = []string{alice.Username}
			pw := startPolicy(t, func(p *scenario.Plan) { p.Extra = append(p.Extra, approvalGate(p.NS, "release", pol)) })
			b := pw.shipToProdGate(t, "human", "1.1.0")
			require.NoError(t, pw.approve(qa, b.Name, "prod"))
			_, ok, _ := counted(pw.gate(t, b.Name, "release"), qa.Username)
			assert.Equal(t, tc.counts, ok, "it counts only where the gate opts in by name")
			if tc.counts {
				pw.expectVerified(t, b)
				return
			}
			pw.expectHeld(t, b, "prod")
			require.NoError(t, pw.approve(alice, b.Name, "prod"))
			pw.expectVerified(t, b)
		})
	}
}

// strictness orders the agents ceilings: deny over approve over auto.
var strictness = map[string]int{v1alpha1.AutonomyAuto: 0, v1alpha1.AutonomyApprove: 1, v1alpha1.AutonomyDeny: 2}

// AgentsShorthand: an environment's agents (auto, approve, deny,
// unset) against the AgentIdentity's autonomy: the stricter applies, as a
// kardinal-agents-<ceiling> gate on every environment of an agent's version,
// and none on a person's.
func AgentsShorthand(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"", v1alpha1.AutonomyAuto, v1alpha1.AutonomyApprove, v1alpha1.AutonomyDeny} {
		for _, autonomy := range []string{v1alpha1.AutonomyAuto, v1alpha1.AutonomyApprove, v1alpha1.AutonomyDeny} {
			env, autonomy := env, autonomy
			t.Run("prod="+orUnset(env)+"/autonomy="+autonomy, func(t *testing.T) {
				t.Parallel()
				pw := startPolicy(t, func(p *scenario.Plan) {
					for i := range p.Pipeline.Spec.Environments {
						if p.Pipeline.Spec.Environments[i].Name == "prod" {
							p.Pipeline.Spec.Environments[i].Agents = env
						}
					}
				}, func(a *v1alpha1.AgentIdentity) {
					if a.Name == "fixer" {
						a.Spec.Autonomy = autonomy
					}
				})
				// A person approves what an agent's version waits for.
				pw.Actors = append(pw.Actors, func(w *scenario.World) error { return approveAgentGates(pw) })
				ceiling := func(e string) string {
					c := autonomy
					if e == "prod" && env != "" && strictness[env] > strictness[c] {
						c = env
					}
					return c
				}
				b := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.1.0")
				scenario.ShipAs(t, pw.World, "agent", b)
				if strictness[autonomy] == 2 || ceiling("prod") == v1alpha1.AutonomyDeny {
					first := "dev"
					if strictness[autonomy] < 2 {
						require.True(t, pw.RunUntil(scenario.MaxRounds, func() bool { return slices.Contains(pw.Verified(pw.p.NS, b.Name), "staging") }),
							"the agent's version reaches staging\n%s", pw.Describe(pw.p.NS))
						first = "prod"
					}
					pw.expectHeld(t, b, first)
					assert.NotEqual(t, "1.1.0", pw.p.Running(pw.World)[first], "deny keeps the agent's version out of %s", first)
				} else {
					pw.expectVerified(t, b)
					pw.p.ExpectRunning(t, pw.World, "1.1.0", pw.p.Envs...)
					// Where the ceiling is approve, it went on because a
					// person approved; elsewhere nobody had to.
					for _, e := range []string{"dev", "staging", "prod"} {
						approved := false
						for _, g := range pw.GateInstances(pw.p.NS, b.Name) {
							if g.Labels["kardinal.io/environment"] == e && strings.HasPrefix(g.Labels["kardinal.io/gate-name"], graph.AgentGatePrefix) {
								_, approved, _ = counted(g, alice.Username)
							}
						}
						assert.Equal(t, ceiling(e) == v1alpha1.AutonomyApprove, approved, "%s: a person's approval counted", e)
					}
				}
				for _, e := range []string{"dev", "staging", "prod"} {
					found := false
					for _, g := range pw.GateInstances(pw.p.NS, b.Name) {
						if g.Labels["kardinal.io/environment"] == e && strings.HasPrefix(g.Labels["kardinal.io/gate-name"], graph.AgentGatePrefix) {
							found = true
							// The gate is named after the environment's ceiling; the
							// AgentIdentity's autonomy applies when it is evaluated.
							want := v1alpha1.AutonomyAuto
							if e == "prod" && env != "" {
								want = env
							}
							assert.Equal(t, graph.AgentGatePrefix+want, g.Labels["kardinal.io/gate-name"], "%s's agent gate", e)
						}
					}
					if slices.Contains(pw.Verified(pw.p.NS, b.Name), e) || e == "dev" {
						assert.True(t, found, "%s has an agent gate", e)
					}
				}
				// A person's version: no agent gate anywhere, and it promotes.
				h := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.2.0")
				scenario.ShipAs(t, pw.World, "human", h)
				pw.expectVerified(t, h)
				for _, g := range pw.GateInstances(pw.p.NS, h.Name) {
					assert.False(t, strings.HasPrefix(g.Labels["kardinal.io/gate-name"], graph.AgentGatePrefix), "a person's version got %s", g.Name)
				}
			})
		}
	}
	t.Run("an agent suspended mid-flight is held where its version is", func(t *testing.T) {
		t.Parallel()
		pw := startPolicy(t, nil)
		b := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.1.0")
		scenario.ShipAs(t, pw.World, "agent", b)
		require.True(t, pw.RunUntil(scenario.MaxRounds, func() bool { return slices.Contains(pw.Verified(pw.p.NS, b.Name), "dev") }))
		require.NoError(t, pw.EditAgent("fixer", func(a *v1alpha1.AgentIdentity) { a.Spec.Suspended = true }))
		pw.expectHeld(t, b, "prod")
	})
}

func orUnset(s string) string {
	if s == "" {
		return "unset"
	}
	return s
}

// approveAgentGates is alice (of no agent's party) approving every waiting
// kardinal-agents-* gate.
func approveAgentGates(pw *policyWorld) error {
	var gates v1alpha1.PolicyGateList
	if err := pw.Client.List(pw.Ctx, &gates, client.InNamespace(pw.p.NS)); err != nil {
		return err
	}
	for _, g := range gates.Items {
		if !strings.HasPrefix(g.Labels["kardinal.io/gate-name"], graph.AgentGatePrefix) || g.Spec.Approval == nil || g.Status.Ready {
			continue
		}
		b, env := g.Labels["kardinal.io/bundle"], g.Labels["kardinal.io/environment"]
		stamp, err := pw.StampFor(alice, actor.Where{Namespace: pw.p.NS, Environment: env})
		if err != nil {
			return err
		}
		_, _, err = lifecycle.RecordApproval(pw.CtxOf(alice), pw.Client, lifecycle.ApprovalRequest{Namespace: pw.p.NS, Bundle: b,
			Environment: env, User: alice.Username, Groups: alice.Groups, Decision: lifecycle.DecisionApprove, Actor: stamp})
		if err != nil && !strings.Contains(err.Error(), "never promotes again") && !strings.Contains(err.Error(), "already") {
			return err
		}
	}
	return nil
}

// freeze is kardinal freeze by id.
func (pw *policyWorld) freeze(t *testing.T, id scenario.Identity, actors, ns string, d time.Duration) string {
	t.Helper()
	cw, _, err := lifecycle.Freeze(pw.CtxOf(id), pw.Client, lifecycle.FreezeRequest{Actors: actors, Namespace: ns,
		Reason: "scenario freeze", For: d, CreatedBy: id.Username, Now: pw.Now()})
	require.NoError(t, err)
	pw.Round()
	return cw.Name
}

// Freeze: agent freezes and stop-the-line hold promotions in
// flight, exemptions let one pipeline through for a while and only in its
// namespace, an agent's exemption is refused and never honoured, and a
// namespace's admin cannot lift someone else's freeze.
func Freeze(t *testing.T) {
	t.Parallel()
	t.Run("an agent freeze holds agents' versions, not people's", func(t *testing.T) {
		t.Parallel()
		pw := startPolicy(t, nil)
		name := pw.freeze(t, ic, v1alpha1.FreezeAgents, "", 0)
		a := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.1.0")
		scenario.ShipAs(t, pw.World, "agent", a)
		pw.expectHeld(t, a, "dev")
		assert.Contains(t, stepMessage(pw, a, "dev"), name, "the held step names the freeze")
		h := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.2.0")
		scenario.ShipAs(t, pw.World, "human", h)
		pw.expectVerified(t, h)
		require.NoError(t, lifecycle.Unfreeze(pw.CtxOf(ic), pw.Client, name, "", &v1alpha1.FreezeLift{By: ic.Username}))
		a2 := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.3.0")
		scenario.ShipAs(t, pw.World, "agent", a2)
		pw.expectVerified(t, a2)
	})
	t.Run("stop-the-line holds a namespace in flight; another namespace's does not", func(t *testing.T) {
		t.Parallel()
		pw := startPolicy(t, nil)
		pw.freeze(t, ic, v1alpha1.FreezeEveryone, "other-team", 0)
		b := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.1.0")
		scenario.ShipAs(t, pw.World, "human", b)
		require.True(t, pw.RunUntil(scenario.MaxRounds, func() bool { return slices.Contains(pw.Verified(pw.p.NS, b.Name), "dev") }))
		name := pw.freeze(t, ic, v1alpha1.FreezeEveryone, pw.p.NS, 0)
		pw.expectHeld(t, b, "prod")
		require.NoError(t, lifecycle.Unfreeze(pw.CtxOf(ic), pw.Client, name, "", &v1alpha1.FreezeLift{By: ic.Username}))
		pw.expectVerified(t, b)
	})
	t.Run("an exemption lets one pipeline through, in its namespace, until it expires", func(t *testing.T) {
		t.Parallel()
		pw := startPolicy(t, nil)
		pw.freeze(t, ic, v1alpha1.FreezeEveryone, "", 0)
		exempt := func(id scenario.Identity, ns string, d time.Duration) error {
			stamp, err := pw.StampFor(id, actor.Where{Namespace: ns, Pipeline: scenario.PipelineName})
			if err != nil {
				return err
			}
			_, err = lifecycle.ExemptFromFreezes(pw.CtxOf(id), pw.Client, lifecycle.ExemptRequest{Namespace: ns, Pipeline: scenario.PipelineName,
				Reason: "hotfix", ExpiresIn: d, CreatedBy: id.Username, Actor: stamp, Now: pw.Now()})
			pw.Round()
			return err
		}
		// Another namespace's exemption for a pipeline of the same name.
		require.NoError(t, exempt(alice, "other-team", time.Hour))
		b := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.1.0")
		scenario.ShipAs(t, pw.World, "human", b)
		pw.expectHeld(t, b, "dev")
		// This namespace's: it goes through, and each step says who let it.
		require.NoError(t, exempt(alice, pw.p.NS, 30*time.Minute))
		pw.expectVerified(t, b)
		var events v1alpha1.AuditEventList
		require.NoError(t, pw.Client.List(pw.Ctx, &events, client.InNamespace(pw.p.NS)))
		exempted := 0
		for _, e := range events.Items {
			if e.Spec.Action == "FreezeExempted" && e.Spec.BundleName == b.Name {
				exempted++
				assert.Contains(t, e.Spec.Message+" "+describeActor(e.Spec.Actor), "alice")
			}
		}
		assert.Positive(t, exempted, "a FreezeExempted AuditEvent per step")
		// Past its expiry, the next version is held again.
		pw.Advance(31 * time.Minute)
		b2 := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.2.0")
		scenario.ShipAs(t, pw.World, "human", b2)
		pw.expectHeld(t, b2, "dev")
	})
	t.Run("an agent's exemption is refused, and one written past admission is not honoured", func(t *testing.T) {
		t.Parallel()
		pw := startPolicy(t, nil)
		name := pw.freeze(t, ic, v1alpha1.FreezeEveryone, "", 0)
		fixer := scenario.ActorIdentities["agent"]
		stamp, err := pw.StampFor(fixer, actor.Where{Namespace: pw.p.NS, Pipeline: scenario.PipelineName})
		require.NoError(t, err)
		_, err = lifecycle.ExemptFromFreezes(pw.CtxOf(fixer), pw.Client, lifecycle.ExemptRequest{Namespace: pw.p.NS, Pipeline: scenario.PipelineName,
			Reason: "let me through", CreatedBy: fixer.Username, Actor: stamp, Now: pw.Now()})
		require.Error(t, err, "the actor policy refuses an agent's exemption")
		// Written straight to the store (an admin with etcd access, a
		// controller bug): the controller ignores it.
		var cw v1alpha1.ChangeWindow
		require.NoError(t, pw.Store().Get(pw.Ctx, client.ObjectKey{Name: name}, &cw))
		cw.Spec.Freeze.Exemptions = append(cw.Spec.Freeze.Exemptions, v1alpha1.FreezeExemption{Namespace: pw.p.NS, Pipeline: scenario.PipelineName,
			Reason: "let me through", CreatedBy: fixer.Username, Actor: stamp, ExpiresAt: metav1.NewTime(pw.Now().Add(time.Hour))})
		require.NoError(t, pw.Store().Update(pw.Ctx, &cw))
		b := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.1.0")
		scenario.ShipAs(t, pw.World, "human", b)
		pw.expectHeld(t, b, "dev")
		require.NoError(t, pw.Client.Get(pw.Ctx, client.ObjectKey{Name: name}, &cw))
		ignored := false
		for _, c := range cw.Status.Conditions {
			if c.Type == "ExemptionIgnored" && c.Status == metav1.ConditionTrue {
				ignored = true
			}
		}
		assert.True(t, ignored, "the freeze says the agent's exemption is ignored: %+v", cw.Status.Conditions)
	})
	t.Run("a namespace's admin lifts only their own freeze of it", func(t *testing.T) {
		t.Parallel()
		pw := startPolicy(t, nil)
		theirs := pw.freeze(t, ic, v1alpha1.FreezeEveryone, pw.p.NS, 0)
		err := lifecycle.Unfreeze(pw.CtxOf(teamAdm), pw.Client, theirs, teamAdm.Username, &v1alpha1.FreezeLift{By: teamAdm.Username})
		require.Error(t, err)
		assert.True(t, errors.Is(err, lifecycle.ErrForbidden), "%v", err)
		var cw v1alpha1.ChangeWindow
		require.NoError(t, pw.Client.Get(pw.Ctx, client.ObjectKey{Name: theirs}, &cw), "ic's freeze is still there")
		// Nor may it be changed in the admin's name (the freezes policy).
		_, _, err = lifecycle.Freeze(pw.CtxOf(teamAdm), pw.Client, lifecycle.FreezeRequest{Actors: v1alpha1.FreezeEveryone, Namespace: pw.p.NS,
			Reason: "mine now", CreatedBy: teamAdm.Username, OnlyOwn: true, Now: pw.Now()})
		require.Error(t, err)
		// Their own freeze of another scope they lift.
		require.NoError(t, lifecycle.Unfreeze(pw.CtxOf(ic), pw.Client, theirs, "", &v1alpha1.FreezeLift{By: ic.Username}))
		own := pw.freeze(t, teamAdm, v1alpha1.FreezeAgents, pw.p.NS, 0)
		require.NoError(t, lifecycle.Unfreeze(pw.CtxOf(teamAdm), pw.Client, own, teamAdm.Username, &v1alpha1.FreezeLift{By: teamAdm.Username}))
	})
}

func stepMessage(pw *policyWorld, b *v1alpha1.Bundle, env string) string {
	s, ok := pw.Steps(pw.p.NS, b.Name)[env]
	if !ok {
		return ""
	}
	return s.Status.Message
}

func describeActor(a *v1alpha1.Actor) string {
	if a == nil {
		return ""
	}
	return actor.Describe(*a)
}

// RequireStamp: a gate on the actor holds a version without a
// verified stamp, and people-only prod holds agents' and bots' versions.
func RequireStamp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, expr string
		admitted   []string // shippers whose version reaches prod
		held       []string
	}{
		{"actor.verified", "actor.verified", []string{"human", "bot", "agent"}, []string{"unstamped"}},
		{"people only", `actor.verified && actor.kind == "human"`, []string{"human"}, []string{"agent", "bot", "unstamped"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, who := range append(append([]string(nil), tc.admitted...), tc.held...) {
				pw := startPolicy(t, func(p *scenario.Plan) { p.Gate.Spec.Expression = tc.expr })
				b := scenario.ImageBundle(pw.p.NS, scenario.PipelineName, "1.1.0")
				if who == "unstamped" {
					// kubectl apply of a hand-written Bundle: no stamp.
					pw.MustApply(b)
				} else {
					scenario.ShipAs(t, pw.World, who, b)
				}
				if slices.Contains(tc.admitted, who) {
					pw.expectVerified(t, b)
					continue
				}
				pw.expectHeld(t, b, "prod")
				g := pw.gate(t, b.Name, pw.p.Gate.Name)
				assert.False(t, g.Status.Ready, "%s: %s", who, g.Status.Reason)
			}
		})
	}
}

// ApprovalFields: structured approval fields through the Graph, on the
// scenario World (spec 23 §8; QA on #2119). The prod gate's approval.fields
// reach its instance (the Graph copies them; the bug the live run found was
// that it did not), so kardinal approve without them is refused before it
// writes; an Approval written another way without them is recorded, not
// counted, and says why; the approve with them counts, the expression reads
// the counted ticket, and prod promotes.
func ApprovalFields(t *testing.T) {
	t.Parallel()
	pol := v1alpha1.GateApprovalPolicy{Required: 1, AllowedUsers: []string{alice.Username, carol.Username},
		Fields: []v1alpha1.ApprovalField{
			{Name: "ticketID", Required: true, Pattern: `^CHG[0-9]{7}$`},
			{Name: "riskAcknowledged", Type: "bool", Required: true, MustBe: "true"},
		}}
	pw := startPolicy(t, func(p *scenario.Plan) {
		g := approvalGate(p.NS, "change", pol)
		g.Spec.Expression = `approvals.fields.ticketID.exists(id, id.startsWith("CHG"))`
		p.Extra = append(p.Extra, g)
	})
	b := pw.shipToProdGate(t, "human", "1.1.0")
	inst := pw.gate(t, b.Name, "change")
	require.NotNil(t, inst.Spec.Approval)
	require.Len(t, inst.Spec.Approval.Fields, 2, "the Graph copies approval.fields into the instance")

	err := pw.approve(alice, b.Name, "prod")
	require.Error(t, err, "kardinal approve without the fields is refused")
	assert.Contains(t, err.Error(), "ticketID")

	// Carol writes her Approval by hand, without the fields.
	stamp, err := pw.StampFor(carol, actor.Where{Namespace: pw.p.NS, Environment: "prod"})
	require.NoError(t, err)
	require.NoError(t, pw.Client.Create(pw.CtxOf(carol), &v1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{Namespace: pw.p.NS, Name: "carol-by-hand",
			Labels: map[string]string{"kardinal.io/bundle": b.Name, "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.ApprovalSpec{Bundle: b.Name, BundleUID: string(b.UID), Environment: "prod", User: carol.Username,
			Groups: carol.Groups, Decision: lifecycle.DecisionApprove, Actor: stamp}}))
	pw.Round()
	pw.Round()
	found, ok, why := counted(pw.gate(t, b.Name, "change"), carol.Username)
	require.True(t, found, "carol's Approval is recorded\n%s", pw.Describe(pw.p.NS))
	assert.False(t, ok)
	assert.Equal(t, "approval.fields: riskAcknowledged is required; ticketID is required", why)
	pw.expectHeld(t, b, "prod")

	stamp, err = pw.StampFor(alice, actor.Where{Namespace: pw.p.NS, Environment: "prod"})
	require.NoError(t, err)
	_, _, err = lifecycle.RecordApproval(pw.CtxOf(alice), pw.Client, lifecycle.ApprovalRequest{Namespace: pw.p.NS, Bundle: b.Name,
		Environment: "prod", User: alice.Username, Groups: alice.Groups, Decision: lifecycle.DecisionApprove, Actor: stamp,
		Fields: map[string]string{"ticketID": "CHG0031337", "riskAcknowledged": "true"}})
	require.NoError(t, err)
	pw.Round()
	pw.Round()
	g := pw.gate(t, b.Name, "change")
	_, ok, why = counted(g, alice.Username)
	assert.True(t, ok, why)
	for _, rec := range g.Status.Approvals {
		if rec.User == alice.Username {
			assert.Equal(t, map[string]string{"ticketID": "CHG0031337", "riskAcknowledged": "true"}, rec.Fields)
		}
	}
	pw.expectVerified(t, b)
}
