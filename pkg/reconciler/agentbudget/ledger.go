// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentbudget

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// The ledger's windows and bounds (docs/design/24-agent-budgets.md sections
// 6 and 14).
const (
	// VersionWindow is versionsPerDay's rolling window.
	VersionWindow = 24 * time.Hour
	// FleetTargetWindow is fleetTargetsPerHour's rolling window.
	FleetTargetWindow = time.Hour
	// IdleRecheck is the soonest the ledger is evaluated again for a count
	// to drop while nothing waits, and the resync while seeded grants are
	// unsettled (their steps carry no status.budget, so send no event).
	IdleRecheck = 5 * time.Minute
	// ExemptionCap is the longest an exemption counts after the controller
	// first sees it (admission has no clock).
	ExemptionCap = 24 * time.Hour
	// MaxGrants is the most grants one ledger holds (the schema's maxItems).
	MaxGrants = 2000
	// MaxAgentGrants is the most grants all of an agent's ledgers hold
	// between them (the schema's rule on status.usage).
	MaxAgentGrants = 3000
	// DefaultMaxObjectBytes is the AgentIdentity size, in JSON, past which
	// the ledger grants nothing more: below etcd's 1.5 MiB with room for the
	// spec.
	DefaultMaxObjectBytes = 1 << 20
)

// The conditions of a ledger (status.usage.<ledger>.conditions). They are
// never in the AgentIdentity's status.conditions, which the agent index
// reconciler owns (section 4.2, A6).
const (
	// ConditionCurrent is True once the ledger is evaluated for the spec's
	// generation.
	ConditionCurrent = "Current"
	// ConditionLedgerFull is True while a cap keeps the ledger from
	// granting: its 2,000 grants, the agent's 3,000, or the object's size.
	ConditionLedgerFull = "LedgerFull"
	// ConditionExemptionIgnored is True while an exemption made by an agent,
	// or one without an actor stamp, is listed: it never counts.
	ConditionExemptionIgnored = "ExemptionIgnored"
)

// LedgerFull reasons.
const (
	FullGrants      = "GrantCap"
	FullAgentGrants = "AgentGrantCap"
	FullObjectSize  = "ObjectSize"
)

// Limit names, as in spec.budgets and usage.rules[].exhausted.
const (
	limitVersions     = "versionsPerDay"
	limitConcurrent   = "maxConcurrent"
	limitFleetTargets = "fleetTargetsPerHour"
	limitUnavailable  = "maxUnavailable"
	limitFleetShare   = "fleetShare"
)

// grantKey identifies a grant: a Bundle into an environment (a fleet
// target's own name). A step deleted and recreated finds the same grant.
type grantKey struct{ uid, env string }

// request is a step that asks for a grant (status.budget.state Waiting), or
// a started step the ledger seeds.
type request struct {
	key       grantKey
	fleet     string // the fleet environment of a fleet target
	namespace string
	pipeline  string
	bundle    string
	name      string // the step's name
	since     time.Time
	created   time.Time
	// targets is the fleet's size; 0 when unknown (fleetShare fails closed).
	targets int
	// started is when a seeded step started, and settled whether it has.
	started time.Time
	settled bool
}

// grantState is what the cluster says about a grant's promotion now.
type grantState struct {
	// bundleFound: the Bundle exists, so its namespace is known.
	bundleFound bool
	namespace   string
	// stepFound: the step exists, in stepState.
	stepFound bool
	stepState string
	// current: the Pipeline's status.environments entry for the grant's
	// environment still names the Bundle.
	current bool
}

// facts are the inputs of one evaluation, read from the cluster.
type facts struct {
	requests []request
	// seeds are the agent's started steps, read when the ledger is new or
	// its spec changed (section 17.2).
	seeds  []request
	grants map[grantKey]grantState
	// otherGrants are the grants of the agent's other ledgers.
	otherGrants int
	// baseBytes is the AgentIdentity's JSON size without this ledger, and
	// maxBytes the size past which nothing more is granted.
	baseBytes int
	maxBytes  int
}

// outcome is one evaluation's result.
type outcome struct {
	// usage is the ledger to write; nil removes it (no budgets).
	usage *v1alpha1.AgentUsage
	// ignored are the exemptions first seen as ignored: one Warning event
	// each.
	ignored []ignoredExemption
	// full is the LedgerFull reason when the ledger is newly full.
	full string
	// granted are this evaluation's new grants.
	granted []v1alpha1.AgentBudgetGrant
	// requeue is when to evaluate again; 0 for no time-based requeue.
	requeue time.Duration
}

type ignoredExemption struct {
	name, reason, by string
}

// settledState: the promotion is no longer under way. A failed step settles
// here and may count in maxUnavailable.
func settledState(state string) bool {
	switch state {
	case "Verified", "Superseded", "Failed", "AbortedByAlarm", "RollingBack":
		return true
	}
	return false
}

// failedState: the states the Graph counts as failures (compact.go's
// failedFleets).
func failedState(state string) bool {
	return state == "Failed" || state == "AbortedByAlarm" || state == "RollingBack"
}

// startedState: the step is past Pending.
func startedState(state string) bool { return state != "" && state != "Pending" }

// matches reports whether rule r applies to a step in namespace ns, in
// environment env, of fleet (its fleet environment; "" for none).
func matches(r *v1alpha1.AgentBudgetRule, ns, env, fleet string) bool {
	nss := r.Namespaces
	if len(nss) == 0 {
		nss = []string{"*"}
	}
	if !actor.Matches(nss, ns) {
		return false
	}
	return actor.Matches(r.Environments, env) || (fleet != "" && actor.Matches(r.Environments, fleet))
}

// matching are the rules of rules that apply to a step, in spec order.
func matching(rules []v1alpha1.AgentBudgetRule, ns, env, fleet string) []string {
	var out []string
	for i := range rules {
		if matches(&rules[i], ns, env, fleet) {
			out = append(out, rules[i].Name)
		}
	}
	return out
}

// versionEnv is the environment a version counts in: a fleet target's fleet.
func versionEnv(env, fleet string) string {
	if fleet != "" {
		return fleet
	}
	return env
}

// counter is one rule's counts while granting.
type counter struct {
	rule      *v1alpha1.AgentBudgetRule
	versions  int
	running   int
	targets   int
	failed    int
	waiting   int
	blocked   bool
	exhausted map[string]bool
}

// ledger is one evaluation in progress.
type ledger struct {
	now    time.Time
	rules  []v1alpha1.AgentBudgetRule
	byName map[string]*counter
	grants []v1alpha1.AgentBudgetGrant
	// versioned: (Bundle UID, version environment) pairs with a grant.
	versioned map[grantKey]bool
	// inFleet: unsettled grants per (Bundle UID, fleet), for fleetShare.
	inFleet map[grantKey]int
	// keys: the (Bundle UID, environment) of every grant.
	keys map[grantKey]bool
}

func newLedger(now time.Time, rules []v1alpha1.AgentBudgetRule) *ledger {
	l := &ledger{now: now, rules: rules, byName: map[string]*counter{},
		versioned: map[grantKey]bool{}, inFleet: map[grantKey]int{}, keys: map[grantKey]bool{}}
	for i := range rules {
		l.byName[rules[i].Name] = &counter{rule: &rules[i], exhausted: map[string]bool{}}
	}
	return l
}

// add records g in the ledger and its rules' counts.
func (l *ledger) add(g v1alpha1.AgentBudgetGrant) {
	l.grants = append(l.grants, g)
	l.keys[grantKey{g.BundleUID, g.Environment}] = true
	l.versioned[grantKey{g.BundleUID, versionEnv(g.Environment, g.Fleet)}] = true
	if g.Fleet != "" && !g.Settled {
		l.inFleet[grantKey{g.BundleUID, g.Fleet}]++
	}
	for _, name := range g.Rules {
		c := l.byName[name]
		if c == nil {
			continue
		}
		if g.Version && l.now.Sub(g.GrantedAt.Time) < VersionWindow {
			c.versions++
		}
		if !g.Settled {
			c.running++
		}
		if g.Fleet != "" && l.now.Sub(g.GrantedAt.Time) < FleetTargetWindow {
			c.targets++
		}
		if g.Unavailable {
			c.failed++
		}
	}
}

// room reports whether rule c can take req, and the limit that cannot.
func (l *ledger) room(c *counter, req request, version bool) (bool, string) {
	r := c.rule
	if r.VersionsPerDay > 0 && version && c.versions+1 > int(r.VersionsPerDay) {
		return false, limitVersions
	}
	if r.MaxConcurrent > 0 && c.running+1 > int(r.MaxConcurrent) {
		return false, limitConcurrent
	}
	if r.FleetTargetsPerHour > 0 && req.fleet != "" && c.targets+1 > int(r.FleetTargetsPerHour) {
		return false, limitFleetTargets
	}
	if r.MaxUnavailable > 0 && c.failed >= int(r.MaxUnavailable) {
		return false, limitUnavailable
	}
	if r.FleetShare != "" && req.fleet != "" {
		share := intstr.FromString(r.FleetShare)
		most, err := graph.MaxUnavailableCount(&share, req.targets)
		if err != nil || req.targets == 0 || l.inFleet[grantKey{req.key.uid, req.fleet}]+1 > max(most, 1) {
			return false, limitFleetShare
		}
	}
	return true, ""
}

// window is how long a settled grant must stay: the longest window it
// counts in.
func (l *ledger) window(g *v1alpha1.AgentBudgetGrant) time.Duration {
	var w time.Duration
	for _, name := range g.Rules {
		c := l.byName[name]
		if c == nil {
			continue
		}
		if g.Version && c.rule.VersionsPerDay > 0 {
			w = max(w, VersionWindow)
		}
		if g.Fleet != "" && c.rule.FleetTargetsPerHour > 0 {
			w = max(w, FleetTargetWindow)
		}
	}
	return w
}

// evaluate works out agent id's ledger from its last one (old, nil for none)
// and the cluster (f), at now (sections 6.2 and 7.3). It reads no clock and
// no object: it is the reconciler's decision, tested on its own.
func evaluate(id *v1alpha1.AgentIdentity, old *v1alpha1.AgentUsage, f facts, now time.Time) outcome {
	now = now.UTC().Truncate(time.Second)
	if len(id.Spec.Budgets) == 0 {
		return outcome{}
	}
	if old == nil {
		old = &v1alpha1.AgentUsage{}
	}
	rules := id.Spec.Budgets
	names := map[string]bool{}
	for i := range rules {
		names[rules[i].Name] = true
	}
	l := newLedger(now, rules)
	respec := old.ObservedGeneration != id.Generation

	// 1. The grants, settled from live state; their rules re-derived after
	// a spec change (a Bundle that is gone keeps the rules still defined).
	for _, g := range old.Grants {
		st := f.grants[grantKey{g.BundleUID, g.Environment}]
		if respec {
			if st.bundleFound {
				g.Rules = matching(rules, st.namespace, g.Environment, g.Fleet)
			} else {
				kept := g.Rules[:0:0]
				for _, n := range g.Rules {
					if names[n] {
						kept = append(kept, n)
					}
				}
				g.Rules = kept
			}
		}
		g.Settled = !st.stepFound || settledState(st.stepState)
		g.Unavailable = st.current && ((st.stepFound && failedState(st.stepState)) || (g.Unavailable && !st.stepFound))
		if g.Settled && !g.Unavailable && now.Sub(g.GrantedAt.Time) >= l.window(&g) {
			continue // no window or state needs it
		}
		l.add(g)
	}
	// 2. Seeding: started steps that hold no grant (section 17.2).
	for _, s := range f.seeds {
		if l.has(s.key) {
			continue
		}
		rs := matching(rules, s.namespace, s.key.env, s.fleet)
		if len(rs) == 0 {
			continue
		}
		g := v1alpha1.AgentBudgetGrant{BundleUID: s.key.uid, Environment: s.key.env, Fleet: s.fleet, Rules: rs,
			GrantedAt: metav1.NewTime(s.started.UTC().Truncate(time.Second)), Settled: s.settled, Seeded: true,
			Version: !l.versioned[grantKey{s.key.uid, versionEnv(s.key.env, s.fleet)}]}
		if g.Settled && now.Sub(g.GrantedAt.Time) >= l.window(&g) {
			continue
		}
		l.add(g)
	}

	// 3. The exemptions as this writer sees them.
	exemptions, inEffect, ignored := exemptionStates(id, old, now)

	// 4. The requests, in order: since, then creation, then namespace and
	// name. Strict per rule: one that does not fit blocks the later ones of
	// its rules.
	reqs := make([]request, 0, len(f.requests))
	for _, q := range f.requests {
		if !l.has(q.key) {
			reqs = append(reqs, q)
		}
	}
	sort.SliceStable(reqs, func(i, j int) bool {
		a, b := reqs[i], reqs[j]
		if !a.since.Equal(b.since) {
			return a.since.Before(b.since)
		}
		if !a.created.Equal(b.created) {
			return a.created.Before(b.created)
		}
		if a.namespace != b.namespace {
			return a.namespace < b.namespace
		}
		return a.name < b.name
	})
	var out outcome
	full := ""
	var fresh []int // indexes in l.grants of this evaluation's grants
	for _, q := range reqs {
		rs := matching(rules, q.namespace, q.key.env, q.fleet)
		if len(rs) == 0 {
			continue // no rule holds it: the step passes on its own
		}
		wait := func() {
			for _, n := range rs {
				l.byName[n].waiting++
			}
		}
		if full == "" {
			switch {
			case len(l.grants) >= MaxGrants:
				full = FullGrants
			case f.otherGrants+len(l.grants) >= MaxAgentGrants:
				full = FullAgentGrants
			}
		}
		if full != "" {
			wait()
			continue
		}
		version := !l.versioned[grantKey{q.key.uid, versionEnv(q.key.env, q.fleet)}]
		g := v1alpha1.AgentBudgetGrant{BundleUID: q.key.uid, Environment: q.key.env, Fleet: q.fleet, Rules: rs,
			GrantedAt: metav1.NewTime(now), Version: version}
		if e := exemptionFor(id, inEffect, q); e != "" {
			g.Exemption = e // needs no room, but is counted
			fresh = append(fresh, len(l.grants))
			l.add(g)
			continue
		}
		fits := true
		for _, n := range rs {
			c := l.byName[n]
			if c.blocked {
				fits = false
				continue
			}
			if ok, limit := l.room(c, q, version); !ok {
				fits = false
				c.exhausted[limit] = true
			}
		}
		if !fits {
			for _, n := range rs {
				l.byName[n].blocked = true
			}
			wait()
			continue
		}
		fresh = append(fresh, len(l.grants))
		l.add(g)
	}

	usage := l.usage(exemptions)
	// 5. The object's size: drop this evaluation's grants, newest first,
	// until the AgentIdentity fits (section 14).
	if f.maxBytes > 0 {
		for len(fresh) > 0 && f.baseBytes+entryBytes(usage) > f.maxBytes {
			last := fresh[len(fresh)-1]
			fresh = fresh[:len(fresh)-1]
			dropped := l.grants[last]
			l = l.without(last)
			for _, n := range dropped.Rules {
				l.byName[n].waiting++
			}
			full = FullObjectSize
			usage = l.usage(exemptions)
		}
		if f.baseBytes+entryBytes(usage) > f.maxBytes {
			full = FullObjectSize
		}
	}
	for _, i := range fresh {
		out.granted = append(out.granted, l.grants[i])
	}

	// 6. Conditions, then when to evaluate again.
	usage.ObservedGeneration = id.Generation
	usage.Conditions = append([]metav1.Condition(nil), old.Conditions...)
	meta.SetStatusCondition(&usage.Conditions, metav1.Condition{Type: ConditionCurrent, Status: metav1.ConditionTrue,
		Reason: "Evaluated", Message: fmt.Sprintf("evaluated for generation %d", id.Generation),
		ObservedGeneration: id.Generation, LastTransitionTime: metav1.NewTime(now)})
	wasFull := meta.IsStatusConditionTrue(old.Conditions, ConditionLedgerFull)
	if full != "" {
		meta.SetStatusCondition(&usage.Conditions, metav1.Condition{Type: ConditionLedgerFull, Status: metav1.ConditionTrue,
			Reason: full, Message: fullMessage(full), ObservedGeneration: id.Generation, LastTransitionTime: metav1.NewTime(now)})
		if !wasFull {
			out.full = full
		}
	} else {
		meta.RemoveStatusCondition(&usage.Conditions, ConditionLedgerFull)
	}
	setIgnoredCondition(&usage.Conditions, id, exemptions, now)
	out.ignored = ignored
	next, when := nextEvaluation(usage, l, now)
	usage.EvaluatedAt = ptrTime(now)
	usage.NextEvaluationAt = next
	out.requeue = when
	out.usage = usage
	return out
}

func (l *ledger) has(k grantKey) bool { return l.keys[k] }

// without is l without its grant i, recounted.
func (l *ledger) without(i int) *ledger {
	n := newLedger(l.now, l.rules)
	for j := range l.grants {
		if j != i {
			n.add(l.grants[j])
		}
	}
	for name, c := range l.byName {
		n.byName[name].waiting, n.byName[name].blocked, n.byName[name].exhausted = c.waiting, c.blocked, c.exhausted
	}
	return n
}

// usage is the ledger's status entry, without conditions and times.
func (l *ledger) usage(exemptions []v1alpha1.AgentBudgetExemptionState) *v1alpha1.AgentUsage {
	u := &v1alpha1.AgentUsage{Grants: append([]v1alpha1.AgentBudgetGrant(nil), l.grants...), Exemptions: exemptions}
	for i := range l.rules {
		r := &l.rules[i]
		c := l.byName[r.Name]
		ru := v1alpha1.AgentBudgetRuleUsage{Name: r.Name, Environments: r.Environments, Namespaces: r.Namespaces,
			FleetShare: r.FleetShare, Waiting: int32(c.waiting)}
		if len(ru.Namespaces) == 0 {
			ru.Namespaces = []string{"*"}
		}
		if r.VersionsPerDay > 0 {
			ru.Versions = l.rate(r.Name, int(r.VersionsPerDay), c.versions, VersionWindow, func(g *v1alpha1.AgentBudgetGrant) bool { return g.Version })
			if c.versions >= int(r.VersionsPerDay) {
				c.exhausted[limitVersions] = true
			}
		}
		if r.MaxConcurrent > 0 {
			ru.Concurrent = &v1alpha1.AgentBudgetCount{Limit: r.MaxConcurrent, Used: int32(c.running)}
			if c.running >= int(r.MaxConcurrent) {
				c.exhausted[limitConcurrent] = true
			}
		}
		if r.FleetTargetsPerHour > 0 {
			ru.FleetTargets = l.rate(r.Name, int(r.FleetTargetsPerHour), c.targets, FleetTargetWindow, func(g *v1alpha1.AgentBudgetGrant) bool { return g.Fleet != "" })
			if c.targets >= int(r.FleetTargetsPerHour) {
				c.exhausted[limitFleetTargets] = true
			}
		}
		if r.MaxUnavailable > 0 {
			ru.Unavailable = &v1alpha1.AgentBudgetCount{Limit: r.MaxUnavailable, Used: int32(c.failed)}
			if c.failed >= int(r.MaxUnavailable) {
				c.exhausted[limitUnavailable] = true
			}
		}
		for _, name := range []string{limitVersions, limitConcurrent, limitFleetTargets, limitUnavailable, limitFleetShare} {
			if c.exhausted[name] {
				ru.Exhausted = append(ru.Exhausted, name)
			}
		}
		u.Rules = append(u.Rules, ru)
	}
	return u
}

// rate is a rolling count: the window's start, and when the oldest counted
// grant leaves it.
func (l *ledger) rate(rule string, limit, used int, window time.Duration, counts func(*v1alpha1.AgentBudgetGrant) bool) *v1alpha1.AgentBudgetCount {
	c := &v1alpha1.AgentBudgetCount{Limit: int32(limit), Used: int32(used), WindowStart: ptrTime(l.now.Add(-window))}
	var oldest *time.Time
	for i := range l.grants {
		g := &l.grants[i]
		if !counts(g) || !contains(g.Rules, rule) || l.now.Sub(g.GrantedAt.Time) >= window {
			continue
		}
		if t := g.GrantedAt.Time; oldest == nil || t.Before(*oldest) {
			oldest = &t
		}
	}
	if oldest != nil {
		c.FreesAt = ptrTime(oldest.Add(window))
	}
	return c
}

// nextEvaluation is when a count next drops for a rule that holds a request,
// or with nothing waiting the earliest drop but not before IdleRecheck; and
// IdleRecheck while seeded grants are unsettled. nil and 0: no time-based
// evaluation.
func nextEvaluation(u *v1alpha1.AgentUsage, l *ledger, now time.Time) (*metav1.Time, time.Duration) {
	var waiting, idle *time.Time
	for i := range u.Rules {
		ru := &u.Rules[i]
		for _, c := range []*v1alpha1.AgentBudgetCount{ru.Versions, ru.FleetTargets} {
			if c == nil || c.FreesAt == nil {
				continue
			}
			t := c.FreesAt.Time
			if ru.Waiting > 0 && (waiting == nil || t.Before(*waiting)) {
				waiting = &t
			}
			if idle == nil || t.Before(*idle) {
				idle = &t
			}
		}
	}
	next := waiting
	if next == nil && idle != nil {
		t := *idle
		if floor := now.Add(IdleRecheck); t.Before(floor) {
			t = floor
		}
		next = &t
	}
	for i := range l.grants {
		if l.grants[i].Seeded && !l.grants[i].Settled {
			if t := now.Add(IdleRecheck); next == nil || t.Before(*next) {
				next = &t
			}
			break
		}
	}
	if next == nil {
		return nil, 0
	}
	return ptrTime(*next), max(next.Sub(now), time.Second)
}

// exemptionStates are the spec's exemptions as this writer sees them, the
// names of those in effect, and the ones first seen as ignored.
func exemptionStates(id *v1alpha1.AgentIdentity, old *v1alpha1.AgentUsage, now time.Time) ([]v1alpha1.AgentBudgetExemptionState, map[string]bool, []ignoredExemption) {
	seen := map[string]v1alpha1.AgentBudgetExemptionState{}
	for _, s := range old.Exemptions {
		seen[s.Name] = s
	}
	var out []v1alpha1.AgentBudgetExemptionState
	inEffect := map[string]bool{}
	var ignored []ignoredExemption
	for i := range id.Spec.BudgetExemptions {
		e := &id.Spec.BudgetExemptions[i]
		st, ok := seen[e.Name]
		if !ok {
			st = v1alpha1.AgentBudgetExemptionState{Name: e.Name, FirstSeenAt: metav1.NewTime(now)}
		}
		reason, by := "", ""
		switch {
		case e.Actor == nil:
			reason = v1alpha1.ExemptionUnverified
		case e.Actor.Kind == v1alpha1.ActorAgent:
			reason, by = v1alpha1.ExemptionMadeByAgent, e.Actor.Agent
		}
		if reason != "" {
			if st.Ignored == "" || !ok {
				ignored = append(ignored, ignoredExemption{name: e.Name, reason: reason, by: by})
			}
			st.Ignored, st.ExpiresAt = reason, st.FirstSeenAt
			out = append(out, st)
			continue
		}
		until := e.ExpiresAt.Time
		if capEnd := st.FirstSeenAt.Add(ExemptionCap); capEnd.Before(until) {
			until = capEnd
		}
		st.Ignored, st.ExpiresAt = "", metav1.NewTime(until.UTC().Truncate(time.Second))
		if now.Before(until) {
			inEffect[e.Name] = true
		}
		out = append(out, st)
	}
	return out, inEffect, ignored
}

// exemptionFor is the first exemption in effect that matches q: its
// namespace, Pipeline and environment (or fleet), and its version when it
// names one.
func exemptionFor(id *v1alpha1.AgentIdentity, inEffect map[string]bool, q request) string {
	for i := range id.Spec.BudgetExemptions {
		e := &id.Spec.BudgetExemptions[i]
		if !inEffect[e.Name] || e.Namespace != q.namespace || e.Pipeline != q.pipeline {
			continue
		}
		if e.Environment != q.key.env && (q.fleet == "" || e.Environment != q.fleet) {
			continue
		}
		if e.Bundle != "" && e.Bundle != q.bundle {
			continue
		}
		return e.Name
	}
	return ""
}

// setIgnoredCondition sets ExemptionIgnored while an exemption is ignored,
// naming each, and removes it when none is.
func setIgnoredCondition(conds *[]metav1.Condition, id *v1alpha1.AgentIdentity, ex []v1alpha1.AgentBudgetExemptionState, now time.Time) {
	var parts []string
	reason := ""
	for _, s := range ex {
		switch s.Ignored {
		case v1alpha1.ExemptionMadeByAgent:
			parts = append(parts, s.Name+" (made by "+exemptionAgent(id, s.Name)+")")
			reason = v1alpha1.ExemptionMadeByAgent
		case v1alpha1.ExemptionUnverified:
			parts = append(parts, s.Name+" (no actor stamp)")
			if reason == "" {
				reason = v1alpha1.ExemptionUnverified
			}
		}
	}
	if len(parts) == 0 {
		meta.RemoveStatusCondition(conds, ConditionExemptionIgnored)
		return
	}
	meta.SetStatusCondition(conds, metav1.Condition{Type: ConditionExemptionIgnored, Status: metav1.ConditionTrue,
		Reason: reason, ObservedGeneration: id.Generation, LastTransitionTime: metav1.NewTime(now),
		Message: "these exemptions do not count, break-glass is for people: " + strings.Join(parts, ", ")})
}

// exemptionAgent names the agent that made exemption name.
func exemptionAgent(id *v1alpha1.AgentIdentity, name string) string {
	for i := range id.Spec.BudgetExemptions {
		if e := &id.Spec.BudgetExemptions[i]; e.Name == name && e.Actor != nil && e.Actor.Agent != "" {
			return "agent " + e.Actor.Agent
		}
	}
	return "an agent"
}

func fullMessage(reason string) string {
	switch reason {
	case FullGrants:
		return fmt.Sprintf("the ledger holds %d grants, its most: new requests wait until grants leave their windows", MaxGrants)
	case FullAgentGrants:
		return fmt.Sprintf("the agent's ledgers hold %d grants between them, their most: new requests wait", MaxAgentGrants)
	}
	return "the AgentIdentity would pass its size bound with more grants: new requests wait"
}

// entryBytes is the JSON size the ledger adds to its AgentIdentity.
func entryBytes(u *v1alpha1.AgentUsage) int {
	b, err := json.Marshal(u)
	if err != nil {
		return 0
	}
	return len(b) + 70 // the key, quotes and a comma
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func ptrTime(t time.Time) *metav1.Time {
	m := metav1.NewTime(t)
	return &m
}
