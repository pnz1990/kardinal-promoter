// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// rebasingGitClient is mockGitClient with RebaseOnRemote.
type rebasingGitClient struct {
	mockGitClient
	rebaseCalls int
	rebaseErrs  []error
}

func (m *rebasingGitClient) RebaseOnRemote(context.Context, string, string, string, string) ([]string, error) {
	m.rebaseCalls++
	if len(m.rebaseErrs) > 0 {
		err := m.rebaseErrs[0]
		m.rebaseErrs = m.rebaseErrs[1:]
		return nil, err
	}
	return []string{"environments/prod/kustomization.yaml"}, nil
}

var _ scm.Rebaser = (*rebasingGitClient)(nil)

// TestGitPushStep_RebasesOntoMovedBranch: on an auto environment, a push
// refused because another writer moved the base branch is rebased onto the
// new head and pushed again, with backoff between attempts; a rebase conflict
// (the other writer changed the same files) or running out of attempts
// restarts the sequence from a fresh clone; any other rebase error fails the
// step. The step never waits between attempts. A pr-review environment
// force-pushes its own branch and never rebases.
func TestGitPushStep_RebasesOntoMovedBranch(t *testing.T) {
	nonFF := fmt.Errorf("push: %w", scm.ErrNonFastForward)
	many := make([]error, steps.MaxRebaseAttempts+1)
	for i := range many {
		many[i] = nonFF
	}
	tests := []struct {
		name        string
		approval    string
		pushErrs    []error
		rebaseErrs  []error
		wantStatus  parentsteps.StepStatus
		wantErr     bool
		wantPushes  int
		wantRebases int
		wantMsg     string
	}{
		{name: "lands after one rebase", approval: "auto", pushErrs: []error{nonFF},
			wantStatus: parentsteps.StepSuccess, wantPushes: 2, wantRebases: 1, wantMsg: "after rebasing onto 1 newer commit"},
		{name: "lands after three rebases", approval: "auto", pushErrs: []error{nonFF, nonFF, nonFF},
			wantStatus: parentsteps.StepSuccess, wantPushes: 4, wantRebases: 3},
		{name: "same files: fresh clone", approval: "auto", pushErrs: []error{nonFF},
			rebaseErrs: []error{fmt.Errorf("%w: environments/prod/kustomization.yaml", scm.ErrRebaseConflict)},
			wantStatus: parentsteps.StepRestart, wantPushes: 1, wantRebases: 1, wantMsg: "redoing the change from a fresh clone"},
		{name: "base commit missing: fresh clone (#1606)", approval: "auto", pushErrs: []error{nonFF},
			rebaseErrs: []error{fmt.Errorf("rebase: commit abc: %w", scm.ErrRebaseBaseMissing)},
			wantStatus: parentsteps.StepRestart, wantPushes: 1, wantRebases: 1, wantMsg: "lacks the commit to rebase from"},
		{name: "keeps moving: fresh clone", approval: "auto", pushErrs: many,
			wantStatus: parentsteps.StepRestart, wantPushes: steps.MaxRebaseAttempts + 1, wantRebases: steps.MaxRebaseAttempts,
			wantMsg: "kept moving"},
		{name: "fetch fails", approval: "auto", pushErrs: []error{nonFF}, rebaseErrs: []error{errors.New("git fetch: boom")},
			wantStatus: parentsteps.StepFailed, wantErr: true, wantPushes: 1, wantRebases: 1},
		{name: "pr-review never rebases", approval: "pr-review",
			wantStatus: parentsteps.StepSuccess, wantPushes: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			git := &rebasingGitClient{mockGitClient: mockGitClient{pushErrs: tc.pushErrs}, rebaseErrs: tc.rebaseErrs}
			state := makeState(t, &git.mockGitClient, nil)
			state.GitClient = git
			state.Sequence = parentsteps.DefaultSequenceForBundle(tc.approval, "image", "", "")
			state.Environment.Approval = tc.approval
			res, err := runStep(t, "git-push", state)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantStatus, res.Status, res.Message)
			assert.Equal(t, tc.wantPushes, git.pushCalls)
			assert.Equal(t, tc.wantRebases, git.rebaseCalls)
			assert.Contains(t, res.Message, tc.wantMsg)
			if tc.wantStatus == parentsteps.StepSuccess && tc.wantRebases > 0 {
				assert.Equal(t, fmt.Sprint(tc.wantRebases), res.Outputs["rebases"])
			} else {
				assert.Empty(t, res.Outputs["rebases"])
			}
			assert.False(t, git.pushForce && tc.approval == "auto", "the base branch is never force-pushed")
		})
	}
}

// TestGitPushStep_BaseMissingRestartsBounded (#1606): every fresh clone asked
// for because the clone lacked the rebase base is counted in
// status.outputs.baseMissingRestarts, which the step reads back from the
// state, so the count survives a requeue or a controller restart. Past
// MaxBaseMissingRestarts the step fails permanently with the reason, instead
// of retrying for ever as contention.
func TestGitPushStep_BaseMissingRestartsBounded(t *testing.T) {
	nonFF := fmt.Errorf("push: %w", scm.ErrNonFastForward)
	missing := fmt.Errorf("rebase: commit abc: %w", scm.ErrRebaseBaseMissing)
	for _, tc := range []struct {
		name       string
		before     string
		wantStatus parentsteps.StepStatus
		wantCount  string
	}{
		{name: "first", wantStatus: parentsteps.StepRestart, wantCount: "1"},
		{name: "counted from status", before: "3", wantStatus: parentsteps.StepRestart, wantCount: "4"},
		{name: "last allowed", before: fmt.Sprint(steps.MaxBaseMissingRestarts - 1), wantStatus: parentsteps.StepRestart,
			wantCount: fmt.Sprint(steps.MaxBaseMissingRestarts)},
		{name: "bound reached: fails", before: fmt.Sprint(steps.MaxBaseMissingRestarts), wantStatus: parentsteps.StepFailed,
			wantCount: fmt.Sprint(steps.MaxBaseMissingRestarts + 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git := &rebasingGitClient{mockGitClient: mockGitClient{pushErrs: []error{nonFF}}, rebaseErrs: []error{missing}}
			state := makeState(t, &git.mockGitClient, nil)
			state.GitClient = git
			state.Sequence = parentsteps.DefaultSequenceForBundle("auto", "image", "", "")
			state.Environment.Approval = "auto"
			if tc.before != "" {
				state.Outputs[steps.OutputBaseMissingRestarts] = tc.before
			}
			res, err := runStep(t, "git-push", state)
			assert.Equal(t, tc.wantStatus, res.Status, res.Message)
			assert.Equal(t, tc.wantCount, res.Outputs[steps.OutputBaseMissingRestarts])
			if tc.wantStatus == parentsteps.StepFailed {
				require.ErrorIs(t, err, parentsteps.ErrPermanent, "not retried as contention")
				assert.Contains(t, res.Message, "fresh clones lacked the commit to rebase from")
				assert.Contains(t, res.Message, "force-pushed")
				return
			}
			require.NoError(t, err)
			assert.Contains(t, res.Message, "lacks the commit to rebase from")
		})
	}
}
