// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestRenderStep: the controller's render step asks for the render, waits
// for the RenderRun the Graph mirrors onto the step, and takes its result;
// a failed render fails the step for good.
func TestRenderStep(t *testing.T) {
	step, err := parentsteps.Lookup(parentsteps.RenderStepName)
	require.NoError(t, err)
	state := func(seq []string, live ...v1alpha1.LiveRenderRun) *parentsteps.StepState {
		return &parentsteps.StepState{Outputs: map[string]string{}, Sequence: seq, LiveRenders: live,
			Git: parentsteps.GitConfig{Branch: "env/prod"}}
	}
	auto := []string{"render", "health-check"}
	pr := []string{"render", "open-pr", "wait-for-merge", "health-check"}

	s := state(pr)
	res, err := step.Execute(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, res.Status)
	assert.Equal(t, map[string]string{"renderRequested": "true", "renderPullRequest": "true"}, res.Outputs,
		"where to push follows the step's list")
	s.Outputs = res.Outputs

	res, err = step.Execute(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, res.Status)
	assert.Contains(t, res.Message, "waiting for the Graph to create the RenderRun")

	for _, phase := range []string{"", "Pending", "Running"} {
		s.LiveRenders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: phase}}
		res, err = step.Execute(context.Background(), s)
		require.NoError(t, err)
		assert.Equal(t, parentsteps.StepPending, res.Status, phase)
		assert.NotZero(t, res.RequeueAfter)
	}

	result := &v1alpha1.RenderRunResult{CommitSHA: "c0ffee", Branch: "kardinal/b/prod", DryCommit: "d00d", Renderer: "helm",
		Objects: 3, MarkerDigest: "abc"}
	s.LiveRenders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded", Result: result}}
	res, err = step.Execute(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, res.Status)
	assert.Equal(t, "kardinal/b/prod", res.Outputs["branch"], "open-pr opens the PR from the branch the Job pushed")
	assert.Empty(t, res.Outputs["commitSHA"], "a pr-review step health checks the merge commit")
	assert.Equal(t, "false", res.Outputs["noChanges"])
	assert.Equal(t, "d00d", res.Outputs["dryCommit"])

	a := state(auto, v1alpha1.LiveRenderRun{Name: "rr", Phase: "Succeeded", Result: &v1alpha1.RenderRunResult{CommitSHA: "c0ffee", Branch: "env/prod"}})
	a.Outputs["renderRequested"] = "true"
	res, err = step.Execute(context.Background(), a)
	require.NoError(t, err)
	assert.Equal(t, "c0ffee", res.Outputs["commitSHA"], "an auto step health checks the pushed render")

	a.LiveRenders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded", Result: &v1alpha1.RenderRunResult{NoChanges: true}}}
	res, err = step.Execute(context.Background(), a)
	require.NoError(t, err)
	assert.Equal(t, "true", res.Outputs["noChanges"], "open-pr and wait-for-merge skip an unchanged render")

	a.LiveRenders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Failed", Message: "drift: x changed"}}
	res, err = step.Execute(context.Background(), a)
	require.Error(t, err)
	assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
	assert.Equal(t, "render failed (RenderRun rr): drift: x changed", res.Message)

	a.LiveRenders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded"}}
	_, err = step.Execute(context.Background(), a)
	assert.Error(t, err, "a success without a result is refused")
}
