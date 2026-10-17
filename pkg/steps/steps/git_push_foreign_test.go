// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	steps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// leaseGit is a git client whose PR branch has head remote ("" absent). It
// pushes with a lease (scm.LeasedPusher): the push is refused when the
// branch's head is not the one leased, as when someone pushed in between
// (moved). Plain pushes are counted apart.
type leaseGit struct {
	headGitClient
	remote string
	moved  string
	leases []string
	plain  int
}

func (g *leaseGit) RemoteBranchHead(context.Context, string, string, scm.GitAuth) (string, error) {
	return g.remote, nil
}

func (g *leaseGit) PushLeased(_ context.Context, _, _, _ string, _ scm.GitAuth, expect string) error {
	g.leases = append(g.leases, expect)
	if g.moved != "" {
		g.remote, g.moved = g.moved, ""
		return scm.ErrLeaseBroken
	}
	g.remote = g.sha
	return nil
}

func (g *leaseGit) Push(context.Context, string, string, string, scm.GitAuth, bool) error {
	g.plain++
	return nil
}

// #2095: git-push never force-pushes kardinal's PR branch over a commit
// someone else pushed there (a crash between the push and the status write,
// or any re-push). It pushes, with a lease on the head it read, only when
// the branch is absent or its head is one of the step's own pushes;
// otherwise it fails with ForeignPRHeadError and pushes nothing. A head that
// moves between the read and the push breaks the lease: no push either.
//
// Covers GIT-PUSH-FOREIGN-01.
func TestGitPushStep_NeverOverAForeignCommit(t *testing.T) {
	const own, foreign = "1111111111111111111111111111111111111111", "ffffffffffffffffffffffffffffffffffffffff"
	for _, tc := range []struct {
		name, remote, moved string
		wantLease           string // "" with wantPush: the branch must be absent
		wantPush            bool
		wantForeignHead     string
	}{
		{name: "the branch is absent", wantPush: true},
		{name: "its head is the step's earlier push", remote: own, wantLease: own, wantPush: true},
		{name: "someone else's commit is on it", remote: foreign, wantForeignHead: foreign},
		{name: "someone pushes between the read and the push", remote: own, moved: foreign, wantLease: own,
			wantForeignHead: foreign},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git := &leaseGit{headGitClient: headGitClient{sha: "2222222222222222222222222222222222222222"},
				remote: tc.remote, moved: tc.moved}
			state := makeState(t, &git.mockGitClient, nil)
			state.GitClient = git
			state.Sequence = parentsteps.DefaultSequenceForBundle("pr-review", "image", "", "")
			state.Environment.Approval = "pr-review"
			state.Outputs = map[string]string{steps.OutputPushedSHA: own, steps.OutputPushedSHAs: own}
			res, err := runStep(t, "git-push", state)
			assert.Zero(t, git.plain, "never a plain force-push of the PR branch")
			if tc.wantForeignHead != "" {
				var fe *parentsteps.ForeignPRHeadError
				require.True(t, errors.As(err, &fe), "%v", err)
				assert.Equal(t, tc.wantForeignHead, fe.Head)
				assert.Equal(t, parentsteps.StepFailed, res.Status)
				assert.Equal(t, tc.wantForeignHead, git.remote, "the foreign commit is still the branch's head")
				return
			}
			require.NoError(t, err)
			require.Len(t, git.leases, 1)
			assert.Equal(t, tc.wantLease, git.leases[0], "leased on the head read")
			assert.Equal(t, git.sha, res.Outputs[steps.OutputPushedSHA])
		})
	}
}
