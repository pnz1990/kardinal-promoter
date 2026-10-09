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
	parentsteps.Register(&gitCommitStep{})
}

// outputNoChanges is set to "true" by git-commit when the working tree already
// matched the target; git-push, open-pr and wait-for-merge then do nothing.
const outputNoChanges = "noChanges"

// noChangesMessage is the step message used when the environment is already
// at the target version.
const noChangesMessage = "no changes: environment already at the target version"

// noChanges reports whether git-commit found nothing to commit in this run.
func noChanges(state *parentsteps.StepState) bool {
	return state.Outputs[outputNoChanges] == "true"
}

type gitCommitStep struct{}

func (s *gitCommitStep) Name() string { return "git-commit" }

func (s *gitCommitStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.GitClient == nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "GitClient not configured"}, nil
	}

	message := fmt.Sprintf("[kardinal] Promote %s to %s\n\nBundle: %s\nPipeline: %s",
		state.BundleName, state.Environment.Name,
		state.BundleName, state.PipelineName)

	authorName := state.Git.AuthorName
	if authorName == "" {
		authorName = "kardinal-promoter"
	}
	authorEmail := state.Git.AuthorEmail
	if authorEmail == "" {
		authorEmail = "kardinal@kardinal.io"
	}

	err := state.GitClient.CommitAll(ctx, state.WorkDir, message, authorName, authorEmail)
	// The branch already has this promotion's commit: an earlier attempt
	// pushed it and its result was lost. The change is this promotion's, so
	// it is not a no-op (deployment metrics count it).
	if errors.Is(err, scm.ErrAlreadyCommitted) {
		return parentsteps.StepResult{
			Status:  parentsteps.StepSuccess,
			Message: "already committed by an earlier attempt of this promotion",
			Outputs: map[string]string{outputNoChanges: "false"},
		}, nil
	}
	if errors.Is(err, scm.ErrNothingToCommit) {
		return parentsteps.StepResult{
			Status:  parentsteps.StepSuccess,
			Message: noChangesMessage,
			Outputs: map[string]string{outputNoChanges: "true"},
		}, nil
	}
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("commit failed: %v", err)}, err
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: "committed changes",
		Outputs: map[string]string{outputNoChanges: "false"},
	}, nil
}
