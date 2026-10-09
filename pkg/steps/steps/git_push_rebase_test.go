// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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
// step. A pr-review environment force-pushes its own branch and never
// rebases.
func TestGitPushStep_RebasesOntoMovedBranch(t *testing.T) {
	var waits []time.Duration
	defer steps.SetRebaseBackoff(func(n int) time.Duration {
		d := time.Millisecond << n
		waits = append(waits, d)
		return d
	})()
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
			waits = nil
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
			if tc.wantRebases > 1 {
				assert.Len(t, waits, tc.wantRebases-1, "a backoff before every rebase but the first")
			}
		})
	}
}
