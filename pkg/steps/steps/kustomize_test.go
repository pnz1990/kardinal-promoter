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

// kustomize_test.go — tests for the pure-Go kustomize-set-image implementation.
//
// These tests DO NOT require the kustomize binary in PATH.
// The kustomize-set-image step is now implemented in pure Go (#494).
// The kustomize-build step uses an injectable KustomizeBuilder for testing.
package steps_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"

	// Import all built-ins to trigger init() registration.
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// --- helpers ---

// writeKustomization creates envPath/kustomization.yaml with the given content.
func writeKustomization(t *testing.T, envPath, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(envPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envPath, "kustomization.yaml"), []byte(content), 0o644))
}

// readKustomization reads envPath/kustomization.yaml and returns its content.
func readKustomization(t *testing.T, envPath string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(envPath, "kustomization.yaml"))
	require.NoError(t, err)
	return string(b)
}

// makeState builds a minimal StepState for kustomize step tests.
func makeKustomizeState(workDir, envName string, images []v1alpha1.ImageRef) *parentsteps.StepState {
	return &parentsteps.StepState{
		WorkDir:     workDir,
		Environment: v1alpha1.EnvironmentSpec{Name: envName},
		Bundle:      v1alpha1.BundleSpec{Images: images},
	}
}

func mustLookup(t *testing.T, name string) parentsteps.Step {
	t.Helper()
	s, err := parentsteps.Lookup(name)
	require.NoError(t, err)
	return s
}

// --- kustomize-set-image tests ---

// TestKustomizeSetImage_AddsNewEntry verifies that a new image entry is appended
// when kustomization.yaml has no existing images list.
func TestKustomizeSetImage_AddsNewEntry(t *testing.T) {
	workDir := t.TempDir()
	envPath := filepath.Join(workDir, "environments", "prod")
	writeKustomization(t, envPath, "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n")

	state := makeKustomizeState(workDir, "prod", []v1alpha1.ImageRef{
		{Repository: "ghcr.io/myorg/myapp", Tag: "v1.2.3"},
	})

	step := mustLookup(t, "kustomize-set-image")
	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)

	yaml := readKustomization(t, envPath)
	assert.Contains(t, yaml, "myapp", "short name must appear as image name")
	assert.Contains(t, yaml, "v1.2.3", "tag must be written")
}

// TestKustomizeSetImage_UpdatesExistingEntry verifies that an existing image entry
// is updated in place without duplicating it.
func TestKustomizeSetImage_UpdatesExistingEntry(t *testing.T) {
	workDir := t.TempDir()
	envPath := filepath.Join(workDir, "environments", "prod")
	initial := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nimages:\n- name: myapp\n  newName: ghcr.io/myorg/myapp\n  newTag: v1.0.0\n"
	writeKustomization(t, envPath, initial)

	state := makeKustomizeState(workDir, "prod", []v1alpha1.ImageRef{
		{Repository: "ghcr.io/myorg/myapp", Tag: "v2.0.0"},
	})
	step := mustLookup(t, "kustomize-set-image")
	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)

	yaml := readKustomization(t, envPath)
	assert.Contains(t, yaml, "v2.0.0", "tag must be updated")
	assert.NotContains(t, yaml, "v1.0.0", "old tag must be replaced")
	// Verify only one image entry with name: myapp (not duplicated).
	assert.Equal(t, 1, countOccurrences(yaml, "name: myapp"), "entry must not be duplicated")
}

// TestKustomizeSetImage_DigestWritten verifies that digest is written when provided.
func TestKustomizeSetImage_DigestWritten(t *testing.T) {
	workDir := t.TempDir()
	envPath := filepath.Join(workDir, "environments", "staging")
	writeKustomization(t, envPath, "kind: Kustomization\n")

	state := makeKustomizeState(workDir, "staging", []v1alpha1.ImageRef{
		{Repository: "ghcr.io/myorg/myapp", Tag: "v1.0.0", Digest: "sha256:abc123"},
	})
	step := mustLookup(t, "kustomize-set-image")
	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Contains(t, readKustomization(t, envPath), "sha256:abc123")
}

// TestKustomizeSetImage_NoImagesToUpdate verifies the step succeeds when no images.
func TestKustomizeSetImage_NoImagesToUpdate(t *testing.T) {
	workDir := t.TempDir()
	state := makeKustomizeState(workDir, "prod", nil)
	step := mustLookup(t, "kustomize-set-image")
	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Contains(t, result.Message, "no images")
}

// TestKustomizeSetImage_MultipleImages verifies that multiple images are all updated.
func TestKustomizeSetImage_MultipleImages(t *testing.T) {
	workDir := t.TempDir()
	envPath := filepath.Join(workDir, "environments", "test")
	writeKustomization(t, envPath, "kind: Kustomization\n")

	state := makeKustomizeState(workDir, "test", []v1alpha1.ImageRef{
		{Repository: "ghcr.io/myorg/frontend", Tag: "v1.0.0"},
		{Repository: "ghcr.io/myorg/backend", Tag: "v2.0.0"},
	})
	step := mustLookup(t, "kustomize-set-image")
	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)

	yaml := readKustomization(t, envPath)
	assert.Contains(t, yaml, "v1.0.0")
	assert.Contains(t, yaml, "v2.0.0")
	assert.Contains(t, yaml, "frontend")
	assert.Contains(t, yaml, "backend")
}

// TestKustomizeSetImage_Idempotent verifies that running the step twice produces
// the same result as running it once — no duplicate entries.
func TestKustomizeSetImage_Idempotent(t *testing.T) {
	workDir := t.TempDir()
	envPath := filepath.Join(workDir, "environments", "prod")
	writeKustomization(t, envPath, "kind: Kustomization\n")

	state := makeKustomizeState(workDir, "prod", []v1alpha1.ImageRef{
		{Repository: "ghcr.io/myorg/myapp", Tag: "v1.2.3"},
	})
	step := mustLookup(t, "kustomize-set-image")

	_, err1 := step.Execute(context.Background(), state)
	_, err2 := step.Execute(context.Background(), state)
	require.NoError(t, err1)
	require.NoError(t, err2)

	yaml := readKustomization(t, envPath)
	// "myapp" must appear exactly twice: once as name: myapp, once inside ghcr.io/myorg/myapp
	assert.LessOrEqual(t, countOccurrences(yaml, "newTag: v1.2.3"), 1,
		"tag entry must not be duplicated on second run")
}

// TestKustomizeSetImage_CustomEnvPath verifies that env.Path overrides the default.
func TestKustomizeSetImage_CustomEnvPath(t *testing.T) {
	workDir := t.TempDir()
	customPath := filepath.Join(workDir, "deploy", "prod")
	require.NoError(t, os.MkdirAll(customPath, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(customPath, "kustomization.yaml"),
		[]byte("kind: Kustomization\n"), 0o644))

	state := &parentsteps.StepState{
		WorkDir:     workDir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod", Path: "deploy/prod"},
		Bundle:      v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/myorg/myapp", Tag: "v1.0.0"}}},
	}
	step := mustLookup(t, "kustomize-set-image")
	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Contains(t, readKustomization(t, customPath), "v1.0.0")
}

// --- kustomize-build tests (injectable builder) ---

type stubKustomizeBuilder struct {
	output []byte
	err    error
	called bool
	dir    string
}

func (s *stubKustomizeBuilder) Build(_ context.Context, dir string) ([]byte, error) {
	s.called = true
	s.dir = dir
	return s.output, s.err
}

func TestKustomizeBuild_WritesRenderedManifest(t *testing.T) {
	workDir := t.TempDir()
	envPath := filepath.Join(workDir, "environments", "prod")
	require.NoError(t, os.MkdirAll(envPath, 0o755))

	stub := &stubKustomizeBuilder{
		output: []byte("apiVersion: apps/v1\nkind: Deployment\n"),
	}
	step := steps.NewKustomizeBuildStep(stub)

	state := &parentsteps.StepState{
		WorkDir:     workDir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle:      v1alpha1.BundleSpec{},
	}

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.True(t, stub.called)

	outputPath := result.Outputs["renderedManifestPath"]
	require.NotEmpty(t, outputPath)
	content, readErr := os.ReadFile(outputPath)
	require.NoError(t, readErr)
	assert.Equal(t, string(stub.output), string(content))
}

func TestKustomizeBuild_BuilderErrorPropagates(t *testing.T) {
	workDir := t.TempDir()
	stub := &stubKustomizeBuilder{
		err: fmt.Errorf("kustomize build failed: bad overlay"),
	}
	step := steps.NewKustomizeBuildStep(stub)

	state := &parentsteps.StepState{
		WorkDir:     workDir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle:      v1alpha1.BundleSpec{},
	}
	result, err := step.Execute(context.Background(), state)
	assert.Error(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
}

// --- helpers ---

func countOccurrences(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
			i += len(sub) - 1
		}
	}
	return n
}
