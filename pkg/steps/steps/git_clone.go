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

// Package steps contains built-in step implementations for the promotion engine.
// Each file registers its step via init() into the parent steps.registry.
package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&gitCloneStep{})
}

// gitCloneStep clones the GitOps repository into state.WorkDir.
// It is idempotent: if WorkDir already exists and is a git repo, it skips the clone.
type gitCloneStep struct{}

func (s *gitCloneStep) Name() string { return "git-clone" }

func (s *gitCloneStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.GitClient == nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "GitClient not configured"}, nil
	}
	if state.WorkDir == "" {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "WorkDir not set"}, nil
	}

	// Idempotency: skip clone if .git already exists.
	if _, err := os.Stat(filepath.Join(state.WorkDir, ".git")); err == nil {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "repo already cloned"}, nil
	}

	if err := state.GitClient.Clone(ctx, state.Git.URL, state.Git.Branch, state.WorkDir); err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("clone failed: %v", err)},
			fmt.Errorf("git-clone: %w", err)
	}

	return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "cloned " + state.Git.URL}, nil
}
