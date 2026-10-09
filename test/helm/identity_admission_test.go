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

// identityAdmissionObjects is every admission object the chart renders for
// release (identity-admission.yaml).
func identityAdmissionObjects(release string) []string {
	var out []string
	for _, p := range []string{"bundle-rejection", "gate-overrides"} {
		out = append(out, "ValidatingAdmissionPolicy/"+release+"-"+p, "ValidatingAdmissionPolicyBinding/"+release+"-"+p)
	}
	return out
}

// identityPolicy renders the chart and returns the ValidatingAdmissionPolicy
// named release-suffix, checking its binding denies.
func identityPolicy(t *testing.T, suffix string) *admissionregistrationv1.ValidatingAdmissionPolicy {
	t.Helper()
	docs := render(t, "kardinal-promoter")
	name := "kardinal-promoter-" + suffix
	var vap admissionregistrationv1.ValidatingAdmissionPolicy
	var binding admissionregistrationv1.ValidatingAdmissionPolicyBinding
	foundVAP, foundBinding := false, false
	for _, d := range docs {
		switch {
		case d.Kind == "ValidatingAdmissionPolicy" && d.Name == name:
			decodeStrict(t, d, &vap)
			foundVAP = true
		case d.Kind == "ValidatingAdmissionPolicyBinding" && d.Name == name:
			decodeStrict(t, d, &binding)
			foundBinding = true
		}
	}
	require.True(t, foundVAP && foundBinding, "policy and binding %s", name)
	assert.Equal(t, name, binding.Spec.PolicyName)
	assert.Equal(t, []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}, binding.Spec.ValidationActions)
	require.NotNil(t, vap.Spec.FailurePolicy)
	assert.Equal(t, admissionregistrationv1.Fail, *vap.Spec.FailurePolicy, "an unevaluable identity check denies")
	return &vap
}

// admits evaluates every validation of vap the way the API server binds the
// variables (object, oldObject, request) and reports whether all pass.
func admits(t *testing.T, vap *admissionregistrationv1.ValidatingAdmissionPolicy, object, oldObject map[string]interface{}, user string) bool {
	t.Helper()
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
		cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType))
	require.NoError(t, err)
	vars := map[string]interface{}{
		"object": object, "request": map[string]interface{}{"userInfo": map[string]interface{}{"username": user}},
	}
	if oldObject == nil {
		vars["oldObject"] = nil
	} else {
		vars["oldObject"] = oldObject
	}
	// Variables are evaluated in order, each seeing the ones before it.
	variables := map[string]interface{}{}
	vars["variables"] = variables
	for _, v := range vap.Spec.Variables {
		ast, iss := env.Compile(v.Expression)
		require.NoError(t, iss.Err(), v.Expression)
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(vars)
		require.NoError(t, err, v.Expression)
		variables[v.Name] = out.Value()
	}
	for _, v := range vap.Spec.Validations {
		ast, iss := env.Compile(v.Expression)
		require.NoError(t, iss.Err(), v.Expression)
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(vars)
		require.NoError(t, err, v.Expression)
		if out.Value() != true {
			return false
		}
		if v.MessageExpression != "" {
			mast, iss := env.Compile(v.MessageExpression)
			require.NoError(t, iss.Err(), v.MessageExpression)
			assert.Equal(t, cel.StringType, mast.OutputType())
		}
	}
	return true
}

// TestIdentityAdmission_BundleRejection: a new spec.rejected must name the
// requesting user; Bundles without one, and the existing rejection of a
// Bundle being updated (the CRD keeps it immutable), are not checked.
func TestIdentityAdmission_BundleRejection(t *testing.T) {
	vap := identityPolicy(t, "bundle-rejection")
	rules := vap.Spec.MatchConstraints.ResourceRules
	require.Len(t, rules, 1)
	assert.Equal(t, []string{"bundles"}, rules[0].Resources)
	assert.ElementsMatch(t, []admissionregistrationv1.OperationType{"CREATE", "UPDATE"}, rules[0].Operations)

	bundle := func(by string) map[string]interface{} {
		spec := map[string]interface{}{"type": "image", "pipeline": "app"}
		if by != "" {
			spec["rejected"] = map[string]interface{}{"by": by, "reason": "bad"}
		}
		return map[string]interface{}{"spec": spec}
	}
	tests := []struct {
		name     string
		old, cur map[string]interface{}
		user     string
		want     bool
	}{
		{name: "create without rejection", cur: bundle(""), user: "ci", want: true},
		{name: "create rejected by self", cur: bundle("alice"), user: "alice", want: true},
		{name: "create rejected in someone else's name", cur: bundle("alice"), user: "mallory", want: false},
		{name: "reject as self", old: bundle(""), cur: bundle("alice"), user: "alice", want: true},
		{name: "reject in someone else's name", old: bundle(""), cur: bundle("alice"), user: "mallory", want: false},
		{name: "controller updates a rejected Bundle", old: bundle("alice"), cur: bundle("alice"),
			user: "system:serviceaccount:kardinal-system:kardinal-promoter", want: true},
		{name: "unrelated update", old: bundle(""), cur: bundle(""), user: "bob", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, admits(t, vap, tc.cur, tc.old, tc.user))
		})
	}
}

// TestIdentityAdmission_GateOverrides: every new or changed spec.overrides
// entry must name the requester, except when the controller writes it for
// the UI; and only kro (the namespace's Graph ServiceAccount) and the
// controller change a gate instance's expression, skipPermission or labels.
func TestIdentityAdmission_GateOverrides(t *testing.T) {
	vap := identityPolicy(t, "gate-overrides")
	rules := vap.Spec.MatchConstraints.ResourceRules
	require.Len(t, rules, 1)
	assert.Equal(t, []string{"policygates"}, rules[0].Resources)

	controller := "system:serviceaccount:" + releaseNS + ":kardinal-promoter"
	const graphSA = "system:serviceaccount:team-a:kardinal-graph"
	override := func(by string) map[string]interface{} {
		return map[string]interface{}{"reason": "hotfix", "expiresAt": "2026-10-09T00:00:00Z", "createdBy": by}
	}
	gate := func(expr string, instance bool, overrides ...map[string]interface{}) map[string]interface{} {
		labels := map[string]interface{}{"kardinal.io/environment": "prod"}
		if instance {
			labels["kardinal.io/bundle"] = "app-v1"
		}
		spec := map[string]interface{}{"expression": expr, "skipPermission": false}
		if len(overrides) > 0 {
			list := make([]interface{}, len(overrides))
			for i, o := range overrides {
				list[i] = o
			}
			spec["overrides"] = list
		}
		return map[string]interface{}{"metadata": map[string]interface{}{"namespace": "team-a", "labels": labels}, "spec": spec}
	}
	relabel := gate("x", true)
	relabel["metadata"].(map[string]interface{})["labels"].(map[string]interface{})["kardinal.io/bundle"] = "app-v2"
	skip := gate("x", true)
	skip["spec"].(map[string]interface{})["skipPermission"] = true

	tests := []struct {
		name     string
		old, cur map[string]interface{}
		user     string
		want     bool
	}{
		{name: "override as self", old: gate("x", true), cur: gate("x", true, override("alice")), user: "alice", want: true},
		{name: "override in someone else's name", old: gate("x", true), cur: gate("x", true, override("bob")), user: "alice", want: false},
		{name: "override without createdBy", old: gate("x", true), cur: gate("x", true, map[string]interface{}{"reason": "r"}), user: "alice", want: false},
		{name: "second override keeps the first", old: gate("x", true, override("bob")),
			cur: gate("x", true, override("bob"), override("alice")), user: "alice", want: true},
		{name: "editing someone else's override", old: gate("x", true, override("bob")),
			cur: gate("x", true, map[string]interface{}{"reason": "longer", "expiresAt": "2027-01-01T00:00:00Z", "createdBy": "bob"}), user: "alice", want: false},
		{name: "removing an override", old: gate("x", true, override("bob")), cur: gate("x", true), user: "alice", want: true},
		{name: "the controller for the UI", old: gate("x", true), cur: gate("x", true, override("kardinal-ui")), user: controller, want: true},
		{name: "create with a forged override", cur: gate("x", true, override("bob")), user: "alice", want: false},
		{name: "create a template gate", cur: gate("x", false), user: "alice", want: true},
		{name: "edit an instance's expression", old: gate("x", true), cur: gate("true", true), user: "alice", want: false},
		{name: "relabel an instance", old: gate("x", true), cur: relabel, user: "alice", want: false},
		{name: "grant skipPermission on an instance", old: gate("x", true), cur: skip, user: "alice", want: false},
		{name: "kro updates an instance", old: gate("x", true), cur: gate("y", true), user: graphSA, want: true},
		{name: "another namespace's Graph SA", old: gate("x", true), cur: gate("y", true), user: "system:serviceaccount:team-b:kardinal-graph", want: false},
		{name: "the controller updates an instance", old: gate("x", true), cur: gate("y", true), user: controller, want: true},
		{name: "edit a template gate", old: gate("x", false), cur: gate("true", false), user: "alice", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, admits(t, vap, tc.cur, tc.old, tc.user))
		})
	}
}

// TestIdentityAdmission_GateOverridesNamespaceMode: in namespace mode the
// gate-overrides binding is limited to the watched namespace, because its
// controller exemption names this release's ServiceAccount; another release's
// controller in another namespace is not refused by it.
func TestIdentityAdmission_GateOverridesNamespaceMode(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want map[string]string
	}{
		{args: nil, want: nil},
		{args: []string{"--set", "controller.watchNamespace=" + releaseNS}, want: map[string]string{"kubernetes.io/metadata.name": releaseNS}},
	} {
		var binding admissionregistrationv1.ValidatingAdmissionPolicyBinding
		for _, d := range render(t, "kardinal-promoter", tc.args...) {
			if d.Kind == "ValidatingAdmissionPolicyBinding" && d.Name == "kardinal-promoter-gate-overrides" {
				decodeStrict(t, d, &binding)
			}
		}
		require.Equal(t, "kardinal-promoter-gate-overrides", binding.Name, "args %v", tc.args)
		if tc.want == nil {
			assert.Nil(t, binding.Spec.MatchResources, "cluster mode binds every namespace")
			continue
		}
		require.NotNil(t, binding.Spec.MatchResources)
		require.NotNil(t, binding.Spec.MatchResources.NamespaceSelector)
		assert.Equal(t, tc.want, binding.Spec.MatchResources.NamespaceSelector.MatchLabels)
	}
}
