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
	"strings"

	sigsyaml "sigs.k8s.io/yaml"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&kustomizeSetImageStep{})
}

// kustomizeSetImageStep patches `kustomization.yaml` to update the images list.
// This is the pure-Go replacement for `kustomize edit set image` that previously
// required the kustomize binary in PATH (#494).
//
// The kustomization.yaml images list format:
//
//	images:
//	- name: my-app                   # matches the original repository name
//	  newName: my-registry/my-app    # optional: override registry/name
//	  newTag: v1.2.3                 # tag override
//	  digest: sha256:abc123          # digest override (takes precedence over tag)
//
// `kustomize edit set image <name>=<newImage>` finds the entry whose `name` matches
// `<name>`, updates newName/newTag/digest, and adds a new entry if not found.
// This implementation replicates that exact semantics without a subprocess.
type kustomizeSetImageStep struct{}

func (s *kustomizeSetImageStep) Name() string { return "kustomize-set-image" }

func (s *kustomizeSetImageStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if len(state.Bundle.Images) == 0 {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no images to update"}, nil
	}

	envPath := filepath.Join(state.WorkDir, envSubdir(state))

	for _, img := range state.Bundle.Images {
		if img.Repository == "" {
			continue
		}
		if err := setImageInKustomization(envPath, img.Repository, img.Tag, img.Digest); err != nil {
			return parentsteps.StepResult{
					Status:  parentsteps.StepFailed,
					Message: fmt.Sprintf("kustomize-set-image %s: %v", img.Repository, err),
				},
				fmt.Errorf("kustomize-set-image %s: %w", img.Repository, err)
		}
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("updated %d images in kustomization.yaml", len(state.Bundle.Images)),
	}, nil
}

// kustomizationImage mirrors the kustomize images entry.
// https://kubectl.docs.kubernetes.io/references/kustomize/kustomization/images/
type kustomizationImage struct {
	Name    string `json:"name" yaml:"name"`
	NewName string `json:"newName,omitempty" yaml:"newName,omitempty"`
	NewTag  string `json:"newTag,omitempty" yaml:"newTag,omitempty"`
	Digest  string `json:"digest,omitempty" yaml:"digest,omitempty"`
}

// setImageInKustomization reads kustomization.yaml in envPath, updates or adds
// the image entry for the given repository name, and writes the file back.
//
// To preserve comments and field ordering in the rest of the file, we use a
// two-pass approach:
//  1. Unmarshal the full file into a map[string]interface{} (preserves all fields).
//  2. Unmarshal only the images slice, update the target entry.
//  3. Re-marshal the images slice back into the map and write the file.
func setImageInKustomization(envPath, repository, tag, digest string) error {
	kustomizationPath := filepath.Join(envPath, "kustomization.yaml")

	// Read existing file (or start with empty map).
	raw := make(map[string]interface{})
	existingBytes, readErr := os.ReadFile(kustomizationPath) //nolint:gosec
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read kustomization.yaml: %w", readErr)
	}
	if len(existingBytes) > 0 {
		if err := sigsyaml.Unmarshal(existingBytes, &raw); err != nil {
			return fmt.Errorf("parse kustomization.yaml: %w", err)
		}
	}

	// Extract images list.
	var images []kustomizationImage
	if rawImages, ok := raw["images"]; ok {
		imagesBytes, err := sigsyaml.Marshal(rawImages)
		if err != nil {
			return fmt.Errorf("marshal images list: %w", err)
		}
		if err := sigsyaml.Unmarshal(imagesBytes, &images); err != nil {
			return fmt.Errorf("unmarshal images list: %w", err)
		}
	}

	// Update or append the image entry.
	images = upsertImage(images, repository, tag, digest)

	// Rebuild raw map with updated images.
	var rawImages interface{}
	imagesBytes, err := sigsyaml.Marshal(images)
	if err != nil {
		return fmt.Errorf("marshal updated images: %w", err)
	}
	if err := sigsyaml.Unmarshal(imagesBytes, &rawImages); err != nil {
		return fmt.Errorf("round-trip images: %w", err)
	}
	raw["images"] = rawImages

	// Write back.
	out, err := sigsyaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal kustomization.yaml: %w", err)
	}
	if err := os.WriteFile(kustomizationPath, out, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("write kustomization.yaml: %w", err)
	}
	return nil
}

// upsertImage updates the entry for `repository` in the images list,
// or appends a new entry if not found.
//
// The `name` field is always the original (short) image name — the repository
// path without registry prefix for simpler kustomization.yaml authoring.
// `newName` is only set when the repository differs from `name` (e.g. when
// switching registries). `newTag` and `digest` override the tag/digest.
func upsertImage(images []kustomizationImage, repository, tag, digest string) []kustomizationImage {
	// The kustomize convention: `name` is the last path component of the image
	// repository (the "short name"), used as the lookup key. The full repository
	// path goes in `newName` if it differs.
	shortName := imageShortName(repository)

	for i := range images {
		if images[i].Name == shortName || images[i].Name == repository {
			images[i].NewTag = tag
			images[i].Digest = digest
			if images[i].Name != repository {
				images[i].NewName = repository
			}
			return images
		}
	}

	// Not found — append new entry.
	entry := kustomizationImage{
		Name:   shortName,
		NewTag: tag,
		Digest: digest,
	}
	if shortName != repository {
		entry.NewName = repository
	}
	return append(images, entry)
}

// imageShortName returns the last path segment of a repository URL,
// which kustomize uses as the canonical lookup name.
// Examples:
//
//	"ghcr.io/myorg/myapp"          → "myapp"
//	"myapp"                        → "myapp"
//	"docker.io/library/nginx"      → "nginx"
func imageShortName(repo string) string {
	// Strip the registry host (everything before the first '/').
	parts := strings.Split(repo, "/")
	return parts[len(parts)-1]
}

// envSubdir returns the subdirectory within WorkDir for the current environment.
func envSubdir(state *parentsteps.StepState) string {
	if p := state.Environment.Path; p != "" {
		return p
	}
	return filepath.Join("environments", state.Environment.Name)
}
