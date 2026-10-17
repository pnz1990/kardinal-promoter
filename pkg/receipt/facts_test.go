// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestGateFacts (QA-3): the level facts of a set of gates, each on its own:
// passed only when there are gates and every one is ready, approved only
// by a counted approve, verified only by a counted passing Verdict, and
// overridden by any override.
func TestGateFacts(t *testing.T) {
	ready := v1alpha1.DecisionGate{Name: "g", Ready: true}
	with := func(f func(*v1alpha1.DecisionGate)) v1alpha1.DecisionGate {
		g := ready
		f(&g)
		return g
	}
	cases := map[string]struct {
		gates []v1alpha1.DecisionGate
		want  v1alpha1.DecisionFacts
	}{
		"no gates":  {nil, v1alpha1.DecisionFacts{}},
		"one ready": {[]v1alpha1.DecisionGate{ready}, v1alpha1.DecisionFacts{Gates: 1, Passed: true}},
		"one not ready": {[]v1alpha1.DecisionGate{ready, with(func(g *v1alpha1.DecisionGate) { g.Ready = false })},
			v1alpha1.DecisionFacts{Gates: 2}},
		"a counted approve": {[]v1alpha1.DecisionGate{with(func(g *v1alpha1.DecisionGate) {
			g.Approvals = []v1alpha1.GateApprovalStatus{{User: "a", Decision: "approve", Counted: true}}
		})}, v1alpha1.DecisionFacts{Gates: 1, Passed: true, Approved: true}},
		"an uncounted approve, a counted reject": {[]v1alpha1.DecisionGate{with(func(g *v1alpha1.DecisionGate) {
			g.Approvals = []v1alpha1.GateApprovalStatus{{User: "a", Decision: "approve"}, {User: "b", Decision: "reject", Counted: true}}
		})}, v1alpha1.DecisionFacts{Gates: 1, Passed: true}},
		"a counted passing Verdict": {[]v1alpha1.DecisionGate{with(func(g *v1alpha1.DecisionGate) {
			g.Verdicts = []v1alpha1.GateVerdictStatus{{Name: "v", Result: "pass", Counted: true}}
		})}, v1alpha1.DecisionFacts{Gates: 1, Passed: true, VerifiedByAgent: true}},
		"an uncounted pass, a counted fail": {[]v1alpha1.DecisionGate{with(func(g *v1alpha1.DecisionGate) {
			g.Verdicts = []v1alpha1.GateVerdictStatus{{Name: "v", Result: "pass"}, {Name: "w", Result: "fail", Counted: true}}
		})}, v1alpha1.DecisionFacts{Gates: 1, Passed: true}},
		"an override on a later gate": {[]v1alpha1.DecisionGate{ready, with(func(g *v1alpha1.DecisionGate) {
			g.Override = &v1alpha1.DecisionOverride{CreatedBy: "carol"}
		})}, v1alpha1.DecisionFacts{Gates: 2, Passed: true, Overridden: true}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, GateFacts(tc.gates))
		})
	}
}

// TestLevels_Facts (QA-3): with the snapshot's facts, the gate levels are
// the facts', whatever gates the record still holds (a cut record can hold
// none of the gate that was overridden); without facts (an older
// controller's snapshot) they come from the record's gates. HEALTHY is the
// record's either way.
func TestLevels_Facts(t *testing.T) {
	healthy := Checks{Health: &Health{Adapter: "argocd", Result: "Healthy"}}
	cut := Record{Gates: []Gate{{Name: "kept", Result: "pass"}}, Checks: healthy}
	cases := map[string]struct {
		rec   Record
		facts *v1alpha1.DecisionFacts
		want  []string
	}{
		"facts over a cut record": {cut, &v1alpha1.DecisionFacts{Gates: 70, Passed: true, Approved: true, Overridden: true, VerifiedByAgent: true},
			[]string{LevelGatesPassed, LevelApproved, LevelVerifiedByAgent, LevelHealthy, LevelOverridden}},
		"facts say not passed": {cut, &v1alpha1.DecisionFacts{Gates: 2},
			[]string{LevelHealthy}},
		"no facts: from the gates": {Record{Gates: []Gate{{Name: "a", Result: "pass", Override: &Override{CreatedBy: "carol"},
			Approvals: []Approval{{Decision: "approve", Counted: true}}, Verdicts: []Verdict{{Result: "pass", Counted: true}}}}},
			nil, []string{LevelGatesPassed, LevelApproved, LevelVerifiedByAgent, LevelOverridden}},
		"no facts, a failed gate": {Record{Gates: []Gate{{Name: "a", Result: "pass"}, {Name: "b", Result: "fail"}}, Checks: healthy},
			nil, []string{LevelHealthy}},
		"no facts, no gates": {Record{}, nil, []string{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, levels(tc.rec, tc.facts))
		})
	}
}
