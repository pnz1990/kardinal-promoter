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
package steps_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"

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
	// kustomize matches name against the full image name in the manifests,
	// as `kustomize edit set image` writes it (C05-steps-09).
	assert.Contains(t, yaml, "- name: ghcr.io/myorg/myapp\n", "full repository must be the image name")
	assert.NotContains(t, yaml, "newName", "a new entry needs no newName")
	assert.Contains(t, yaml, "newTag: v1.2.3", "tag must be written")
}

// TestKustomizeSetImage_Matching covers kustomize's image matching: full-name
// entries, short-name placeholder entries and two repositories with the same
// short name (C05-steps-09, C05-steps-15).
func TestKustomizeSetImage_Matching(t *testing.T) {
	cases := []struct {
		name    string
		initial string
		images  []v1alpha1.ImageRef
		want    string
	}{
		{
			name:    "full-name entry updated in place",
			initial: "images:\n- name: ghcr.io/myorg/myapp\n  newTag: v1\n",
			images:  []v1alpha1.ImageRef{{Repository: "ghcr.io/myorg/myapp", Tag: "v2"}},
			want:    "images:\n- name: ghcr.io/myorg/myapp\n  newTag: v2\n",
		},
		{
			name:    "short-name placeholder gets newName and a full-name entry is added",
			initial: "images:\n- name: myapp\n  newTag: v1\n",
			images:  []v1alpha1.ImageRef{{Repository: "ghcr.io/myorg/myapp", Tag: "v2"}},
			want:    "images:\n- name: myapp\n  newTag: v2\n  newName: ghcr.io/myorg/myapp\n- name: ghcr.io/myorg/myapp\n  newTag: v2\n",
		},
		{
			name:    "legacy kardinal entry is kept and a full-name entry is added",
			initial: "images:\n- name: myapp\n  newName: ghcr.io/myorg/myapp\n  newTag: v1\n",
			images:  []v1alpha1.ImageRef{{Repository: "ghcr.io/myorg/myapp", Tag: "v2"}},
			want:    "images:\n- name: myapp\n  newName: ghcr.io/myorg/myapp\n  newTag: v2\n- name: ghcr.io/myorg/myapp\n  newTag: v2\n",
		},
		{
			name:    "short-name entry shared by two Bundle images is left alone",
			initial: "images:\n- name: app\n  newTag: v1\n",
			images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/a/app", Tag: "1"},
				{Repository: "ghcr.io/b/app", Tag: "2"},
			},
			want: "images:\n- name: app\n  newTag: v1\n- name: ghcr.io/a/app\n  newTag: \"1\"\n- name: ghcr.io/b/app\n  newTag: \"2\"\n",
		},
		{
			name:    "same short name, two repositories",
			initial: "resources:\n- deployment.yaml\n",
			images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/a/app", Tag: "1"},
				{Repository: "ghcr.io/b/app", Tag: "2"},
			},
			want: "resources:\n- deployment.yaml\nimages:\n- name: ghcr.io/a/app\n  newTag: \"1\"\n- name: ghcr.io/b/app\n  newTag: \"2\"\n",
		},
		{
			name:    "short-name entry pointing elsewhere is left alone",
			initial: "images:\n- name: app\n  newName: ghcr.io/other/app\n  newTag: v1\n",
			images:  []v1alpha1.ImageRef{{Repository: "ghcr.io/myorg/app", Tag: "v2"}},
			want:    "images:\n- name: app\n  newName: ghcr.io/other/app\n  newTag: v1\n- name: ghcr.io/myorg/app\n  newTag: v2\n",
		},
		{
			name:    "digest replaces tag",
			initial: "images:\n- name: ghcr.io/myorg/myapp\n  newTag: v1\n",
			images:  []v1alpha1.ImageRef{{Repository: "ghcr.io/myorg/myapp", Digest: "sha256:abc"}},
			want:    "images:\n- name: ghcr.io/myorg/myapp\n  digest: sha256:abc\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			envPath := filepath.Join(workDir, "environments", "prod")
			writeKustomization(t, envPath, tc.initial)
			result, err := mustLookup(t, "kustomize-set-image").Execute(context.Background(),
				makeKustomizeState(workDir, "prod", tc.images))
			require.NoError(t, err)
			assert.Equal(t, parentsteps.StepSuccess, result.Status)
			assert.Equal(t, tc.want, readKustomization(t, envPath))
		})
	}
}

// TestKustomizeSetImage_KustomizationFileNames verifies every file name
// kustomize accepts is edited in place, and a directory without one fails
// (C05-steps-16).
func TestKustomizeSetImage_KustomizationFileNames(t *testing.T) {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		t.Run(name, func(t *testing.T) {
			workDir := t.TempDir()
			envPath := filepath.Join(workDir, "environments", "prod")
			require.NoError(t, os.MkdirAll(envPath, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(envPath, name), []byte("kind: Kustomization\n"), 0o644))
			result, err := mustLookup(t, "kustomize-set-image").Execute(context.Background(),
				makeKustomizeState(workDir, "prod", []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "v1"}}))
			require.NoError(t, err)
			assert.Equal(t, parentsteps.StepSuccess, result.Status)
			entries, err := os.ReadDir(envPath)
			require.NoError(t, err)
			assert.Len(t, entries, 1, "no second kustomization file may be created")
			data, err := os.ReadFile(filepath.Join(envPath, name))
			require.NoError(t, err)
			assert.Contains(t, string(data), "newTag: v1")
		})
	}
	t.Run("none", func(t *testing.T) {
		workDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(workDir, "environments", "prod"), 0o755))
		result, err := mustLookup(t, "kustomize-set-image").Execute(context.Background(),
			makeKustomizeState(workDir, "prod", []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "v1"}}))
		assert.Error(t, err)
		assert.Equal(t, parentsteps.StepFailed, result.Status)
		assert.Contains(t, result.Message, "no kustomization file")
	})
}

// TestKustomizeSetImage_KeepsComments verifies comments and key order survive
// the edit (C05-steps-31).
func TestKustomizeSetImage_KeepsComments(t *testing.T) {
	workDir := t.TempDir()
	envPath := filepath.Join(workDir, "environments", "prod")
	initial := "# prod overlay\nnamespace: prod # keep me\nresources:\n- ../../base\nimages:\n- name: ghcr.io/o/app # the app\n  newTag: v1\npatches:\n- path: patch.yaml\n"
	writeKustomization(t, envPath, initial)
	_, err := mustLookup(t, "kustomize-set-image").Execute(context.Background(),
		makeKustomizeState(workDir, "prod", []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "v2"}}))
	require.NoError(t, err)
	assert.Equal(t, strings.Replace(initial, "newTag: v1", "newTag: v2", 1), readKustomization(t, envPath))
}

// TestEnvPathConfinement verifies environments[].path and a symlinked env dir
// cannot make a step write outside the checkout (C05-steps-13).
func TestEnvPathConfinement(t *testing.T) {
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "v1"}}
	cases := []struct {
		name  string
		path  string
		setup func(t *testing.T, workDir, outside string)
	}{
		{name: "dot-dot", path: "../victim"},
		{name: "nested dot-dot", path: "environments/../../victim"},
		{name: "absolute", path: "/tmp/victim"},
		{name: "symlink escape", path: "environments/prod", setup: func(t *testing.T, workDir, outside string) {
			require.NoError(t, os.MkdirAll(filepath.Join(workDir, "environments"), 0o755))
			require.NoError(t, os.Symlink(outside, filepath.Join(workDir, "environments", "prod")))
		}},
	}
	for _, step := range []string{"kustomize-set-image", "helm-set-image"} {
		for _, tc := range cases {
			t.Run(step+"/"+tc.name, func(t *testing.T) {
				base := t.TempDir()
				workDir := filepath.Join(base, "work")
				outside := filepath.Join(base, "victim")
				require.NoError(t, os.MkdirAll(workDir, 0o755))
				writeKustomization(t, outside, "kind: Kustomization\n")
				require.NoError(t, os.WriteFile(filepath.Join(outside, "values.yaml"), []byte("image:\n  tag: old\n"), 0o644))
				if tc.setup != nil {
					tc.setup(t, workDir, outside)
				}
				state := makeKustomizeState(workDir, "prod", images)
				state.Environment.Path = tc.path

				result, err := mustLookup(t, step).Execute(context.Background(), state)
				assert.Error(t, err)
				assert.Equal(t, parentsteps.StepFailed, result.Status)
				assert.Equal(t, "kind: Kustomization\n", readKustomization(t, outside), "outside kustomization must be untouched")
				values, readErr := os.ReadFile(filepath.Join(outside, "values.yaml"))
				require.NoError(t, readErr)
				assert.Equal(t, "image:\n  tag: old\n", string(values), "outside values must be untouched")
			})
		}
	}
	t.Run("helm valuesFile dot-dot", func(t *testing.T) {
		base := t.TempDir()
		workDir := filepath.Join(base, "work")
		require.NoError(t, os.MkdirAll(filepath.Join(workDir, "environments", "prod"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(base, "values.yaml"), []byte("image:\n  tag: old\n"), 0o644))
		state := makeKustomizeState(workDir, "prod", images)
		state.Environment.Update.Helm = &v1alpha1.HelmUpdateConfig{ValuesFile: "../../../values.yaml"}
		result, err := mustLookup(t, "helm-set-image").Execute(context.Background(), state)
		assert.Error(t, err)
		assert.Equal(t, parentsteps.StepFailed, result.Status)
		values, readErr := os.ReadFile(filepath.Join(base, "values.yaml"))
		require.NoError(t, readErr)
		assert.Equal(t, "image:\n  tag: old\n", string(values))
	})
}

// TestHelmSetImage_ImageShapes verifies digests are pinned and Bundles the
// single-value path template cannot express fail loudly (C05-steps-17).
func TestHelmSetImage_ImageShapes(t *testing.T) {
	cases := []struct {
		name    string
		images  []v1alpha1.ImageRef
		values  string
		want    string
		wantMsg string
	}{
		{name: "tag", images: []v1alpha1.ImageRef{{Repository: "r/app", Tag: "v2"}},
			values: "image:\n  repository: r/app # repo\n  tag: v1\n", want: "image:\n  repository: r/app # repo\n  tag: v2\n"},
		{name: "tag and digest", images: []v1alpha1.ImageRef{{Repository: "r/app", Tag: "v2", Digest: "sha256:abc"}},
			values: "image:\n  tag: v1\n", want: "image:\n  tag: v2@sha256:abc\n"},
		{name: "digest only", images: []v1alpha1.ImageRef{{Repository: "r/app", Digest: "sha256:abc"}},
			values: "image:\n  tag: v1\n", wantMsg: "digest but no tag"},
		{name: "two images", images: []v1alpha1.ImageRef{{Repository: "r/a", Tag: "1"}, {Repository: "r/b", Tag: "2"}},
			values: "image:\n  tag: v1\n", wantMsg: "has 2 images"},
		{name: "path through a scalar", images: []v1alpha1.ImageRef{{Repository: "r/app", Tag: "v2"}},
			values: "image: r/app:v1\n", wantMsg: "image is not a mapping"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			envPath := filepath.Join(workDir, "environments", "prod")
			require.NoError(t, os.MkdirAll(envPath, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(envPath, "values.yaml"), []byte(tc.values), 0o644))
			result, err := mustLookup(t, "helm-set-image").Execute(context.Background(),
				makeKustomizeState(workDir, "prod", tc.images))
			got, readErr := os.ReadFile(filepath.Join(envPath, "values.yaml"))
			require.NoError(t, readErr)
			if tc.wantMsg != "" {
				assert.Error(t, err)
				assert.Equal(t, parentsteps.StepFailed, result.Status)
				assert.Contains(t, result.Message, tc.wantMsg)
				assert.Equal(t, tc.values, string(got), "values must be untouched on failure")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, parentsteps.StepSuccess, result.Status)
			assert.Equal(t, tc.want, string(got))
		})
	}
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
