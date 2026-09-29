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

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&gitCloneStep{})
}

// gitCloneStep checks out the GitOps repository into state.WorkDir.
//
// It always starts from a fresh clone of the remote base branch: any tree left
// in WorkDir by an earlier run (a crash, or another promotion) is removed
// first, so the promotion never commits changes it did not make. WorkDir is
// private to one PromotionStep (see parentsteps.WorkDirFor).
//
// For a config Bundle it also checks out ConfigRef.GitRepo at ConfigRef.CommitSHA
// into parentsteps.ConfigSourceDir(WorkDir) and sets Outputs["configSourceDir"]
// for config-merge.
//
// layout: branch is rejected here, before anything is cloned (C05-steps-10).
type gitCloneStep struct{}

// layoutBranchNotImplemented is the failure message for layout: branch.
const layoutBranchNotImplemented = "layout: branch is not implemented: kardinal does not write rendered " +
	"manifests to an env/<name> branch yet, so this promotion would change nothing; " +
	"use layout: directory (see docs/rendered-manifests.md)"

// layoutBranch reports whether the Pipeline or the environment asks for
// layout: branch.
func layoutBranch(state *parentsteps.StepState) bool {
	return state.Environment.Layout == "branch" || state.Pipeline.Git.Layout == "branch"
}

func (s *gitCloneStep) Name() string { return "git-clone" }

func (s *gitCloneStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.GitClient == nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "GitClient not configured"}, nil
	}
	if state.WorkDir == "" {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "WorkDir not set"}, nil
	}
	if layoutBranch(state) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: layoutBranchNotImplemented}, nil
	}

	repoURL := scm.RedactURL(state.Git.URL)
	if err := os.RemoveAll(state.WorkDir); err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("clean work dir: %v", err)},
			fmt.Errorf("git-clone: clean work dir: %w", err)
	}
	if err := state.GitClient.Clone(ctx, state.Git.URL, state.Git.Branch, state.WorkDir, state.Git.Token); err != nil {
		msg := scm.RedactURL(fmt.Sprintf("clone %s failed: %v", repoURL, err))
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: msg},
			fmt.Errorf("git-clone: %s", msg)
	}

	result := parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "cloned " + repoURL}

	if ref := state.Bundle.ConfigRef; state.Bundle.Type == "config" && ref != nil && ref.CommitSHA != "" {
		srcURL := ref.GitRepo
		if srcURL == "" {
			srcURL = state.Git.URL
		}
		srcDir := parentsteps.ConfigSourceDir(state.WorkDir)
		if err := os.RemoveAll(srcDir); err != nil {
			return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("clean config source dir: %v", err)},
				fmt.Errorf("git-clone: clean config source dir: %w", err)
		}
		// Only send the pipeline token to the host it belongs to.
		srcToken := ""
		if scm.SameHost(srcURL, state.Git.URL) {
			srcToken = state.Git.Token
		}
		if err := state.GitClient.CloneAt(ctx, srcURL, ref.CommitSHA, srcDir, srcToken); err != nil {
			msg := scm.RedactURL(fmt.Sprintf("clone config source %s@%s failed: %v", scm.RedactURL(srcURL), ref.CommitSHA, err))
			return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: msg},
				fmt.Errorf("git-clone: %s", msg)
		}
		result.Message += fmt.Sprintf("; config source %s@%s", scm.RedactURL(srcURL), ref.CommitSHA)
		result.Outputs = map[string]string{"configSourceDir": srcDir}
	}

	return result, nil
}
