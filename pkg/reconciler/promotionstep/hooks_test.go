// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

func live(results ...string) *v1alpha1.PromotionStepLive {
	l := &v1alpha1.PromotionStepLive{}
	for i := 0; i+1 < len(results); i += 2 {
		l.Hooks = append(l.Hooks, v1alpha1.LiveHookRun{Name: results[i], Result: results[i+1], Message: "msg-" + results[i]})
	}
	return l
}

func reconcileHookStep(t *testing.T, c client.Client, name string) (*v1alpha1.PromotionStep, ctrl.Result) {
	t.Helper()
	r := &promotionstep.Reconciler{Client: c, SCM: &noopSCM{}, GitClient: &noopGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}})
	require.NoError(t, err)
	var ps v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &ps))
	return &ps, res
}

// TestPreHooksHoldPending: a step with pre-deploy hooks starts only once
// every hook succeeded in spec.live.hooks (its own spec, written by the
// Graph's mirror), and fails at once when one failed. It never reads a
// HookRun.
func TestPreHooksHoldPending(t *testing.T) {
	cases := []struct {
		name      string
		live      *v1alpha1.PromotionStepLive
		wantState string
		wantMsg   string
	}{
		{"no mirror yet", nil, "", "waiting for pre-deploy hook migrate: not started"},
		{"first running", live("migrate", "Running"), "", "waiting for pre-deploy hook migrate: Running"},
		{"second not created", live("migrate", "Succeeded"), "", "waiting for pre-deploy hook seed: not started"},
		{"second pending", live("migrate", "Succeeded", "seed", "Pending"), "", "waiting for pre-deploy hook seed: Pending"},
		{"first failed", live("migrate", "Failed"), "Failed", "pre-deploy hook migrate failed: msg-migrate"},
		{"second failed", live("migrate", "Succeeded", "seed", "Failed"), "Failed", "pre-deploy hook seed failed"},
		{"all succeeded", live("seed", "Succeeded", "migrate", "Succeeded"), "Promoting", "initialized"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := labelled(makeStep("step", "p", "b1", "test"))
			ps.Spec.PreHooks = []string{"migrate", "seed"}
			ps.Spec.Live = tc.live
			c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
			got, res := reconcileHookStep(t, c, "step")
			assert.Equal(t, tc.wantState, got.Status.State)
			assert.Contains(t, got.Status.Message, tc.wantMsg)
			if tc.wantState == "" {
				assert.Positive(t, res.RequeueAfter, "a held step requeues as a fallback to the mirror's wake-up")
				// Idempotent: a second reconcile holds the same way.
				again, _ := reconcileHookStep(t, c, "step")
				assert.Equal(t, got.Status, again.Status)
			}
		})
	}
}

// TestPostHooksVerifying: a step with post-deploy hooks goes from a passed
// health check to Verifying (status.verificationStartedAt, which the Graph
// gates the post HookRuns on), and is Verified only when every post hook
// succeeded. A failed post hook applies onHealthFailure.
func TestPostHooksVerifying(t *testing.T) {
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Spec.PostHooks = []string{"smoke", "e2e"}
	ps.Status.State = "HealthChecking"
	c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))

	got, _ := reconcileHookStep(t, c, "step")
	require.Equal(t, promotionstep.StateVerifying, got.Status.State)
	require.NotNil(t, got.Status.VerificationStartedAt)
	started := got.Status.VerificationStartedAt.DeepCopy()
	assert.Contains(t, got.Status.Message, "running 2 post-deploy hook(s): smoke, e2e")
	assert.False(t, meta.IsStatusConditionTrue(got.Status.Conditions, "Verified"))

	// Waiting: the state holds and verificationStartedAt is set once.
	got.Spec.Live = live("smoke", "Succeeded", "e2e", "Running")
	require.NoError(t, c.Update(context.Background(), got))
	got, _ = reconcileHookStep(t, c, "step")
	assert.Equal(t, promotionstep.StateVerifying, got.Status.State)
	assert.Equal(t, "waiting for post-deploy hook e2e: Running", got.Status.Message)
	assert.True(t, started.Equal(got.Status.VerificationStartedAt))

	got.Spec.Live = live("smoke", "Succeeded", "e2e", "Succeeded")
	require.NoError(t, c.Update(context.Background(), got))
	got, _ = reconcileHookStep(t, c, "step")
	assert.Equal(t, "Verified", got.Status.State)
	c2 := meta.FindStatusCondition(got.Status.Conditions, "Verified")
	require.NotNil(t, c2)
	assert.Equal(t, "PostHooksSucceeded", c2.Reason)
	assert.Contains(t, auditActions(t, c), "PromotionSucceeded")
}

// TestPostHookFailureAppliesPolicy: a failed post hook is a health failure:
// onHealthFailure none fails the step, abort stops it for a human.
func TestPostHookFailureAppliesPolicy(t *testing.T) {
	cases := []struct {
		policy, want string
	}{
		{"", "Failed"},
		{"none", "Failed"},
		{"abort", "AbortedByAlarm"},
	}
	for _, tc := range cases {
		t.Run("onHealthFailure="+tc.policy, func(t *testing.T) {
			pl := makePipeline("p")
			pl.Spec.Environments[0].OnHealthFailure = tc.policy
			ps := labelled(makeStep("step", "p", "b1", "test"))
			ps.Spec.PostHooks = []string{"smoke"}
			ps.Spec.Live = live("smoke", "Failed")
			ps.Status.State = promotionstep.StateVerifying
			c := newClient(t, ps, pl, makeBundle("b1", "p"))
			got, _ := reconcileHookStep(t, c, "step")
			assert.Equal(t, tc.want, got.Status.State)
			assert.Contains(t, got.Status.Message, "post-deploy hook smoke failed: msg-smoke")
		})
	}
}

// TestHookMessagesNameTheHook: with the hook's Pipeline name in the mirror,
// messages name it and its HookRun.
func TestHookMessagesNameTheHook(t *testing.T) {
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Spec.PreHooks = []string{"p-b1-test-pre-migrate"}
	ps.Spec.Live = &v1alpha1.PromotionStepLive{Hooks: []v1alpha1.LiveHookRun{{
		Name: "p-b1-test-pre-migrate", Hook: "migrate", Phase: "pre", Result: "Failed", Message: "exit 3"}}}
	c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
	got, _ := reconcileHookStep(t, c, "step")
	assert.Equal(t, "Failed", got.Status.State)
	assert.Equal(t, "pre-deploy hook migrate (HookRun p-b1-test-pre-migrate) failed: exit 3", got.Status.Message)
}

// TestStepWithoutPostHooksSkipsVerifying: a passed health check verifies a
// step without post hooks directly, as before.
func TestStepWithoutPostHooksSkipsVerifying(t *testing.T) {
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Status.State = "HealthChecking"
	c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
	got, _ := reconcileHookStep(t, c, "step")
	assert.Equal(t, "Verified", got.Status.State)
	assert.Nil(t, got.Status.VerificationStartedAt)
}

// TestVerifyingStepSuperseded: Verifying is cancellable; a superseded
// Bundle's step does not turn Verified when its hooks finish later.
func TestVerifyingStepSuperseded(t *testing.T) {
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Spec.PostHooks = []string{"smoke"}
	ps.Status.State = promotionstep.StateVerifying
	b := makeBundle("b1", "p")
	b.Status.Phase = "Superseded"
	c := newClient(t, ps, makePipeline("p"), b)
	got, _ := reconcileHookStep(t, c, "step")
	assert.Equal(t, "Failed", got.Status.State)
	assert.Contains(t, got.Status.Message, "superseded")
}

// TestHooksSkippedRecorded: a hook its HookRun Skipped (added to the
// Pipeline after the step started) is noted on the step as condition
// HooksSkipped, in any state, and does not block it (regression, QA #1493).
func TestHooksSkippedRecorded(t *testing.T) {
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Status.State = "WaitingForMerge"
	ps.Spec.Live = &v1alpha1.PromotionStepLive{Hooks: []v1alpha1.LiveHookRun{{
		Name: "p-b1-test-pre-migrate-1234", Hook: "migrate", Phase: "pre", Result: "Skipped"}}}
	c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
	got, _ := reconcileHookStep(t, c, "step")
	cond := meta.FindStatusCondition(got.Status.Conditions, promotionstep.ConditionHooksSkipped)
	require.NotNil(t, cond)
	assert.Contains(t, cond.Message, "pre-deploy hook migrate")

	pending := labelled(makeStep("step2", "p", "b1", "test"))
	pending.Spec.PreHooks = []string{"a"}
	pending.Spec.Live = &v1alpha1.PromotionStepLive{Hooks: []v1alpha1.LiveHookRun{{Name: "a", Result: "Skipped"}}}
	c2 := newClient(t, pending, makePipeline("p"), makeBundle("b1", "p"))
	got2, _ := reconcileHookStep(t, c2, "step2")
	assert.Equal(t, "Promoting", got2.Status.State, "a Skipped pre hook does not hold the step")
}

// TestVerifyingEventText: entering Verifying records an Event that says the
// step verifies with post-deploy hooks and analyses, not hooks only (QA
// #1502 round 3).
func TestVerifyingEventText(t *testing.T) {
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Spec.Analyses = []string{"smoke"}
	ps.Status.State = "HealthChecking"
	c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
	rec := events.NewFakeRecorder(10)
	r := &promotionstep.Reconciler{Client: c, SCM: &noopSCM{}, GitClient: &noopGit{}, Recorder: rec,
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "step", Namespace: "default"}})
	require.NoError(t, err)
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step", Namespace: "default"}, &got))
	require.Equal(t, promotionstep.StateVerifying, got.Status.State)
	evs := drain(rec)
	assert.Contains(t, strings.Join(evs, "\n"), "env test: health check passed, verifying (post-deploy hooks and analyses)", "%v", evs)
	assert.NotContains(t, strings.Join(evs, "\n"), "running post-deploy hooks")
}

// TestHookRecordsKept: the step records on its own status each hook whose
// HookRun started (hook, phase, spec hash, result), updates it as the run
// finishes, and never changes a final result for the same spec hash, so a
// HookRun the Graph applies again takes the recorded result (regression,
// #1544 review: a deleted running pre-hook HookRun ran its migration twice).
func TestHookRecordsKept(t *testing.T) {
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Spec.PreHooks = []string{"hr-migrate"}
	ps.Spec.Live = &v1alpha1.PromotionStepLive{Hooks: []v1alpha1.LiveHookRun{
		{Name: "hr-migrate", Hook: "migrate", Phase: "pre", Result: "Running", SpecHash: "h1", Message: "Job running"},
		{Name: "hr-seed", Hook: "seed", Phase: "pre", Result: "Pending"},
	}}
	c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
	got, _ := reconcileHookStep(t, c, "step")
	assert.Equal(t, []v1alpha1.HookRecord{{Hook: "migrate", Phase: "pre", SpecHash: "h1", Result: "Running", Message: "Job running"}},
		got.Status.HookRecords, "a started run is recorded; a Pending one is not")

	got.Spec.Live.Hooks[0].Result, got.Spec.Live.Hooks[0].Message = "Succeeded", "Job completed"
	require.NoError(t, c.Update(context.Background(), got))
	got, _ = reconcileHookStep(t, c, "step")
	require.Len(t, got.Status.HookRecords, 1)
	assert.Equal(t, "Succeeded", got.Status.HookRecords[0].Result)

	// The recreated HookRun reports the recorded result, or (a buggy
	// report) Failed for the same job: the final record stays.
	got.Spec.Live.Hooks[0].Result = "Failed"
	require.NoError(t, c.Update(context.Background(), got))
	got, _ = reconcileHookStep(t, c, "step")
	assert.Equal(t, "Succeeded", got.Status.HookRecords[0].Result, "a final result is not changed for the same spec hash")

	// An edited hook (another spec hash) that runs is recorded anew.
	got.Spec.Live.Hooks[0].SpecHash, got.Spec.Live.Hooks[0].Result = "h2", "Running"
	require.NoError(t, c.Update(context.Background(), got))
	got, _ = reconcileHookStep(t, c, "step")
	assert.Equal(t, "h2", got.Status.HookRecords[0].SpecHash)
	assert.Equal(t, "Running", got.Status.HookRecords[0].Result)
}
