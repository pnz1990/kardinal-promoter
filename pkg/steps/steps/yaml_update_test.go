// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

const deploymentManifest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app # the app
spec:
  template:
    spec:
      containers:
        - name: app
          image: ghcr.io/org/app:1.0.0 # pinned by kardinal
        - name: sidecar
          image: ghcr.io/org/sidecar:0.1.0
`

const valuesYAML = `image:
  tag: "1.0.0"
sidecar: {}
`

// yamlState is a step state for environment prod whose directory holds
// files, with update.yaml.updates and the Bundle images given.
func yamlState(t *testing.T, files map[string]string, updates []v1alpha1.YAMLUpdate, images ...v1alpha1.ImageRef) (*parentsteps.StepState, string) {
	t.Helper()
	dir := t.TempDir()
	envDir := filepath.Join(dir, "environments", "prod")
	for name, content := range files {
		p := filepath.Join(envDir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	return &parentsteps.StepState{
		WorkDir: dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod", Update: v1alpha1.UpdateConfig{
			Strategy: "yaml", YAML: &v1alpha1.YAMLUpdateConfig{Updates: updates}}},
		Bundle:  v1alpha1.BundleSpec{Images: images},
		Outputs: map[string]string{},
	}, envDir
}

func readEnvFile(t *testing.T, envDir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(envDir, name))
	require.NoError(t, err)
	return string(b)
}

var (
	appV2     = v1alpha1.ImageRef{Repository: "ghcr.io/org/app", Tag: "2.0.0", Digest: "sha256:abc"}
	sidecarV2 = v1alpha1.ImageRef{Repository: "ghcr.io/org/sidecar", Tag: "0.2.0"}
)

// TestYAMLUpdate_SeveralFilesAndPaths (#1448): one step sets list-indexed and
// nested paths in several files, from the image each update names, keeping
// comments and creating missing mapping keys.
func TestYAMLUpdate_SeveralFilesAndPaths(t *testing.T) {
	state, envDir := yamlState(t, map[string]string{"deploy/deployment.yaml": deploymentManifest, "values.yaml": valuesYAML},
		[]v1alpha1.YAMLUpdate{
			{File: "deploy/deployment.yaml", Path: "spec.template.spec.containers[0].image", Image: "ghcr.io/org/app", Value: "image"},
			{File: "deploy/deployment.yaml", Path: "spec.template.spec.containers[1].image", Image: "ghcr.io/org/sidecar", Value: "image"},
			{File: "values.yaml", Path: "image.tag", Image: "ghcr.io/org/app"},
			{File: "values.yaml", Path: "image.digest", Image: "ghcr.io/org/app", Value: "digest"},
			{File: "values.yaml", Path: "sidecar.ref", Image: "ghcr.io/org/app", Value: "imageWithDigest"},
			{File: "values.yaml", Path: "pinned", Image: "ghcr.io/org/app", Value: "tagWithDigest"},
		}, appV2, sidecarV2)
	res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
	require.NoError(t, err)
	require.Equal(t, parentsteps.StepSuccess, res.Status, res.Message)

	dep := readEnvFile(t, envDir, "deploy/deployment.yaml")
	assert.Contains(t, dep, "image: ghcr.io/org/app:2.0.0 # pinned by kardinal", "comment kept")
	assert.Contains(t, dep, "image: ghcr.io/org/sidecar:0.2.0")
	assert.Contains(t, dep, "name: app # the app")
	vals := readEnvFile(t, envDir, "values.yaml")
	assert.Contains(t, vals, `tag: "2.0.0"`, "quoting style kept")
	assert.Contains(t, vals, "digest: sha256:abc")
	assert.Contains(t, vals, "ref: ghcr.io/org/app:2.0.0@sha256:abc")
	assert.Contains(t, vals, "pinned: 2.0.0@sha256:abc")
	assert.Equal(t, "environments/prod/deploy/deployment.yaml,environments/prod/values.yaml", res.Outputs["yamlFiles"])

	// Idempotent.
	res, err = mustLookup(t, "yaml-update").Execute(context.Background(), state)
	require.NoError(t, err)
	require.Equal(t, parentsteps.StepSuccess, res.Status)
	assert.Equal(t, dep, readEnvFile(t, envDir, "deploy/deployment.yaml"))
	assert.Equal(t, vals, readEnvFile(t, envDir, "values.yaml"))
}

// TestYAMLUpdate_FailsWithoutWriting: an edit that cannot be applied fails
// the step for good, and no file is written, the valid edits before it
// included.
func TestYAMLUpdate_FailsWithoutWriting(t *testing.T) {
	good := v1alpha1.YAMLUpdate{File: "values.yaml", Path: "image.tag"}
	tests := []struct {
		name    string
		bad     v1alpha1.YAMLUpdate
		images  []v1alpha1.ImageRef
		wantMsg string
	}{
		{"unknown image", v1alpha1.YAMLUpdate{File: "values.yaml", Path: "x", Image: "ghcr.io/org/nope"},
			[]v1alpha1.ImageRef{appV2}, "the Bundle has no image ghcr.io/org/nope"},
		{"ambiguous image", v1alpha1.YAMLUpdate{File: "values.yaml", Path: "x"},
			[]v1alpha1.ImageRef{appV2, sidecarV2}, "the Bundle has 2 images: set image"},
		{"missing digest", v1alpha1.YAMLUpdate{File: "values.yaml", Path: "x", Value: "digest"},
			[]v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "2.0.0"}}, "has no digest"},
		{"missing list element", v1alpha1.YAMLUpdate{File: "deploy/deployment.yaml", Path: "spec.template.spec.containers[5].image"},
			[]v1alpha1.ImageRef{appV2}, "containers has 2 elements, no [5]"},
		{"path through a scalar", v1alpha1.YAMLUpdate{File: "values.yaml", Path: "image.tag.deeper"},
			[]v1alpha1.ImageRef{appV2}, "not a mapping"},
		{"replacing a mapping", v1alpha1.YAMLUpdate{File: "values.yaml", Path: "image"},
			[]v1alpha1.ImageRef{appV2}, "image is not a scalar"},
		{"index into a mapping", v1alpha1.YAMLUpdate{File: "values.yaml", Path: "image[0]"},
			[]v1alpha1.ImageRef{appV2}, "is not a list"},
		{"escaping file", v1alpha1.YAMLUpdate{File: "../../../etc/passwd", Path: "x"},
			[]v1alpha1.ImageRef{appV2}, "must stay inside the repository"},
		{"bad path", v1alpha1.YAMLUpdate{File: "values.yaml", Path: "a..b"},
			[]v1alpha1.ImageRef{appV2}, "invalid path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, envDir := yamlState(t, map[string]string{"deploy/deployment.yaml": deploymentManifest, "values.yaml": valuesYAML},
				[]v1alpha1.YAMLUpdate{good, tt.bad}, tt.images...)
			res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
			require.Error(t, err)
			assert.True(t, errors.Is(err, parentsteps.ErrPermanent), "%v is permanent", err)
			assert.Equal(t, parentsteps.StepFailed, res.Status)
			assert.Contains(t, res.Message, tt.wantMsg)
			assert.Equal(t, valuesYAML, readEnvFile(t, envDir, "values.yaml"), "nothing written")
			assert.Equal(t, deploymentManifest, readEnvFile(t, envDir, "deploy/deployment.yaml"), "nothing written")
		})
	}
}

// TestYAMLUpdate_NoImagesAndMissingConfig: a Bundle without images changes
// nothing; a yaml strategy without updates fails for good.
func TestYAMLUpdate_NoImagesAndMissingConfig(t *testing.T) {
	state, envDir := yamlState(t, map[string]string{"values.yaml": valuesYAML},
		[]v1alpha1.YAMLUpdate{{File: "values.yaml", Path: "image.tag"}})
	res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, "no images to update", res.Message)
	assert.Equal(t, valuesYAML, readEnvFile(t, envDir, "values.yaml"))

	state.Environment.Update.YAML = nil
	state.Bundle.Images = []v1alpha1.ImageRef{appV2}
	_, err = mustLookup(t, "yaml-update").Execute(context.Background(), state)
	require.Error(t, err)
	assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
}

// TestDefaultSequence_YAML: update.strategy yaml runs yaml-update between
// git-clone and git-commit, after config-merge for a mixed Bundle.
func TestDefaultSequence_YAML(t *testing.T) {
	assert.Equal(t, []string{"git-clone", "yaml-update", "git-commit", "git-push", "health-check"},
		parentsteps.DefaultSequenceForBundle("auto", "image", "yaml", ""))
	assert.Equal(t, []string{"git-clone", "config-merge", "yaml-update", "git-commit", "git-push", "open-pr", "wait-for-merge", "health-check"},
		parentsteps.DefaultSequenceForBundle("pr-review", "mixed", "yaml", ""))
}

// TestYAMLUpdate_RefusesUnsafeInput covers the QA findings on #1498: a
// second YAML document, an anchored or aliased value, a symbolic link, an
// absolute path, a file over 4 MiB. Each fails for good and changes nothing.
func TestYAMLUpdate_RefusesUnsafeInput(t *testing.T) {
	big := "image:\n  tag: \"1.0.0\"\npad: \"" + strings.Repeat("x", 4<<20) + "\"\n"
	tests := []struct {
		name    string
		files   map[string]string
		update  v1alpha1.YAMLUpdate
		link    bool
		wantMsg string
	}{
		{"second document", map[string]string{"values.yaml": valuesYAML + "---\nkind: Secret\n"},
			v1alpha1.YAMLUpdate{File: "values.yaml", Path: "image.tag"}, false, "more than one YAML document"},
		{"anchored value", map[string]string{"values.yaml": "base: &tag \"1.0.0\"\nimage:\n  tag: *tag\n"},
			v1alpha1.YAMLUpdate{File: "values.yaml", Path: "image.tag"}, false, "anchors and aliases"},
		{"anchored mapping", map[string]string{"values.yaml": "image: &img\n  tag: \"1.0.0\"\nother: *img\n"},
			v1alpha1.YAMLUpdate{File: "values.yaml", Path: "image.tag"}, false, "anchors and aliases"},
		{"absolute path", map[string]string{"values.yaml": valuesYAML},
			v1alpha1.YAMLUpdate{File: "/etc/values.yaml", Path: "image.tag"}, false, "must be relative"},
		{"file over 4 MiB", map[string]string{"values.yaml": big},
			v1alpha1.YAMLUpdate{File: "values.yaml", Path: "image.tag"}, false, "larger than 4 MiB"},
		{"symbolic link", map[string]string{"values.yaml": valuesYAML},
			v1alpha1.YAMLUpdate{File: "link.yaml", Path: "image.tag"}, true, "is a symbolic link"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, envDir := yamlState(t, tt.files, []v1alpha1.YAMLUpdate{tt.update}, appV2)
			if tt.link {
				require.NoError(t, os.Symlink("values.yaml", filepath.Join(envDir, "link.yaml")))
			}
			res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
			require.Error(t, err)
			assert.True(t, errors.Is(err, parentsteps.ErrPermanent), "%v is permanent", err)
			assert.Contains(t, res.Message, tt.wantMsg)
			for name, content := range tt.files {
				assert.Equal(t, content, readEnvFile(t, envDir, name), "%s unchanged", name)
			}
		})
	}
}

// TestYAMLUpdate_NoTempFilesLeft: the atomic write leaves only the edited
// files behind.
func TestYAMLUpdate_NoTempFilesLeft(t *testing.T) {
	state, envDir := yamlState(t, map[string]string{"values.yaml": valuesYAML, "deploy/deployment.yaml": deploymentManifest},
		[]v1alpha1.YAMLUpdate{{File: "values.yaml", Path: "image.tag"}, {File: "deploy/deployment.yaml",
			Path: "spec.template.spec.containers[0].image", Value: "image"}}, appV2)
	_, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
	require.NoError(t, err)
	var names []string
	require.NoError(t, filepath.WalkDir(envDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, filepath.Base(p))
		}
		return err
	}))
	assert.ElementsMatch(t, []string{"values.yaml", "deployment.yaml"}, names)
}
