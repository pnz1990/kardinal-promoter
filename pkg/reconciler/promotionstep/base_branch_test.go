// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// cloneRecorder is a pushRecorder that also records the branch of every clone.
type cloneRecorder struct {
	pushRecorder
	clones []string
}

func (g *cloneRecorder) Clone(_ context.Context, _, branch, _, _ string) error {
	g.clones = append(g.clones, branch)
	return nil
}

// baseRecorder is a mockSCM that records the base branch of every PR it opens.
type baseRecorder struct {
	*mockSCM
	bases []string
}

func (s *baseRecorder) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	s.bases = append(s.bases, base)
	return s.mockSCM.OpenPR(ctx, repo, title, body, head, base)
}

// TestBaseBranchDefault (B99): a Pipeline whose spec.git.branch is empty works
// against main. git-clone checks out main, an auto step pushes to main and
// records the pushed commit, and a pr-review step opens its PR against main.
// The open-pr step used to send the empty value as the PR base, which every
// SCM refuses, while the auto push already went to main. A branch that is set
// is used everywhere instead.
func TestBaseBranchDefault(t *testing.T) {
	tests := []struct {
		name, branch, env, want string
		wantState               string
		wantPushes              []string
		wantBases               []string
		wantCommit              string
	}{
		{name: "unset pr-review", branch: "", env: "prod", want: "main", wantState: "WaitingForMerge",
			wantPushes: []string{"kardinal/37a8eec1/bundle-1/prod force=true"}, wantBases: []string{"main"}},
		{name: "unset auto", branch: "", env: "test", want: "main", wantState: "HealthChecking",
			wantPushes: []string{"main force=false"}, wantCommit: newSHA},
		{name: "set pr-review", branch: "release", env: "prod", want: "release", wantState: "WaitingForMerge",
			wantPushes: []string{"kardinal/37a8eec1/bundle-1/prod force=true"}, wantBases: []string{"release"}},
		{name: "set auto", branch: "release", env: "test", want: "release", wantState: "HealthChecking",
			wantPushes: []string{"release force=false"}, wantCommit: newSHA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pl, b := makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")
			pl.Spec.Git.Branch = tt.branch // the fake client applies no CRD default
			step := builtStep(t, pl, b, tt.env)
			step.Status.State = "Promoting"
			c := newClient(t, step, pl, b, openPRStatus(step.Spec.PRStatusRef, "", 0))
			m := &baseRecorder{mockSCM: &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/5", prNumber: 5}}
			git := &cloneRecorder{pushRecorder: pushRecorder{headGit: headGit{sha: newSHA}}}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: git,
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			reconcileStep(t, r, step.Name) // records the step list
			reconcileStep(t, r, step.Name) // runs it
			got := getStep(t, c, step.Name)
			require.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Equal(t, []string{tt.want}, git.clones, "git-clone branch")
			assert.Equal(t, tt.wantPushes, git.pushes)
			assert.Equal(t, tt.wantBases, m.bases, "PR base")
			assert.Equal(t, tt.wantCommit, got.Status.Outputs["commitSHA"], "pushed commit recorded")
		})
	}
}
