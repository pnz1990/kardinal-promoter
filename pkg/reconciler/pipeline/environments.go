// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// The environment projection (status.environments, status.summary and
// status.environmentSummary) is for display: kubectl get, the CLI and the UI
// read it. Graph-first: it is computed from the Bundle, PromotionStep and
// PolicyGate status their own reconcilers write, it is written only to this
// Pipeline's status, and no controller decision reads it.

// EnvironmentNames are the environments of p the projection reports, in spec
// order: a fleet environment runs nothing itself, so it expands to its
// targets, as the UI fleet board does.
func EnvironmentNames(p *kardinalv1alpha1.Pipeline) []string {
	fleets, _ := graph.FleetTargetEnvironments(p)
	names := make([]string, 0, len(p.Spec.Environments))
	for _, env := range p.Spec.Environments {
		if env.Fleet != nil {
			names = append(names, fleets[env.Name]...)
		} else {
			names = append(names, env.Name)
		}
	}
	return names
}

// ProjectEnvironments returns status.environments of p. bundles and steps are
// p's (other pipelines' are skipped); gates are its gate instances. prev is
// the current status.environments: an entry whose Bundle and State did not
// change keeps its Since, so a second call with the same input returns the
// same value. now is the Since of a new state; a zero now (a reader without
// the controller's clock, such as the CLI) leaves it unknown (nil).
func ProjectEnvironments(p *kardinalv1alpha1.Pipeline, bundles []kardinalv1alpha1.Bundle,
	steps []kardinalv1alpha1.PromotionStep, gates []kardinalv1alpha1.PolicyGate,
	prev []kardinalv1alpha1.PipelineEnvironmentStatus, now time.Time) []kardinalv1alpha1.PipelineEnvironmentStatus {
	all := make(map[string]*kardinalv1alpha1.Bundle, len(bundles))
	for i := range bundles {
		if bundles[i].Spec.Pipeline == p.Name {
			all[bundles[i].Name] = &bundles[i]
		}
	}
	byEnv := map[string][]*kardinalv1alpha1.PromotionStep{}
	var mine []*kardinalv1alpha1.PromotionStep
	for i := range steps {
		s := &steps[i]
		if s.Spec.PipelineName != p.Name {
			continue
		}
		byEnv[s.Spec.Environment] = append(byEnv[s.Spec.Environment], s)
		mine = append(mine, s)
	}
	// Gate instances that hold an in-flight Bundle back, by environment.
	held := map[string][]heldBy{}
	for i := range gates {
		g := &gates[i]
		b := all[g.Labels["kardinal.io/bundle"]]
		if b == nil || lifecycle.Halted(b) || !inFlightBundlePhases[b.Status.Phase] {
			continue
		}
		// All of the Pipeline's steps, not only the gate environment's:
		// before the environment has a step, a gate holds the Bundle only
		// once every upstream environment is Verified (graph.GateHolds).
		if graph.GateHoldsSteps(p, b, g, mine) {
			env := g.Labels["kardinal.io/environment"]
			held[env] = append(held[env], heldBy{bundle: b, gate: gateName(g)})
		}
	}
	old := make(map[string]*kardinalv1alpha1.PipelineEnvironmentStatus, len(prev))
	for i := range prev {
		old[prev[i].Name] = &prev[i]
	}
	versions := map[string]string{}
	version := func(name string) string {
		v, ok := versions[name]
		if !ok {
			if b := all[name]; b != nil {
				v = scm.BundleVersion(b.Spec)
			}
			versions[name] = v
		}
		return v
	}

	names := EnvironmentNames(p)
	out := make([]kardinalv1alpha1.PipelineEnvironmentStatus, 0, len(names))
	for _, name := range names {
		envSteps := byEnv[name]
		e := kardinalv1alpha1.PipelineEnvironmentStatus{Name: name}
		e.Bundle = lifecycle.DeployedInSteps(envSteps, p.Name, name, all).Bundle
		e.Version = version(e.Bundle)

		cur := currentStep(envSteps, all, e.Bundle)
		curBundle := ""
		if cur != nil {
			curBundle = cur.Spec.BundleName
			e.State = cur.Status.State
			if e.State == "" {
				e.State = "Pending"
			}
		}
		// Gates holding a newer Bundle than the step's (or the step's own,
		// while it is Pending) are what the environment waits for.
		if hb := held[name]; len(hb) > 0 {
			newest := hb[0].bundle
			for _, h := range hb[1:] {
				if lifecycle.CompareCreation(h.bundle, newest) > 0 {
					newest = h.bundle
				}
			}
			if c := all[curBundle]; c == nil || newest.Name == curBundle || lifecycle.CompareCreation(newest, c) > 0 {
				for _, h := range hb {
					if h.bundle.Name == newest.Name && !contains(e.BlockedBy, h.gate) {
						e.BlockedBy = append(e.BlockedBy, h.gate)
					}
				}
				sort.Strings(e.BlockedBy)
				if newest.Name != curBundle {
					curBundle, e.State = newest.Name, "Pending"
					cur = nil
				}
			}
		}
		if curBundle != "" && (e.State == "Pending") && lifecycle.HeldFrom(p, name, curBundle) != nil {
			e.Held = true
		}
		// A freeze holds the step before it starts or between its git steps
		// (status.frozenBy, #1974).
		if cur != nil && cur.Status.FrozenBy != nil && (e.State == "Pending" || e.State == "Promoting") {
			e.FrozenBy = cur.Status.FrozenBy.Name
		}
		// An agent's budget holds the step before it starts (status.budget
		// Waiting, docs/design/24-agent-budgets.md section 7.2).
		if cur != nil && cur.Status.Budget != nil && cur.Status.Budget.State == "Waiting" && e.State == "Pending" {
			e.BudgetHeldBy = cur.Status.Budget.Agent
		}
		if curBundle != "" && curBundle != e.Bundle {
			e.IncomingBundle = curBundle
			e.IncomingVersion = version(curBundle)
		}
		if cur != nil && e.State == "WaitingForMerge" {
			e.PRURL = cur.Status.PRURL
		}
		switch {
		case e.State == "":
		case e.State == "Verified" && cur != nil && verifiedSince(envSteps, curBundle) != nil:
			e.Since = verifiedSince(envSteps, curBundle)
		case old[name] != nil && old[name].Since != nil && old[name].State == e.State &&
			stateBundle(old[name]) == curBundle:
			e.Since = old[name].Since.DeepCopy()
		case now.IsZero():
		default:
			t := metav1.NewTime(now.UTC().Truncate(time.Second))
			e.Since = &t
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type heldBy struct {
	bundle *kardinalv1alpha1.Bundle
	gate   string
}

// gateName is the name a user gave the gate: the gate-name label of an
// instance, as kardinal explain and status print it.
func gateName(g *kardinalv1alpha1.PolicyGate) string {
	if n := g.Labels["kardinal.io/gate-name"]; n != "" {
		return n
	}
	if n := g.Labels["kardinal.io/gate-template"]; n != "" {
		return n
	}
	return g.Name
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// stateBundle is the Bundle an entry's State is about.
func stateBundle(e *kardinalv1alpha1.PipelineEnvironmentStatus) string {
	if e.IncomingBundle != "" {
		return e.IncomingBundle
	}
	return e.Bundle
}

// currentStep is the newest step in the environment of the newest Bundle
// there, by the rule DerivePhase uses: Superseded and Rejected Bundles are
// skipped, except a Rejected Bundle whose change is live there. When none
// counts, it is the newest step of the deployed Bundle, which still runs
// there even when a newer Bundle superseded it before reaching it.
func currentStep(steps []*kardinalv1alpha1.PromotionStep, all map[string]*kardinalv1alpha1.Bundle,
	deployed string) *kardinalv1alpha1.PromotionStep {
	var best *kardinalv1alpha1.PromotionStep
	var bestBundle *kardinalv1alpha1.Bundle
	for _, s := range steps {
		b := all[s.Spec.BundleName]
		if b == nil {
			continue
		}
		if lifecycle.Halted(b) && (!lifecycle.Rejected(b) || !lifecycle.RejectedLiveStep(b, s)) {
			continue
		}
		if best == nil || lifecycle.CompareCreation(b, bestBundle) > 0 ||
			(b.Name == bestBundle.Name && newerStep(s, best)) {
			best, bestBundle = s, b
		}
	}
	if best != nil {
		return best
	}
	for _, s := range steps {
		if s.Spec.BundleName == deployed && (best == nil || newerStep(s, best)) {
			best = s
		}
	}
	return best
}

func newerStep(a, b *kardinalv1alpha1.PromotionStep) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.After(b.CreationTimestamp.Time)
	}
	return a.Name > b.Name
}

// verifiedSince is the latest Verified time of bundle's steps in the
// environment (the UI's verifiedAt), nil when none records one.
func verifiedSince(steps []*kardinalv1alpha1.PromotionStep, bundle string) *metav1.Time {
	var at time.Time
	for _, s := range steps {
		if s.Spec.BundleName != bundle {
			continue
		}
		if t, ok := lifecycle.VerifiedTime(s); ok && t.After(at) {
			at = t
		}
	}
	if at.IsZero() {
		return nil
	}
	t := metav1.NewTime(at.UTC())
	return &t
}

// summaryMaxNamed is how many environments (and PRs) one clause of
// status.summary names before "and N more".
const summaryMaxNamed = 6

// Summarize returns status.summary: one sentence on where p's versions are,
// for example "1.5.0 live in test, uat; waiting for PR merge in prod (#42)".
// An invalid spec (Ready=False) leads the sentence; a paused Pipeline, or
// a paused fleet, says so.
//
// A fleet's targets are counted under the fleet's name, and so are the
// environments of a wave with more environments than a clause names
// ("7 of 9 in wave 2"): "7.2.0 live in canary and 27 of 42 pops; promoting
// in 5 pops".
// The first clause that counts a group gives its size. Other environments
// are named as they are, at most summaryMaxNamed per clause, then "and N
// more"; so are a clause's PRs.
func Summarize(p *kardinalv1alpha1.Pipeline, ready metav1.Condition, envs []kardinalv1alpha1.PipelineEnvironmentStatus) string {
	type clause struct {
		version, what string
		// places are the clause's environment names and groups, in the
		// order they first appear; count is how many environments each
		// group has in the clause.
		places []string
		count  map[string]int
		prs    []string
	}
	groupOf, size := summaryGroups(p)
	var clauses []*clause
	for i := range envs {
		e := &envs[i]
		if e.State == "" {
			continue
		}
		v := e.Version
		if e.IncomingBundle != "" {
			v = e.IncomingVersion
		}
		st := display.Step(e.State)
		switch {
		case e.FrozenBy != "":
			st = display.Frozen(e.State, e.FrozenBy)
		case e.Held:
			st = display.Held(e.State)
		case e.BudgetHeldBy != "":
			st = display.HeldByBudget(e.State, e.BudgetHeldBy)
		case len(e.BlockedBy) > 0:
			st = display.HeldByGate(e.State, strings.Join(e.BlockedBy, ", "))
		}
		what := lowerFirst(st.Label)
		var c *clause
		for _, x := range clauses {
			if x.version == v && x.what == what {
				c = x
			}
		}
		if c == nil {
			c = &clause{version: v, what: what, count: map[string]int{}}
			clauses = append(clauses, c)
		}
		if g, ok := groupOf[e.Name]; ok {
			if c.count[g] == 0 {
				c.places = append(c.places, g)
			}
			c.count[g]++
		} else {
			c.places = append(c.places, e.Name)
		}
		if e.PRURL != "" {
			c.prs = append(c.prs, shortPR(e.PRURL))
		}
	}
	parts := make([]string, 0, len(clauses))
	last := ""
	// A version that starts the sentence keeps its case ("sha-9349a3f live
	// in test", "main live in test"): a tag is a name, not a word.
	versionFirst := len(clauses) > 0 && clauses[0].version != ""
	sized := map[string]bool{}
	for _, c := range clauses {
		var items []string
		named, more, at, grouped := 0, 0, 0, false
		for _, place := range c.places {
			n, isGroup := c.count[place]
			switch {
			case !isGroup && named == summaryMaxNamed:
				more++
				continue
			case !isGroup:
				named++
				items = append(items, place)
				at = len(items)
				continue
			}
			grouped = true
			// A wave reads "7 of 9 in wave 2", a fleet "27 of 42 pops".
			in := " "
			if strings.HasPrefix(place, "wave ") {
				in = " in "
			}
			switch {
			case sized[place]:
				items = append(items, fmt.Sprintf("%d%s%s", n, in, place))
			case n == size[place]:
				items = append(items, fmt.Sprintf("all %d%s%s", n, in, place))
			default:
				items = append(items, fmt.Sprintf("%d of %d%s%s", n, size[place], in, place))
			}
			sized[place] = true
		}
		if more > 0 {
			// "N more" follows the named environments it stands for, before
			// any group counted after them.
			items = slices.Insert(items, at, fmt.Sprintf("%d more", more))
		}
		s := c.what + " in " + joinItems(items, more > 0 || grouped)
		if c.version != "" && c.version != last {
			s = c.version + " " + s
		}
		last = c.version
		if len(c.prs) > 0 {
			prs, more := capped(c.prs)
			if more > 0 {
				prs = append(prs, fmt.Sprintf("%d more", more))
			}
			s += " (" + strings.Join(prs, ", ") + ")"
		}
		parts = append(parts, s)
	}
	sentence := strings.Join(parts, "; ")
	if sentence == "" {
		sentence = lowerFirst(display.Pipeline("Unknown", false).Label)
	}
	if p.Spec.Paused {
		sentence = "paused; " + sentence
		versionFirst = false
	} else if len(p.Spec.PausedFleets) > 0 {
		// A paused fleet (kardinal pause --env <fleet>) starts no target.
		noun := "fleet "
		if len(p.Spec.PausedFleets) > 1 {
			noun = "fleets "
		}
		sentence = noun + strings.Join(p.Spec.PausedFleets, ", ") + " paused; " + sentence
		versionFirst = false
	}
	if ready.Type != "" && ready.Status != metav1.ConditionTrue && ready.Reason != "" {
		sentence = fmt.Sprintf("not ready (%s); %s", ready.Reason, sentence)
		versionFirst = false
	}
	if versionFirst {
		return sentence
	}
	// A version reads as it is written ("sha-9349a3f", "main"): only a sentence
	// that starts with words is capitalised.
	if len(clauses) > 0 && clauses[0].version != "" && strings.HasPrefix(sentence, clauses[0].version+" ") {
		return sentence
	}
	return upperFirst(sentence)
}

// summaryGroups returns the group status.summary counts each grouped
// environment under, and each group's size: a fleet target its fleet
// environment, and an environment of a wave with more than summaryMaxNamed
// environments (fleet targets aside) "wave <n>".
func summaryGroups(p *kardinalv1alpha1.Pipeline) (groupOf map[string]string, size map[string]int) {
	groupOf, size = map[string]string{}, map[string]int{}
	fleets, _ := graph.FleetTargetEnvironments(p)
	waves := map[int][]string{}
	for _, env := range p.Spec.Environments {
		if env.Fleet != nil {
			for _, t := range fleets[env.Name] {
				groupOf[t] = env.Name
			}
			size[env.Name] = len(fleets[env.Name])
			continue
		}
		if env.Wave > 0 {
			waves[env.Wave] = append(waves[env.Wave], env.Name)
		}
	}
	for w, names := range waves {
		if len(names) <= summaryMaxNamed {
			continue
		}
		g := fmt.Sprintf("wave %d", w)
		for _, n := range names {
			groupOf[n] = g
		}
		size[g] = len(names)
	}
	return groupOf, size
}

// capped returns at most summaryMaxNamed of names and how many it left out.
func capped(names []string) ([]string, int) {
	if len(names) <= summaryMaxNamed {
		return slices.Clone(names), 0
	}
	return slices.Clone(names[:summaryMaxNamed]), len(names) - summaryMaxNamed
}

// joinItems joins a clause's places. Named environments alone are a comma
// list ("test, uat"); with "N more" or a group count (and) the last place is
// joined with "and" ("canary and 27 of 42 pops", "a, b, c, d, e, f and 3
// more", "a, b, c, d, e, f, 2 more and 4 of 10 pops").
func joinItems(items []string, and bool) string {
	if len(items) < 2 || !and {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// EnvironmentLine returns status.environmentSummary: env=version for each
// environment, "-" where nothing runs yet.
func EnvironmentLine(envs []kardinalv1alpha1.PipelineEnvironmentStatus) string {
	parts := make([]string, 0, len(envs))
	for _, e := range envs {
		v := e.Version
		if v == "" {
			v = "-"
		}
		parts = append(parts, e.Name+"="+strings.ReplaceAll(v, " ", "_"))
	}
	return strings.Join(parts, " ")
}

// shortPR is "#42" for a pull request URL ending in /42, else the URL.
func shortPR(url string) string {
	i := strings.LastIndexByte(url, '/')
	if i < 0 || i == len(url)-1 {
		return url
	}
	n := url[i+1:]
	for _, r := range n {
		if r < '0' || r > '9' {
			return url
		}
	}
	return "#" + n
}

// lowerFirst lower-cases the first letter, keeping acronyms ("PR") as they are.
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	if r2, _ := utf8.DecodeRuneInString(s[n:]); unicode.IsUpper(r2) {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// environmentsEqual reports whether a and b are the same projection.
func environmentsEqual(a, b []kardinalv1alpha1.PipelineEnvironmentStatus) bool {
	return equality.Semantic.DeepEqual(a, b)
}
