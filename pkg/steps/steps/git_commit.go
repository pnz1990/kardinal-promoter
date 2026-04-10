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
	"fmt"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&gitCommitStep{})
}

// gitCommitStep stages all changes and creates a commit with structured provenance.
// It is idempotent: if there are no changes, it creates an empty commit to record intent.
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

	if err := state.GitClient.CommitAll(ctx, state.WorkDir, message, authorName, authorEmail); err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("commit failed: %v", err)},
			fmt.Errorf("git-commit: %w", err)
	}

	return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "committed changes"}, nil
}
