// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

func analyses(entries ...string) []v1alpha1.LiveAnalysisRun {
	var out []v1alpha1.LiveAnalysisRun
	for i := 0; i+1 < len(entries); i += 2 {
		out = append(out, v1alpha1.LiveAnalysisRun{Name: entries[i], Template: "tmpl-" + entries[i], Phase: entries[i+1], Message: "msg"})
	}
	return out
}

// verifyingStep is a step in Verifying with two analyses (and the hooks
// given), started at started.
func verifyingStep(started time.Time, hooks []string, live *v1alpha1.PromotionStepLive) *v1alpha1.PromotionStep {
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Spec.Analyses = []string{"tmpl-run-a", "tmpl-run-b"}
	ps.Spec.PostHooks = hooks
	ps.Spec.Live = live
	ps.Status.State = promotionstep.StateVerifying
	t := metav1.NewTime(started)
	ps.Status.VerificationStartedAt = &t
	return ps
}

// TestVerifyingAnalyses: the step is Verified when every AnalysisRun is
// Successful (Inconclusive too with inconclusive: pass), applies
// onHealthFailure when one is Failed, Error or Inconclusive, or the analyses
// run past spec.verification.timeout, and waits otherwise.
func TestVerifyingAnalyses(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name         string
		live         []v1alpha1.LiveAnalysisRun
		inconclusive string
		started      time.Time
		timeout      string
		wantState    string
		wantMsg      string
	}{
		{"no mirror yet", nil, "", now, "", "Verifying", "waiting for analysis tmpl-run-a: not started"},
		{"one running", analyses("run-a", "Successful", "run-b", "Running"), "", now, "", "Verifying",
			"waiting for analysis tmpl-run-b (AnalysisRun run-b): Running"},
		{"all successful", analyses("run-a", "Successful", "run-b", "Successful"), "", now, "", "Verified", "verification passed"},
		{"failed", analyses("run-a", "Successful", "run-b", "Failed"), "", now, "", "Failed",
			"analysis tmpl-run-b (AnalysisRun run-b) failed: msg"},
		{"error", analyses("run-a", "Error", "run-b", "Running"), "", now, "", "Failed", "analysis tmpl-run-a (AnalysisRun run-a) error"},
		{"inconclusive fails by default", analyses("run-a", "Inconclusive", "run-b", "Successful"), "", now, "", "Failed", "inconclusive"},
		{"inconclusive passes", analyses("run-a", "Inconclusive", "run-b", "Successful"), "pass", now, "", "Verified", ""},
		{"timed out", analyses("run-a", "Successful", "run-b", "Running"), "", now.Add(-31 * time.Minute), "", "Failed",
			"did not finish within 30m0s"},
		{"custom timeout not reached", analyses("run-a", "Successful", "run-b", "Running"), "", now.Add(-31 * time.Minute), "1h", "Verifying", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pl := makePipeline("p")
			ps := verifyingStep(tc.started, nil, &v1alpha1.PromotionStepLive{Analyses: tc.live})
			ps.Spec.AnalysisPolicy = &v1alpha1.StepAnalysisPolicy{Inconclusive: tc.inconclusive, Timeout: tc.timeout}
			c := newClient(t, ps, pl, makeBundle("b1", "p"))
			got, res := reconcileHookStep(t, c, "step")
			assert.Equal(t, tc.wantState, got.Status.State)
			assert.Contains(t, got.Status.Message, tc.wantMsg)
			if tc.wantState == "Verified" {
				c := meta.FindStatusCondition(got.Status.Conditions, "Verified")
				require.NotNil(t, c)
				assert.Equal(t, "VerificationSucceeded", c.Reason)
			}
			if tc.wantState == "Verifying" {
				assert.Positive(t, res.RequeueAfter)
				again, _ := reconcileHookStep(t, c, "step")
				assert.Equal(t, got.Status, again.Status, "idempotent while waiting")
			}
		})
	}
}

// TestVerifyingHooksAndAnalyses: hooks and analyses both have to pass; a
// failed hook fails the step even while analyses run.
func TestVerifyingHooksAndAnalyses(t *testing.T) {
	now := time.Now()
	pl := makePipeline("p")

	ps := verifyingStep(now, []string{"e2e"}, &v1alpha1.PromotionStepLive{
		Hooks:    []v1alpha1.LiveHookRun{{Name: "e2e", Result: "Running"}},
		Analyses: analyses("run-a", "Successful", "run-b", "Successful"),
	})
	c := newClient(t, ps, pl, makeBundle("b1", "p"))
	got, _ := reconcileHookStep(t, c, "step")
	assert.Equal(t, promotionstep.StateVerifying, got.Status.State)
	assert.Contains(t, got.Status.Message, "waiting for post-deploy hook e2e")

	got.Spec.Live.Hooks[0].Result = "Failed"
	require.NoError(t, c.Update(context.Background(), got))
	got, _ = reconcileHookStep(t, c, "step")
	assert.Equal(t, "Failed", got.Status.State)
	assert.Contains(t, got.Status.Message, "post-deploy hook e2e failed")
}

// TestHealthPassEntersVerifyingForAnalyses: a step with analyses and no
// hooks goes from HealthChecking to Verifying.
func TestHealthPassEntersVerifyingForAnalyses(t *testing.T) {
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Spec.Analyses = []string{"tmpl-a"}
	ps.Status.State = "HealthChecking"
	c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
	got, _ := reconcileHookStep(t, c, "step")
	assert.Equal(t, promotionstep.StateVerifying, got.Status.State)
	assert.NotNil(t, got.Status.VerificationStartedAt)
	assert.Contains(t, got.Status.Message, "running 1 analysis(es): tmpl-a")
}

// TestVerifyingNewestRunWins: when a translation replaced a template's run
// (the template changed), the step waits for the newest run, says so, and
// keeps its timeout (regression, QA #1502).
func TestVerifyingNewestRunWins(t *testing.T) {
	started := time.Now().Add(-5 * time.Minute)
	live := &v1alpha1.PromotionStepLive{Analyses: []v1alpha1.LiveAnalysisRun{
		{Name: "old", Template: "tmpl-run-a", Created: "2026-10-09T10:00:00Z", Phase: "Failed"},
		{Name: "new", Template: "tmpl-run-a", Created: "2026-10-09T10:05:00Z", Phase: "Running"},
		{Name: "b", Template: "tmpl-run-b", Created: "2026-10-09T10:00:00Z", Phase: "Successful"},
	}}
	ps := verifyingStep(started, nil, live)
	c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
	got, _ := reconcileHookStep(t, c, "step")
	assert.Equal(t, promotionstep.StateVerifying, got.Status.State, "the replaced Failed run does not fail the step")
	assert.Contains(t, got.Status.Message, "AnalysisRun new")
	assert.Contains(t, got.Status.Message, "was replaced after its template changed")
	assert.Equal(t, ps.Status.VerificationStartedAt.Unix(), got.Status.VerificationStartedAt.Unix(), "the timeout is not restarted")

	got.Spec.Live.Analyses[1].Phase = "Successful"
	require.NoError(t, c.Update(context.Background(), got))
	got, _ = reconcileHookStep(t, c, "step")
	assert.Equal(t, "Verified", got.Status.State)
}

// TestVerifyingPolicyFromStep: the verdict policy is the step's snapshot,
// not the Pipeline's current one (regression, QA #1502).
func TestVerifyingPolicyFromStep(t *testing.T) {
	pl := makePipeline("p")
	pl.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "tmpl-run-a"}}, Inconclusive: "pass"}
	ps := verifyingStep(time.Now(), nil, &v1alpha1.PromotionStepLive{Analyses: analyses("run-a", "Inconclusive", "run-b", "Successful")})
	ps.Spec.AnalysisPolicy = &v1alpha1.StepAnalysisPolicy{Inconclusive: "fail"}
	c := newClient(t, ps, pl, makeBundle("b1", "p"))
	got, _ := reconcileHookStep(t, c, "step")
	assert.Equal(t, "Failed", got.Status.State, "the step's snapshot says fail")
}
