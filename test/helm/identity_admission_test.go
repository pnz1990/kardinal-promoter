// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// identityAdmissionObjects is every admission object the chart renders for
// release (identity-admission.yaml).
func identityAdmissionObjects(release string) []string {
	var out []string
	for _, p := range []string{"bundle-rejection", "gate-overrides", "approvals"} {
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
	// CREATE is checked too: a gate instance made by hand is refused, not only an edit.
	assert.ElementsMatch(t, []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
		rules[0].Operations)

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
	withSpec := func(g map[string]interface{}, k string, v interface{}) map[string]interface{} {
		g["spec"].(map[string]interface{})[k] = v
		return g
	}
	unlabelled := func(g map[string]interface{}) map[string]interface{} {
		delete(g["metadata"].(map[string]interface{})["labels"].(map[string]interface{}), "kardinal.io/bundle")
		return g
	}
	withMeta := func(g map[string]interface{}, k string, v interface{}) map[string]interface{} {
		g["metadata"].(map[string]interface{})[k] = v
		return g
	}
	owned := func() map[string]interface{} {
		return withMeta(gate("x", true), "ownerReferences", []interface{}{
			map[string]interface{}{"apiVersion": "kro.run/v1alpha1", "kind": "Graph", "name": "app-v1", "uid": "u1"}})
	}
	finalized := func() map[string]interface{} {
		return withMeta(owned(), "finalizers", []interface{}{"foregroundDeletion"})
	}
	const gc = "system:serviceaccount:kube-system:generic-garbage-collector"
	const nsController = "system:serviceaccount:kube-system:namespace-controller"
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
		{name: "create a gate instance by hand", cur: gate("true", true), user: "alice", want: false},
		{name: "create a gate instance by hand, as cluster admin", cur: gate("true", true), user: "kubernetes-admin", want: false},
		{name: "kro creates a gate instance", cur: gate("x", true), user: graphSA, want: true},
		{name: "the controller creates a gate instance", cur: gate("x", true), user: controller, want: true},
		{name: "edit an instance's message", old: gate("x", true), cur: withSpec(gate("x", true), "message", "changed"), user: "alice", want: false},
		{name: "edit an instance's recheckInterval", old: gate("x", true), cur: withSpec(gate("x", true), "recheckInterval", "1h"), user: "alice", want: false},
		{name: "add a field to an instance's spec", old: gate("x", true), cur: withSpec(gate("x", true), "generated", true), user: "alice", want: false},
		{name: "remove an instance's label", old: gate("x", true), cur: unlabelled(gate("x", true)), user: "alice", want: false},
		{name: "create a template gate", cur: gate("x", false), user: "alice", want: true},
		{name: "edit an instance's expression", old: gate("x", true), cur: gate("true", true), user: "alice", want: false},
		{name: "relabel an instance", old: gate("x", true), cur: relabel, user: "alice", want: false},
		{name: "grant skipPermission on an instance", old: gate("x", true), cur: skip, user: "alice", want: false},
		{name: "kro updates an instance", old: gate("x", true), cur: gate("y", true), user: graphSA, want: true},
		{name: "another namespace's Graph SA", old: gate("x", true), cur: gate("y", true), user: "system:serviceaccount:team-b:kardinal-graph", want: false},
		{name: "the controller updates an instance", old: gate("x", true), cur: gate("y", true), user: controller, want: true},
		{name: "edit a template gate", old: gate("x", false), cur: gate("true", false), user: "alice", want: true},
		// The metadata is frozen too, but for what the API server writes.
		{name: "annotate an instance", old: gate("x", true), cur: withMeta(gate("x", true), "annotations",
			map[string]interface{}{"note": "x"}), user: "alice", want: false},
		{name: "force a recheck", old: withMeta(gate("x", true), "annotations", map[string]interface{}{"a": "1"}),
			cur:  withMeta(gate("x", true), "annotations", map[string]interface{}{"a": "1", "kardinal.io/force-recheck": "123"}),
			user: "alice", want: true},
		{name: "force a recheck on an instance without annotations", old: gate("x", true),
			cur: withMeta(gate("x", true), "annotations", map[string]interface{}{"kardinal.io/force-recheck": "123"}), user: "alice", want: true},
		{name: "change another annotation with the recheck", old: withMeta(gate("x", true), "annotations", map[string]interface{}{"a": "1"}),
			cur:  withMeta(gate("x", true), "annotations", map[string]interface{}{"a": "2", "kardinal.io/force-recheck": "123"}),
			user: "alice", want: false},
		{name: "drop an annotation", old: withMeta(gate("x", true), "annotations", map[string]interface{}{"a": "1"}),
			cur: gate("x", true), user: "alice", want: false},
		{name: "re-own an instance", old: owned(), cur: withMeta(owned(), "ownerReferences", []interface{}{
			map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "Bundle", "name": "other", "uid": "u2"}}),
			user: "alice", want: false},
		{name: "drop an instance's owner", old: owned(), cur: gate("x", true), user: "alice", want: false},
		{name: "add a finalizer to an instance", old: owned(), cur: finalized(), user: "alice", want: false},
		{name: "override with a new resourceVersion and managedFields", old: withMeta(gate("x", true), "resourceVersion", "1"),
			cur: withMeta(withMeta(withMeta(gate("x", true, override("alice")), "resourceVersion", "2"), "generation", 3),
				"managedFields", []interface{}{map[string]interface{}{"manager": "kardinal"}}), user: "alice", want: true},
		{name: "the garbage collector removes a finalizer", old: finalized(), cur: owned(), user: gc, want: true},
		{name: "the garbage collector removes an owner", old: owned(), cur: gate("x", true), user: gc, want: true},
		{name: "the namespace controller updates an instance", old: finalized(), cur: owned(), user: nsController, want: true},
		{name: "the garbage collector cannot create an instance", cur: gate("x", true), user: gc, want: false},
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

// TestIdentityAdmission_GateInstanceFieldsComplete: the gate-overrides
// policy compares every PolicyGate spec field but overrides, so a field added
// to the API cannot be changed on a gate instance by hand unnoticed.
func TestIdentityAdmission_GateInstanceFieldsComplete(t *testing.T) {
	vap := identityPolicy(t, "gate-overrides")
	var only string
	for _, v := range vap.Spec.Variables {
		if v.Name == "onlyOverrides" {
			only = v.Expression
		}
	}
	require.NotEmpty(t, only)
	typ := reflect.TypeOf(v1alpha1.PolicyGateSpec{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name == "overrides" {
			assert.NotContains(t, only, "object.spec.overrides", "overrides may change")
			continue
		}
		assert.Contains(t, only, "object.spec."+name+" == oldObject.spec."+name, "spec.%s must not change on a gate instance", name)
}
}

// TestIdentityAdmission_Approvals: an Approval is admitted only in the
// requester's own name, with groups the requester has, and with the
// kardinal.io/bundle and kardinal.io/environment labels equal to the spec;
// the labels cannot change later.
func TestIdentityAdmission_Approvals(t *testing.T) {
	vap := identityPolicy(t, "approvals")
	approval := func(user string, groups []interface{}, bundleLabel, envLabel string) map[string]interface{} {
		labels := map[string]interface{}{}
		if bundleLabel != "" {
			labels["kardinal.io/bundle"] = bundleLabel
		}
		if envLabel != "" {
			labels["kardinal.io/environment"] = envLabel
		}
		return map[string]interface{}{
			"metadata": map[string]interface{}{"labels": labels},
			"spec": map[string]interface{}{"bundle": "app-v1", "environment": "prod", "user": user,
				"groups": groups, "decision": "approve"},
		}
	}
	admitsAs := func(obj, old map[string]interface{}, user string, groups []interface{}) bool {
		t.Helper()
		env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
			cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType), ext.Strings())
		require.NoError(t, err)
		vars := map[string]interface{}{"object": obj, "oldObject": nil, "variables": map[string]interface{}{},
			"request": map[string]interface{}{"userInfo": map[string]interface{}{"username": user, "groups": groups}}}
		if old != nil {
			vars["oldObject"] = old
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
				_, iss := env.Compile(v.MessageExpression)
				require.NoError(t, iss.Err(), v.MessageExpression)
			}
		}
		return true
	}
	mine := []interface{}{"release-managers", "system:authenticated"}
	tests := []struct {
		name     string
		obj, old map[string]interface{}
		user     string
		want     bool
	}{
		{name: "own approval", obj: approval("alice", []interface{}{"release-managers"}, "app-v1", "prod"), user: "alice", want: true},
		{name: "someone else's name", obj: approval("bob", []interface{}{}, "app-v1", "prod"), user: "alice", want: false},
		{name: "a group the requester does not have", obj: approval("alice", []interface{}{"admins"}, "app-v1", "prod"), user: "alice", want: false},
		{name: "bundle label differs from spec", obj: approval("alice", []interface{}{}, "app-v2", "prod"), user: "alice", want: false},
		{name: "environment label missing", obj: approval("alice", []interface{}{}, "app-v1", ""), user: "alice", want: false},
		{name: "relabel later", old: approval("alice", []interface{}{}, "app-v1", "prod"),
			obj: approval("alice", []interface{}{}, "app-v2", "prod"), user: "alice", want: false},
		{name: "update keeping the labels", old: approval("alice", []interface{}{}, "app-v1", "prod"),
			obj: approval("alice", []interface{}{}, "app-v1", "prod"), user: "system:serviceaccount:kube-system:generic-garbage-collector", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, admitsAs(tc.obj, tc.old, tc.user, mine))
		})
	}
	// Every metadata field a client can set is compared, but for what the
	// API server writes or never lets change.
	serverOwned := map[string]bool{"managedFields": true, "resourceVersion": true, "generation": true, "name": true,
		"namespace": true, "uid": true, "creationTimestamp": true, "selfLink": true, "deletionTimestamp": true,
		"deletionGracePeriodSeconds": true}
	meta := reflect.TypeOf(metav1.ObjectMeta{})
	for i := 0; i < meta.NumField(); i++ {
		name := strings.Split(meta.Field(i).Tag.Get("json"), ",")[0]
		if serverOwned[name] {
			continue
		}
		if name == "annotations" {
			assert.Contains(t, only, "variables.annotationsKept", "annotations are compared but for kardinal.io/force-recheck")
			continue
		}
		assert.Contains(t, only, "object.metadata."+name+" == oldObject.metadata."+name, "metadata.%s must not change on a gate instance", name)
	}
}
