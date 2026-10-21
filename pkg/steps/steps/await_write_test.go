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

// TestAwaitWriteStep (#2366): a batched environment's step asks its
// BranchWrite to write its change, waits for the answer to its latest
// request (not an earlier one, nor another step's), and takes the commit; a
// failed write fails the step retryably and its retry asks again; a write
// the push guard cancelled ends the step as git-push's guard does.
func TestAwaitWriteStep(t *testing.T) {
	step, err := parentsteps.Lookup(parentsteps.AwaitWriteStepName)
	require.NoError(t, err)
	const bw = "app-v2-write"
	state := func(request int64, outputs map[string]string, live ...v1alpha1.LiveBranchWrite) *parentsteps.StepState {
		if outputs == nil {
			outputs = map[string]string{}
		}
		return &parentsteps.StepState{Write: bw, StepUID: "uid-1", WriteRequest: request, LiveWrites: live, Outputs: outputs,
			Environment: v1alpha1.EnvironmentSpec{Name: "prod"}, Git: parentsteps.GitConfig{Branch: "main"}}
	}
	answer := func(request int64, uid string, r v1alpha1.BranchWriteResult) v1alpha1.LiveBranchWrite {
		r.Request, r.UID, r.Round = request, uid, 4
		return v1alpha1.LiveBranchWrite{Name: bw, Result: &r}
	}
	ctx := context.Background()

	res, err := step.Execute(ctx, &parentsteps.StepState{Outputs: map[string]string{}})
	require.Error(t, err, "no BranchWrite")
	assert.ErrorIs(t, err, parentsteps.ErrPermanent)
	assert.Equal(t, parentsteps.StepFailed, res.Status)

	res, err = step.Execute(ctx, state(0, nil))
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, res.Status)
	assert.Equal(t, map[string]string{parentsteps.OutputWriteRequest: "new"}, res.Outputs, "it asks")

	for name, live := range map[string][]v1alpha1.LiveBranchWrite{
		"no answer":                 nil,
		"an earlier request's":      {answer(1, "uid-1", v1alpha1.BranchWriteResult{CommitSHA: "old"})},
		"another step's":            {answer(2, "uid-0", v1alpha1.BranchWriteResult{CommitSHA: "theirs"})},
		"another BranchWrite's":     {{Name: "other", Result: &v1alpha1.BranchWriteResult{Request: 2, CommitSHA: "x"}}},
		"a BranchWrite with no row": {{Name: bw}},
	} {
		res, err = step.Execute(ctx, state(2, nil, live...))
		require.NoError(t, err, name)
		assert.Equal(t, parentsteps.StepPending, res.Status, name)
		assert.Positive(t, res.RequeueAfter, name)
		assert.Empty(t, res.Outputs, "%s: no new request", name)
	}

	res, err = step.Execute(ctx, state(2, nil, answer(2, "uid-1", v1alpha1.BranchWriteResult{CommitSHA: "abc123"})))
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, res.Status)
	assert.Equal(t, map[string]string{"commitSHA": "abc123", "branch": "main", "noChanges": "false", "writtenBy": bw + "/4"}, res.Outputs)

	res, err = step.Execute(ctx, state(2, nil, answer(2, "uid-1", v1alpha1.BranchWriteResult{CommitSHA: "head", NoChanges: true})))
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, res.Status)
	assert.Equal(t, "true", res.Outputs["noChanges"])
	assert.Equal(t, "head", res.Outputs["commitSHA"], "the head that already had the version")

	failed := answer(2, "uid-1", v1alpha1.BranchWriteResult{Error: "push main: connection reset"})
	res, err = step.Execute(ctx, state(2, nil, failed))
	require.Error(t, err)
	assert.False(t, errors.Is(err, parentsteps.ErrPermanent), "retryable")
	assert.Equal(t, parentsteps.StepFailed, res.Status)
	assert.Contains(t, res.Message, "connection reset")
	res, err = step.Execute(ctx, state(2, res.Outputs, failed))
	require.NoError(t, err, "the retry")
	assert.Equal(t, parentsteps.StepPending, res.Status)
	assert.Equal(t, "new", res.Outputs[parentsteps.OutputWriteRequest], "the retry asks again")

	_, err = step.Execute(ctx, state(2, nil, answer(2, "uid-1", v1alpha1.BranchWriteResult{Cancelled: true, NewerBundle: "app-v3"})))
	assert.ErrorIs(t, err, parentsteps.ErrNewerPushed)
	assert.ErrorContains(t, err, "newer bundle app-v3 already pushed to prod")
	_, err = step.Execute(ctx, state(2, nil, answer(2, "uid-1", v1alpha1.BranchWriteResult{Cancelled: true,
		Error: "bundle app-v2 was superseded — promotion cancelled"})))
	assert.ErrorIs(t, err, parentsteps.ErrStalePush)
	assert.NotErrorIs(t, err, parentsteps.ErrNewerPushed)
	assert.ErrorContains(t, err, "superseded")
}

// TestAwaitWriteSequence: a batched environment runs await-write, then the
// health check; every name is a registered step.
func TestAwaitWriteSequence(t *testing.T) {
	seq := parentsteps.AwaitWriteSequence()
	assert.Equal(t, []string{"await-write", "health-check"}, seq)
	for _, name := range seq {
		_, err := parentsteps.Lookup(name)
		require.NoError(t, err, name)
	}
}
