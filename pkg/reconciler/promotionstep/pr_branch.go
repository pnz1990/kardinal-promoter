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
	"strings"

	"github.com/rs/zerolog"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
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
		branch = builtinsteps.PRBranch(ps.Spec.BundleName, ps.Spec.Environment)
	}
	if !strings.HasPrefix(branch, builtinsteps.PRBranchPrefix) || len(branch) == len(builtinsteps.PRBranchPrefix) {
		return ""
	}
	return branch
}

// branchDeleteError is a closeStepPR error that came after the PR was closed:
// only its head branch is left to delete.
type branchDeleteError struct {
	pr     int
	branch string
	err    error
}

func (e *branchDeleteError) Error() string {
	return fmt.Sprintf("PR #%d is closed, but deleting its branch %s failed: %v", e.pr, e.branch, e.err)
}

func (e *branchDeleteError) Unwrap() error { return e.err }

// closeByHand is what to do by hand when closeStepPR failed for good: close
// the PR, or, when the PR is closed and only its branch is left, delete the
// branch.
func closeByHand(err error) string {
	var be *branchDeleteError
	if errors.As(err, &be) {
		return "delete branch " + be.branch + " by hand"
	}
	return "close it by hand"
}

// deletePRBranch deletes the head branch of the step's closed, unmerged PR
// number in repo. GitHub's merge API merges a closed PR without reopening it,
// so closing alone did not keep a late merge from delivering; with its head
// branch gone, no provider merges it. A branch that is already gone is not an
// error, so a retry after a crash between the close and the delete, or after
// a lost response, succeeds. A provider that cannot delete branches leaves it.
func (r *Reconciler) deletePRBranch(ctx context.Context, ps *v1alpha1.PromotionStep, repo string, num int) error {
	branch := prHeadBranch(ps)
	deleter, ok := r.SCM.(scm.BranchDeleter)
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
