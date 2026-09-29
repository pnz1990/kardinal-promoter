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

// gitPushStep pushes the promotion commit.
//
//   - approval: pr-review pushes to the kardinal-owned branch
//     kardinal/<bundle>/<env> with force, so a re-run after a controller
//     restart (which re-clones and re-commits) replaces the earlier push
//     instead of failing non-fast-forward.
//   - approval: auto pushes to the base branch without force. If the base
//     branch moved since the clone (another environment pushed first), the
//     step returns StepRestart and the engine re-runs the sequence from a
//     fresh clone.
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
	branch := fmt.Sprintf("kardinal/%s/%s", state.BundleName, state.Environment.Name)
	force := true
	if state.Environment.Approval != "pr-review" {
		branch = state.Git.Branch
		if branch == "" {
			branch = "main"
		}
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
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("push failed: %v", err)},
			fmt.Errorf("git-push: %w", err)
	}

	outputs := map[string]string{"branch": branch}
	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: "pushed " + branch,
		Outputs: outputs,
	}, nil
}
