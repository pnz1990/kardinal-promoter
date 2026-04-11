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
	"os/exec"
	"path/filepath"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&kustomizeBuildStep{})
}

// kustomizeBuildStep runs `kustomize build <envPath>` and writes the rendered
// YAML to a file in the working directory. It stores the output path in
// Outputs["renderedManifestPath"] for use by subsequent steps (e.g., git-commit).
//
// This step is used in the "layout: branch" pipeline mode where kustomize
// templates are rendered at promotion time and committed to an env-specific branch.
//
// Idempotent: running kustomize build on the same input produces the same output.
// If kustomize is not in PATH, the step fails with a helpful error message.
type kustomizeBuildStep struct{}

func (s *kustomizeBuildStep) Name() string { return "kustomize-build" }

func (s *kustomizeBuildStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	// Determine the path to run kustomize build against.
	envPath := filepath.Join(state.WorkDir, envSubdir(state))

	// Output file: rendered manifests go alongside the env directory.
	outputFile := filepath.Join(state.WorkDir, fmt.Sprintf("rendered-%s.yaml", state.Environment.Name))

	// Run `kustomize build <envPath>` and capture output.
	cmd := exec.CommandContext(ctx, "kustomize", "build", envPath)
	out, err := cmd.Output()
	if err != nil {
		// Provide a clear error when kustomize is missing.
		if isBinaryNotFound(err) {
			return parentsteps.StepResult{
					Status:  parentsteps.StepFailed,
					Message: "kustomize binary not found in PATH — install kustomize to use layout: branch",
				},
				fmt.Errorf("kustomize-build: kustomize not in PATH: %w", err)
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return parentsteps.StepResult{
					Status:  parentsteps.StepFailed,
					Message: fmt.Sprintf("kustomize build failed: %s", string(ee.Stderr)),
				},
				fmt.Errorf("kustomize-build: %w", err)
		}
		return parentsteps.StepResult{
				Status:  parentsteps.StepFailed,
				Message: fmt.Sprintf("kustomize build error: %v", err),
			},
			fmt.Errorf("kustomize-build: %w", err)
	}

	// Write rendered output to file.
	if writeErr := os.WriteFile(outputFile, out, 0o644); writeErr != nil {
		return parentsteps.StepResult{
				Status:  parentsteps.StepFailed,
				Message: fmt.Sprintf("write rendered manifest: %v", writeErr),
			},
			fmt.Errorf("kustomize-build: write output: %w", writeErr)
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("rendered %d bytes from %s", len(out), envPath),
		Outputs: map[string]string{
			"renderedManifestPath": outputFile,
			"renderedManifestSize": fmt.Sprintf("%d", len(out)),
		},
	}, nil
}

// isBinaryNotFound returns true if err indicates a binary was not found.
func isBinaryNotFound(err error) bool {
	if err == nil {
		return false
	}
	return os.IsNotExist(err) ||
		err.Error() == "exec: \"kustomize\": executable file not found in $PATH"
}
