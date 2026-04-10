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
	parentsteps.Register(&kustomizeSetImageStep{})
}

// kustomizeSetImageStep runs `kustomize edit set image` for each image in the Bundle.
// It is idempotent: kustomize edit set image is a pure replacement operation.
type kustomizeSetImageStep struct{}

func (s *kustomizeSetImageStep) Name() string { return "kustomize-set-image" }

func (s *kustomizeSetImageStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if len(state.Bundle.Images) == 0 {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no images to update"}, nil
	}

	envPath := filepath.Join(state.WorkDir, envSubdir(state))

	// kustomize binary must be in PATH.
	for _, img := range state.Bundle.Images {
		if img.Repository == "" {
			continue
		}
		newImage := img.Repository
		if img.Tag != "" {
			newImage += ":" + img.Tag
		}
		if img.Digest != "" {
			newImage += "@" + img.Digest
		}

		cmd := exec.CommandContext(ctx, "kustomize", "edit", "set", "image",
			img.Repository+"="+newImage)
		cmd.Dir = envPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return parentsteps.StepResult{
					Status:  parentsteps.StepFailed,
					Message: fmt.Sprintf("kustomize edit set image: %s: %v", string(out), err),
				},
				fmt.Errorf("kustomize-set-image %s: %w", img.Repository, err)
		}
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("updated %d images", len(state.Bundle.Images)),
	}, nil
}

// envSubdir returns the subdirectory within WorkDir for the current environment.
// Uses env.Path if set, otherwise defaults to "environments/<env-name>".
func envSubdir(state *parentsteps.StepState) string {
	if p := state.Environment.Path; p != "" {
		return p
	}
	return filepath.Join("environments", state.Environment.Name)
}

// kustomizeInEnvPath checks if kustomize binary exists in PATH — used in tests.
func kustomizeInEnvPath() bool {
	_, err := exec.LookPath("kustomize")
	return err == nil
}

// createMinimalKustomization creates a minimal kustomization.yaml for tests.
func createMinimalKustomization(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	content := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images: []
`
	return os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(content), 0o600)
}
