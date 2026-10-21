// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package daycases is the enterprise's day in the life
// (test/scenario/enterprise/day/...): three tenants with several Pipelines
// each and a fleet, people, bots, individually and group-registered agents
// of every autonomy and a suspended one, all on one World, through one
// timeline of v0.12's features together. Day runs it in a fixed order
// (timeline) or with a seeded random interleaving of its episodes (seeds),
// and checks the day's invariants after every step.
package daycases

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// The tenants: team-a ships through a verify gate and a release managers'
// approval (agents ship here), team-b reviews every promotion in a PR, and
// team-c rolls a fleet out with canaries that roll back.
const (
	TeamA = "team-a"
	TeamB = "team-b"
	TeamC = "team-c"
	// Web is each tenant's second Pipeline (the Plan's is scenario.PipelineName).
	Web = "web"
	// releaseGate is team-a's release managers' approval on prod.
	releaseGate = "release"
	// forged is a principal nobody is: a stamp naming it is a forgery.
	forged = "forged@corp.example.com"
	// verifyTimeout bounds team-a's verify gate.
	verifyTimeout = 10 * time.Minute
)

// The day's people, bots and agents.
var (
	sa  = []string{"system:serviceaccounts", "system:authenticated"}
	dev = scenario.ActorIdentities["human"]
	bot = scenario.ActorIdentities["bot"]
	bob = scenario.Identity{Username: scenario.Sponsor, Groups: []string{"system:authenticated"}}
	rm  = scenario.Identity{Username: "rm@corp.example.com", Groups: []string{"release-managers", "system:authenticated"}}
	ic  = scenario.Identity{Username: "ic@corp.example.com", Groups: []string{"incident-commanders", "system:authenticated"}}
	// fixer (autonomy auto) ships in team-a and team-c; careful (approve)
	// and banned (deny) in team-a; triage is registered by its group, in
	// every team; parked is suspended.
	fixer   = scenario.ActorIdentities["agent"]
	careful = scenario.Identity{Username: scenario.SAUser("careful"), Groups: sa}
	banned  = scenario.Identity{Username: scenario.SAUser("banned"), Groups: sa}
	triage  = scenario.ActorIdentities["agent-group"]
	parked  = scenario.ActorIdentities["suspended"]
	// shipper (autonomy auto) ships in team-a under shipperRule.
	shipper = scenario.Identity{Username: scenario.SAUser("shipper"), Groups: sa}
)

// shipperRule is the shipper's budget: two versions a day into prod, one
// at a time (docs/design/24-agent-budgets.md).
var shipperRule = v1alpha1.AgentBudgetRule{Name: "prod", Environments: []string{"prod"}, VersionsPerDay: 2, MaxConcurrent: 1}

// agents are the registry: name, subject principal, sponsor.
var agents = map[string]struct{ principal, sponsor string }{
	"fixer":          {fixer.Username, scenario.Sponsor},
	"careful":        {careful.Username, scenario.Sponsor},
	"banned":         {banned.Username, scenario.Sponsor},
	"triage":         {triage.Username, scenario.Sponsor},
	"parked":         {parked.Username, scenario.Sponsor},
	"shipper":        {shipper.Username, scenario.Sponsor},
	scenario.QAAgent: {scenario.SAUser(scenario.QAAgent), scenario.QASponsor},
}

// registry is the day's AgentIdentities.
func registry() []client.Object {
	id := func(name string, sub v1alpha1.AgentSubject, ns []string, autonomy string, suspended bool) *v1alpha1.AgentIdentity {
		a := scenario.AgentIdentityOf(name, []v1alpha1.AgentSubject{sub}, ns, suspended)
		a.Spec.Autonomy = autonomy
		return a
	}
	qa := id(scenario.QAAgent, scenario.SASubject(scenario.QAAgent), []string{TeamA}, v1alpha1.AutonomyAuto, false)
	qa.Spec.Sponsor.User = scenario.QASponsor
	ship := id("shipper", scenario.SASubject("shipper"), []string{TeamA}, v1alpha1.AutonomyAuto, false)
	ship.Spec.Budgets = []v1alpha1.AgentBudgetRule{shipperRule}
	return []client.Object{
		id("fixer", scenario.SASubject("fixer"), []string{TeamA, TeamC}, v1alpha1.AutonomyAuto, false),
		id("careful", scenario.SASubject("careful"), []string{TeamA}, v1alpha1.AutonomyApprove, false),
		id("banned", scenario.SASubject("banned"), []string{TeamA}, v1alpha1.AutonomyDeny, false),
		id("triage", v1alpha1.AgentSubject{Kind: "Group", Name: triage.Groups[0]}, []string{"team-*"}, v1alpha1.AutonomyAuto, false),
		id("parked", scenario.SASubject("parked"), []string{TeamA}, v1alpha1.AutonomyAuto, true),
		qa,
		ship,
	}
}

// Day is the enterprise for one day.
type Day struct {
	*scenario.World
	t     *testing.T
	plans map[string]*scenario.Plan
	rng   *rand.Rand
	// approve are the Bundles the release managers approve (approver).
	approve map[string]bool
	// verdicts is what QAAgent finds in a Bundle (default: pass).
	verdicts map[string]scenario.AgentResult
	// outage is true while the agent index is gone: people wait.
	outage bool
	// outageSince is true from an outage to the first check after it, and
	// errSeen the reconcile errors seen by then (CheckInvariants).
	outageSince bool
	errSeen     map[string]int
	// handVerified are the AgentRuns whose refused Verdict the QA lead
	// made up for (qaLead).
	handVerified map[string]bool
	// What the day did, for the invariants: freezes, pauses and holds with
	// when they started and ended.
	freezes []*freezeRec
	pauses  []*pauseRec
	holds   []*holdRec
	// seen is each PromotionStep's state at the last observation, and
	// gatesReady each PolicyGate's readiness.
	seen       map[string]obs
	gatesReady map[string]bool
	// probes are forged writes the API server admitted (logged).
	probes []string
	// violations are the invariants broken, as found each round.
	violations []string
}

type freezeRec struct {
	name, ns, actors string
	from, to         time.Time
	exempt           map[string]bool // ns/pipeline
}

type pauseRec struct {
	ns, pipeline string
	from, to     time.Time
}

type holdRec struct {
	ns, pipeline, env, bundle string
	from, to                  time.Time
}

type obs struct {
	state string
	at    time.Time
}

// NewDay sets the enterprise up: the tenants, their Pipelines and gates, and
// the agent registry, every object admitted by the chart's policies.
func NewDay(t *testing.T, seed int64) *Day {
	t.Helper()
	w := scenario.NewWorld(t, scenario.Options{SCM: scenario.SCMGitHub, PolicyNamespaces: []string{scenario.PolicyNS}, Actors: true,
		// As the controller runs: an object is reconciled when a watch wakes it.
		Watch: true,
		// The shipper's budget (shipperRule) is the day's only one.
		Budgets: true})
	d := &Day{World: w, t: t, plans: map[string]*scenario.Plan{}, rng: rand.New(rand.NewSource(seed)), //nolint:gosec // a seeded interleaving
		approve: map[string]bool{}, verdicts: map[string]scenario.AgentResult{}, seen: map[string]obs{},
		errSeen: map[string]int{}, handVerified: map[string]bool{}}
	t.Cleanup(func() {
		if t.Failed() {
			for _, ns := range []string{TeamA, TeamB, TeamC} {
				t.Logf("%s after %d rounds:\n%s", ns, w.Rounds(), w.Describe(ns))
			}
			var steps v1alpha1.PromotionStepList
			if err := w.Client.List(w.Ctx, &steps); err == nil {
				for _, s := range steps.Items {
					if s.Status.State == "AbortedByAlarm" || s.Status.State == "Failed" {
						raw, _ := json.Marshal(s.Status)
						t.Logf("step %s/%s status: %s", s.Namespace, s.Name, raw)
					}
				}
			}
		}
	})
	w.MustApply(registry()...)
	combos := map[string]scenario.Combo{
		TeamA: {Update: "kustomize", Health: "deployment", Gate: "verify", Topology: "linear", Delivery: "direct", SCM: scenario.SCMGitHub, Outcome: "success", Actor: "agent"},
		TeamB: {Update: "kustomize", Health: "deployment", Gate: "cel", Topology: "linear", Delivery: "pr-review", SCM: scenario.SCMGitHub, Outcome: "success", Actor: "human"},
		TeamC: {Update: "kustomize", Health: "argoRollouts", Gate: "cel", Topology: "fleet", Delivery: "direct", SCM: scenario.SCMGitHub, Outcome: "rollback", Actor: "human"},
	}
	for _, ns := range []string{TeamA, TeamB, TeamC} {
		p := scenario.BuildPlan(combos[ns], ns, w.Now(), false)
		p.Pipeline.Spec.Git.URL = repoURL(ns, scenario.PipelineName)
		isolate(p.Pipeline)
		if ns == TeamA {
			p.Gate.Spec.Verify.Timeout = verifyTimeout.String()
			p.Gate.Spec.Verify.OnTimeout = "fail"
			p.Extra = append(p.Extra, &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: releaseGate,
				Labels: map[string]string{"kardinal.io/scope": "team", "kardinal.io/applies-to": "prod"}},
				Spec: v1alpha1.PolicyGateSpec{Expression: "true", Message: "a release manager approves",
					Approval: &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true,
						AllowedGroups: []string{"release-managers"}, AllowedUsers: []string{bob.Username}}}})
		}
		require.NoError(t, p.Setup(w))
		web := p.Pipeline.DeepCopy()
		web.Name = Web
		web.Spec.Git.URL = repoURL(ns, Web)
		require.NoError(t, w.Git.CreateRepo(web.Spec.Git.URL, "main", scenario.RepoFiles(web, scenario.BaseTag)))
		w.MustApply(p.Pipeline, web)
		d.plans[ns] = p
	}
	w.Agents = map[string]scenario.Agent{scenario.QAAgent: func(run v1alpha1.AgentRun) scenario.AgentResult {
		if r, ok := d.verdicts[run.Spec.BundleName]; ok {
			return r
		}
		return scenario.AgentResult{Result: v1alpha1.VerdictPass, Summary: "explored the checkout"}
	}}
	w.Actors = append(w.Actors, d.observe, scenario.Reviewer, d.approver, d.qaLead)
	w.Kro.Expect(noIndex) // #1929
	w.Round()
	w.Round()
	return d
}

// isolate runs each environment (and fleet target) of p in a namespace of
// its tenant's own, <ns>-<env>: the tenants' Pipelines share names.
func isolate(p *v1alpha1.Pipeline) {
	at := func(h v1alpha1.HealthConfig, env string) *v1alpha1.HealthConfig {
		out := h.DeepCopy()
		target := p.Namespace + "-" + env
		switch h.Type {
		case "argoRollouts":
			out.ArgoRollouts = &v1alpha1.HealthTargetRef{Namespace: target}
		default:
			out.Resource = &v1alpha1.ResourceRef{Namespace: target}
		}
		return out
	}
	for i := range p.Spec.Environments {
		e := &p.Spec.Environments[i]
		e.Health = *at(e.Health, e.Name)
		if e.Fleet != nil {
			for j := range e.Fleet.Targets {
				e.Fleet.Targets[j].Health = at(e.Health, e.Fleet.Targets[j].Name)
			}
		}
	}
}

func repoURL(ns, p string) string { return "https://github.com/" + ns + "/" + p + "-gitops" }

// SeedList is the seeded interleavings part runs: seed <part> (one seed a
// part, five over the day, to keep each package fast); 1 to 8 with
// KARDINAL_SCENARIO_ENTERPRISE=full; 1 to KARDINAL_SCENARIO_DAY_SEEDS; or
// only KARDINAL_SCENARIO_DAY_SEED (to replay one).
func SeedList(part int) []int64 {
	if v, err := strconv.ParseInt(os.Getenv("KARDINAL_SCENARIO_DAY_SEED"), 10, 64); err == nil && v >= 0 {
		return []int64{v}
	}
	n := 0
	if os.Getenv("KARDINAL_SCENARIO_ENTERPRISE") == "full" {
		n = 8
	}
	if v, err := strconv.Atoi(os.Getenv("KARDINAL_SCENARIO_DAY_SEEDS")); err == nil && v > 0 {
		n = v
	}
	if n == 0 {
		return []int64{int64(part)}
	}
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}

// approver is the release managers approving every waiting approval gate
// of the Bundles in d.approve, as kardinal approve does.
func (d *Day) approver(w *scenario.World) error {
	if d.outage {
		return nil
	}
	var gates v1alpha1.PolicyGateList
	if err := w.Client.List(w.Ctx, &gates); err != nil {
		return err
	}
	for _, g := range gates.Items {
		b, env := g.Labels["kardinal.io/bundle"], g.Labels["kardinal.io/environment"]
		if !d.approve[b] || g.Spec.Approval == nil || g.Status.Ready || env == "" {
			continue
		}
		if found, _, _ := counted(g, rm.Username); found {
			continue
		}
		if err := d.approveAs(rm, g.Namespace, b, env); err != nil && !strings.Contains(err.Error(), "never promotes again") {
			return fmt.Errorf("rm approves %s/%s in %s: %w", g.Namespace, b, env, err)
		}
	}
	return nil
}

// qaLead is the QA lead verifying by hand what QAAgent found when the API
// server refused its Verdict (an agent index outage refuses agents' writes,
// failing closed), before the gate's timeout: the gate allows QALead.
func (d *Day) qaLead(w *scenario.World) error {
	if d.outage {
		return nil
	}
	for _, r := range w.AgentVerdictRefusals() {
		if d.handVerified[r.Run] {
			continue
		}
		var run v1alpha1.AgentRun
		if err := w.Client.Get(w.Ctx, client.ObjectKey{Namespace: TeamA, Name: r.Run}, &run); err != nil {
			return fmt.Errorf("the refused run %s: %w", r.Run, err)
		}
		d.handVerified[r.Run] = true
		res, ok := d.verdicts[run.Spec.BundleName]
		if !ok {
			res = scenario.AgentResult{Result: v1alpha1.VerdictPass}
		}
		if res.Result == "" {
			continue
		}
		if err := w.RecordVerdict(scenario.Identity{Username: scenario.QALead, Groups: []string{"system:authenticated"}}, TeamA,
			run.Spec.BundleName, run.Spec.Environment, run.Spec.Check, res.Result, nil); err != nil {
			return fmt.Errorf("the QA lead verifies %s: %w", run.Spec.BundleName, err)
		}
		d.t.Logf("round %d: QA lead verified %s by hand (%s): the agent's Verdict was refused: %v", d.Rounds(),
			run.Spec.BundleName, res.Result, r.Reasons)
	}
	return nil
}

func (d *Day) approveAs(id scenario.Identity, ns, bundle, env string) error {
	stamp, err := d.StampFor(id, actor.Where{Namespace: ns, Environment: env})
	if err != nil {
		return err
	}
	_, _, err = lifecycle.RecordApproval(d.CtxOf(id), d.Client, lifecycle.ApprovalRequest{Namespace: ns, Bundle: bundle,
		Environment: env, User: id.Username, Groups: id.Groups, Decision: lifecycle.DecisionApprove, Comment: "day", Actor: stamp})
	return err
}

// counted reports how g counted user's approval: found, counted, why not.
func counted(g v1alpha1.PolicyGate, user string) (bool, bool, string) {
	for _, a := range g.Status.Approvals {
		if a.User == user {
			return true, a.Counted, a.Reason
		}
	}
	return false, false, ""
}

// ship creates a version of ns/pipeline as id, as kardinal create bundle
// does; the CLI and the API server must admit it.
func (d *Day) ship(id scenario.Identity, ns, pipeline, tag string) *v1alpha1.Bundle {
	d.t.Helper()
	b := scenario.ImageBundle(ns, pipeline, tag)
	cliErr, refused, err := d.CreateBundle(id, b)
	require.NoError(d.t, err)
	require.NoError(d.t, cliErr, "the CLI refused %s's %s", id.Username, b.Name)
	require.Empty(d.t, refused, "the API server refused %s's %s", id.Username, b.Name)
	return b
}

// gate is bundle's instance of gate template name in env, or nil.
func (d *Day) gate(ns, bundle, name, env string) *v1alpha1.PolicyGate {
	for _, g := range d.GateInstances(ns, bundle) {
		if g.Labels["kardinal.io/gate-name"] == name && g.Labels["kardinal.io/environment"] == env {
			return &g
		}
	}
	return nil
}

func (d *Day) verifiedIn(ns, bundle, env string) bool {
	for _, e := range d.Verified(ns, bundle) {
		if e == env {
			return true
		}
	}
	return false
}

func (d *Day) phase(ns, bundle string) string { return d.BundlePhase(ns)[bundle] }

// violate records a broken invariant (failing the test at the next check).
func (d *Day) violate(format string, args ...interface{}) {
	d.violations = append(d.violations, fmt.Sprintf("round %d: ", d.Rounds())+fmt.Sprintf(format, args...))
}

// pending is a step that has not started.
func pending(state string) bool { return state == "" || state == "Pending" }

// started is a step that went past where a freeze, a pause or a hold
// keeps it.
func started(state string) bool {
	switch state {
	case "Promoting", "WaitingForMerge", "HealthChecking", "Verifying", "Verified":
		return true
	}
	return false
}

// observe runs at the start of every round: what changed since the last
// one must have been allowed. No step starts while a freeze that selects it
// (without an exemption it went through under), a pause of its Pipeline or a
// hold of its environment on another version was in force; a prod step is
// created only once every gate of its environment is ready.
func (d *Day) observe(w *scenario.World) error {
	now := w.Now()
	var steps v1alpha1.PromotionStepList
	if err := w.Client.List(w.Ctx, &steps); err != nil {
		return err
	}
	for i := range steps.Items {
		s := &steps.Items[i]
		key := s.Namespace + "/" + s.Name
		prev, ok := d.seen[key]
		if !ok {
			prev = obs{state: "Pending", at: s.CreationTimestamp.Time}
			d.checkEvidence(s)
		}
		if pending(prev.state) && started(s.Status.State) {
			d.checkStart(s, prev.at, now)
		}
		d.seen[key] = obs{state: s.Status.State, at: now}
	}
	// What kro sees of the gates in this round: a step created in it is
	// checked against this at the next observation (checkEvidence).
	var gates v1alpha1.PolicyGateList
	if err := w.Client.List(w.Ctx, &gates); err != nil {
		return err
	}
	d.gatesReady = map[string]bool{}
	for _, g := range gates.Items {
		d.gatesReady[g.Namespace+"/"+g.Name] = g.Status.Ready
	}
	return nil
}

// inForce reports whether [from, to) covered all of [since, until].
func inForce(from, to, since, until time.Time) bool {
	return !from.After(since) && (to.IsZero() || !to.Before(until))
}

func (d *Day) checkStart(s *v1alpha1.PromotionStep, since, until time.Time) {
	b := d.bundle(s.Namespace, s.Spec.BundleName)
	rollback := b != nil && b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != ""
	agentVersion := b != nil && b.Spec.Actor != nil && b.Spec.Actor.Kind == v1alpha1.ActorAgent
	exempted := false
	for _, c := range s.Status.Conditions {
		if c.Type == "FreezeExempted" && c.Status == metav1.ConditionTrue {
			exempted = true
		}
	}
	for _, f := range d.freezes {
		if !inForce(f.from, f.to, since, until) || (f.ns != "" && f.ns != s.Namespace) ||
			(f.actors == v1alpha1.FreezeAgents && !agentVersion) {
			continue
		}
		switch {
		case !exempted:
			d.violate("step %s/%s (%s) started under freeze %s", s.Namespace, s.Name, s.Status.State, f.name)
		case !f.exempt[s.Namespace+"/"+s.Spec.PipelineName]:
			d.violate("step %s/%s went through freeze %s exempted, but nobody exempted %s", s.Namespace, s.Name, f.name, s.Spec.PipelineName)
		}
	}
	for _, p := range d.pauses {
		if inForce(p.from, p.to, since, until) && p.ns == s.Namespace && p.pipeline == s.Spec.PipelineName {
			d.violate("step %s/%s (%s) started while %s was paused", s.Namespace, s.Name, s.Status.State, p.pipeline)
		}
	}
	for _, h := range d.holds {
		if inForce(h.from, h.to, since, until) && h.ns == s.Namespace && h.pipeline == s.Spec.PipelineName &&
			h.env == s.Spec.Environment && h.bundle != s.Spec.BundleName && !rollback {
			d.violate("step %s/%s started in %s held on %s", s.Namespace, s.Name, h.env, h.bundle)
		}
	}
}

// checkEvidence: a new step of a gated (prod) environment has every gate of
// its environment ready.
func (d *Day) checkEvidence(s *v1alpha1.PromotionStep) {
	p := d.plans[s.Namespace]
	if p == nil || !contains(p.GateEnvs, s.Spec.Environment) {
		return
	}
	b := d.bundle(s.Namespace, s.Spec.BundleName)
	if b != nil && b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != "" {
		return
	}
	fleet := graph.FleetOf(p.Pipeline, s.Spec.Environment)
	n := 0
	for _, g := range d.GateInstances(s.Namespace, s.Spec.BundleName) {
		env := g.Labels["kardinal.io/environment"]
		if env != s.Spec.Environment && (fleet == "" || env != fleet) {
			continue
		}
		n++
		// As kro saw the gate when it created the step: in the round before
		// this observation.
		if ready, seen := d.gatesReady[g.Namespace+"/"+g.Name]; !seen || !ready {
			d.violate("step %s/%s created with gate %s not ready (seen: %t): %s", s.Namespace, s.Name,
				g.Labels["kardinal.io/gate-name"], seen, g.Status.Reason)
		}
	}
	if n == 0 {
		d.violate("step %s/%s of a gated environment has no gate instance", s.Namespace, s.Name)
	}
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

// party is whom an approval or a version speaks for: an agent's sponsor, or
// the person.
func party(principal string) string {
	for _, a := range agents {
		if a.principal == principal {
			return a.sponsor
		}
	}
	return principal
}

// personal are the AuditEvents of what a person or an agent did.
var personal = map[string]bool{"PipelinePaused": true, "PipelineResumed": true, "HoldCreated": true, "HoldReleased": true,
	"BundleRejected": true, "FreezeStarted": true, "FreezeLifted": true, "ApprovalRecorded": true, "ApprovalRevoked": true,
	"VerdictRecorded": true, "GateOverridden": true}

// noGroupStamp are the records a group-registered agent's stamp is never
// recorded on (actor.VerifyStamp).
var noGroupStamp = map[string]bool{"PipelinePaused": true, "PipelineResumed": true, "HoldCreated": true, "HoldReleased": true,
	"BundleRejected": true}

// CheckInvariants are the day's invariants, after every step: nothing broke
// a freeze, a pause, a hold or a gate (observe); no approval of the
// version's own party counted; every AuditEvent names a verified actor or
// says why not; no forged stamp is recorded; kro, the SCM and the
// reconcilers saw no error.
func (d *Day) CheckInvariants() {
	t := d.t
	t.Helper()
	require.Empty(t, d.violations, "invariants broken")
	var bundles v1alpha1.BundleList
	require.NoError(t, d.Client.List(d.Ctx, &bundles))
	author := map[string]*v1alpha1.Actor{}
	for i := range bundles.Items {
		b := &bundles.Items[i]
		author[b.Namespace+"/"+b.Name] = b.Spec.Actor
		require.NotNil(t, b.Spec.Actor, "Bundle %s/%s has no actor stamp", b.Namespace, b.Name)
		d.checkStamp("Bundle "+b.Namespace+"/"+b.Name, "", *b.Spec.Actor)
	}
	var gates v1alpha1.PolicyGateList
	require.NoError(t, d.Client.List(d.Ctx, &gates))
	for _, g := range gates.Items {
		a := author[g.Namespace+"/"+g.Labels["kardinal.io/bundle"]]
		if a == nil || g.Spec.Approval == nil || !g.Spec.Approval.ExcludeAuthor {
			continue
		}
		for _, ap := range g.Status.Approvals {
			require.False(t, ap.Counted && party(ap.User) == party(a.Principal),
				"gate %s/%s counted %s's approval of a version of their own party (%s)", g.Namespace, g.Name, ap.User, actor.Describe(*a))
		}
	}
	var events v1alpha1.AuditEventList
	require.NoError(t, d.Client.List(d.Ctx, &events))
	for _, e := range events.Items {
		what := fmt.Sprintf("AuditEvent %s/%s (%s: %s)", e.Namespace, e.Name, e.Spec.Action, e.Spec.Message)
		switch {
		case e.Spec.Actor != nil:
			d.checkStamp(what, e.Spec.Action, *e.Spec.Actor)
		case personal[e.Spec.Action]:
			require.True(t, strings.Contains(e.Spec.Message, "not recorded") || strings.Contains(e.Spec.Message, "audit log names who"),
				"%s names no actor and does not say why", what)
		case e.Spec.Action == "FreezeExempted":
			require.Contains(t, e.Spec.Message, " by ", "%s does not name who made the exemption", what)
		case e.Spec.Action == "BudgetExempted":
			require.Contains(t, e.Spec.Message, " made by ", "%s does not name who made the exemption", what)
		case e.Spec.Action == "PRMerged":
			require.Contains(t, e.Spec.Message, " by reviewer", "%s does not name the merger", what)
		case strings.HasPrefix(e.Spec.Action, "Promotion") || strings.HasPrefix(e.Spec.Action, "Rollback"):
			require.Fail(t, "a promotion record names no actor", what)
		}
	}
	require.Empty(t, d.Kro.Errors(), "kro hard errors")
	require.Empty(t, d.SCM.Unknown(), "requests the fake SCM does not serve")
	n, last := d.Errors()
	for kind, k := range n {
		if k == d.errSeen[kind] {
			continue
		}
		// #1929: while the agent index is missing, the actor policy's binding
		// refuses the controller's own Bundles too (an automatic rollback).
		require.True(t, d.outageSince && strings.Contains(last[kind], noIndex),
			"%d %s reconcile errors, the last: %s", k-d.errSeen[kind], kind, last[kind])
		d.errSeen[kind] = k
	}
	if !d.outage {
		d.outageSince = false
	}
}

// noIndex is the API server's refusal of every write the actor policy
// matches while the agent index is missing (#1929).
const noIndex = "no params found for policy binding with `Deny` parameterNotFoundAction"

// checkStamp: a recorded stamp is a registered agent's own (its principal
// and sponsor; never a group-registered one's where those are not
// recorded), a bot's ServiceAccount, the controller, or a person, and never
// the forged principal.
func (d *Day) checkStamp(what, action string, a v1alpha1.Actor) {
	t := d.t
	t.Helper()
	require.NotEqual(t, forged, a.Principal, "%s records a forged stamp", what)
	switch a.Kind {
	case v1alpha1.ActorAgent:
		reg, ok := agents[a.Agent]
		require.True(t, ok, "%s records unregistered agent %q", what, a.Agent)
		// An AgentRun votes as its own ServiceAccount (kr-<run>).
		run := strings.HasPrefix(a.Principal, actor.ServiceAccountPrefix) && strings.Contains(a.Principal, ":kr-")
		if !run || a.Agent != scenario.QAAgent {
			require.Equal(t, reg.principal, a.Principal, "%s: agent %s's principal", what, a.Agent)
		}
		require.Equal(t, reg.sponsor, a.Sponsor, "%s: agent %s's sponsor", what, a.Agent)
		require.False(t, a.Agent == "triage" && noGroupStamp[action], "%s records a group-registered agent's stamp", what)
	case v1alpha1.ActorBot:
		require.True(t, strings.HasPrefix(a.Principal, actor.ServiceAccountPrefix), "%s: bot %s", what, a.Principal)
		require.Equal(t, a.Principal, party(a.Principal), "%s: an agent's ServiceAccount stamped as a bot", what)
	case v1alpha1.ActorController:
		require.Equal(t, scenario.ControllerUser, a.Principal, "%s", what)
	case v1alpha1.ActorHuman:
		require.False(t, strings.HasPrefix(a.Principal, actor.ServiceAccountPrefix), "%s: a ServiceAccount stamped as a person", what)
		require.Equal(t, a.Principal, party(a.Principal), "%s: an agent stamped as a person", what)
	default:
		require.Failf(t, "unknown actor kind", "%s: %+v", what, a)
	}
}

// Converge: once the day's writes stop, status settles and stays settled:
// three more rounds write nothing.
func (d *Day) Converge() {
	d.t.Helper()
	settled := false
	for i := 0; i < 12 && !settled; i++ {
		settled = d.Round() == 0
	}
	if !settled {
		before := d.versions()
		d.Round()
		require.Fail(d.t, "writes did not stop within 12 rounds", "still written: %v", d.changed(before))
	}
	for i := 0; i < 3; i++ {
		// Reconciles may run (a timer that is due: an AgentRun waits for its
		// Job); none may write, but a Failed Bundle's gates, which are
		// evaluated again every recheckInterval (it can recover).
		before := d.versions()
		d.Round()
		var wrote []string
		for _, c := range d.changed(before) {
			if !d.recheck(c) {
				wrote = append(wrote, c)
			}
		}
		require.Empty(d.t, wrote, "a settled day wrote again")
	}
}

// versions is the resourceVersion of every object of the kinds kardinal
// writes, for what a round changed.
func (d *Day) versions() map[string]string {
	out := map[string]string{}
	for _, l := range []client.ObjectList{&v1alpha1.BundleList{}, &v1alpha1.PromotionStepList{}, &v1alpha1.PolicyGateList{},
		&v1alpha1.PipelineList{}, &v1alpha1.PRStatusList{}, &v1alpha1.AuditEventList{}, &v1alpha1.ChangeWindowList{},
		&v1alpha1.AgentRunList{}, &v1alpha1.VerdictList{}, &v1alpha1.AgentIdentityList{}, &v1alpha1.AgentIndexList{},
		&v1alpha1.ApprovalList{}, &v1alpha1.StepTemplateList{}} {
		require.NoError(d.t, d.Client.List(d.Ctx, l))
		items, err := apimeta.ExtractList(l)
		require.NoError(d.t, err)
		for _, it := range items {
			o := it.(client.Object)
			raw, _ := json.Marshal(o)
			out[fmt.Sprintf("%T %s/%s", l, o.GetNamespace(), o.GetName())] = o.GetResourceVersion() + "\x00" + string(raw)
		}
	}
	return out
}

func (d *Day) changed(before map[string]string) []string {
	var out []string
	for k, v := range d.versions() {
		rv, obj, _ := strings.Cut(v, "\x00")
		brv, bobj, _ := strings.Cut(before[k], "\x00")
		if brv != rv {
			out = append(out, k+": "+jsonDiff(bobj, obj))
		}
	}
	sort.Strings(out)
	return out
}

// CheckEvidence: every version Verified in prod got there with its
// evidence: in team-a a counted verdict of QAAgent's run and a release
// manager's counted approval; in team-b a PR merged by the reviewer, whom
// its PRMerged AuditEvent names.
func (d *Day) CheckEvidence() {
	t := d.t
	t.Helper()
	var events v1alpha1.AuditEventList
	require.NoError(t, d.Client.List(d.Ctx, &events))
	merged := map[string]string{}
	for _, e := range events.Items {
		if e.Spec.Action == "PRMerged" {
			merged[e.Namespace+"/"+e.Spec.BundleName+"/"+e.Spec.Environment] = e.Spec.Message
		}
	}
	var steps v1alpha1.PromotionStepList
	require.NoError(t, d.Client.List(d.Ctx, &steps))
	n := 0
	for _, s := range steps.Items {
		if s.Status.State != "Verified" || s.Spec.Environment != "prod" {
			continue
		}
		n++
		switch s.Namespace {
		case TeamA:
			v := d.gate(TeamA, s.Spec.BundleName, d.plans[TeamA].Gate.Name, "prod")
			require.NotNil(t, v, "%s: no verify gate", s.Name)
			ran := false
			for _, vd := range v.Status.Verdicts {
				pass := vd.Counted && vd.Result == v1alpha1.VerdictPass
				ran = ran || (pass && vd.Run > 0 && strings.Contains(vd.Verifier, scenario.QAAgent)) ||
					(pass && strings.Contains(vd.Verifier, scenario.QALead) && d.refused(s.Spec.BundleName))
			}
			require.True(t, ran, "%s reached prod without a counted run of %s (or the QA lead's, for a refused one): %+v",
				s.Name, scenario.QAAgent, v.Status.Verdicts)
			r := d.gate(TeamA, s.Spec.BundleName, releaseGate, "prod")
			require.NotNil(t, r, "%s: no release gate", s.Name)
			_, ok, why := counted(*r, rm.Username)
			require.True(t, ok, "%s reached prod without the release manager's counted approval (%s)", s.Name, why)
		case TeamB:
			require.Equal(t, "reviewer", s.Status.Outputs["prMergedBy"], "%s: who merged its PR", s.Name)
			require.Contains(t, merged[s.Namespace+"/"+s.Spec.BundleName+"/prod"], "by reviewer", "%s: its PRMerged AuditEvent", s.Name)
		}
	}
	t.Logf("%d prod promotions with their evidence", n)
}

// Summary logs what the day did.
func (d *Day) Summary() {
	counts := map[string]int{}
	var events v1alpha1.AuditEventList
	require.NoError(d.t, d.Client.List(d.Ctx, &events))
	for _, e := range events.Items {
		counts[e.Spec.Action]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	d.t.Logf("%d rounds; AuditEvents %s; admitted forgery probes %v", d.Rounds(), strings.Join(parts, " "), d.probes)
}

// bundle is ns/name, or nil when there is none.
func (d *Day) bundle(ns, name string) *v1alpha1.Bundle {
	var b v1alpha1.Bundle
	if err := d.Client.Get(d.Ctx, client.ObjectKey{Namespace: ns, Name: name}, &b); err != nil {
		require.True(d.t, apierrors.IsNotFound(err), "get bundle %s/%s: %v", ns, name, err)
		return nil
	}
	return &b
}

// refused reports whether QAAgent's Verdict on bundle was refused.
func (d *Day) refused(bundle string) bool {
	for _, r := range d.AgentVerdictRefusals() {
		if strings.Contains(r.Run, bundle) {
			return true
		}
	}
	return false
}

// jsonDiff is what changed between two JSON objects, top-level field by
// field (status, field by field), for a failure message.
func jsonDiff(a, b string) string {
	var x, y map[string]interface{}
	_ = json.Unmarshal([]byte(a), &x)
	_ = json.Unmarshal([]byte(b), &y)
	flat := func(m map[string]interface{}) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			if sub, ok := v.(map[string]interface{}); ok && (k == "status" || k == "metadata" || k == "spec") {
				for k2, v2 := range sub {
					raw, _ := json.Marshal(v2)
					out[k+"."+k2] = string(raw)
				}
				continue
			}
			raw, _ := json.Marshal(v)
			out[k] = string(raw)
		}
		return out
	}
	fx, fy := flat(x), flat(y)
	var parts []string
	for k, v := range fy {
		if fx[k] != v && k != "metadata.resourceVersion" {
			parts = append(parts, fmt.Sprintf("%s: %.300s -> %.300s", k, fx[k], v))
		}
	}
	for k, v := range fx {
		if _, ok := fy[k]; !ok {
			parts = append(parts, fmt.Sprintf("%s: %.300s -> (gone)", k, v))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// recheck reports whether change (changed) is a timer that came due: a
// Failed Bundle's gate evaluated again (only its lastEvaluatedAt moved), or
// an AgentRun past its deadline (the verify timeout's run, which never
// finishes).
func (d *Day) recheck(change string) bool {
	if strings.HasPrefix(change, "*v1alpha1.AgentRunList ") && strings.Contains(change, `status.phase: "Running" -> "Failed"`) &&
		strings.Contains(change, "did not finish before the") {
		return true
	}
	name, diff, _ := strings.Cut(strings.TrimPrefix(change, "*v1alpha1.PolicyGateList "), ": ")
	if strings.HasPrefix(change, "*v1alpha1.PolicyGateList ") && strings.HasPrefix(diff, "status.lastEvaluatedAt:") && !strings.Contains(diff, "; ") {
		ns, gate, _ := strings.Cut(name, "/")
		var g v1alpha1.PolicyGate
		if err := d.Client.Get(d.Ctx, client.ObjectKey{Namespace: ns, Name: gate}, &g); err == nil {
			return d.phase(ns, g.Labels["kardinal.io/bundle"]) == "Failed"
		}
	}
	return false
}
