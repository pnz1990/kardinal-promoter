// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// crdSchema returns the openAPIV3Schema node at the given property path of the
// first version of a generated CRD in config/crd/bases. A path element "[]"
// descends into array items.
func crdSchema(t *testing.T, file string, path ...string) map[string]interface{} {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "config", "crd", "bases", file))
	require.NoError(t, err)
	var crd map[string]interface{}
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	node := crd["spec"].(map[string]interface{})["versions"].([]interface{})[0].(map[string]interface{})
	node = node["schema"].(map[string]interface{})["openAPIV3Schema"].(map[string]interface{})
	for _, p := range path {
		if p == "[]" {
			node = node["items"].(map[string]interface{})
			continue
		}
		node = node["properties"].(map[string]interface{})[p].(map[string]interface{})
	}
	return node
}

// failingRules evaluates the x-kubernetes-validations rules of a schema node
// against self (a JSON-shaped value) and returns the messages of the rules that
// reject it. It uses cel-go directly with self as dyn, which is sufficient for
// the has()/comparison rules checked here.
func failingRules(t *testing.T, node map[string]interface{}, self map[string]interface{}) []string {
	t.Helper()
	rules, _ := node["x-kubernetes-validations"].([]interface{})
	require.NotEmpty(t, rules, "schema node has no x-kubernetes-validations")
	env, err := cel.NewEnv(cel.Variable("self", cel.DynType))
	require.NoError(t, err)
	var failed []string
	for _, r := range rules {
		rule := r.(map[string]interface{})
		ast, iss := env.Compile(rule["rule"].(string))
		require.NoError(t, iss.Err(), "rule %q", rule["rule"])
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(map[string]interface{}{"self": self})
		require.NoError(t, err, "rule %q", rule["rule"])
		if out.Value() != true {
			failed = append(failed, rule["message"].(string))
		}
	}
	return failed
}

// TestChangeWindowCRDRules verifies the ChangeWindow admission rules: a blackout
// needs start and end, a recurring window needs a schedule, and allowedHours must
// be HH:MM-HH:MM (C04-gates-03).
func TestChangeWindowCRDRules(t *testing.T) {
	spec := crdSchema(t, "kardinal.io_changewindows.yaml", "spec")
	tests := []struct {
		name string
		self map[string]interface{}
		want []string
	}{
		{"blackout ok", map[string]interface{}{"type": "blackout", "start": "2026-12-20T00:00:00Z", "end": "2027-01-02T00:00:00Z"}, nil},
		{"blackout without end", map[string]interface{}{"type": "blackout", "start": "2026-12-20T00:00:00Z"},
			[]string{"a blackout ChangeWindow requires start and end"}},
		{"recurring ok", map[string]interface{}{"type": "recurring", "schedule": map[string]interface{}{"allowedHours": "09:00-17:00"}}, nil},
		{"recurring without schedule", map[string]interface{}{"type": "recurring"},
			[]string{"a recurring ChangeWindow requires schedule"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, failingRules(t, spec, tt.self))
		})
	}

	hours := crdSchema(t, "kardinal.io_changewindows.yaml", "spec", "schedule", "allowedHours")
	re := regexp.MustCompile(hours["pattern"].(string))
	for _, ok := range []string{"09:00-17:00", "22:00-02:00", "00:00-24:00"} {
		assert.True(t, re.MatchString(ok), ok)
	}
	for _, bad := range []string{"9:00-17:00", "09:00-24:30", "24:00-09:00", "09:00", "9am-5pm"} {
		assert.False(t, re.MatchString(bad), bad)
	}
}

// TestPipelineCRDRejectsAutoRollback verifies that environments[].autoRollback,
// which has no implementation, is rejected loudly instead of silently ignored
// (C04-gates-07, C08-api-config-14).
func TestPipelineCRDRejectsAutoRollback(t *testing.T) {
	env := crdSchema(t, "kardinal.io_pipelines.yaml", "spec", "environments", "[]")
	assert.Empty(t, failingRules(t, env, map[string]interface{}{"name": "prod", "onHealthFailure": "rollback"}))
	failed := failingRules(t, env, map[string]interface{}{
		"name": "prod", "autoRollback": map[string]interface{}{"failureThreshold": 3},
	})
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0], "environments[].autoRollback is not implemented")
}
