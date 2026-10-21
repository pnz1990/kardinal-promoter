// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentcases

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise"
)

// burstRule is the bursting agent's budget: into prod, one promotion at a
// time and three versions a day.
var burstRule = v1alpha1.AgentBudgetRule{Name: "prod", Environments: []string{"prod"}, MaxConcurrent: 1, VersionsPerDay: 3}

// othersRounds bounds the rounds the agents without budgets take to get a
// burst's versions Verified everywhere: what they take with budgets off
// (23 at every size, hack/ci/perf-baseline.txt) and room for the burst's
// approvals.
const othersRounds = 45

// RunBudgets is the enterprise of Run with agent budgets on
// (docs/design/24-agent-budgets.md section 19): the registry, the active
// agents shipping in bursts, and one of them, the burster, with
// burstRule on prod and a Pipeline per lane of its own. Its versions beyond
// the rule are held, Pending with the budget message, never refused or
// failed, while the other agents' versions converge as they do without
// budgets (SCEN-AGENT-BUDGET-01); at every round its unsettled grants are at
// most 1 and its grants in the window at most 3. A held version goes on
// when a granted one settles and when the window rolls over (the clock
// jumps to the ledger's freesAt), and a held version superseded by a newer
// one leaves the queue without a grant (SCEN-AGENT-BUDGET-02).
func RunBudgets(t *testing.T) {
	sz := agentSizeFromEnv()
	reg := agentRegistry(sz)
	require.Len(t, reg.active, sz.Agents, "the registry has an active agent per team in turn")
	w := scenario.NewWorld(t, scenario.Options{PolicyNamespaces: []string{scenario.PolicyNS}, Watch: true, Tick: 30 * time.Second,
		Actors: true, Budgets: true, MaxIndexBytes: sz.IndexBytes})
	require.NoError(t, w.Client.Create(w.Ctx, scenario.TenantObjects(scenario.PolicyNS)[0]))
	w.MustApply(enterprise.OrgGates()...)
	for i := 1; i <= sz.Teams; i++ {
		enterprise.Tenant(t, w, enterprise.Team(i), sz.PipelinesPerTeam, i%2 == 0)
	}
	w.MustApply(reg.ids...)
	require.True(t, w.RunUntil(20, func() bool { return w.Round() == 0 }), "the registry settles")
	assert.Zero(t, w.Reconciles()[scenario.KindAgentBudget]-len(reg.ids),
		"the budget reconciler evaluates each AgentIdentity once: none has budgets, so it writes nothing")

	burster, others := reg.active[0], reg.active[1:]
	lanes := make([]string, sz.PipelinesPerTeam)
	for j := range lanes {
		lanes[j] = fmt.Sprintf("burst-%02d", j+1)
		enterprise.AddPipeline(t, w, burster.ns, lanes[j], teamIndex(burster.ns)%2 == 0)
	}
	setBudgets(t, w, burster.name, burstRule)
	require.True(t, w.RunUntil(5, func() bool { return ledgerCurrent(w, burster.name) }), "the burster's ledger is evaluated")

	watch := &budgetWatch{t: t, agent: burster.name, ns: burster.ns, rule: burstRule, waitedOn: map[string][]string{}}
	w.Actors = append(w.Actors, scenario.Reviewer, approveAs, watch.check)

	newest := map[string]string{} // ns/pipeline -> the newest version of an agent without budgets
	shipOthers := func(burst int) {
		for _, a := range others {
			b := scenario.ImageBundle(a.ns, a.pipeline, fmt.Sprintf("1.%d.0", burst))
			b.Name = fmt.Sprintf("%s-%s-b%d", a.pipeline, a.name, burst)
			cliErr, refused, err := w.CreateBundle(a.id, b)
			require.NoError(t, err)
			require.NoError(t, cliErr, "agent %s", a.name)
			require.Empty(t, refused, "agent %s", a.name)
			newest[a.ns+"/"+a.pipeline] = b.Name
		}
	}
	othersVerified := func() bool {
		for key, b := range newest {
			ns, _, _ := strings.Cut(key, "/")
			if w.Bundle(ns, b).Status.Phase != "Verified" {
				return false
			}
		}
		return true
	}
	ship := func(lane string, v int) string {
		b := scenario.ImageBundle(burster.ns, lane, fmt.Sprintf("3.%d.0", v))
		b.Name = fmt.Sprintf("%s-v%d", lane, v)
		cliErr, refused, err := w.CreateBundle(burster.id, b)
		require.NoError(t, err)
		require.NoError(t, cliErr, "the burster's version %s is not refused: its budget holds it in prod", b.Name)
		require.Empty(t, refused, "the burster's version %s is admitted", b.Name)
		return b.Name
	}
	phase := func(name string) string { return w.Bundle(burster.ns, name).Status.Phase }

	// Burst 1: a version into every lane. The first into prod is granted;
	// the next waits (maxConcurrent) until it settles, then goes on.
	shipOthers(1)
	var v1 []string
	for _, l := range lanes {
		v1 = append(v1, ship(l, 1))
	}
	start := w.Rounds()
	require.True(t, w.RunUntil(othersRounds+30*len(lanes), func() bool {
		return othersVerified() && allPhase(phase, v1, "Verified")
	}), "burst 1: every version is Verified, the burster's one prod promotion at a time\n%s", w.Describe(burster.ns))
	t.Logf("burst 1: %d versions of agents without budgets and %d of the burster Verified in %d rounds",
		len(newest), len(v1), w.Rounds()-start)
	// All but the first granted waited while another was under way, and
	// went on when it settled.
	waited := 0
	for _, b := range v1 {
		if slices.Contains(watch.waitedOn[b], "maxConcurrent") {
			waited++
		}
	}
	assert.GreaterOrEqual(t, waited, len(v1)-1, "the burster's versions waited for one another in prod: %v", watch.waitedOn)

	// Burst 2: a version into every lane again. Its versions beyond the
	// day's three are held in prod, Pending with the budget message, while
	// the other agents' converge as before.
	shipOthers(2)
	var v2 []string
	for _, l := range lanes {
		v2 = append(v2, ship(l, 2))
	}
	start = w.Rounds()
	grantsLeft := int(burstRule.VersionsPerDay) - len(v1)
	settled := func() bool {
		verified, held := 0, 0
		for _, b := range v2 {
			switch {
			case phase(b) == "Verified":
				verified++
			case heldInProd(w, burster.ns, b):
				held++
			}
		}
		return othersVerified() && verified == grantsLeft && held == len(v2)-grantsLeft
	}
	require.True(t, w.RunUntil(othersRounds+30*len(lanes), settled),
		"burst 2: the other agents' versions are Verified, %d of the burster's too and the rest held in prod\n%s",
		grantsLeft, w.Describe(burster.ns))
	burst2 := w.Rounds() - start
	t.Logf("burst 2: the other agents' versions Verified, the burster's %d held, in %d rounds", len(v2)-grantsLeft, burst2)
	assert.LessOrEqual(t, burst2, othersRounds, "the agents without budgets are not slowed by the burster's")
	// Held, not failed: they stay so while the window has not rolled over.
	for i := 0; i < 20; i++ {
		w.Round()
	}
	var held []string
	for _, b := range v2 {
		if phase(b) != "Verified" {
			held = append(held, b)
		}
	}
	require.Len(t, held, len(v2)-grantsLeft)
	for _, b := range held {
		require.True(t, heldInProd(w, burster.ns, b), "%s is still held in prod 10 minutes on", b)
		assert.Equal(t, "Promoting", phase(b), "a held version is not failed")
		assert.Contains(t, watch.waitedOn[b], "versionsPerDay", "%s waits for the day's budget", b)
	}
	u := w.Ledger(burster.name)
	require.NotNil(t, u)
	ru := ruleUsage(u, burstRule.Name)
	require.NotNil(t, ru)
	assert.Contains(t, ru.Exhausted, "versionsPerDay")
	assert.EqualValues(t, len(held), ru.Waiting, "the ledger counts the held requests")

	// A newer version into a held one's lane supersedes it, and it leaves
	// the queue without a grant.
	superseded := held[len(held)-1]
	uid := string(w.Bundle(burster.ns, superseded).UID)
	v3 := ship(w.Bundle(burster.ns, superseded).Spec.Pipeline, 3)
	require.True(t, w.RunUntil(othersRounds, func() bool {
		return phase(superseded) == "Superseded" && heldInProd(w, burster.ns, v3)
	}), "%s supersedes %s, and is held in prod in its place\n%s", v3, superseded, w.Describe(burster.ns))
	u = w.Ledger(burster.name)
	for _, g := range u.Grants {
		assert.NotEqual(t, uid, g.BundleUID, "the superseded %s was never granted", superseded)
	}
	assert.EqualValues(t, len(held), ruleUsage(u, burstRule.Name).Waiting, "the superseded request left the queue; %s's took its place", v3)

	// The window rolls over: the held versions go on, one at a time.
	held = append(held[:len(held)-1], v3)
	frees := ruleUsage(u, burstRule.Name).Versions.FreesAt
	require.NotNil(t, frees, "the exhausted versionsPerDay says when it frees")
	w.Advance(frees.Sub(w.Now()) + time.Second)
	start = w.Rounds()
	require.True(t, w.RunUntil(30*len(held)+othersRounds, func() bool { return allPhase(phase, held, "Verified") }),
		"after the window rolls over every held version is Verified\n%s", w.Describe(burster.ns))
	t.Logf("window rolled over: %d held versions Verified in %d rounds", len(held), w.Rounds()-start)

	watch.report()
	assert.LessOrEqual(t, watch.maxUnsettled, int(burstRule.MaxConcurrent), "unsettled grants at any round")
	assert.LessOrEqual(t, watch.maxInWindow, int(burstRule.VersionsPerDay), "version grants in any 24 hours")
	assert.Equal(t, 1, watch.maxUnsettled, "the burster had a promotion under way")
	assert.Equal(t, 3, watch.maxInWindow, "the burster used its day")
	n, last2 := w.Errors()
	assert.Empty(t, n, "reconcile errors: %v", last2)
	assert.Empty(t, w.Kro.Errors())
	assert.Empty(t, w.SCM.Unknown())
}

// setBudgets gives agent rules, as an admin edits its AgentIdentity.
func setBudgets(t *testing.T, w *scenario.World, agent string, rules ...v1alpha1.AgentBudgetRule) {
	t.Helper()
	var ai v1alpha1.AgentIdentity
	require.NoError(t, w.Client.Get(w.Ctx, types.NamespacedName{Name: agent}, &ai))
	ai.Spec.Budgets = rules
	require.NoError(t, w.Client.Update(scenario.AsUser(w.Ctx, scenario.AdminUser, scenario.AdminGroups...), &ai))
}

// ledgerCurrent reports whether agent's ledger was evaluated for its spec.
func ledgerCurrent(w *scenario.World, agent string) bool {
	var ai v1alpha1.AgentIdentity
	if err := w.Client.Get(w.Ctx, types.NamespacedName{Name: agent}, &ai); err != nil {
		return false
	}
	u, ok := ai.Status.Usage[scenario.Release]
	return ok && u.ObservedGeneration == ai.Generation
}

func ruleUsage(u *v1alpha1.AgentUsage, name string) *v1alpha1.AgentBudgetRuleUsage {
	for i := range u.Rules {
		if u.Rules[i].Name == name {
			return &u.Rules[i]
		}
	}
	return nil
}

func allPhase(phase func(string) string, bundles []string, want string) bool {
	for _, b := range bundles {
		if phase(b) != want {
			return false
		}
	}
	return true
}

// heldInProd reports whether bundle's prod step waits for its budget:
// Pending, with status.budget Waiting and the budget message.
func heldInProd(w *scenario.World, ns, bundle string) bool {
	s, ok := w.Steps(ns, bundle)["prod"]
	return ok && (s.Status.State == psrec.StatePending || s.Status.State == "Pending") && s.Status.Budget != nil &&
		s.Status.Budget.State == psrec.BudgetWaiting && strings.Contains(s.Status.Message, "budget")
}

// budgetWatch checks, at the start of every round, the burster's ledger
// against its rule and its steps: no promotion of the burster fails, a
// step of no other agent is ever held, and it records which limits each
// held version waited on.
type budgetWatch struct {
	t     *testing.T
	agent string
	ns    string
	rule  v1alpha1.AgentBudgetRule

	maxUnsettled, maxInWindow int
	// waitedOn are the exhausted limits seen while a version's prod step
	// waited, by Bundle.
	waitedOn   map[string][]string
	violations []string
}

func (b *budgetWatch) check(w *scenario.World) error {
	var ai v1alpha1.AgentIdentity
	if err := w.Client.Get(w.Ctx, types.NamespacedName{Name: b.agent}, &ai); err != nil {
		return fmt.Errorf("get AgentIdentity %s: %w", b.agent, err)
	}
	now := w.Now()
	u, ok := ai.Status.Usage[scenario.Release]
	if !ok {
		return nil
	}
	unsettled, inWindow := 0, 0
	for _, g := range u.Grants {
		if !slices.Contains(g.Rules, b.rule.Name) {
			continue
		}
		if !g.Settled {
			unsettled++
		}
		if g.Version && g.GrantedAt.Add(24*time.Hour).After(now) {
			inWindow++
		}
	}
	b.maxUnsettled, b.maxInWindow = max(b.maxUnsettled, unsettled), max(b.maxInWindow, inWindow)
	if unsettled > int(b.rule.MaxConcurrent) || inWindow > int(b.rule.VersionsPerDay) {
		b.violations = append(b.violations, fmt.Sprintf("round %d: %d unsettled grants, %d in the window", w.Rounds(), unsettled, inWindow))
	}
	if c := meta.FindStatusCondition(u.Conditions, "LedgerFull"); c != nil && c.Status == "True" {
		b.violations = append(b.violations, fmt.Sprintf("round %d: ledger full: %s", w.Rounds(), c.Message))
	}
	var exhausted []string
	if ru := ruleUsage(&u, b.rule.Name); ru != nil {
		exhausted = ru.Exhausted
	}
	var steps v1alpha1.PromotionStepList
	if err := w.Client.List(w.Ctx, &steps); err != nil {
		return fmt.Errorf("list steps: %w", err)
	}
	for _, s := range steps.Items {
		mine := s.Namespace == b.ns && strings.HasPrefix(s.Spec.PipelineName, "burst-")
		switch {
		case !mine && s.Status.Budget != nil:
			b.violations = append(b.violations, fmt.Sprintf("round %d: %s/%s of an agent without budgets has status.budget %+v",
				w.Rounds(), s.Namespace, s.Name, *s.Status.Budget))
		case mine && s.Status.State == "Failed" && strings.Contains(s.Status.Message, "superseded before this step started"):
			// A newer version superseded it while it waited: it failed
			// before it started, as any unstarted step of a superseded
			// version does, and is no request any more.
			if lifecycle.WaitsForBudget(&s) {
				b.violations = append(b.violations, fmt.Sprintf("round %d: the superseded %s/%s still waits", w.Rounds(), s.Namespace, s.Name))
			}
		case mine && s.Status.State == "Failed":
			b.violations = append(b.violations, fmt.Sprintf("round %d: the burster's %s/%s failed: %s", w.Rounds(), s.Namespace, s.Name, s.Status.Message))
		case mine && lifecycle.WaitsForBudget(&s):
			for _, e := range exhausted {
				if !slices.Contains(b.waitedOn[s.Spec.BundleName], e) {
					b.waitedOn[s.Spec.BundleName] = append(b.waitedOn[s.Spec.BundleName], e)
				}
			}
		}
	}
	return nil
}

func (b *budgetWatch) report() {
	b.t.Helper()
	keys := make([]string, 0, len(b.waitedOn))
	for k := range b.waitedOn {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.t.Logf("held: %s waited on %v", k, b.waitedOn[k])
	}
	assert.Empty(b.t, b.violations, "the burster's budget")
}
