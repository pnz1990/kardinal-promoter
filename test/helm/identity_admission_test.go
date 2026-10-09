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
	for _, p := range []string{"bundle-rejection"} {
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
		cel.Variable("request", cel.DynType))
	require.NoError(t, err)
	vars := map[string]interface{}{
		"object": object, "request": map[string]interface{}{"userInfo": map[string]interface{}{"username": user}},
	}
	if oldObject == nil {
		vars["oldObject"] = nil
	} else {
		vars["oldObject"] = oldObject
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
