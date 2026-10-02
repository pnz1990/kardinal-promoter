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

package steps

import (
	"context"
	"errors"
	"fmt"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&gitPushStep{})
}

// PRBranchPrefix prefixes every branch kardinal pushes a promotion to and
// opens a PR from (PRBranch). The PromotionStep reconciler deletes the head
// branch of a PR it closed only when the branch is under this prefix.
const PRBranchPrefix = "kardinal/"

// PRBranch is the branch git-push pushes a pr-review promotion to and open-pr
// opens the PR from: kardinal/<bundle>/<env>.
func PRBranch(bundle, env string) string {
	return fmt.Sprintf("%s%s/%s", PRBranchPrefix, bundle, env)
}

// gitPushStep pushes the promotion commit. Which branch follows from the step
// sequence being run (StepState.Sequence), not from the live approval, so an
// approval edit made while the step runs cannot strand the commit:
//
//   - A sequence with open-pr (approval: pr-review) pushes to the
//     kardinal-owned branch kardinal/<bundle>/<env> with force, so a re-run
//     after a controller restart (which re-clones and re-commits) replaces
//     the earlier push instead of failing non-fast-forward.
//   - Any other sequence (approval: auto) pushes to the base branch without
//     force. If the base branch moved since the clone (another environment
//     pushed first), the step returns StepRestart and the engine re-runs the
//     sequence from a fresh clone.
//   - When git-commit found nothing to commit, nothing is pushed.
type gitPushStep struct{}

func (s *gitPushStep) Name() string { return "git-push" }

func (s *gitPushStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.GitClient == nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "GitClient not configured"}, nil
	}
	if layoutBranch(state) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: layoutBranchNotImplemented},
			parentsteps.Permanent(errors.New(layoutBranchNotImplemented))
	}
	if noChanges(state) {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "nothing to push: " + noChangesMessage}, nil
	}

	// Promotion branch name: kardinal/<bundle>/<env>
	branch := PRBranch(state.BundleName, state.Environment.Name)
	force := true
	if !state.OpensPR() {
		branch = state.Git.Branch
		force = false
	}

	err := state.GitClient.Push(ctx, state.WorkDir, "origin", branch, state.Git.Token, force)
	if !force && errors.Is(err, scm.ErrNonFastForward) {
		return parentsteps.StepResult{
			Status:  parentsteps.StepRestart,
			Message: fmt.Sprintf("base branch %s moved while promoting; retrying from a fresh clone", branch),
		}, nil
	}
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("push failed: %v", err)}, err
	}

	outputs := map[string]string{"branch": branch}
	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: "pushed " + branch,
		Outputs: outputs,
	}, nil
}
