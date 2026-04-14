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
	parentsteps.Register(&kustomizeBuildStep{
		builder: &execKustomizeBuilder{},
	})
}

// NewKustomizeBuildStep creates a kustomize-build step with the given KustomizeBuilder.
// Used in tests to inject a stub builder that doesn't require the kustomize binary.
func NewKustomizeBuildStep(builder KustomizeBuilder) parentsteps.Step {
	return &kustomizeBuildStep{builder: builder}
}

// KustomizeBuilder is the interface for running a kustomize build.
// The production implementation uses exec.Command("kustomize"); tests inject a stub.
// Replace with sigs.k8s.io/kustomize/krusty when the library is available (#494 follow-up).
type KustomizeBuilder interface {
	// Build runs kustomize build on the given directory and returns the rendered YAML.
	Build(ctx context.Context, dir string) ([]byte, error)
}

// kustomizeBuildStep runs kustomize build <envPath> and writes the rendered
// YAML to a file in the working directory. It stores the output path in
// Outputs["renderedManifestPath"] for use by subsequent steps (e.g., git-commit).
//
// The `builder` field is injectable — tests supply a stub that doesn't require
// kustomize in PATH. Production uses execKustomizeBuilder.
//
// kustomize-set-image no longer requires the binary (#494). kustomize-build still
// does until sigs.k8s.io/kustomize/krusty is added to go.mod (#494 follow-up).
type kustomizeBuildStep struct {
	builder KustomizeBuilder
}

func (s *kustomizeBuildStep) Name() string { return "kustomize-build" }

func (s *kustomizeBuildStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	envPath := filepath.Join(state.WorkDir, envSubdir(state))
	outputFile := filepath.Join(state.WorkDir, fmt.Sprintf("rendered-%s.yaml", state.Environment.Name))

	out, err := s.builder.Build(ctx, envPath)
	if err != nil {
		return parentsteps.StepResult{
				Status:  parentsteps.StepFailed,
				Message: fmt.Sprintf("kustomize build failed: %v", err),
			},
			fmt.Errorf("kustomize-build: %w", err)
	}

	if writeErr := os.WriteFile(outputFile, out, 0o644); writeErr != nil { //nolint:gosec
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

// --- Production implementation ---

// execKustomizeBuilder calls the kustomize binary via exec.Command.
// Replaced by a library-based implementation in #494 follow-up
// (sigs.k8s.io/kustomize/krusty, when added to go.mod).
type execKustomizeBuilder struct{}

func (b *execKustomizeBuilder) Build(ctx context.Context, dir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "kustomize", "build", dir)
	out, err := cmd.Output()
	if err != nil {
		if isBinaryNotFound(err) {
			return nil, fmt.Errorf("kustomize binary not found in PATH — install kustomize to use layout: branch")
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("kustomize build: %s", string(ee.Stderr))
		}
		return nil, fmt.Errorf("kustomize build: %w", err)
	}
	return out, nil
}

// isBinaryNotFound returns true if err indicates a binary was not found.
func isBinaryNotFound(err error) bool {
	if err == nil {
		return false
	}
	return os.IsNotExist(err) ||
		err.Error() == "exec: \"kustomize\": executable file not found in $PATH"
}
