// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// MaxGateVerdicts is how many verdicts a verify gate counts: the Graph
// copies at most one more (the CRD's maxItems), and a gate that gets more
// is held, so a flood of Verdicts cannot hide the ones that count.
const MaxGateVerdicts = 100

// Verify outcomes (docs/design/19-verdicts.md, section 6.3).
const (
	VerifyWaiting  = "Waiting"
	VerifyVerified = "Verified"
	VerifyFailed   = "Failed"
	VerifyTimedOut = "TimedOut"
)

// VerdictTally is how a verify gate counts its spec.verdicts: the shared
// rules, so the CLI, the UI API and the PolicyGate reconciler agree.
type VerdictTally struct {
	// Policy is the gate's verify policy with its defaults; nil without one.
	Policy *kardinalv1alpha1.GateVerifyPolicy
	// Required and MaxFailures are the policy's quorum and tolerance.
	Required, MaxFailures int
	// Passed are the passing voters' labels (agent name or principal), in
	// vote order.
	Passed []string
	// Failed are the failing votes, as "<label>: <summary>".
	Failed []string
	// Inconclusive is how many votes are inconclusive and wait.
	Inconclusive int
	// HighFindings is the number of high or critical findings in counted
	// votes.
	HighFindings int
	// Records say how each verdict was counted, sorted by name.
	Records []kardinalv1alpha1.GateVerdictStatus
	// Held is why the gate cannot count at all ("" when it can).
	Held string
	// Deadline is when the wait ends (nil: no timeout, or the verified
	// environment has not passed its health check yet).
	Deadline *time.Time
	// Outcome is VerifyWaiting, VerifyVerified, VerifyFailed or
	// VerifyTimedOut ("" without a policy).
	Outcome string
}

// VerifyPolicyOf returns gate's verify policy with every default applied,
// or nil.
func VerifyPolicyOf(gate *kardinalv1alpha1.PolicyGate) *kardinalv1alpha1.GateVerifyPolicy {
	if gate.Spec.Verify == nil {
		return nil
	}
	p := gate.Spec.Verify.DeepCopy()
	p.Required = max(p.Required, 1)
	if p.ExcludeAuthor == nil {
		t := true
		p.ExcludeAuthor = &t
	}
	if p.OnInconclusive == "" {
		p.OnInconclusive = kardinalv1alpha1.VerifyHold
	}
	if p.OnTimeout == "" {
		p.OnTimeout = kardinalv1alpha1.VerifyFail
	}
	if p.Run != nil && p.Run.Count == 0 {
		p.Run.Count = p.Required
	}
	return p
}

// VerifyTimeout is the policy's timeout, 0 for none or an invalid value
// (the CRD pattern admits only valid durations).
func VerifyTimeout(p *kardinalv1alpha1.GateVerifyPolicy) time.Duration {
	if p == nil || p.Timeout == "" {
		return 0
	}
	d, err := time.ParseDuration(p.Timeout)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// VerifyDeadline is when a gate with policy p stops waiting: the verified
// environment's health check time plus the timeout. nil when p has no
// timeout or healthCheckedAt is unknown.
func VerifyDeadline(p *kardinalv1alpha1.GateVerifyPolicy, healthCheckedAt *time.Time) *time.Time {
	d := VerifyTimeout(p)
	if d == 0 || healthCheckedAt == nil {
		return nil
	}
	t := healthCheckedAt.Add(d)
	return &t
}

// VerifierActor is the actor of a copied verdict: its stamp, or the
// read-side unknown actor with its writer.
func VerifierActor(v kardinalv1alpha1.GateVerdict) kardinalv1alpha1.Actor {
	return actor.OrUnknown(v.Actor, v.By)
}

// verifierLabel is how the reason names a voter: the agent, else the
// principal.
func verifierLabel(a kardinalv1alpha1.Actor) string {
	if a.Kind == kardinalv1alpha1.ActorAgent {
		return a.Agent
	}
	return a.Principal
}

// VerdictWriterAllowed reports whether the policy lets a verdict by v count
// at all, and why not. allowedAgents, allowedUsers and allowedGroups are one
// allowlist (#2411): an agent counts only when it is in allowedAgents, and
// anyone else only when it is in allowedUsers or one of its groups is in
// allowedGroups. A gate that lists nobody at all counts every verdict by
// someone who is not an agent; one that lists only agents counts agents
// only. An unverified verdict (written while the AgentIndex was not Ready,
// #2451) never counts.
func VerdictWriterAllowed(p *kardinalv1alpha1.GateVerifyPolicy, v kardinalv1alpha1.Actor, groups []string) (bool, string) {
	if v.Kind == kardinalv1alpha1.ActorUnverified {
		return false, fmt.Sprintf("%s is unverified: written while kardinal's agent index was not Ready, so it cannot count; "+
			"write the verdict again", v.Principal)
	}
	if v.Kind == kardinalv1alpha1.ActorAgent {
		if slices.Contains(p.AllowedAgents, v.Agent) {
			return true, ""
		}
		return false, fmt.Sprintf("agent %s is not in verify.allowedAgents", v.Agent)
	}
	if len(p.AllowedAgents) == 0 && len(p.AllowedUsers) == 0 && len(p.AllowedGroups) == 0 {
		return true, ""
	}
	if slices.Contains(p.AllowedUsers, v.Principal) || slices.ContainsFunc(groups, func(g string) bool {
		return slices.Contains(p.AllowedGroups, g)
	}) {
		return true, ""
	}
	if len(p.AllowedUsers) == 0 && len(p.AllowedGroups) == 0 {
		return false, fmt.Sprintf("%s is not an allowed verifier: the gate counts only the agents in verify.allowedAgents (%s); "+
			"list people in verify.allowedUsers or allowedGroups to count them", v.Principal, strings.Join(p.AllowedAgents, ", "))
	}
	return false, fmt.Sprintf("%s is not in verify.allowedUsers or allowedGroups", v.Principal)
}

// VerdictPartyBlock returns why the one-party rule refuses a verdict by v
// on work by author, or "". With excludeAuthor the author's party never
// counts; an agent's verdict on work of its own party never counts at all.
func VerdictPartyBlock(p *kardinalv1alpha1.GateVerifyPolicy, author, v kardinalv1alpha1.Actor) string {
	exclude := p.ExcludeAuthor == nil || *p.ExcludeAuthor
	if !exclude && v.Kind != kardinalv1alpha1.ActorAgent {
		return ""
	}
	if author.Principal == "" {
		if v.Kind == kardinalv1alpha1.ActorAgent {
			return "the version's author is unknown, so an agent's verdict cannot be checked against it"
		}
		return ""
	}
	if actor.SameParty(author, v) {
		return "the author's party (" + actor.Describe(author) + ")"
	}
	return ""
}

// voterOf is the vote key of a verdict: its run when admission bound it to
// one, else the verifier's party.
func voterOf(v kardinalv1alpha1.GateVerdict, a kardinalv1alpha1.Actor) string {
	if v.Run != nil {
		return "run:" + strconv.Itoa(v.Run.Index)
	}
	return "party:" + actor.PartyKey(a)
}

// TallyVerdicts counts gate's spec.verdicts for its Bundle against
// spec.verify. author is the version's author (its Bundle's actor),
// bundleUID the Bundle's UID (a verdict for another UID never counts, even
// if the Graph copied it), deadline the end of the wait (VerifyDeadline) and
// now the evaluation time.
func TallyVerdicts(gate *kardinalv1alpha1.PolicyGate, author kardinalv1alpha1.Actor, bundleUID string,
	deadline *time.Time, now time.Time) VerdictTally {
	p := VerifyPolicyOf(gate)
	t := VerdictTally{Policy: p, Deadline: deadline}
	if p == nil {
		return t
	}
	t.Required, t.MaxFailures = p.Required, p.MaxFailures
	if len(gate.Spec.Verdicts) > MaxGateVerdicts {
		t.Held = fmt.Sprintf("more than %d verdicts for check %s; delete the ones that do not count", MaxGateVerdicts, p.Check)
	}
	if author.Principal == "" && (p.ExcludeAuthor == nil || *p.ExcludeAuthor) && t.Held == "" {
		t.Held = "verify.excludeAuthor cannot be checked: the version has no verified author"
	}
	bundle, env := gate.Labels["kardinal.io/bundle"], p.Environment
	verdicts := slices.Clone(gate.Spec.Verdicts)
	// Oldest first, so the last verdict of a voter is its vote.
	sort.SliceStable(verdicts, func(i, j int) bool {
		a, b := verdictTime(verdicts[i]), verdictTime(verdicts[j])
		if !a.Equal(b) {
			return a.Before(b)
		}
		return verdicts[i].Name < verdicts[j].Name
	})
	latest := map[string]int{}
	eligible := make([]bool, len(verdicts))
	records := make([]kardinalv1alpha1.GateVerdictStatus, len(verdicts))
	for i, v := range verdicts {
		a := VerifierActor(v)
		rec := kardinalv1alpha1.GateVerdictStatus{Name: v.Name, Verifier: actor.Describe(a), Result: v.Result,
			Summary: v.Summary, EvidenceURL: v.EvidenceURL, Findings: v.Findings, CreatedAt: v.CreatedAt}
		if v.Run != nil {
			rec.Run, rec.Attempt = v.Run.Index, v.Run.Attempt
		}
		switch {
		case (bundle != "" && v.Bundle != bundle) || (env != "" && v.Environment != env) || v.Check != p.Check:
			rec.Reason = fmt.Sprintf("for bundle %s, %s, check %s: not this gate's", v.Bundle, v.Environment, v.Check)
		case v.BundleUID != bundleUID:
			rec.Reason = "for another version of " + v.Bundle + " (UID " + v.BundleUID + ")"
		default:
			if ok, why := VerdictWriterAllowed(p, a, v.Groups); !ok {
				rec.Reason = why
			} else if why := VerdictPartyBlock(p, author, a); why != "" {
				rec.Reason = "not counted: " + why
			} else if deadline != nil && v.CreatedAt != nil && v.CreatedAt.After(*deadline) {
				rec.Reason = "written after the timeout (" + p.Timeout + ")"
			} else {
				eligible[i] = true
				if prev, ok := latest[voterOf(v, a)]; ok {
					records[prev].Reason = "superseded by " + v.Name
					eligible[prev] = false
				}
				latest[voterOf(v, a)] = i
			}
		}
		records[i] = rec
	}
	for i, v := range verdicts {
		if !eligible[i] || t.Held != "" {
			if t.Held != "" && records[i].Reason == "" {
				records[i].Reason = t.Held
			}
			continue
		}
		a := VerifierActor(v)
		label := verifierLabel(a)
		switch {
		case v.Result == kardinalv1alpha1.VerdictPass:
			records[i].Counted = true
			t.Passed = append(t.Passed, label)
			t.HighFindings += v.HighFindings
		case v.Result == kardinalv1alpha1.VerdictFail ||
			(v.Result == kardinalv1alpha1.VerdictInconclusive && p.OnInconclusive == kardinalv1alpha1.VerifyFail):
			records[i].Counted = true
			if v.Result == kardinalv1alpha1.VerdictInconclusive {
				records[i].Reason = "inconclusive counts as a fail (onInconclusive: fail)"
			}
			t.Failed = append(t.Failed, label+": "+v.Summary)
			t.HighFindings += v.HighFindings
		default:
			t.Inconclusive++
			if p.OnInconclusive == kardinalv1alpha1.VerifyRetry && v.Run != nil && v.Run.Attempt <= p.Retries {
				records[i].Reason = fmt.Sprintf("inconclusive (retrying: attempt %d of %d)", v.Run.Attempt+1, p.Retries+1)
			} else {
				records[i].Reason = "inconclusive (waiting for more verdicts)"
			}
		}
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	t.Records = records
	switch {
	case t.Held != "":
		t.Outcome = VerifyWaiting
	case len(t.Failed) > t.MaxFailures:
		t.Outcome = VerifyFailed
	case len(t.Passed) >= t.Required:
		t.Outcome = VerifyVerified
	case deadline != nil && now.After(*deadline):
		t.Outcome = VerifyTimedOut
	default:
		t.Outcome = VerifyWaiting
	}
	return t
}

func verdictTime(v kardinalv1alpha1.GateVerdict) time.Time {
	if v.CreatedAt == nil {
		return time.Time{}
	}
	return v.CreatedAt.Time
}

// voters is the passing voters as the reason names them: in order, with
// "×N" for repeats ("qa-explorer ×2, alice").
func voters(labels []string) string {
	var order []string
	n := map[string]int{}
	for _, l := range labels {
		if n[l] == 0 {
			order = append(order, l)
		}
		n[l]++
	}
	parts := make([]string, len(order))
	for i, l := range order {
		parts[i] = l
		if n[l] > 1 {
			parts[i] += fmt.Sprintf(" ×%d", n[l])
		}
	}
	return strings.Join(parts, ", ")
}

// count is "<passed> of <required>", with the voters when there are some.
func (t VerdictTally) count() string {
	s := fmt.Sprintf("%d of %d", len(t.Passed), t.Required)
	if len(t.Passed) > 0 {
		s += " (" + voters(t.Passed) + ")"
	}
	return s
}

// Blocked returns why the verify policy holds the gate, or "" when it is
// met (or allowed on timeout) or the gate has no policy.
func (t VerdictTally) Blocked() string {
	if t.Policy == nil {
		return ""
	}
	if t.Held != "" {
		return t.Held
	}
	switch t.Outcome {
	case VerifyFailed:
		msg := "verification failed: " + t.Failed[0]
		if len(t.Failed) > 1 {
			msg += fmt.Sprintf(" (and %d more)", len(t.Failed)-1)
		}
		return msg
	case VerifyVerified:
		return ""
	case VerifyTimedOut:
		if t.Policy.OnTimeout == kardinalv1alpha1.VerifyAllow {
			return ""
		}
		return fmt.Sprintf("verification timed out after %s: %s", t.Policy.Timeout, t.count())
	}
	msg := "waiting for verdicts: " + t.count()
	if t.Inconclusive > 0 {
		what := "waiting"
		if t.Policy.OnInconclusive == kardinalv1alpha1.VerifyRetry {
			what = "retrying"
		}
		msg += fmt.Sprintf("; %d inconclusive, %s", t.Inconclusive, what)
	}
	return msg
}

// Met returns the reason suffix of a gate whose verify policy passes.
func (t VerdictTally) Met() string {
	switch {
	case t.Policy == nil || t.Held != "":
		return ""
	case t.Outcome == VerifyVerified:
		return "verified: " + t.count()
	case t.Outcome == VerifyTimedOut && t.Policy.OnTimeout == kardinalv1alpha1.VerifyAllow:
		return fmt.Sprintf("verification timed out after %s, allowed by onTimeout: %s", t.Policy.Timeout, t.count())
	}
	return ""
}

// Context is the verdicts CEL variable (docs/design/19-verdicts.md,
// section 6.6). A gate without a verify policy gets zeros.
func (t VerdictTally) Context() map[string]interface{} {
	verifiers := make([]interface{}, len(t.Passed))
	for i, v := range t.Passed {
		verifiers[i] = v
	}
	return map[string]interface{}{
		"passed":       int64(len(t.Passed)),
		"failed":       int64(len(t.Failed)),
		"inconclusive": int64(t.Inconclusive),
		"required":     int64(t.Required),
		"verifiers":    verifiers,
		"timedOut":     t.Outcome == VerifyTimedOut,
		"findings":     map[string]interface{}{"high": int64(t.HighFindings)},
	}
}

// VerdictStanding is where a would-be verifier stands with a verify gate.
type VerdictStanding struct {
	// Counts is true when a verdict by the caller would be a vote.
	Counts bool
	// Reason is why it would not ("" when it would).
	Reason string
}

// VerdictStandingOf is whether a verdict by v (with groups) on gate's
// Bundle, written by author, would count. A gate without verify, or one
// already decided, says so.
func VerdictStandingOf(gate *kardinalv1alpha1.PolicyGate, author, v kardinalv1alpha1.Actor, groups []string) VerdictStanding {
	p := VerifyPolicyOf(gate)
	if p == nil {
		return VerdictStanding{Reason: "gate " + gate.Name + " has no verify policy"}
	}
	if ok, why := VerdictWriterAllowed(p, v, groups); !ok {
		return VerdictStanding{Reason: why}
	}
	if why := VerdictPartyBlock(p, author, v); why != "" {
		return VerdictStanding{Reason: why}
	}
	return VerdictStanding{Counts: true}
}
