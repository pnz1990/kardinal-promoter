// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	builtinsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// prHeadBranch returns the branch the step's PR was opened from: the branch
// git-push reported, or, as open-pr falls back to, builtinsteps.PRBranch. It
// returns "" for a branch kardinal does not own (not under
// builtinsteps.PRBranchPrefix), so a step whose outputs name any other branch
// never deletes it.
func prHeadBranch(ps *v1alpha1.PromotionStep) string {
	branch := ps.Status.Outputs["branch"]
	if branch == "" && ps.Spec.BundleName != "" && ps.Spec.Environment != "" {
		branch = builtinsteps.PRBranch(ps.Namespace, ps.Spec.BundleName, ps.Spec.Environment)
	}
	if !strings.HasPrefix(branch, builtinsteps.PRBranchPrefix) || len(branch) == len(builtinsteps.PRBranchPrefix) {
		return ""
	}
	return branch
}

// branchDeleteError is a closeStepPR error that came after the PR was closed,
// or for a step that opened no PR (pr 0): only its head branch is left to
// delete.
type branchDeleteError struct {
	pr     int
	branch string
	err    error
}

func (e *branchDeleteError) Error() string {
	if e.pr <= 0 {
		return fmt.Sprintf("the step opened no PR, but deleting its branch %s failed: %v", e.branch, e.err)
	}
	return fmt.Sprintf("PR #%d is closed, but deleting its branch %s failed: %v", e.pr, e.branch, e.err)
}

func (e *branchDeleteError) Unwrap() error { return e.err }

// closeByHand is what to do by hand when closeStepPR failed for good: close
// the PR, or, when the PR is closed (or there is none) and only its branch is
// left, delete the branch.
func closeByHand(err error) string {
	var be *branchDeleteError
	if errors.As(err, &be) {
		return "delete branch " + be.branch + " by hand"
	}
	return "close it by hand"
}

// closedPRBranch deletes the head branch of the step's closed, unmerged PR
// number in repo (deletePRBranch), or, with keep, logs that it is kept: a new
// step pushes it again (handleDeleted).
func (r *Reconciler) closedPRBranch(ctx context.Context, ps *v1alpha1.PromotionStep, repo string, num int, keep bool) error {
	if !keep {
		return r.deletePRBranch(ctx, ps, repo, num)
	}
	zerolog.Ctx(ctx).Info().Int("pr", num).Str("step", ps.Name).Str("branch", prHeadBranch(ps)).
		Msg("kept the head branch of the closed PR: the step comes back and pushes it again")
	return nil
}

// deletePRBranch deletes the head branch of the step's closed, unmerged PR
// number in repo. GitHub's merge API merges a closed PR without reopening it,
// so closing alone did not keep a late merge from delivering; with its head
// branch gone, no provider merges it. A branch that is already gone is not an
// error, so a retry after a crash between the close and the delete, or after
// a lost response, succeeds. A provider that cannot delete branches leaves it.
func (r *Reconciler) deletePRBranch(ctx context.Context, ps *v1alpha1.PromotionStep, repo string, num int) error {
	branch := prHeadBranch(ps)
	provider, err := r.scmFor(ctx, ps)
	if err != nil {
		return &branchDeleteError{pr: num, branch: branch, err: err}
	}
	deleter, ok := provider.(scm.BranchDeleter)
	if branch == "" || !ok {
		return nil
	}
	if err := deleter.DeleteBranch(ctx, repo, branch); err != nil {
		return &branchDeleteError{pr: num, branch: branch, err: err}
	}
	zerolog.Ctx(ctx).Info().Int("pr", num).Str("step", ps.Name).Str("branch", branch).
		Msg("deleted the head branch of the closed PR")
	return nil
}

// deleteBranchWithoutPR deletes the head branch of a step that opened no PR
// and is ending: it failed, was superseded, or was deleted while no new step
// pushes the branch at once (handleDeleted). The branch can be there with no
// PR: git-push ran and open-pr did not, or an earlier step for the same Bundle
// and environment was deleted on its own and kept the branch for a new step
// that then opened no PR. Nothing else deletes it. Only a step that opens a PR
// pushes such a branch: its recorded sequence has open-pr, or, not started
// yet, its environment's sequence does. The repository is the Pipeline's, as
// for open-pr; when the Pipeline is gone nothing names it, and the branch is
// left. A provider that cannot delete branches leaves it too. A branch that
// is already gone is not an error.
func (r *Reconciler) deleteBranchWithoutPR(ctx context.Context, ps *v1alpha1.PromotionStep) error {
	branch := prHeadBranch(ps)
	provider, perr := r.scmFor(ctx, ps)
	if perr != nil {
		return &branchDeleteError{branch: branch, err: perr}
	}
	deleter, ok := provider.(scm.BranchDeleter)
	started := len(ps.Status.Steps) > 0
	if branch == "" || !ok || (started && !opensPR(ps)) {
		return nil
	}
	log := zerolog.Ctx(ctx).With().Str("step", ps.Name).Str("branch", branch).Logger()
	pipeline, err := r.loadPipeline(ctx, ps)
	if apierrors.IsNotFound(err) {
		log.Info().Msg("left the branch of a step that opened no PR: its Pipeline is gone")
		return nil
	}
	if err != nil {
		return &branchDeleteError{branch: branch, err: err}
	}
	// The Bundle type does not change whether the sequence opens a PR.
	env := findEnv(pipeline, ps.Spec.Environment)
	if !started && !slices.Contains(steps.DefaultSequenceForBundle(env.Approval, "", env.Update.Strategy, env.Layout), openPRStep) {
		return nil
	}
	repo, err := scm.RepoFromURL(pipeline.Spec.Git.URL)
	if err == nil {
		err = deleter.DeleteBranch(ctx, repo, branch)
	}
	if err != nil {
		return &branchDeleteError{branch: branch, err: err}
	}
	log.Info().Msg("deleted the head branch of a step that opened no PR")
	return nil
}
