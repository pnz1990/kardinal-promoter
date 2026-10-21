// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentbudget

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

var t0 = time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

func agent(rules ...v1alpha1.AgentBudgetRule) *v1alpha1.AgentIdentity {
	return &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1},
		Spec: v1alpha1.AgentIdentitySpec{Budgets: rules}}
}

func rule(name string, envs ...string) v1alpha1.AgentBudgetRule {
	return v1alpha1.AgentBudgetRule{Name: name, Environments: envs, Namespaces: []string{"*"}}
}

func ask(uid, env string, at time.Time) request {
	return request{key: grantKey{uid, env}, namespace: "payments", pipeline: "checkout", bundle: "b-" + uid,
		name: uid + "-" + env, since: at, created: at}
}

func target(uid, env, fleet string, targets int, at time.Time) request {
	q := ask(uid, env, at)
	q.fleet, q.targets = fleet, targets
	return q
}

// running: a grant's step exists and is under way.
func running() grantState {
	return grantState{bundleFound: true, namespace: "payments", stepFound: true, stepState: "Promoting"}
}

func done(state string) grantState {
	return grantState{bundleFound: true, namespace: "payments", stepFound: true, stepState: state}
}

func granted(out outcome) []string {
	var keys []string
	for _, g := range out.granted {
		keys = append(keys, g.BundleUID+"/"+g.Environment)
	}
	return keys
}

func ruleUsage(t *testing.T, u *v1alpha1.AgentUsage, name string) v1alpha1.AgentBudgetRuleUsage {
	t.Helper()
	require.NotNil(t, u)
	for _, r := range u.Rules {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no usage for rule %s", name)
	return v1alpha1.AgentBudgetRuleUsage{}
}

// TestMatching: a rule applies by namespace and environment; a fleet target
// through its own name or its fleet's; several rules may apply.
// Covers BUDGET-LEDGER-01.
func TestMatching(t *testing.T) {
	rules := []v1alpha1.AgentBudgetRule{
		{Name: "prod", Environments: []string{"prod", "prod-*"}},
		{Name: "payments", Environments: []string{"*"}, Namespaces: []string{"payments-*"}},
		{Name: "eu", Environments: []string{"prod-eu"}, Namespaces: []string{"payments", "billing"}},
	}
	for _, tc := range []struct {
		name, ns, env, fleet string
		want                 []string
	}{
		{"an exact environment", "shop", "prod", "", []string{"prod"}},
		{"a prefix", "shop", "prod-us", "", []string{"prod"}},
		{"no match", "shop", "uat", "", nil},
		{"a fleet target by its fleet", "shop", "eu-1", "prod", []string{"prod"}},
		{"a fleet target by its own name", "payments", "prod-eu", "", []string{"prod", "eu"}},
		{"a namespace prefix", "payments-a", "uat", "", []string{"payments"}},
		{"namespaces default to every one", "anything", "prod", "", []string{"prod"}},
		{"a namespace not listed", "shop", "prod-eu", "", []string{"prod"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Matching(rules, tc.ns, tc.env, tc.fleet))
		})
	}
}

// TestGranting: requests are granted in arrival order, strictly per rule; a
// blocked rule does not block another rule's requests; a version counts once
// per Bundle and environment (a fleet's targets are one); maxUnavailable
// holds until the environment moves on.
// Covers BUDGET-LEDGER-01.
func TestGranting(t *testing.T) {
	now := t0
	t.Run("strict order per rule", func(t *testing.T) {
		r := rule("prod", "prod")
		r.MaxConcurrent = 1
		out := evaluate(agent(r), nil, facts{requests: []request{ask("b", "prod", now.Add(2*time.Second)), ask("a", "prod", now.Add(time.Second))}}, now)
		assert.Equal(t, []string{"a/prod"}, granted(out), "the older request first")
		u := ruleUsage(t, out.usage, "prod")
		assert.Equal(t, int32(1), u.Concurrent.Used)
		assert.Equal(t, int32(1), u.Waiting)
		assert.Equal(t, []string{"maxConcurrent"}, u.Exhausted)
	})
	t.Run("a blocked rule does not block another rule's requests", func(t *testing.T) {
		p, s := rule("prod", "prod"), rule("staging", "staging")
		p.MaxConcurrent, s.MaxConcurrent = 1, 5
		old := &v1alpha1.AgentUsage{ObservedGeneration: 1, Grants: []v1alpha1.AgentBudgetGrant{
			{BundleUID: "x", Environment: "prod", Rules: []string{"prod"}, GrantedAt: metav1.NewTime(now.Add(-time.Hour))}}}
		f := facts{grants: map[grantKey]grantState{{"x", "prod"}: running()},
			requests: []request{ask("a", "prod", now), ask("b", "staging", now.Add(time.Second))}}
		out := evaluate(agent(p, s), old, f, now)
		assert.Equal(t, []string{"b/staging"}, granted(out))
	})
	t.Run("a request that does not fit blocks the later ones of its rules", func(t *testing.T) {
		r := rule("prod", "prod")
		r.VersionsPerDay = 1
		f := facts{requests: []request{ask("a", "prod", now), target("b", "prod-eu", "prod", 4, now.Add(time.Second)),
			ask("c", "prod", now.Add(2*time.Second))}}
		out := evaluate(agent(r), nil, f, now)
		assert.Equal(t, []string{"a/prod"}, granted(out))
		assert.Equal(t, int32(2), ruleUsage(t, out.usage, "prod").Waiting)
	})
	t.Run("a version counts once per Bundle and environment; a fleet's targets are one", func(t *testing.T) {
		r := rule("prod", "prod")
		r.VersionsPerDay = 1
		f := facts{requests: []request{target("a", "eu-1", "prod", 3, now), target("a", "eu-2", "prod", 3, now.Add(time.Second)),
			target("a", "eu-3", "prod", 3, now.Add(2*time.Second))}}
		out := evaluate(agent(r), nil, f, now)
		assert.Equal(t, []string{"a/eu-1", "a/eu-2", "a/eu-3"}, granted(out))
		assert.True(t, out.granted[0].Version)
		assert.False(t, out.granted[1].Version)
		assert.Equal(t, int32(1), ruleUsage(t, out.usage, "prod").Versions.Used)
	})
	t.Run("fleetTargetsPerHour counts targets only", func(t *testing.T) {
		r := rule("prod", "prod", "prod-*")
		r.FleetTargetsPerHour = 2
		f := facts{requests: []request{target("a", "eu-1", "prod", 9, now), target("a", "eu-2", "prod", 9, now.Add(time.Second)),
			ask("b", "prod-us", now.Add(2*time.Second)), target("a", "eu-3", "prod", 9, now.Add(3*time.Second))}}
		out := evaluate(agent(r), nil, f, now)
		assert.Equal(t, []string{"a/eu-1", "a/eu-2", "b/prod-us"}, granted(out), "a plain environment is not a target")
		assert.Equal(t, int32(2), ruleUsage(t, out.usage, "prod").FleetTargets.Used)
	})
	t.Run("maxUnavailable holds while the environment runs the failed version", func(t *testing.T) {
		r := rule("prod", "prod", "prod-*")
		r.MaxUnavailable = 1
		old := &v1alpha1.AgentUsage{ObservedGeneration: 1, Grants: []v1alpha1.AgentBudgetGrant{
			{BundleUID: "x", Environment: "prod-eu", Rules: []string{"prod"}, GrantedAt: metav1.NewTime(now.Add(-time.Hour))}}}
		failed := done("Failed")
		failed.current = true
		out := evaluate(agent(r), old, facts{grants: map[grantKey]grantState{{"x", "prod-eu"}: failed},
			requests: []request{ask("a", "prod-us", now)}}, now)
		assert.Empty(t, out.granted)
		require.Len(t, out.usage.Grants, 1)
		assert.True(t, out.usage.Grants[0].Unavailable)
		assert.True(t, out.usage.Grants[0].Settled)
		assert.Equal(t, []string{"maxUnavailable"}, ruleUsage(t, out.usage, "prod").Exhausted)

		// A newer version there (from anyone) ends it.
		moved := done("Failed")
		out = evaluate(agent(r), out.usage, facts{grants: map[grantKey]grantState{{"x", "prod-eu"}: moved},
			requests: []request{ask("a", "prod-us", now)}}, now)
		assert.Equal(t, []string{"a/prod-us"}, granted(out))
		assert.Len(t, out.usage.Grants, 1, "the settled, available grant of a rule with no window is dropped")
	})
	t.Run("a step gone after it failed keeps the place while its environment runs it", func(t *testing.T) {
		r := rule("prod", "prod")
		r.MaxUnavailable = 1
		old := &v1alpha1.AgentUsage{ObservedGeneration: 1, Grants: []v1alpha1.AgentBudgetGrant{
			{BundleUID: "x", Environment: "prod", Rules: []string{"prod"}, Unavailable: true, Settled: true,
				GrantedAt: metav1.NewTime(now.Add(-time.Hour))}}}
		out := evaluate(agent(r), old, facts{grants: map[grantKey]grantState{{"x", "prod"}: {bundleFound: true, namespace: "payments", current: true}}}, now)
		assert.True(t, out.usage.Grants[0].Unavailable)
	})
	t.Run("a request no rule matches is left alone", func(t *testing.T) {
		r := rule("prod", "prod")
		r.MaxConcurrent = 1
		out := evaluate(agent(r), nil, facts{requests: []request{ask("a", "uat", now)}}, now)
		assert.Empty(t, out.granted)
		assert.Zero(t, ruleUsage(t, out.usage, "prod").Waiting)
	})
	t.Run("an already granted request is not granted twice", func(t *testing.T) {
		r := rule("prod", "prod")
		r.MaxConcurrent = 2
		old := &v1alpha1.AgentUsage{ObservedGeneration: 1, Grants: []v1alpha1.AgentBudgetGrant{
			{BundleUID: "a", Environment: "prod", Rules: []string{"prod"}, GrantedAt: metav1.NewTime(now.Add(-time.Minute))}}}
		// The Graph recreated the step: it is Pending again, and asks.
		out := evaluate(agent(r), old, facts{grants: map[grantKey]grantState{{"a", "prod"}: done("Pending")},
			requests: []request{ask("a", "prod", now)}}, now)
		assert.Empty(t, out.granted, "a step deleted and recreated finds the same grant")
		assert.Len(t, out.usage.Grants, 1)
	})
}

// TestWindows: rolling counts, windowStart and freesAt, nextEvaluationAt
// with and without waiting requests, pruning, and a clock that steps back.
// Covers BUDGET-LEDGER-01.
func TestWindows(t *testing.T) {
	r := rule("prod", "prod")
	r.VersionsPerDay = 2
	g := func(uid string, age time.Duration, state grantState) (v1alpha1.AgentBudgetGrant, grantState) {
		return v1alpha1.AgentBudgetGrant{BundleUID: uid, Environment: "prod", Rules: []string{"prod"}, Version: true,
			GrantedAt: metav1.NewTime(t0.Add(-age))}, state
	}
	old := &v1alpha1.AgentUsage{ObservedGeneration: 1}
	f := facts{grants: map[grantKey]grantState{}}
	for _, x := range []struct {
		uid string
		age time.Duration
	}{{"old", 25 * time.Hour}, {"a", 20 * time.Hour}, {"b", 2 * time.Hour}} {
		gr, st := g(x.uid, x.age, done("Verified"))
		old.Grants = append(old.Grants, gr)
		f.grants[grantKey{x.uid, "prod"}] = st
	}
	f.requests = []request{ask("c", "prod", t0)}
	out := evaluate(agent(r), old, f, t0)
	u := ruleUsage(t, out.usage, "prod")
	assert.Equal(t, int32(2), u.Versions.Used, "the grant 25 hours old has left the window")
	assert.Equal(t, t0.Add(-VersionWindow), u.Versions.WindowStart.Time)
	assert.Equal(t, t0.Add(4*time.Hour), u.Versions.FreesAt.Time, "the oldest counted grant leaves in 4 hours")
	assert.Empty(t, out.granted)
	assert.Equal(t, int32(1), u.Waiting)
	require.NotNil(t, out.usage.NextEvaluationAt)
	assert.Equal(t, t0.Add(4*time.Hour), out.usage.NextEvaluationAt.Time, "with a request waiting: when room frees")
	assert.Equal(t, 4*time.Hour, out.requeue)
	assert.Len(t, out.usage.Grants, 2, "the settled grant out of every window is dropped")

	t.Run("nothing waiting: the earliest drop, not before IdleRecheck", func(t *testing.T) {
		near := &v1alpha1.AgentUsage{ObservedGeneration: 1, Grants: []v1alpha1.AgentBudgetGrant{
			{BundleUID: "a", Environment: "prod", Rules: []string{"prod"}, Version: true, GrantedAt: metav1.NewTime(t0.Add(-VersionWindow + time.Minute))}}}
		out := evaluate(agent(r), near, facts{grants: map[grantKey]grantState{{"a", "prod"}: done("Verified")}}, t0)
		assert.Equal(t, t0.Add(IdleRecheck), out.usage.NextEvaluationAt.Time)
	})
	t.Run("nothing to drop and nothing waiting: no requeue", func(t *testing.T) {
		c := rule("prod", "prod")
		c.MaxConcurrent = 1
		out := evaluate(agent(c), nil, facts{}, t0)
		assert.Nil(t, out.usage.NextEvaluationAt)
		assert.Zero(t, out.requeue)
	})
	t.Run("the clock steps back", func(t *testing.T) {
		back := t0.Add(-3 * time.Hour) // the "b" grant is now in the future
		out := evaluate(agent(r), out.usage, f, back)
		u := ruleUsage(t, out.usage, "prod")
		assert.Equal(t, int32(2), u.Versions.Used, "a grant from the future still counts")
		assert.Empty(t, out.granted)
	})
}

// TestExemptions: an exemption in effect grants without room and is
// counted; the 24-hour cap from first sight; removal ends it; one made by an
// agent or without a stamp never counts, with a condition and one event.
// Covers BUDGET-LEDGER-02.
func TestExemptions(t *testing.T) {
	r := rule("prod", "prod")
	r.MaxConcurrent = 1
	person := &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}
	ex := func(name string, a *v1alpha1.Actor, expires time.Time) v1alpha1.AgentBudgetExemption {
		return v1alpha1.AgentBudgetExemption{Name: name, Namespace: "payments", Pipeline: "checkout", Environment: "prod",
			Reason: "hotfix", ExpiresAt: metav1.NewTime(expires), CreatedBy: "bob", Actor: a}
	}
	full := &v1alpha1.AgentUsage{ObservedGeneration: 1, Grants: []v1alpha1.AgentBudgetGrant{
		{BundleUID: "x", Environment: "prod", Rules: []string{"prod"}, GrantedAt: metav1.NewTime(t0.Add(-time.Hour))}}}
	busy := func() facts {
		return facts{grants: map[grantKey]grantState{{"x", "prod"}: running()}, requests: []request{ask("a", "prod", t0)},
			stampsVerified: true}
	}

	t.Run("in effect: granted without room, counted", func(t *testing.T) {
		id := agent(r)
		id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{ex("inc-1", person, t0.Add(time.Hour))}
		out := evaluate(id, full, busy(), t0)
		require.Len(t, out.granted, 1)
		assert.Equal(t, "inc-1", out.granted[0].Exemption)
		assert.Equal(t, int32(2), ruleUsage(t, out.usage, "prod").Concurrent.Used, "an exempt grant is counted")
		require.Len(t, out.usage.Exemptions, 1)
		assert.Equal(t, t0, out.usage.Exemptions[0].FirstSeenAt.Time)
		assert.Equal(t, t0.Add(time.Hour), out.usage.Exemptions[0].ExpiresAt.Time)
	})
	t.Run("a version-scoped exemption matches only that version", func(t *testing.T) {
		id := agent(r)
		e := ex("inc-1", person, t0.Add(time.Hour))
		e.Bundle = "b-other"
		id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{e}
		assert.Empty(t, evaluate(id, full, busy(), t0).granted)
	})
	t.Run("the 24-hour cap from first sight, and no restart once expired", func(t *testing.T) {
		id := agent(r)
		id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{ex("inc-1", person, t0.Add(7*24*time.Hour))}
		first := evaluate(id, full, facts{grants: map[grantKey]grantState{{"x", "prod"}: running()}, stampsVerified: true}, t0)
		assert.Equal(t, t0.Add(ExemptionCap), first.usage.Exemptions[0].ExpiresAt.Time)
		later := t0.Add(ExemptionCap + time.Minute)
		out := evaluate(id, first.usage, busy(), later)
		assert.Empty(t, out.granted, "expired at the cap")
		assert.Equal(t, t0, out.usage.Exemptions[0].FirstSeenAt.Time, "still listed, so the cap does not restart")
	})
	t.Run("removal ends it", func(t *testing.T) {
		id := agent(r)
		id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{ex("inc-1", person, t0.Add(time.Hour))}
		first := evaluate(id, full, facts{grants: map[grantKey]grantState{{"x", "prod"}: running()}, stampsVerified: true}, t0)
		id.Spec.BudgetExemptions, id.Generation = nil, 2
		out := evaluate(id, first.usage, busy(), t0.Add(time.Minute))
		assert.Empty(t, out.granted)
		assert.Empty(t, out.usage.Exemptions)
	})
	for _, tc := range []struct {
		name   string
		actor  *v1alpha1.Actor
		reason string
		note   string
	}{
		{"made by an agent", &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:other",
			Agent: "other", Sponsor: "carol"}, v1alpha1.ExemptionMadeByAgent, "inc-1 (made by agent other)"},
		{"without an actor stamp", nil, v1alpha1.ExemptionUnverified, "inc-1 (no actor stamp)"},
	} {
		t.Run("ignored: "+tc.name, func(t *testing.T) {
			id := agent(r)
			id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{ex("inc-1", tc.actor, t0.Add(time.Hour))}
			out := evaluate(id, full, busy(), t0)
			assert.Empty(t, out.granted, "it never counts")
			require.Len(t, out.usage.Exemptions, 1)
			assert.Equal(t, tc.reason, out.usage.Exemptions[0].Ignored)
			c := meta.FindStatusCondition(out.usage.Conditions, ConditionExemptionIgnored)
			require.NotNil(t, c)
			assert.Equal(t, tc.reason, c.Reason)
			assert.Contains(t, c.Message, tc.note)
			require.Len(t, out.ignored, 1, "one event")
			again := evaluate(id, out.usage, busy(), t0.Add(time.Minute))
			assert.Empty(t, again.ignored, "not repeated on a re-run")
			assert.True(t, unchanged(out.usage, again.usage))
		})
	}
	t.Run("a stamped exemption counts only while the stamp policy is in force", func(t *testing.T) {
		id := agent(r)
		id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{ex("inc-1", person, t0.Add(time.Hour))}
		f := busy()
		f.stampsVerified = false
		out := evaluate(id, full, f, t0)
		assert.Empty(t, out.granted, "anyone could have written a person's stamp")
		assert.Equal(t, v1alpha1.ExemptionUnverified, out.usage.Exemptions[0].Ignored)
		c := meta.FindStatusCondition(out.usage.Conditions, ConditionExemptionIgnored)
		require.NotNil(t, c)
		assert.Contains(t, c.Message, "inc-1 (its actor stamp is not checked: the agent-budgets admission policy is not installed)")
		f.stampsVerified = true
		back := evaluate(id, out.usage, f, t0.Add(time.Minute))
		require.Len(t, back.granted, 1, "counted once the policy is back")
		assert.Equal(t, t0.Add(time.Hour), back.usage.Exemptions[0].ExpiresAt.Time, "from its first sight: the cap does not restart")
	})
	t.Run("the condition goes once the exemption is removed", func(t *testing.T) {
		id := agent(r)
		id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{ex("inc-1", nil, t0.Add(time.Hour))}
		first := evaluate(id, full, busy(), t0)
		id.Spec.BudgetExemptions, id.Generation = nil, 2
		out := evaluate(id, first.usage, busy(), t0)
		assert.Nil(t, meta.FindStatusCondition(out.usage.Conditions, ConditionExemptionIgnored))
	})
}

// TestFleetShare: a version's unsettled targets in one fleet, plus this one,
// within the share of the fleet's current size, rounded up and at least one.
// Covers BUDGET-LEDGER-01.
func TestFleetShare(t *testing.T) {
	for _, tc := range []struct {
		name    string
		share   string
		targets int
		want    int
	}{
		{"25% of 10 is 3 (rounded up)", "25%", 10, 3},
		{"1% of 10 is 1 (at least one)", "1%", 10, 1},
		{"100% of 4 is 4", "100%", 4, 4},
		{"the fleet's size unknown: none", "50%", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := rule("prod", "prod")
			r.FleetShare = tc.share
			var reqs []request
			for i := range 6 {
				reqs = append(reqs, target("a", fmt.Sprintf("t-%d", i), "prod", tc.targets, t0.Add(time.Duration(i)*time.Second)))
			}
			out := evaluate(agent(r), nil, facts{requests: reqs}, t0)
			assert.Len(t, out.granted, tc.want)
			if tc.want < 6 {
				assert.Contains(t, ruleUsage(t, out.usage, "prod").Exhausted, "fleetShare")
			}
		})
	}
	t.Run("per version: another version has its own share", func(t *testing.T) {
		r := rule("prod", "prod")
		r.FleetShare = "10%"
		out := evaluate(agent(r), nil, facts{requests: []request{target("a", "t-1", "prod", 10, t0),
			target("a", "t-2", "prod", 10, t0.Add(time.Second)), target("b", "t-3", "prod", 10, t0.Add(2*time.Second))}}, t0)
		assert.Equal(t, []string{"a/t-1"}, granted(out), "strict order: a's second target blocks the rule for b")
	})
	t.Run("a fleet grown since translation uses its new size", func(t *testing.T) {
		r := rule("prod", "prod")
		r.FleetShare = "50%"
		old := &v1alpha1.AgentUsage{ObservedGeneration: 1, Grants: []v1alpha1.AgentBudgetGrant{
			{BundleUID: "a", Environment: "t-0", Fleet: "prod", Rules: []string{"prod"}, GrantedAt: metav1.NewTime(t0)}}}
		f := facts{grants: map[grantKey]grantState{{"a", "t-0"}: running()}, requests: []request{target("a", "t-1", "prod", 2, t0)}}
		assert.Empty(t, evaluate(agent(r), old, f, t0).granted, "50% of 2 is 1, taken")
		f.requests = []request{target("a", "t-1", "prod", 4, t0)}
		assert.Len(t, evaluate(agent(r), old, f, t0).granted, 1, "50% of 4 is 2")
	})
}

// TestSeeding: on a new ledger, started steps inside the window are counted,
// settled or not; settled ones outside it are not; ones under way are.
// Covers BUDGET-LEDGER-03.
func TestSeeding(t *testing.T) {
	r := rule("prod", "prod", "prod-*")
	r.VersionsPerDay, r.MaxConcurrent = 5, 5
	seed := func(uid, env string, age time.Duration, settled bool) request {
		q := ask(uid, env, time.Time{})
		q.started, q.settled = t0.Add(-age), settled
		return q
	}
	out := evaluate(agent(r), nil, facts{seeds: []request{
		seed("a", "prod", time.Hour, true),        // this morning's, done: counted
		seed("b", "prod", 30*time.Hour, true),     // yesterday's, done: not
		seed("c", "prod-eu", 30*time.Hour, false), // still under way: counted
		seed("d", "uat", time.Hour, false),        // no rule: not
	}}, t0)
	var keys []string
	for _, g := range out.usage.Grants {
		assert.True(t, g.Seeded)
		keys = append(keys, g.BundleUID+"/"+g.Environment)
	}
	assert.Equal(t, []string{"a/prod", "c/prod-eu"}, keys)
	u := ruleUsage(t, out.usage, "prod")
	assert.Equal(t, int32(1), u.Versions.Used, "c's version is older than the window")
	assert.Equal(t, int32(1), u.Concurrent.Used)
	require.NotNil(t, out.usage.NextEvaluationAt)
	assert.LessOrEqual(t, out.requeue, IdleRecheck, "re-read every 5 minutes while a seeded grant is unsettled")

	again := evaluate(agent(r), out.usage, facts{seeds: []request{seed("a", "prod", time.Hour, true)},
		grants: map[grantKey]grantState{{"a", "prod"}: done("Verified"), {"c", "prod-eu"}: running()}}, t0)
	assert.Len(t, again.usage.Grants, 2, "seeding twice adds nothing")
}

// TestLedgerFull: 2,000 grants in the ledger, 3,000 across the agent's
// ledgers, or the object's size: new requests wait, existing grants stay,
// LedgerFull says why, once.
// Covers BUDGET-LEDGER-02.
func TestLedgerFull(t *testing.T) {
	r := rule("prod", "prod*")
	r.FleetTargetsPerHour = 1000
	many := func(n int) (*v1alpha1.AgentUsage, map[grantKey]grantState) {
		u := &v1alpha1.AgentUsage{ObservedGeneration: 1}
		st := map[grantKey]grantState{}
		for i := range n {
			uid := fmt.Sprintf("u-%d", i)
			u.Grants = append(u.Grants, v1alpha1.AgentBudgetGrant{BundleUID: uid, Environment: "prod-1", Fleet: "prod",
				Rules: []string{"prod"}, GrantedAt: metav1.NewTime(t0.Add(-time.Minute))})
			st[grantKey{uid, "prod-1"}] = running()
		}
		return u, st
	}
	for _, tc := range []struct {
		name   string
		grants int
		other  int
		bytes  int
		reason string
	}{
		{"2,000 grants in the ledger", MaxGrants, 0, 0, FullGrants},
		{"3,000 across the agent's ledgers", 1000, 2000, 0, FullAgentGrants},
		{"the object's size", 10, 0, 100, FullObjectSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, st := many(tc.grants)
			f := facts{grants: st, otherGrants: tc.other, requests: []request{ask("new", "prod", t0)}}
			if tc.bytes > 0 {
				f.baseBytes, f.maxBytes = 1000, 1000+tc.bytes // the ledger alone is past it
			}
			out := evaluate(agent(r), old, f, t0)
			assert.Empty(t, out.granted)
			assert.Len(t, out.usage.Grants, tc.grants, "existing grants keep working")
			c := meta.FindStatusCondition(out.usage.Conditions, ConditionLedgerFull)
			require.NotNil(t, c)
			assert.Equal(t, tc.reason, c.Reason)
			assert.Equal(t, tc.reason, out.full, "one event")
			assert.Equal(t, int32(1), ruleUsage(t, out.usage, "prod").Waiting)
			again := evaluate(agent(r), out.usage, f, t0)
			assert.Empty(t, again.full, "not repeated")
		})
	}
	t.Run("the other ledgers keep granting", func(t *testing.T) {
		old, st := many(10)
		out := evaluate(agent(r), old, facts{grants: st, otherGrants: 2000, requests: []request{ask("new", "prod", t0)}}, t0)
		assert.Len(t, out.granted, 1, "10 + 2,000 is under 3,000")
		assert.Nil(t, meta.FindStatusCondition(out.usage.Conditions, ConditionLedgerFull))
	})
}

// TestRespec: after a spec change a grant's rules are re-derived from its
// Bundle's namespace; a grant whose Bundle is gone keeps the rules still
// defined; budgets removed remove the ledger.
// Covers BUDGET-LEDGER-01.
func TestRespec(t *testing.T) {
	r := rule("prod", "prod")
	r.MaxConcurrent = 3
	old := &v1alpha1.AgentUsage{ObservedGeneration: 1, Grants: []v1alpha1.AgentBudgetGrant{
		{BundleUID: "a", Environment: "prod", Rules: []string{"prod"}, GrantedAt: metav1.NewTime(t0)},
		{BundleUID: "gone", Environment: "prod", Rules: []string{"prod", "old"}, Unavailable: true, GrantedAt: metav1.NewTime(t0)}}}
	id := agent(r, v1alpha1.AgentBudgetRule{Name: "all", Environments: []string{"*"}, MaxConcurrent: 9})
	id.Generation = 2
	out := evaluate(id, old, facts{grants: map[grantKey]grantState{{"a", "prod"}: running(),
		{"gone", "prod"}: {current: false}}}, t0)
	assert.Equal(t, []string{"prod", "all"}, out.usage.Grants[0].Rules)
	assert.Equal(t, int64(2), out.usage.ObservedGeneration)
	assert.Len(t, out.usage.Grants, 1, "the gone Bundle's grant is settled and available, with no window: dropped")

	id.Spec.Budgets = nil
	assert.Nil(t, evaluate(id, out.usage, facts{}, t0).usage, "no budgets: no ledger")
}

// TestIdempotent: the same inputs give an unchanged ledger, so a re-run
// writes nothing.
// Covers BUDGET-LEDGER-01.
func TestIdempotent(t *testing.T) {
	r := rule("prod", "prod")
	r.MaxConcurrent, r.VersionsPerDay = 1, 3
	f := facts{requests: []request{ask("a", "prod", t0), ask("b", "prod", t0.Add(time.Second))}}
	first := evaluate(agent(r), nil, f, t0)
	f.grants = map[grantKey]grantState{{"a", "prod"}: running()}
	second := evaluate(agent(r), first.usage, f, t0.Add(10*time.Second))
	assert.Empty(t, second.granted)
	assert.True(t, unchanged(first.usage, second.usage), "only the evaluation times moved")
	assert.False(t, unchanged(nil, second.usage))
}
