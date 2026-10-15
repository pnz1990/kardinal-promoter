// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

var (
	vt0     = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	alice   = v1.Actor{Kind: v1.ActorHuman, Principal: "alice"}
	bob     = v1.Actor{Kind: v1.ActorHuman, Principal: "bob"}
	fixer   = v1.Actor{Kind: v1.ActorAgent, Principal: "system:serviceaccount:ci:fixer", Agent: "fixer", Sponsor: "alice"}
	qa      = v1.Actor{Kind: v1.ActorAgent, Principal: "system:serviceaccount:payments:qa", Agent: "qa", Sponsor: "bob"}
	aliceQA = v1.Actor{Kind: v1.ActorAgent, Principal: "system:serviceaccount:payments:alice-qa", Agent: "alice-qa", Sponsor: "alice"}
)

func verdict(name string, by v1.Actor, result string, at time.Duration, run, attempt int) v1.GateVerdict {
	ts := metav1.NewTime(vt0.Add(at))
	a := by
	v := v1.GateVerdict{Name: name, CreatedAt: &ts, Bundle: "b1", BundleUID: "uid-1", Environment: "uat",
		Check: "qa", Result: result, Summary: name + " summary", By: by.Principal, Actor: &a}
	if run > 0 {
		v.Run = &v1.VerdictRun{Name: name, Index: run, Attempt: attempt}
	}
	return v
}

func verifyGate(p v1.GateVerifyPolicy, verdicts ...v1.GateVerdict) *v1.PolicyGate {
	if p.Check == "" {
		p.Check = "qa"
	}
	p.Environment = "uat"
	return &v1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "g", Labels: map[string]string{"kardinal.io/bundle": "b1", "kardinal.io/environment": "prod"}},
		Spec:       v1.PolicyGateSpec{Expression: "true", Verify: &p, Verdicts: verdicts},
	}
}

func TestTallyVerdicts(t *testing.T) {
	deadline := vt0.Add(time.Hour)
	after := vt0.Add(2 * time.Hour)
	tests := []struct {
		name     string
		policy   v1.GateVerifyPolicy
		author   v1.Actor
		verdicts []v1.GateVerdict
		deadline *time.Time
		now      time.Time
		outcome  string
		blocked  string
		met      string
		counted  map[string]bool
		reasons  map[string]string
	}{
		{
			name:   "2 of 3 runs with one fail tolerated",
			policy: v1.GateVerifyPolicy{Required: 2, MaxFailures: 1, AllowedAgents: []string{"qa"}},
			author: alice,
			verdicts: []v1.GateVerdict{verdict("r1", qa, "pass", 1, 1, 1), verdict("r2", qa, "fail", 2, 2, 1),
				verdict("r3", qa, "pass", 3, 3, 1)},
			outcome: lifecycle.VerifyVerified,
			met:     "verified: 2 of 2 (qa ×2)",
			counted: map[string]bool{"r1": true, "r2": true, "r3": true},
		},
		{
			name:     "a fail blocks by default",
			policy:   v1.GateVerifyPolicy{Required: 2, AllowedAgents: []string{"qa"}},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("r1", qa, "pass", 1, 1, 1), verdict("r2", qa, "fail", 2, 2, 1)},
			outcome:  lifecycle.VerifyFailed,
			blocked:  "verification failed: qa: r2 summary",
		},
		{
			name:     "a fail wins over a met quorum",
			policy:   v1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{"qa"}},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("r1", qa, "pass", 1, 1, 1), verdict("r2", qa, "fail", 2, 2, 1)},
			outcome:  lifecycle.VerifyFailed,
			blocked:  "verification failed: qa: r2 summary",
		},
		{
			name:   "inconclusive then a retried pass",
			policy: v1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{"qa"}, OnInconclusive: "retry", Retries: 1},
			author: alice,
			verdicts: []v1.GateVerdict{verdict("r1-a1", qa, "inconclusive", 1, 1, 1),
				verdict("r1-a2", qa, "pass", 2, 1, 2)},
			outcome: lifecycle.VerifyVerified,
			counted: map[string]bool{"r1-a1": false, "r1-a2": true},
			reasons: map[string]string{"r1-a1": "superseded by r1-a2"},
		},
		{
			name:     "inconclusive waits and says it retries",
			policy:   v1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{"qa"}, OnInconclusive: "retry", Retries: 1},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("r1-a1", qa, "inconclusive", 1, 1, 1)},
			outcome:  lifecycle.VerifyWaiting,
			blocked:  "waiting for verdicts: 0 of 1; 1 inconclusive, retrying",
			reasons:  map[string]string{"r1-a1": "inconclusive (retrying: attempt 2 of 2)"},
		},
		{
			name:     "inconclusive counts as a fail with onInconclusive fail",
			policy:   v1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{"qa"}, OnInconclusive: "fail"},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("r1", qa, "inconclusive", 1, 1, 1)},
			outcome:  lifecycle.VerifyFailed,
		},
		{
			name:     "the author agent's own verdict is refused",
			policy:   v1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{"fixer"}},
			author:   fixer,
			verdicts: []v1.GateVerdict{verdict("v1", fixer, "pass", 1, 0, 0)},
			outcome:  lifecycle.VerifyWaiting,
			blocked:  "waiting for verdicts: 0 of 1",
			reasons:  map[string]string{"v1": "not counted: the author's party (agent fixer for alice)"},
		},
		{
			name:     "the sponsor's own agent is refused, even without excludeAuthor",
			policy:   v1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{"alice-qa"}, ExcludeAuthor: ptr(false)},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("v1", aliceQA, "pass", 1, 0, 0)},
			outcome:  lifecycle.VerifyWaiting,
			reasons:  map[string]string{"v1": "not counted: the author's party (alice)"},
		},
		{
			name:     "an agent with the same sponsor as the author agent is refused",
			policy:   v1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{"alice-qa"}},
			author:   fixer,
			verdicts: []v1.GateVerdict{verdict("v1", aliceQA, "pass", 1, 0, 0)},
			outcome:  lifecycle.VerifyWaiting,
		},
		{
			name:     "the author person's own verdict is refused with excludeAuthor",
			policy:   v1.GateVerifyPolicy{Required: 1},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("v1", alice, "pass", 1, 0, 0)},
			outcome:  lifecycle.VerifyWaiting,
		},
		{
			name:     "the author person counts without excludeAuthor",
			policy:   v1.GateVerifyPolicy{Required: 1, ExcludeAuthor: ptr(false)},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("v1", alice, "pass", 1, 0, 0)},
			outcome:  lifecycle.VerifyVerified,
		},
		{
			name:     "an agent not in allowedAgents does not count",
			policy:   v1.GateVerifyPolicy{Required: 1},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("v1", qa, "pass", 1, 0, 0)},
			outcome:  lifecycle.VerifyWaiting,
			reasons:  map[string]string{"v1": "agent qa is not in verify.allowedAgents"},
		},
		{
			name:     "a person not in allowedUsers does not count",
			policy:   v1.GateVerifyPolicy{Required: 1, AllowedUsers: []string{"carol"}},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("v1", bob, "pass", 1, 0, 0)},
			outcome:  lifecycle.VerifyWaiting,
		},
		{
			name:   "a person and an agent make two votes",
			policy: v1.GateVerifyPolicy{Required: 2, AllowedAgents: []string{"qa"}},
			author: alice,
			verdicts: []v1.GateVerdict{verdict("v1", bob, "pass", 1, 0, 0),
				verdict("v2", v1.Actor{Kind: v1.ActorAgent, Principal: "sa:q2", Agent: "qa", Sponsor: "carol"}, "pass", 2, 1, 1)},
			outcome: lifecycle.VerifyVerified,
			met:     "verified: 2 of 2 (bob, qa)",
		},
		{
			name:     "an external voter votes once, its latest verdict wins",
			policy:   v1.GateVerifyPolicy{Required: 2},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("v1", bob, "fail", 1, 0, 0), verdict("v2", bob, "pass", 2, 0, 0)},
			outcome:  lifecycle.VerifyWaiting,
			blocked:  "waiting for verdicts: 1 of 2 (bob)",
			reasons:  map[string]string{"v1": "superseded by v2"},
		},
		{
			name:     "a verdict for another version's UID does not count",
			policy:   v1.GateVerifyPolicy{Required: 1},
			author:   alice,
			verdicts: []v1.GateVerdict{func() v1.GateVerdict { v := verdict("v1", bob, "pass", 1, 0, 0); v.BundleUID = "uid-0"; return v }()},
			outcome:  lifecycle.VerifyWaiting,
			reasons:  map[string]string{"v1": "for another version of b1 (UID uid-0)"},
		},
		{
			name:     "a verdict for another bundle does not count",
			policy:   v1.GateVerifyPolicy{Required: 1},
			author:   alice,
			verdicts: []v1.GateVerdict{func() v1.GateVerdict { v := verdict("v1", bob, "pass", 1, 0, 0); v.Bundle = "b0"; return v }()},
			outcome:  lifecycle.VerifyWaiting,
		},
		{
			name:     "timeout fails",
			policy:   v1.GateVerifyPolicy{Required: 2, Timeout: "1h"},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("v1", bob, "pass", 1, 0, 0)},
			deadline: &deadline,
			now:      after,
			outcome:  lifecycle.VerifyTimedOut,
			blocked:  "verification timed out after 1h: 1 of 2 (bob)",
		},
		{
			name:     "timeout allows when configured",
			policy:   v1.GateVerifyPolicy{Required: 2, Timeout: "1h", OnTimeout: "allow"},
			author:   alice,
			deadline: &deadline,
			now:      after,
			outcome:  lifecycle.VerifyTimedOut,
			met:      "verification timed out after 1h, allowed by onTimeout: 0 of 2",
		},
		{
			name:     "a verdict after the timeout does not count",
			policy:   v1.GateVerifyPolicy{Required: 1, Timeout: "1h"},
			author:   alice,
			verdicts: []v1.GateVerdict{verdict("v1", bob, "pass", 90*time.Minute, 0, 0)},
			deadline: &deadline,
			now:      after,
			outcome:  lifecycle.VerifyTimedOut,
			reasons:  map[string]string{"v1": "written after the timeout (1h)"},
		},
		{
			name:     "before the deadline the gate waits",
			policy:   v1.GateVerifyPolicy{Required: 1, Timeout: "1h"},
			author:   alice,
			deadline: &deadline,
			now:      vt0.Add(time.Minute),
			outcome:  lifecycle.VerifyWaiting,
		},
		{
			name:     "an unknown author holds an excludeAuthor gate",
			policy:   v1.GateVerifyPolicy{Required: 1},
			author:   v1.Actor{Kind: v1.ActorUnknown},
			verdicts: []v1.GateVerdict{verdict("v1", bob, "pass", 1, 0, 0)},
			outcome:  lifecycle.VerifyWaiting,
			blocked:  "verify.excludeAuthor cannot be checked: the version has no verified author",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := tc.now
			if now.IsZero() {
				now = vt0
			}
			tally := lifecycle.TallyVerdicts(verifyGate(tc.policy, tc.verdicts...), tc.author, "uid-1", tc.deadline, now)
			assert.Equal(t, tc.outcome, tally.Outcome)
			if tc.blocked != "" {
				assert.Equal(t, tc.blocked, tally.Blocked())
			}
			if tc.met != "" {
				assert.Empty(t, tally.Blocked())
				assert.Equal(t, tc.met, tally.Met())
			}
			for name, want := range tc.counted {
				assert.Equal(t, want, record(t, tally, name).Counted, name)
			}
			for name, want := range tc.reasons {
				assert.Equal(t, want, record(t, tally, name).Reason, name)
			}
		})
	}
}

func TestTallyVerdictsTooMany(t *testing.T) {
	var vs []v1.GateVerdict
	for i := range lifecycle.MaxGateVerdicts + 1 {
		vs = append(vs, verdict(fmt.Sprintf("v%03d", i), bob, "pass", time.Duration(i), 0, 0))
	}
	tally := lifecycle.TallyVerdicts(verifyGate(v1.GateVerifyPolicy{Required: 1}, vs...), alice, "uid-1", nil, vt0)
	assert.Contains(t, tally.Blocked(), "more than 100 verdicts")
	assert.Empty(t, tally.Met())
}

func TestTallyVerdictsTieOnCreatedAt(t *testing.T) {
	// Same timestamp: the name orders them, so the vote is deterministic.
	a, b := verdict("va", bob, "fail", 1, 0, 0), verdict("vb", bob, "pass", 1, 0, 0)
	for _, order := range [][]v1.GateVerdict{{a, b}, {b, a}} {
		tally := lifecycle.TallyVerdicts(verifyGate(v1.GateVerifyPolicy{Required: 1}, order...), alice, "uid-1", nil, vt0)
		assert.Equal(t, lifecycle.VerifyVerified, tally.Outcome)
	}
}

func TestVerdictContext(t *testing.T) {
	v := verdict("v1", bob, "pass", 1, 0, 0)
	v.HighFindings = 2
	ctx := lifecycle.TallyVerdicts(verifyGate(v1.GateVerifyPolicy{Required: 2}, v), alice, "uid-1", nil, vt0).Context()
	assert.Equal(t, int64(1), ctx["passed"])
	assert.Equal(t, int64(2), ctx["required"])
	assert.Equal(t, []interface{}{"bob"}, ctx["verifiers"])
	assert.Equal(t, int64(2), ctx["findings"].(map[string]interface{})["high"])
	none := lifecycle.TallyVerdicts(&v1.PolicyGate{}, alice, "uid-1", nil, vt0)
	assert.Equal(t, int64(0), none.Context()["required"])
	assert.Empty(t, none.Blocked())
}

func TestVerdictStandingOf(t *testing.T) {
	g := verifyGate(v1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{"qa", "fixer"}})
	assert.True(t, lifecycle.VerdictStandingOf(g, alice, qa, nil).Counts)
	s := lifecycle.VerdictStandingOf(g, fixer, fixer, nil)
	assert.False(t, s.Counts)
	assert.Contains(t, s.Reason, "the author's party")
	assert.False(t, lifecycle.VerdictStandingOf(&v1.PolicyGate{}, alice, qa, nil).Counts)
}

func TestVerifyDeadline(t *testing.T) {
	p := &v1.GateVerifyPolicy{Timeout: "30m"}
	require.Nil(t, lifecycle.VerifyDeadline(p, nil))
	d := lifecycle.VerifyDeadline(p, &vt0)
	require.NotNil(t, d)
	assert.Equal(t, vt0.Add(30*time.Minute), *d)
	assert.Nil(t, lifecycle.VerifyDeadline(&v1.GateVerifyPolicy{}, &vt0))
}

func record(t *testing.T, tally lifecycle.VerdictTally, name string) v1.GateVerdictStatus {
	t.Helper()
	for _, r := range tally.Records {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no record %s", name)
	return v1.GateVerdictStatus{}
}

func ptr[T any](v T) *T { return &v }
