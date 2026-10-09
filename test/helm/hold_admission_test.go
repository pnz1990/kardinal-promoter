// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
)

const holdController = "system:serviceaccount:" + releaseNS + ":kardinal-promoter"

// holdWritesPolicy is the rendered hold-writes policy and its binding.
func holdWritesPolicy(t *testing.T, args ...string) (*admissionregistrationv1.ValidatingAdmissionPolicy, *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	t.Helper()
	var vap *admissionregistrationv1.ValidatingAdmissionPolicy
	var binding *admissionregistrationv1.ValidatingAdmissionPolicyBinding
	for _, d := range render(t, "kardinal-promoter", args...) {
		if d.Name != "kardinal-promoter-hold-writes" {
			continue
		}
		switch d.Kind {
		case "ValidatingAdmissionPolicy":
			vap = &admissionregistrationv1.ValidatingAdmissionPolicy{}
			decodeStrict(t, d, vap)
		case "ValidatingAdmissionPolicyBinding":
			binding = &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
			decodeStrict(t, d, binding)
		}
	}
	require.NotNil(t, vap, "hold-writes policy rendered")
	require.NotNil(t, binding, "hold-writes binding rendered")
	return vap, binding
}

// holdAdmits evaluates the policy as the API server would for a CREATE
// (oldObj nil) or UPDATE of a Pipeline by user, with mayHold the answer of
// the authorizer for update pipelines/hold (the CEL authorizer is the API
// server's; cel-go has none). It returns the messages of the failed
// validations.
func holdAdmits(t *testing.T, vap *admissionregistrationv1.ValidatingAdmissionPolicy, user string, mayHold bool,
	oldHolds, newHolds []interface{}) []string {
	t.Helper()
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
		cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType))
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
	pipeline := func(holds []interface{}) map[string]interface{} {
		spec := map[string]interface{}{"environments": []interface{}{}}
		if holds != nil {
			spec["holds"] = holds
		}
		return map[string]interface{}{"metadata": map[string]interface{}{"name": "app", "namespace": "team-a"}, "spec": spec}
	}
	op := "UPDATE"
	var old interface{} = pipeline(oldHolds)
	if oldHolds == nil {
		op, old = "CREATE", nil
	}
	variables := map[string]interface{}{}
	vars := map[string]interface{}{
		"object": pipeline(newHolds), "oldObject": old, "variables": variables,
		"request": map[string]interface{}{"operation": op, "userInfo": map[string]interface{}{"username": user}},
	}
	for _, v := range vap.Spec.Variables {
		if v.Name == "mayHold" {
			assert.Contains(t, v.Expression, "subresource('hold')")
			assert.Contains(t, v.Expression, "check('update')")
			variables[v.Name] = mayHold
			continue
		}
		variables[v.Name] = eval(v.Expression, vars)
	}
	var failed []string
	for _, v := range vap.Spec.Validations {
		if ok, _ := eval(v.Expression, vars).(bool); !ok {
			failed = append(failed, v.Expression)
		}
	}
	return failed
}

// TestHelmTemplateHoldWritesPolicy (#1528 QA): changing Pipeline spec.holds
// needs update on pipelines/hold, new entries name the caller in createdBy
// and have a createdAt (an expiresAt after it), entries are not edited in
// place, the controller is exempt, and other Pipeline edits pass.
func TestHelmTemplateHoldWritesPolicy(t *testing.T) {
	vap, binding := holdWritesPolicy(t)
	rule := vap.Spec.MatchConstraints.ResourceRules[0]
	assert.ElementsMatch(t, []admissionregistrationv1.OperationType{"CREATE", "UPDATE"}, rule.Operations)
	assert.Equal(t, []string{"pipelines"}, rule.Resources)
	assert.Equal(t, admissionregistrationv1.Fail, *vap.Spec.FailurePolicy)
	assert.Equal(t, []admissionregistrationv1.ValidationAction{"Deny"}, binding.Spec.ValidationActions)
	assert.Nil(t, binding.Spec.MatchResources)
	_, nsBinding := holdWritesPolicy(t, "--namespace", "team-a", "--set", "controller.watchNamespace=team-a")
	assert.Equal(t, "team-a", nsBinding.Spec.MatchResources.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"])

	hold := func(env, by string, extra ...string) map[string]interface{} {
		h := map[string]interface{}{"environment": env, "bundle": "app-rollback-1", "reason": "INC-42",
			"createdAt": "2026-10-09T10:00:00Z"}
		if by != "" {
			h["createdBy"] = by
		}
		for i := 0; i+1 < len(extra); i += 2 {
			if extra[i+1] == "" {
				delete(h, extra[i])
				continue
			}
			h[extra[i]] = extra[i+1]
		}
		return h
	}
	list := func(hs ...map[string]interface{}) []interface{} {
		out := []interface{}{}
		for _, h := range hs {
			out = append(out, h)
		}
		return out
	}
	tests := []struct {
		name       string
		user       string
		mayHold    bool
		old, new   []interface{}
		wantFailed int
	}{
		{name: "alice holds with pipelines/hold", user: "alice", mayHold: true,
			old: list(), new: list(hold("prod", "alice"))},
		{name: "alice releases with pipelines/hold", user: "alice", mayHold: true,
			old: list(hold("prod", "bob")), new: list()},
		{name: "plain update on the Pipeline cannot hold", user: "alice",
			old: list(), new: list(hold("prod", "alice")), wantFailed: 1},
		{name: "plain update on the Pipeline cannot release", user: "alice",
			old: list(hold("prod", "bob")), new: list(), wantFailed: 1},
		{name: "plain update of other fields passes", user: "alice",
			old: list(hold("prod", "bob")), new: list(hold("prod", "bob"))},
		{name: "createdBy must be the caller", user: "alice", mayHold: true,
			old: list(), new: list(hold("prod", "bob")), wantFailed: 1},
		{name: "createdBy is required", user: "alice", mayHold: true,
			old: list(), new: list(hold("prod", "")), wantFailed: 1},
		{name: "an entry is not edited in place", user: "alice", mayHold: true,
			old: list(hold("prod", "bob")), new: list(hold("prod", "alice", "reason", "changed")), wantFailed: 1},
		{name: "createdAt is required", user: "alice", mayHold: true,
			old: list(), new: list(hold("prod", "alice", "createdAt", "")), wantFailed: 1},
		{name: "expiresAt after createdAt", user: "alice", mayHold: true,
			old: list(), new: list(hold("prod", "alice", "expiresAt", "2026-10-10T10:00:00Z"))},
		{name: "expiresAt before createdAt", user: "alice", mayHold: true,
			old: list(), new: list(hold("prod", "alice", "expiresAt", "2026-10-08T10:00:00Z")), wantFailed: 1},
		{name: "a Pipeline created with a hold needs pipelines/hold", user: "alice",
			old: nil, new: list(hold("prod", "alice")), wantFailed: 1},
		{name: "a Pipeline created without holds passes", user: "alice",
			old: nil, new: nil},
		{name: "the controller removes an expired hold and writes UI holds", user: holdController,
			old: list(hold("prod", "bob")), new: list(hold("test", "ui-user"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failed := holdAdmits(t, vap, tc.user, tc.mayHold, tc.old, tc.new)
			assert.Len(t, failed, tc.wantFailed, "failed: %q", failed)
		})
	}
}
