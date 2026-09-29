// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

type compositeAction struct {
	Inputs  map[string]any `json:"inputs"`
	Outputs map[string]struct {
		Value string `json:"value"`
	} `json:"outputs"`
	Runs struct {
		Steps []struct {
			ID  string            `json:"id"`
			Run string            `json:"run"`
			Env map[string]string `json:"env"`
		} `json:"steps"`
	} `json:"runs"`
}

// TestCreateBundleActionPassesInputsViaEnv checks that no ${{ }} expression is
// expanded inside a run: script, where a hostile input would become code, and
// that every declared output is mapped from a step output.
func TestCreateBundleActionPassesInputsViaEnv(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "actions", "create-bundle", "action.yml"))
	require.NoError(t, err)
	var action compositeAction
	require.NoError(t, yaml.Unmarshal(data, &action))
	require.NotEmpty(t, action.Runs.Steps)

	stepIDs := map[string]bool{}
	for _, s := range action.Runs.Steps {
		assert.NotContains(t, s.Run, "${{", "step %q expands an expression inside its script", s.ID)
		stepIDs[s.ID] = true
	}
	for name := range action.Inputs {
		envName := "INPUT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		found := false
		for _, s := range action.Runs.Steps {
			if s.Env[envName] == "${{ inputs."+name+" }}" {
				found = true
			}
		}
		assert.True(t, found, "input %q is not passed to a step as %s", name, envName)
	}
	require.NotEmpty(t, action.Outputs)
	for name, out := range action.Outputs {
		want := ".outputs." + name + " }}"
		assert.True(t, strings.HasSuffix(out.Value, want), "output %q has no step value mapping: %q", name, out.Value)
		id := strings.TrimSuffix(strings.TrimPrefix(out.Value, "${{ steps."), want)
		assert.True(t, stepIDs[id], "output %q maps from unknown step %q", name, id)
	}
}

// TestCreateBundleActionScript runs the action's shell tests, which drive
// create-bundle.sh against a fake curl.
func TestCreateBundleActionScript(t *testing.T) {
	cmd := exec.Command("bash", filepath.Join(repoRoot(t), ".github", "actions", "create-bundle", "test.sh"))
	cmd.Env = append(os.Environ(), "KUBECONFIG="+os.DevNull)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "PASS: all")
}
