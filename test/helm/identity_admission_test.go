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

// identityAdmissionObjects are the admission objects identity-admission.yaml
// renders for release.
func identityAdmissionObjects(release string) []string {
	var out []string
	for _, p := range []string{"scoped-writes"} {
		out = append(out, "ValidatingAdmissionPolicy/"+release+"-"+p, "ValidatingAdmissionPolicyBinding/"+release+"-"+p)
	}
	return out
}

// TestHelmTemplateScopedWritesPolicy verifies the scoped-writes
// ValidatingAdmissionPolicy: always rendered, matching UPDATE on pipelines
// and policygates, exempting the controller and the Graph ServiceAccount,
// checking the virtual subresources with the authorizer, and bound to the
// watched namespace only in namespace mode.
func TestHelmTemplateScopedWritesPolicy(t *testing.T) {
	find := func(docs []map[string]interface{}, kind string) map[string]interface{} {
		for _, d := range docs {
			if d["kind"] == kind && dig(d, "metadata", "name") == "kardinal-promoter-scoped-writes" {
				return d
			}
		}
		return nil
	}
	docs := renderChart(t, "kardinal-promoter")
	vap := find(docs, "ValidatingAdmissionPolicy")
	require.NotNil(t, vap)
	rule := dig(vap, "spec", "matchConstraints", "resourceRules").([]interface{})[0]
	assert.Equal(t, []interface{}{"UPDATE"}, dig(rule, "operations"))
	assert.Equal(t, []interface{}{"pipelines", "policygates"}, dig(rule, "resources"))
	vars := map[string]string{}
	for _, v := range dig(vap, "spec", "variables").([]interface{}) {
		vars[dig(v, "name").(string)] = dig(v, "expression").(string)
	}
	assert.Contains(t, vars["exempt"], `"system:serviceaccount:default:kardinal-promoter"`)
	assert.Contains(t, vars["exempt"], `'kardinal-graph'`)
	assert.Contains(t, vars["limited"], `subresource('edit')`)
	assert.Contains(t, vars["limited"], `'pause' : 'override'`)
	binding := find(docs, "ValidatingAdmissionPolicyBinding")
	require.NotNil(t, binding)
	assert.Equal(t, []interface{}{"Deny"}, dig(binding, "spec", "validationActions"))
	assert.Nil(t, dig(binding, "spec", "matchResources"))

	nsBinding := find(renderChart(t, "kardinal-promoter", "--namespace", "team-a", "--set", "controller.watchNamespace=team-a"),
		"ValidatingAdmissionPolicyBinding")
	assert.Equal(t, "team-a", dig(nsBinding, "spec", "matchResources", "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name"))
}

// scopedWritesPolicy is the rendered scoped-writes policy.
func scopedWritesPolicy(t *testing.T) *admissionregistrationv1.ValidatingAdmissionPolicy {
	t.Helper()
	for _, d := range render(t, "kardinal-promoter") {
		if d.Kind == "ValidatingAdmissionPolicy" && d.Name == "kardinal-promoter-scoped-writes" {
			var vap admissionregistrationv1.ValidatingAdmissionPolicy
			decodeStrict(t, d, &vap)
			return &vap
		}
	}
	t.Fatal("no scoped-writes policy")
	return nil
}

// scopedAdmits evaluates every validation of vap as the API server would for
// an UPDATE of resource by user, with the authorizer-based variables (check,
// exempt, limited) given: the CEL authorizer is the API server's, so the test
// decides who holds which subresource and checks the rest of the policy.
func scopedAdmits(t *testing.T, vap *admissionregistrationv1.ValidatingAdmissionPolicy, resource string,
	object, oldObject map[string]interface{}, user string, limited bool) bool {
	t.Helper()
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
		cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType))
	require.NoError(t, err)
	variables := map[string]interface{}{"limited": limited, "exempt": false}
	vars := map[string]interface{}{
		"object": object, "oldObject": oldObject, "variables": variables,
		"request": map[string]interface{}{
			"userInfo": map[string]interface{}{"username": user},
			"resource": map[string]interface{}{"group": "kardinal.io", "resource": resource},
		},
	}
	eval := func(expr string) interface{} {
		ast, iss := env.Compile(expr)
		require.NoError(t, iss.Err(), expr)
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(vars)
		require.NoError(t, err, expr)
		return out.Value()
	}
	for _, v := range vap.Spec.Variables {
		if _, given := variables[v.Name]; given || v.Name == "check" {
			continue
		}
		variables[v.Name] = eval(v.Expression)
	}
	for _, v := range vap.Spec.Validations {
		if eval(v.Expression) != true {
			return false
		}
	}
	return true
}

// TestScopedWrites_AuthorizerVariables: limited means holding the action
// subresource and not <resource>/edit, checked with the API server's
// authorizer on the object; the controller and the Graph ServiceAccount are
// exempt.
func TestScopedWrites_AuthorizerVariables(t *testing.T) {
	vap := scopedWritesPolicy(t)
	vars := map[string]string{}
	for _, v := range vap.Spec.Variables {
		vars[v.Name] = v.Expression
	}
	assert.Contains(t, vars["check"], "authorizer.group('kardinal.io').resource(request.resource.resource)")
	assert.Contains(t, vars["check"], ".namespace(object.metadata.namespace).name(object.metadata.name)")
	assert.Contains(t, vars["limited"], "!variables.exempt")
	assert.Contains(t, vars["limited"], "'pause' : 'override').check('update').allowed()")
	assert.Contains(t, vars["limited"], "!variables.check.subresource('edit').check('update').allowed()")
	assert.Contains(t, vars["exempt"], `"system:serviceaccount:`+releaseNS+`:kardinal-promoter"`)
	assert.Contains(t, vars["exempt"], `'kardinal-graph'`)
}

// TestScopedWrites_Rules runs every rule of the scoped-writes policy for a
// limited caller (pause or override, not edit), and checks an unlimited one
// is not held by it.
func TestScopedWrites_Rules(t *testing.T) {
	vap := scopedWritesPolicy(t)
	const alice = "system:serviceaccount:team-a:alice"
	meta := func(mod func(m map[string]interface{})) map[string]interface{} {
		m := map[string]interface{}{
			"name": "obj", "namespace": "team-a", "uid": "u1", "resourceVersion": "10", "generation": int64(3),
			"labels":        map[string]interface{}{"kardinal.io/environment": "prod"},
			"managedFields": []interface{}{map[string]interface{}{"manager": "kubectl"}},
		}
		if mod != nil {
			mod(m)
		}
		return m
	}
	pipeline := func(paused bool, branch string, mod func(map[string]interface{})) map[string]interface{} {
		return map[string]interface{}{"metadata": meta(mod), "spec": map[string]interface{}{
			"paused": paused, "git": map[string]interface{}{"url": "https://git.example/a.git", "branch": branch}}}
	}
	ov := func(by, created, expires string) map[string]interface{} {
		o := map[string]interface{}{"reason": "hotfix", "createdBy": by}
		if created != "" {
			o["createdAt"] = created
		}
		if expires != "" {
			o["expiresAt"] = expires
		}
		return o
	}
	gate := func(expr string, mod func(map[string]interface{}), overrides ...map[string]interface{}) map[string]interface{} {
		spec := map[string]interface{}{"expression": expr}
		if len(overrides) > 0 {
			list := make([]interface{}, len(overrides))
			for i, o := range overrides {
				list[i] = o
			}
			spec["overrides"] = list
		}
		return map[string]interface{}{"metadata": meta(mod), "spec": spec}
	}
	const t0, t1h, t25h = "2026-10-09T12:00:00Z", "2026-10-09T13:00:00Z", "2026-10-10T13:00:00Z"
	first := ov("bob", t0, t1h)
	tests := []struct {
		name     string
		resource string
		old, cur map[string]interface{}
		limited  bool
		want     bool
	}{
		{"pause", "pipelines", pipeline(false, "main", nil), pipeline(true, "main", nil), true, true},
		{"server-set metadata may change", "pipelines", pipeline(false, "main", nil),
			pipeline(true, "main", func(m map[string]interface{}) {
				m["resourceVersion"], m["generation"] = "11", int64(4)
				m["managedFields"] = []interface{}{}
			}), true, true},
		{"another spec field", "pipelines", pipeline(false, "main", nil), pipeline(true, "other", nil), true, false},
		{"a label", "pipelines", pipeline(false, "main", nil),
			pipeline(true, "main", func(m map[string]interface{}) { m["labels"] = map[string]interface{}{"x": "y"} }), true, false},
		{"an annotation", "pipelines", pipeline(false, "main", nil),
			pipeline(false, "main", func(m map[string]interface{}) { m["annotations"] = map[string]interface{}{"a": "b"} }), true, false},
		{"a finalizer", "pipelines", pipeline(false, "main", nil),
			pipeline(false, "main", func(m map[string]interface{}) { m["finalizers"] = []interface{}{"x/hold"} }), true, false},
		{"garbage collection by ownerReference", "policygates", gate("x", nil),
			gate("x", func(m map[string]interface{}) {
				m["ownerReferences"] = []interface{}{map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "name": "gone", "uid": "dead"}}
			}), true, false},
		{"removing a label", "policygates", gate("x", nil),
			gate("x", func(m map[string]interface{}) { delete(m, "labels") }), true, false},
		{"override as self", "policygates", gate("x", nil), gate("x", nil, ov(alice, t0, t1h)), true, true},
		{"second override keeps the first", "policygates", gate("x", nil, first), gate("x", nil, first, ov(alice, t0, t1h)), true, true},
		{"override in someone else's name", "policygates", gate("x", nil), gate("x", nil, ov("bob", t0, t1h)), true, false},
		{"override without createdBy", "policygates", gate("x", nil),
			gate("x", nil, map[string]interface{}{"reason": "r", "createdAt": t0, "expiresAt": t1h}), true, false},
		{"editing an existing override", "policygates", gate("x", nil, first),
			gate("x", nil, ov("bob", t0, t25h)), true, false},
		{"removing an override", "policygates", gate("x", nil, first), gate("x", nil), true, false},
		{"replacing an override with your own", "policygates", gate("x", nil, first), gate("x", nil, ov(alice, t0, t1h)), true, false},
		{"expiring past the cap", "policygates", gate("x", nil), gate("x", nil, ov(alice, t0, t25h)), true, false},
		{"expiring at the cap", "policygates", gate("x", nil), gate("x", nil, ov(alice, t0, "2026-10-10T12:00:00Z")), true, true},
		{"expiring before it was created", "policygates", gate("x", nil), gate("x", nil, ov(alice, t1h, t0)), true, false},
		{"no createdAt", "policygates", gate("x", nil), gate("x", nil, ov(alice, "", t1h)), true, false},
		{"no expiresAt", "policygates", gate("x", nil), gate("x", nil, ov(alice, t0, "")), true, false},
		{"changing the expression", "policygates", gate("x", nil), gate("true", nil), true, false},
		{"not limited: an editor changes anything", "policygates", gate("x", nil, first),
			gate("true", func(m map[string]interface{}) { m["labels"] = map[string]interface{}{} }), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, scopedAdmits(t, vap, tt.resource, tt.cur, tt.old, alice, tt.limited))
		})
	}

	// gateOverrides.maxMinutes moves the cap.
	for _, d := range render(t, "kardinal-promoter", "--set", "gateOverrides.maxMinutes=30") {
		if d.Kind == "ValidatingAdmissionPolicy" && d.Name == "kardinal-promoter-scoped-writes" {
			assert.Contains(t, string(d.raw), "duration('30m')")
		}
	}
}

// TestGateOverrideCapIsOneValue: gateOverrides.maxMinutes sets both the
// controller flag the UI API and the PolicyGate reconciler read
// (--gate-override-max-minutes, default 1440 as in ui_api.go) and the
// scoped-writes policy's bound, so the three never differ.
func TestGateOverrideCapIsOneValue(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "1440"},
		{[]string{"--set", "gateOverrides.maxMinutes=30"}, "30"},
	} {
		docs := render(t, "kardinal-promoter", tc.args...)
		var vap, deploy string
		for _, d := range docs {
			switch {
			case d.Kind == "ValidatingAdmissionPolicy" && d.Name == "kardinal-promoter-scoped-writes":
				vap = string(d.raw)
			case d.Kind == "Deployment":
				deploy = string(d.raw)
			}
		}
		assert.Contains(t, vap, "duration('"+tc.want+"m')")
		assert.Contains(t, deploy, `"--gate-override-max-minutes=`+tc.want+`"`)
	}
}
