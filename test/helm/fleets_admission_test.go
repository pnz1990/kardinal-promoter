// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
)

// TestFleetsPolicy (#1835): the fleets policy refuses a
// Pipeline whose spec.pausedFleets names an environment without a fleet (a
// paused fleet renamed, removed or made plain), with a message that says to
// resume it first, and admits one whose entries are all fleets.
//
// Covers FLEET-14.
func TestFleetsPolicy(t *testing.T) {
	var vap *admissionregistrationv1.ValidatingAdmissionPolicy
	bound := false
	for _, d := range render(t, "kardinal-promoter") {
		if d.Name != "kardinal-promoter-fleets" {
			continue
		}
		switch d.Kind {
		case "ValidatingAdmissionPolicy":
			vap = &admissionregistrationv1.ValidatingAdmissionPolicy{}
			decodeStrict(t, d, vap)
		case "ValidatingAdmissionPolicyBinding":
			bound = true
		}
	}
	require.NotNil(t, vap, "fleets policy rendered")
	assert.True(t, bound, "fleets binding rendered")
	assert.Equal(t, admissionregistrationv1.Fail, *vap.Spec.FailurePolicy)

	env, err := cel.NewEnv(ext.Strings(), cel.Variable("object", cel.DynType), cel.Variable("variables", cel.DynType))
	require.NoError(t, err)
	eval := func(expr string, vars map[string]interface{}) interface{} {
		ast, iss := env.Compile(expr)
		require.NoError(t, iss.Err(), expr)
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(vars)
		require.NoError(t, err, expr)
		return out.Value()
	}
	fleet := map[string]interface{}{"targets": []interface{}{map[string]interface{}{"name": "a"}}}
	check := func(envs []interface{}, paused []interface{}) []string {
		spec := map[string]interface{}{"environments": envs}
		if paused != nil {
			spec["pausedFleets"] = paused
		}
		vars := map[string]interface{}{"object": map[string]interface{}{"metadata": map[string]interface{}{"name": "app"}, "spec": spec}}
		variables := map[string]interface{}{}
		vars["variables"] = variables
		for _, v := range vap.Spec.Variables {
			variables[v.Name] = eval(v.Expression, vars)
		}
		var msgs []string
		for _, v := range vap.Spec.Validations {
			if eval(v.Expression, vars) != true {
				if v.MessageExpression != "" {
					msgs = append(msgs, eval(v.MessageExpression, vars).(string))
				} else {
					msgs = append(msgs, v.Message)
				}
			}
		}
		return msgs
	}
	envs := []interface{}{map[string]interface{}{"name": "test"}, map[string]interface{}{"name": "prod", "fleet": fleet}}
	assert.Empty(t, check(envs, nil), "no pause")
	assert.Empty(t, check(envs, []interface{}{"prod"}), "a paused fleet")
	assert.Equal(t, []string{"spec.pausedFleets names test, gone, which is not an environment with a fleet: " +
		"resume it first (kardinal resume app --env <name>), then rename or remove the fleet"},
		check(envs, []interface{}{"prod", "test", "gone"}))
}
