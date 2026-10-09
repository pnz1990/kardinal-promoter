// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

const internalValues = "image:\n  tag: \"1.0.0\"\n"

// yamlTwoFiles is a yaml-update of two files, a.yaml and b.yaml.
func yamlTwoFiles(t *testing.T) (*parentsteps.StepState, string) {
	t.Helper()
	dir := t.TempDir()
	env := filepath.Join(dir, "environments", "prod")
	require.NoError(t, os.MkdirAll(env, 0o755))
	for _, f := range []string{"a.yaml", "b.yaml"} {
		require.NoError(t, os.WriteFile(filepath.Join(env, f), []byte(internalValues), 0o644))
	}
	return &parentsteps.StepState{
		WorkDir: dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod", Update: v1alpha1.UpdateConfig{Strategy: "yaml",
			YAML: &v1alpha1.YAMLUpdateConfig{Updates: []v1alpha1.YAMLUpdate{
				{File: "a.yaml", Path: "image.tag"}, {File: "b.yaml", Path: "image.tag"}}}}},
		Bundle:  v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: "r/app", Tag: "2.0.0"}}},
		Outputs: map[string]string{},
	}, env
}

func envFiles(t *testing.T, env string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(env)
	require.NoError(t, err)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(env, e.Name()))
		require.NoError(t, err)
		out[e.Name()] = string(b)
	}
	return out
}

// TestYAMLUpdate_RenameFailureRollsBack: when the rename of the second file
// fails, the first, already replaced, gets its old content back and no
// temporary file is left: the checkout is never half edited.
func TestYAMLUpdate_RenameFailureRollsBack(t *testing.T) {
	state, env := yamlTwoFiles(t)
	orig := renameInRoot
	t.Cleanup(func() { renameInRoot = orig })
	renameInRoot = func(root *os.Root, from, to string) error {
		if filepath.Base(to) == "b.yaml" {
			return errors.New("injected rename failure")
		}
		return orig(root, from, to)
	}
	res, err := (&yamlUpdateStep{}).Execute(context.Background(), state)
	require.Error(t, err)
	assert.Contains(t, res.Message, "injected rename failure")
	assert.Equal(t, map[string]string{"a.yaml": internalValues, "b.yaml": internalValues}, envFiles(t, env),
		"a.yaml is restored and no temporary file is left")
}

// TestYAMLUpdate_ReparseCheck: an encoded file that does not parse back to
// one mapping, or no longer holds a value where it was set, fails the step
// before anything is written.
func TestYAMLUpdate_ReparseCheck(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		wantMsg string
	}{
		{"not YAML", "image: [\n", "re-parse"},
		{"two documents", "image:\n  tag: \"2.0.0\"\n---\nkind: Secret\n", "more than one YAML document"},
		{"value lost", "image:\n  tag: \"1.0.0\"\n", `image.tag is "1.0.0", not "2.0.0"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, env := yamlTwoFiles(t)
			orig := encodeYAMLDoc
			t.Cleanup(func() { encodeYAMLDoc = orig })
			encodeYAMLDoc = func(*yamlDoc) ([]byte, error) { return []byte(tt.encoded), nil }
			res, err := (&yamlUpdateStep{}).Execute(context.Background(), state)
			require.Error(t, err)
			assert.Contains(t, res.Message, tt.wantMsg)
			assert.Equal(t, map[string]string{"a.yaml": internalValues, "b.yaml": internalValues}, envFiles(t, env))
		})
	}
}
