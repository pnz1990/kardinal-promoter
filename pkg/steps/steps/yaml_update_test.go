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

// TestYAMLEdits_TrailingEmptyDocument: a file ending with "---", or with an
// empty or null document after it, is one document. kustomize-set-image,
// helm-set-image and yaml-update edit it (QA round 3 on #1498: refusing it
// broke files that worked before); a second document with content is still
// refused.
func TestYAMLEdits_TrailingEmptyDocument(t *testing.T) {
	img := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "2.0.0"}}
	for _, trailer := range []string{"---\n", "---\n---\n", "--- null\n", "---\n...\n"} {
		t.Run(trailer, func(t *testing.T) {
			workDir := t.TempDir()
			envPath := filepath.Join(workDir, "environments", "prod")
			writeKustomization(t, envPath, "kind: Kustomization\n"+trailer)
			_, err := mustLookup(t, "kustomize-set-image").Execute(context.Background(), makeKustomizeState(workDir, "prod", img))
			require.NoError(t, err)
			assert.Contains(t, readKustomization(t, envPath), "newTag: 2.0.0")

			require.NoError(t, os.WriteFile(filepath.Join(envPath, "values.yaml"), []byte("image:\n  tag: v1\n"+trailer), 0o644))
			_, err = mustLookup(t, "helm-set-image").Execute(context.Background(), makeKustomizeState(workDir, "prod", img))
			require.NoError(t, err)
			assert.Equal(t, "image:\n  tag: 2.0.0\n", readEnvFile(t, envPath, "values.yaml"))

			state, envDir := yamlState(t, map[string]string{"values.yaml": valuesYAML + trailer},
				[]v1alpha1.YAMLUpdate{{File: "values.yaml", Path: "image.tag"}}, appV2)
			_, err = mustLookup(t, "yaml-update").Execute(context.Background(), state)
			require.NoError(t, err)
			assert.Contains(t, readEnvFile(t, envDir, "values.yaml"), `tag: "2.0.0"`)
		})
	}
	for _, trailer := range []string{
		"---\nkind: Secret\n", "---\n- a\n",
		// A comment-only document followed by one with content.
		"---\n# x\n---\nb: 2\n", "--- # x\n---\n- a\n",
	} {
		state, envDir := yamlState(t, map[string]string{"values.yaml": valuesYAML + trailer},
			[]v1alpha1.YAMLUpdate{{File: "values.yaml", Path: "image.tag"}}, appV2)
		res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
		require.Error(t, err, trailer)
		assert.Contains(t, res.Message, "more than one YAML document")
		assert.Equal(t, valuesYAML+trailer, readEnvFile(t, envDir, "values.yaml"))
	}
}

// TestYAMLEdits_TrailingComments: comments after the document, after a
// trailing "---" or on it ("--- # end"), and a "---" inside a block scalar do
// not make the file a multi-document stream. The edit succeeds and every
// comment is still in the file (QA on #1498: refusing them broke files that
// worked on main).
func TestYAMLEdits_TrailingComments(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		comments []string
	}{
		{"comment after a trailing separator", valuesYAML + "---\n# end\n", []string{"# end"}},
		{"comment on a trailing separator", valuesYAML + "--- # end\n", []string{"# end"}},
		{"comment-only last document", valuesYAML + "---\n# a comment only\n", []string{"# a comment only"}},
		{"empty document, then a comment-only one", valuesYAML + "---\n---\n# end\n", []string{"# end"}},
		{"comment-only document between separators", valuesYAML + "---\n# x\n---\n", []string{"# x"}},
		{"two comment-only documents", valuesYAML + "--- # one\n--- # two\n", []string{"# one", "# two"}},
		{"foot comment and a trailing comment", valuesYAML + "# foot\n--- # end\n", []string{"# foot", "# end"}},
		{"separator inside a block scalar", "image:\n  tag: \"1.0.0\"\nnotes: |\n  a\n  ---\n  # not a comment\n  b\n---\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, envDir := yamlState(t, map[string]string{"values.yaml": tt.content},
				[]v1alpha1.YAMLUpdate{{File: "values.yaml", Path: "image.tag"}}, appV2)
			res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
			require.NoError(t, err, res.Message)
			got := readEnvFile(t, envDir, "values.yaml")
			assert.Contains(t, got, `tag: "2.0.0"`)
			for _, c := range tt.comments {
				assert.Contains(t, got, c)
			}
			if strings.Contains(tt.name, "block scalar") {
				assert.Contains(t, got, "notes: |\n  a\n  ---\n  # not a comment\n  b\n")
			}
		})
	}
}

// TestYAMLUpdate_RefusesSymlinkedDirectories: a symbolic link anywhere on
// the path, the environment directory or a directory in update.yaml.file,
// fails for good and nothing is written through it, also when the link stays
// inside the checkout.
func TestYAMLUpdate_RefusesSymlinkedDirectories(t *testing.T) {
	t.Run("environment directory", func(t *testing.T) {
		state, envDir := yamlState(t, map[string]string{"values.yaml": valuesYAML},
			[]v1alpha1.YAMLUpdate{{File: "values.yaml", Path: "image.tag"}}, appV2)
		// environments/staging is a link to environments/prod.
		require.NoError(t, os.Symlink("prod", filepath.Join(filepath.Dir(envDir), "staging")))
		state.Environment.Name = "staging"
		res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
		require.Error(t, err)
		assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
		assert.Contains(t, res.Message, "environments/staging is a symbolic link")
		assert.Equal(t, valuesYAML, readEnvFile(t, envDir, "values.yaml"))
	})
	t.Run("directory in the file path", func(t *testing.T) {
		state, envDir := yamlState(t, map[string]string{"real/values.yaml": valuesYAML},
			[]v1alpha1.YAMLUpdate{{File: "linked/values.yaml", Path: "image.tag"}}, appV2)
		require.NoError(t, os.Symlink("real", filepath.Join(envDir, "linked")))
		res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
		require.Error(t, err)
		assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
		assert.Contains(t, res.Message, "environments/prod/linked is a symbolic link")
		assert.Equal(t, valuesYAML, readEnvFile(t, envDir, "real/values.yaml"))
	})
	t.Run("file path through a file", func(t *testing.T) {
		state, _ := yamlState(t, map[string]string{"values.yaml": valuesYAML},
			[]v1alpha1.YAMLUpdate{{File: "values.yaml/x.yaml", Path: "image.tag"}}, appV2)
		res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
		require.Error(t, err)
		assert.Contains(t, res.Message, "is not a directory")
	})
}

// TestYAMLUpdate_RefusesSiblingAtRuntime: a file that leaves the environment
// directory (../staging/values.yaml) is refused when the step runs, not only
// by the CRD pattern, and the sibling environment is untouched.
func TestYAMLUpdate_RefusesSiblingAtRuntime(t *testing.T) {
	for _, file := range []string{"../staging/values.yaml", "./../staging/values.yaml", "a/../../staging/values.yaml"} {
		state, envDir := yamlState(t, map[string]string{"values.yaml": valuesYAML},
			[]v1alpha1.YAMLUpdate{{File: file, Path: "image.tag"}}, appV2)
		staging := filepath.Join(filepath.Dir(envDir), "staging")
		require.NoError(t, os.MkdirAll(staging, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(staging, "values.yaml"), []byte(valuesYAML), 0o644))
		res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
		require.Error(t, err, file)
		assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
		assert.Contains(t, res.Message, "must stay inside the repository", file)
		b, err := os.ReadFile(filepath.Join(staging, "values.yaml"))
		require.NoError(t, err)
		assert.Equal(t, valuesYAML, string(b), "the sibling environment is untouched")
	}
}

// TestYAMLUpdate_RefusesMergeKeysAndDuplicateKeys: a merge key (<<) on the
// path, or a key that appears twice in one mapping, fails for good: the value
// set might not be the one consumers read.
func TestYAMLUpdate_RefusesMergeKeysAndDuplicateKeys(t *testing.T) {
	tests := []struct {
		name, content, path, wantMsg string
	}{
		{"merge key on the path", "defaults: {tag: \"1.0.0\"}\nimage:\n  <<: {tag: \"0.9.0\"}\n  repo: app\n", "image.tag", "merge keys (<<)"},
		{"merge key at the top", "<<: {image: {tag: \"0.9.0\"}}\nimage:\n  tag: \"1.0.0\"\n", "image.tag", "merge keys (<<)"},
		{"duplicate key", "image:\n  tag: \"1.0.0\"\n  tag: \"1.1.0\"\n", "image.tag", `key "tag" appears twice`},
		{"duplicate key elsewhere", "image:\n  tag: \"1.0.0\"\nother:\n  a: 1\n  a: 2\n", "image.tag", `key "a" appears twice`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, envDir := yamlState(t, map[string]string{"values.yaml": tt.content},
				[]v1alpha1.YAMLUpdate{{File: "values.yaml", Path: tt.path}}, appV2)
			res, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
			require.Error(t, err)
			assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
			assert.Contains(t, res.Message, tt.wantMsg)
			assert.Equal(t, tt.content, readEnvFile(t, envDir, "values.yaml"))
		})
	}
	// A quoted "<<" is an ordinary key.
	state, envDir := yamlState(t, map[string]string{"values.yaml": "image:\n  \"<<\": x\n  tag: \"1.0.0\"\n"},
		[]v1alpha1.YAMLUpdate{{File: "values.yaml", Path: "image.tag"}}, appV2)
	_, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Contains(t, readEnvFile(t, envDir, "values.yaml"), `tag: "2.0.0"`)
}

// TestYAMLUpdate_KeepsModeAndIgnoresPlantedTemp: the edited file keeps its
// mode, and a symbolic link committed at the temporary file's name is
// replaced, not written through (O_EXCL).
func TestYAMLUpdate_KeepsModeAndIgnoresPlantedTemp(t *testing.T) {
	state, envDir := yamlState(t, map[string]string{"values.yaml": valuesYAML},
		[]v1alpha1.YAMLUpdate{{File: "values.yaml", Path: "image.tag"}}, appV2)
	p := filepath.Join(envDir, "values.yaml")
	require.NoError(t, os.Chmod(p, 0o600))
	target := filepath.Join(envDir, "other.yaml")
	require.NoError(t, os.WriteFile(target, []byte("keep: me\n"), 0o644))
	require.NoError(t, os.Symlink("other.yaml", p+".kardinal-tmp"))

	_, err := mustLookup(t, "yaml-update").Execute(context.Background(), state)
	require.NoError(t, err)
	info, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the mode is kept")
	assert.Contains(t, readEnvFile(t, envDir, "values.yaml"), `tag: "2.0.0"`)
	assert.Equal(t, "keep: me\n", readEnvFile(t, envDir, "other.yaml"), "nothing written through the planted link")
	_, err = os.Lstat(p + ".kardinal-tmp")
	assert.True(t, os.IsNotExist(err), "no temporary file left")
}
