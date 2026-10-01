//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// pausedMsg is the message of a step a paused pipeline holds.
const pausedMsg = "pipeline " + pipelineName + " is paused — resume with: kardinal resume " + pipelineName

// Held steps re-check the freeze gate once a minute, so a resume reaches them
// within resumeTimeout.
const resumeTimeout = 90 * time.Second

// getPipeline reads the test's Pipeline.
func getPipeline(t *testing.T, a *app) *v1alpha1.Pipeline {
	t.Helper()
	var p v1alpha1.Pipeline
	require.NoError(t, a.e.Client.Get(context.Background(), client.ObjectKey{Namespace: a.ns, Name: pipelineName}, &p))
	return &p
}

// assertFreezeGate checks that g is kardinal's freeze gate of p: labelled as
// a system freeze gate, never true, and owned by the Pipeline so deleting the
// Pipeline deletes it.
func assertFreezeGate(t *testing.T, g *v1alpha1.PolicyGate, p *v1alpha1.Pipeline) {
	t.Helper()
	assert.Equal(t, map[string]string{
		"kardinal.io/pipeline": p.Name, "kardinal.io/scope": "system", "kardinal.io/freeze": "true",
	}, g.Labels)
	assert.Equal(t, "false", g.Spec.Expression)
	assert.Equal(t, "Pipeline "+p.Name+" is paused — resume with: kardinal resume "+p.Name, g.Spec.Message)
	owner := metav1.GetControllerOf(g)
	if assert.NotNil(t, owner, "the freeze gate has a controller owner") {
		assert.Equal(t, "Pipeline", owner.Kind)
		assert.Equal(t, p.UID, owner.UID)
	}
}

// pausedCondition matches a Pipeline whose Paused condition says the freeze
// gate holds it.
func pausedCondition(p *v1alpha1.Pipeline) bool {
	c := meta.FindStatusCondition(p.Status.Conditions, "Paused")
	return c != nil && c.Status == metav1.ConditionTrue && c.Reason == "FreezeGateActive" &&
		c.Message == "new promotions are held by PolicyGate freeze-"+p.Name
}

// noPausedCondition matches a Pipeline without a Paused condition.
func noPausedCondition(p *v1alpha1.Pipeline) bool {
	return meta.FindStatusCondition(p.Status.Conditions, "Paused") == nil
}

// pause runs kardinal pause and checks its output and the freeze gate.
func pause(t *testing.T, a *app) {
	t.Helper()
	assert.Equal(t, "Pipeline "+pipelineName+" paused. No new promotions will start; in-flight steps hold at the next safe point.\n",
		a.e.MustKardinal(t, a.ns, "pause", pipelineName))
	a.e.WaitFreezeGate(t, a.ns, pipelineName, 10*time.Second)
}

// resume runs kardinal resume and checks its output and that the freeze gate
// is gone.
func resume(t *testing.T, a *app) {
	t.Helper()
	assert.Equal(t, "Pipeline "+pipelineName+" resumed.\n", a.e.MustKardinal(t, a.ns, "resume", pipelineName))
	a.e.WaitNoFreezeGate(t, a.ns, pipelineName, 10*time.Second)
}

// TestGate_PauseHoldsNewPromotions checks kardinal pause: it creates the
// Pipeline's freeze gate, the Pipeline reports a Paused condition, get
// pipelines marks it [PAUSED], and a new Bundle's step waits in Pending with a
// message saying how to resume. kardinal resume deletes the gate and removes
// the condition, and the held step promotes.
//
// Covers PAUSE-01.
func TestGate_PauseHoldsNewPromotions(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	pause(t, a)
	assertFreezeGate(t, e.WaitFreezeGate(t, a.ns, pipelineName, 10*time.Second), getPipeline(t, a))
	e.WaitPipeline(t, a.ns, pipelineName, gateTimeout, "Paused", pausedCondition)
	assert.Contains(t, e.MustKardinal(t, a.ns, "get", "pipelines"), pipelineName+" [PAUSED]")

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepMessage(t, a.ns, pipelineName, bundle, "test", "Pending", pausedMsg, time.Minute)
	e.StepHeld(t, a.ns, pipelineName, bundle, "test", pausedMsg, holdFor)
	assertEnvAt(t, a, "test", fixtures.V1)

	resume(t, a)
	e.WaitPipeline(t, a.ns, pipelineName, gateTimeout, "without a Paused condition", noPausedCondition)
	assert.NotContains(t, e.MustKardinal(t, a.ns, "get", "pipelines"), "[PAUSED]")
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", resumeTimeout+promoteTimeout)
	assertEnvAt(t, a, "test", fixtures.V2)
}

// TestGate_PauseHoldsAtSafePoints checks what a pause does to steps already
// running. test waits for its PR merge when the pipeline is paused: the merge
// still deploys and the health check still verifies it. prod's step, created
// during the pause, holds in Pending. After resume it starts, and its push
// fails and retries (the repo is archived); a second pause holds it in
// Promoting before its next git step, with no further retries. Once the repo
// is writable and the pipeline resumed, the held step continues where it
// stopped and prod promotes.
//
// Covers PAUSE-02, PAUSE-03.
func TestGate_PauseHoldsAtSafePoints(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	archiver, ok := e.Git.(gitserver.Archiver)
	require.True(t, ok, "the %s git server cannot archive a repo", e.Git.Kind())
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "test PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	pause(t, a)
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assertEnvAt(t, a, "test", fixtures.V2)
	e.WaitFreezeGate(t, a.ns, pipelineName, time.Second)

	e.WaitStepMessage(t, a.ns, pipelineName, bundle, "prod", "Pending", pausedMsg, time.Minute)
	e.StepHeld(t, a.ns, pipelineName, bundle, "prod", pausedMsg, holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	require.NoError(t, archiver.SetArchived(ctx, a.repo, true))
	t.Cleanup(func() {
		if err := archiver.SetArchived(context.Background(), a.repo, false); err != nil {
			t.Errorf("unarchive %s: %v", a.repo.Name, err)
		}
	})
	resume(t, a)
	e.WaitStepMessage(t, a.ns, pipelineName, bundle, "prod", "Promoting", "retrying in ", resumeTimeout)
	pause(t, a)
	held := e.WaitStepMessage(t, a.ns, pipelineName, bundle, "prod", "Promoting", pausedMsg, time.Minute)
	after := e.StepHeldIn(t, a.ns, pipelineName, bundle, "prod", "Promoting", pausedMsg, holdFor)
	assert.Equal(t, held.Status.RetryCount, after.Status.RetryCount, "a held step does not retry")
	assertEnvAt(t, a, "prod", fixtures.V1)

	require.NoError(t, archiver.SetArchived(ctx, a.repo, false))
	resume(t, a)
	e.WaitPipeline(t, a.ns, pipelineName, gateTimeout, "without a Paused condition", noPausedCondition)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", resumeTimeout+promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_PausedFieldPausesPipeline checks that setting spec.paused on the
// Pipeline, as kubectl patch does, pauses it like kardinal pause: the
// controller creates the freeze gate and the Paused condition, and a new
// Bundle's step waits in Pending. Clearing the field deletes the gate and the
// condition, and the step promotes.
//
// Covers PAUSE-04.
func TestGate_PausedFieldPausesPipeline(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	e.SetPipelinePaused(t, a.ns, pipelineName, true)
	assertFreezeGate(t, e.WaitFreezeGate(t, a.ns, pipelineName, gateTimeout), getPipeline(t, a))
	e.WaitPipeline(t, a.ns, pipelineName, gateTimeout, "Paused", pausedCondition)
	assert.Contains(t, e.MustKardinal(t, a.ns, "get", "pipelines"), pipelineName+" [PAUSED]")

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepMessage(t, a.ns, pipelineName, bundle, "test", "Pending", pausedMsg, time.Minute)
	e.StepHeld(t, a.ns, pipelineName, bundle, "test", pausedMsg, holdFor)
	assertEnvAt(t, a, "test", fixtures.V1)

	e.SetPipelinePaused(t, a.ns, pipelineName, false)
	e.WaitNoFreezeGate(t, a.ns, pipelineName, gateTimeout)
	e.WaitPipeline(t, a.ns, pipelineName, gateTimeout, "without a Paused condition", noPausedCondition)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", resumeTimeout+promoteTimeout)
	assertEnvAt(t, a, "test", fixtures.V2)
}
