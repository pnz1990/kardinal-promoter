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
	parentsteps.Register(&gitPushStep{})
}

// gitPushStep pushes the promotion branch to the remote.
// It is idempotent: pushing an already-pushed branch is a no-op.
type gitPushStep struct{}

func (s *gitPushStep) Name() string { return "git-push" }

func (s *gitPushStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.GitClient == nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "GitClient not configured"}, nil
	}

	// Promotion branch name: kardinal/<bundle>/<env>
	branch := fmt.Sprintf("kardinal/%s/%s", state.BundleName, state.Environment.Name)

	if err := state.GitClient.Push(ctx, state.WorkDir, "origin", branch, state.Git.Token); err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("push failed: %v", err)},
			fmt.Errorf("git-push: %w", err)
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: "pushed " + branch,
		Outputs: map[string]string{"branch": branch},
	}, nil
}
