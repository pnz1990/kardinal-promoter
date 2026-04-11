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
	"os"
	"path/filepath"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&configMergeStep{})
}

// configMergeStep applies configuration changes from a Bundle's configRef.
// Strategy: overlay — copies files from a "config-source/" subdirectory in the
// working directory (cloned by git-clone from the configRef commit) over the
// environment directory. This is a simplified overlay that replaces changed files.
//
// Idempotent: copying the same files twice produces the same result.
type configMergeStep struct{}

func (s *configMergeStep) Name() string { return "config-merge" }

func (s *configMergeStep) Execute(_ context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.Bundle.ConfigRef == nil || state.Bundle.ConfigRef.CommitSHA == "" {
		return parentsteps.StepResult{
			Status:  parentsteps.StepSuccess,
			Message: "no config ref — nothing to merge",
		}, nil
	}

	// The git-clone step clones to WorkDir. Config source is expected at:
	// WorkDir/config-source/<commitSHA[:8]>/
	// If the git-clone step put the config repo at WorkDir directly, we read
	// from there. Use the configSourceDir key from Outputs if set by a prior step.
	configSourceDir := state.Outputs["configSourceDir"]
	if configSourceDir == "" {
		// Default: assume the whole WorkDir is the config source.
		configSourceDir = state.WorkDir
	}

	envPath := filepath.Join(state.WorkDir, envSubdir(state))
	if err := os.MkdirAll(envPath, 0o755); err != nil {
		return parentsteps.StepResult{
				Status:  parentsteps.StepFailed,
				Message: fmt.Sprintf("mkdir env path %s: %v", envPath, err),
			},
			fmt.Errorf("config-merge: mkdir: %w", err)
	}

	// Walk the config source directory and copy files to the env path.
	var mergedCount int
	err := filepath.WalkDir(configSourceDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(configSourceDir, path)
		if err != nil {
			return err
		}
		destPath := filepath.Join(envPath, rel)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(destPath), err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", destPath, err)
		}
		mergedCount++
		return nil
	})
	if err != nil {
		return parentsteps.StepResult{
				Status:  parentsteps.StepFailed,
				Message: fmt.Sprintf("walk config source: %v", err),
			},
			fmt.Errorf("config-merge: %w", err)
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("merged %d files from config commit %s", mergedCount, state.Bundle.ConfigRef.CommitSHA[:min(8, len(state.Bundle.ConfigRef.CommitSHA))]),
		Outputs: map[string]string{
			"mergedFiles": fmt.Sprintf("%d", mergedCount),
		},
	}, nil
}
