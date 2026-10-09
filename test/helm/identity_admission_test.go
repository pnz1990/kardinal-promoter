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
	for _, p := range []string{"bundle-rejection", "gate-overrides", "approvals", "bundle-creator", "graph-objects", "scoped-writes"} {
		out = append(out, "ValidatingAdmissionPolicy/"+release+"-"+p, "ValidatingAdmissionPolicyBinding/"+release+"-"+p)
	}
	return out
}

// identityPolicy renders the chart and returns the ValidatingAdmissionPolicy
// named release-suffix, checking its binding denies.
func identityPolicy(t *testing.T, suffix string, args ...string) *admissionregistrationv1.ValidatingAdmissionPolicy {
	t.Helper()
	docs := render(t, "kardinal-promoter", args...)
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
// The requester is in system:authenticated only: a case that needs groups
// (a ServiceAccount's) uses admitsGroups, so an exemption that trusted a
// group could not pass by accident.
func admits(t *testing.T, vap *admissionregistrationv1.ValidatingAdmissionPolicy, object, oldObject map[string]interface{}, user string) bool {
	t.Helper()
	return admitsGroups(t, vap, object, oldObject, user, []interface{}{"system:authenticated"})
}

// admitsGroups is admits for a requester in groups.
// request adds fields (resource, subResource) to the request.
func admitsGroups(t *testing.T, vap *admissionregistrationv1.ValidatingAdmissionPolicy, object, oldObject map[string]interface{},
	user string, groups []interface{}, request ...map[string]interface{}) bool {
	t.Helper()
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
		cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType), ext.Strings())
	require.NoError(t, err)
	req := map[string]interface{}{
		"userInfo":  map[string]interface{}{"username": user, "groups": groups},
		"operation": operation(object, oldObject)}
	// request.namespace is the object's namespace, as the API server sets it.
	for _, o := range []map[string]interface{}{oldObject, object} {
		if md, ok := o["metadata"].(map[string]interface{}); ok {
			if ns, ok := md["namespace"].(string); ok {
				req["namespace"] = ns
			}
		}
	}
	for _, r := range request {
		for k, v := range r {
			if k == "userInfoExtra" {
				req["userInfo"].(map[string]interface{})["extra"] = v
				continue
			}
			req[k] = v
		}
	}
	vars := map[string]interface{}{"object": object, "request": req}
	if object == nil {
		vars["object"] = nil
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

// TestIdentityAdmission_NamespaceMode: in namespace mode every identity
// binding is limited to the watched namespace, because its exemptions name
// this release's controller; another release's controller in another
// namespace is not refused by them. Cluster mode binds every namespace.
func TestIdentityAdmission_NamespaceMode(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want map[string]string
	}{
		{args: nil, want: nil},
		{args: []string{"--set", "controller.watchNamespace=" + releaseNS}, want: map[string]string{"kubernetes.io/metadata.name": releaseNS}},
	} {
		bindings := map[string]admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
		for _, d := range render(t, "kardinal-promoter", tc.args...) {
			if d.Kind == "ValidatingAdmissionPolicyBinding" {
				var b admissionregistrationv1.ValidatingAdmissionPolicyBinding
				decodeStrict(t, d, &b)
				bindings[b.Name] = b
			}
		}
		for _, obj := range identityAdmissionObjects("kardinal-promoter") {
			name, ok := strings.CutPrefix(obj, "ValidatingAdmissionPolicyBinding/")
			if !ok {
				continue
			}
			binding, found := bindings[name]
			require.True(t, found, "binding %s, args %v", name, tc.args)
			if tc.want == nil {
				assert.Nil(t, binding.Spec.MatchResources, "%s: cluster mode binds every namespace", name)
				continue
			}
			require.NotNil(t, binding.Spec.MatchResources, name)
			require.NotNil(t, binding.Spec.MatchResources.NamespaceSelector, name)
			assert.Equal(t, tc.want, binding.Spec.MatchResources.NamespaceSelector.MatchLabels, name)
		}
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
			"request": map[string]interface{}{"userInfo": map[string]interface{}{"username": user, "groups": groups},
				"operation": operation(obj, old)}}
		if old != nil {
			vars["oldObject"] = old
		}
		if obj == nil {
			vars["object"] = nil
		}
		variables := vars["variables"].(map[string]interface{})
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
				_, iss := env.Compile(v.MessageExpression)
				require.NoError(t, iss.Err(), v.MessageExpression)
			}
		}
		return true
	}
	owned := func(a map[string]interface{}, kind, name, uid string) map[string]interface{} {
		a["spec"].(map[string]interface{})["bundleUID"] = "uid-1"
		a["metadata"].(map[string]interface{})["ownerReferences"] = []interface{}{
			map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": kind, "name": name, "uid": uid}}
		return a
	}
	withUID := func(a map[string]interface{}) map[string]interface{} {
		a["spec"].(map[string]interface{})["bundleUID"] = "uid-1"
		return a
	}
	controller := "system:serviceaccount:" + releaseNS + ":kardinal-promoter"
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
		{name: "revoke your own", old: approval("alice", []interface{}{}, "app-v1", "prod"), user: "alice", want: true},
		{name: "delete someone else's", old: approval("alice", []interface{}{}, "app-v1", "prod"), user: "mallory", want: false},
		{name: "delete someone else's, as cluster admin", old: approval("alice", []interface{}{}, "app-v1", "prod"), user: "kubernetes-admin", want: false},
		{name: "garbage collection with the Bundle", old: approval("alice", []interface{}{}, "app-v1", "prod"),
			user: "system:serviceaccount:kube-system:generic-garbage-collector", want: true},
		{name: "namespace deletion", old: approval("alice", []interface{}{}, "app-v1", "prod"),
			user: "system:serviceaccount:kube-system:namespace-controller", want: true},
		{name: "kardinal's controller cannot revoke it", old: approval("alice", []interface{}{}, "app-v1", "prod"),
			user: controller, want: false},
		// The only owner an Approval may name is the Bundle it approves.
		{name: "owned by its Bundle", obj: owned(approval("alice", []interface{}{}, "app-v1", "prod"), "Bundle", "app-v1", "uid-1"),
			user: "alice", want: true},
		{name: "no owner", obj: withUID(approval("alice", []interface{}{}, "app-v1", "prod")), user: "alice", want: true},
		{name: "owned by another Bundle", obj: owned(approval("alice", []interface{}{}, "app-v1", "prod"), "Bundle", "app-v1", "uid-2"),
			user: "alice", want: false},
		{name: "owned by something else", obj: owned(approval("alice", []interface{}{}, "app-v1", "prod"), "ConfigMap", "app-v1", "uid-1"),
			user: "alice", want: false},
		{name: "owner changed later", old: owned(approval("alice", []interface{}{}, "app-v1", "prod"), "Bundle", "app-v1", "uid-1"),
			obj: withUID(approval("alice", []interface{}{}, "app-v1", "prod")), user: "alice", want: false},
		{name: "the garbage collector updates owners", old: owned(approval("alice", []interface{}{}, "app-v1", "prod"), "Bundle", "app-v1", "uid-1"),
			obj:  withUID(approval("alice", []interface{}{}, "app-v1", "prod")),
			user: "system:serviceaccount:kube-system:generic-garbage-collector", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, admitsAs(tc.obj, tc.old, tc.user, mine))
		})
	}
}

// operation is the admission operation of a request with obj and old.
func operation(obj, old map[string]interface{}) string {
	switch {
	case old == nil:
		return "CREATE"
	case obj == nil:
		return "DELETE"
	}
	return "UPDATE"
}

// TestIdentityAdmission_BundleCreator: kardinal.io/created-by, which an
// approval gate's excludeAuthor reads, is optional on a new Bundle but must
// be the requester; it cannot be added, changed or removed later; the
// controller names the creator of the Bundles it creates.
func TestIdentityAdmission_BundleCreator(t *testing.T) {
	vap := identityPolicy(t, "bundle-creator")
	controller := "system:serviceaccount:" + releaseNS + ":kardinal-promoter"
	bundle := func(creator string) map[string]interface{} {
		md := map[string]interface{}{"name": "app-v1"}
		if creator != "" {
			md["annotations"] = map[string]interface{}{"kardinal.io/created-by": creator}
		}
		return map[string]interface{}{"metadata": md, "spec": map[string]interface{}{"pipeline": "app"}}
	}
	tests := []struct {
		name     string
		old, cur map[string]interface{}
		user     string
		want     bool
	}{
		{name: "create with own name", cur: bundle("alice"), user: "alice", want: true},
		{name: "create without the annotation", cur: bundle(""), user: "alice", want: true},
		{name: "create in someone else's name", cur: bundle("bob"), user: "alice", want: false},
		{name: "the controller names the creator", cur: bundle("subscription:app"), user: controller, want: true},
		// Only the exact controller ServiceAccount: another one in the
		// release namespace (a second controller instance), or one of the
		// same name elsewhere, may not.
		{name: "another ServiceAccount of the release namespace cannot", cur: bundle("bundle-api"),
			user: "system:serviceaccount:" + releaseNS + ":variant-1", want: false},
		{name: "a ServiceAccount elsewhere cannot", cur: bundle("bundle-api"),
			user: "system:serviceaccount:team-a:kardinal-promoter", want: false},
		{name: "unchanged on update", old: bundle("alice"), cur: bundle("alice"), user: "bob", want: true},
		{name: "added later", old: bundle(""), cur: bundle("bob"), user: "bob", want: false},
		{name: "changed later", old: bundle("alice"), cur: bundle("bob"), user: "bob", want: false},
		{name: "removed later", old: bundle("alice"), cur: bundle(""), user: "bob", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, admits(t, vap, tc.cur, tc.old, tc.user))
		})
	}
	// The release namespace's ServiceAccount groups do not exempt anyone.
	variant := "system:serviceaccount:" + releaseNS + ":variant-1"
	assert.False(t, admitsGroups(t, vap, bundle("bundle-api"), nil, variant,
		[]interface{}{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:" + releaseNS}))
	// admission.controllerUsernames names more exact usernames.
	listed := identityPolicy(t, "bundle-creator", "--set", "admission.controllerUsernames={"+variant+"}")
	assert.True(t, admits(t, listed, bundle("bundle-api"), nil, variant), "a listed username names the creator")
	assert.True(t, admits(t, listed, bundle("subscription:app"), nil, controller), "the controller still does")
	assert.False(t, admits(t, listed, bundle("bundle-api"), nil, "system:serviceaccount:"+releaseNS+":variant-2"))
	// No wildcards: an entry is one exact username.
	assert.False(t, admits(t, listed, bundle("bundle-api"), nil, variant+"x"))
}

// TestIdentityAdmission_GraphObjects: the objects a promotion Graph makes
// (PromotionStep, PRStatus, HookRun, RenderRun, ImageVerification, and
// kardinal's Argo Rollouts AnalysisRuns) are created and changed only by
// kro (the namespace's Graph ServiceAccount) and kardinal's controllers,
// their status included; the garbage collector and the namespace controller
// may update them. Nobody else can forge one or edit spec.live, a step's
// state or a PRStatus's merge: not a cluster admin without impersonation,
// not the Graph ServiceAccount's own token or Pods (only kro, which
// impersonates it, is the Graph), and HookRuns cannot be deleted by others.
//
// Covers GRAPH-OBJECTS-03.
func TestIdentityAdmission_GraphObjects(t *testing.T) {
	vap := identityPolicy(t, "graph-objects")
	rules := vap.Spec.MatchConstraints.ResourceRules
	require.Len(t, rules, 4)
	assert.Equal(t, []admissionregistrationv1.OperationType{admissionregistrationv1.Delete}, rules[1].Operations)
	assert.Equal(t, []string{"hookruns"}, rules[1].Resources, "a deleted HookRun would run its hook again")
	rules = append(rules[:1], rules[2:]...)
	assert.ElementsMatch(t, []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
		rules[0].Operations, "DELETE recreates, it forges nothing")
	for _, k := range []string{"promotionsteps", "prstatuses", "hookruns", "renderruns", "imageverifications"} {
		assert.Contains(t, rules[0].Resources, k)
		assert.Contains(t, rules[0].Resources, k+"/status", "status is checked too")
	}
	assert.Equal(t, []string{"metricchecks", "metricchecks/status"}, rules[1].Resources)
	assert.Equal(t, []string{"argoproj.io"}, rules[2].APIGroups)
	assert.Equal(t, []admissionregistrationv1.OperationType{admissionregistrationv1.Create}, rules[2].Operations)

	controller := "system:serviceaccount:" + releaseNS + ":kardinal-promoter"
	const graphSA = "system:serviceaccount:team-a:kardinal-graph"
	obj := func(labels map[string]interface{}, spec map[string]interface{}) map[string]interface{} {
		md := map[string]interface{}{"name": "app-v1-prod", "namespace": "team-a"}
		if labels != nil {
			md["labels"] = labels
		}
		return map[string]interface{}{"metadata": md, "spec": spec}
	}
	step := func(live string) map[string]interface{} {
		return obj(map[string]interface{}{"kardinal.io/bundle": "app-v1"},
			map[string]interface{}{"environment": "prod", "live": map[string]interface{}{"gates": live}})
	}
	withAnnotations := func(o map[string]interface{}, a map[string]interface{}) map[string]interface{} {
		o["metadata"].(map[string]interface{})["annotations"] = a
		return o
	}
	withExtra := func(r map[string]interface{}, k, v string) map[string]interface{} {
		out := map[string]interface{}{"userInfoExtra": map[string]interface{}{k: []interface{}{v}}}
		for key, val := range r {
			out[key] = val
		}
		return out
	}
	res := func(group, resource, sub string) map[string]interface{} {
		return map[string]interface{}{"resource": map[string]interface{}{"group": group, "version": "v1alpha1", "resource": resource},
			"subResource": sub}
	}
	steps, stepStatus := res("kardinal.io", "promotionsteps", ""), res("kardinal.io", "promotionsteps", "status")
	delete(steps, "subResource") // the API server leaves it out for the object itself
	prs := res("kardinal.io", "prstatuses", "status")
	hooks, verifications := res("kardinal.io", "hookruns", ""), res("kardinal.io", "imageverifications", "")
	runs := res("argoproj.io", "analysisruns", "")
	mcs, mcStatus := res("kardinal.io", "metricchecks", ""), res("kardinal.io", "metricchecks", "status")
	mcInstance := func() map[string]interface{} {
		return obj(map[string]interface{}{"kardinal.io/bundle": "app-v1", "kardinal.io/metric-template": "errors"},
			map[string]interface{}{"provider": "prometheus"})
	}
	mcTemplate := func() map[string]interface{} {
		return obj(map[string]interface{}{"kardinal.io/environment": "prod"}, map[string]interface{}{"provider": "prometheus"})
	}
	user := []interface{}{"system:authenticated"}
	saGroups := []interface{}{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:team-a"}
	tests := []struct {
		name     string
		old, cur map[string]interface{}
		user     string
		groups   []interface{}
		req      map[string]interface{}
		want     bool
	}{
		{name: "kro creates a step", cur: step("[]"), user: graphSA, groups: saGroups, req: steps, want: true},
		{name: "a token minted for the Graph ServiceAccount", cur: step("[]"), user: graphSA, groups: saGroups,
			req: withExtra(steps, "authentication.kubernetes.io/credential-id", "JTI=abc"), want: false},
		{name: "a Pod running as the Graph ServiceAccount", cur: step("[]"), user: graphSA, groups: saGroups,
			req: withExtra(steps, "authentication.kubernetes.io/pod-name", "evil"), want: false},
		{name: "a user deletes a HookRun", old: step("[]"), user: "alice", req: hooks, want: false},
		{name: "kro deletes a HookRun", old: step("[]"), user: graphSA, groups: saGroups, req: hooks, want: true},
		{name: "the controller deletes a HookRun", old: step("[]"), user: controller, req: hooks, want: true},
		{name: "the garbage collector deletes a HookRun", old: step("[]"),
			user: "system:serviceaccount:kube-system:generic-garbage-collector", req: hooks, want: true},
		{name: "a token for the Graph SA deletes a HookRun", old: step("[]"), user: graphSA, groups: saGroups,
			req: withExtra(hooks, "authentication.kubernetes.io/credential-id", "JTI=abc"), want: false},
		{name: "kro patches spec.live", old: step("[]"), cur: step("[ok]"), user: graphSA, groups: saGroups, req: steps, want: true},
		{name: "the controller writes step status", old: step("[]"), cur: step("[]"), user: controller, req: stepStatus, want: true},
		{name: "a user forges a step", cur: step("[]"), user: "alice", req: steps, want: false},
		{name: "a cluster admin forges a step", cur: step("[]"), user: "kubernetes-admin", req: steps, want: false},
		{name: "a user forges spec.live", old: step("[blocked]"), cur: step("[ok]"), user: "alice", req: steps, want: false},
		{name: "a user sets a step's state", old: step("[]"), cur: step("[]"), user: "alice", req: stepStatus, want: false},
		{name: "a user marks a PR merged", old: step("[]"), cur: step("[]"), user: "alice", req: prs, want: false},
		{name: "a user forges a HookRun", cur: step("[]"), user: "alice", req: hooks, want: false},
		{name: "a user forges an ImageVerification", cur: step("[]"), user: "alice", req: verifications, want: false},
		{name: "another namespace's Graph SA", cur: step("[]"), user: "system:serviceaccount:team-b:kardinal-graph",
			groups: []interface{}{"system:serviceaccounts:team-b"}, req: steps, want: false},
		{name: "a ServiceAccount of the namespace", cur: step("[]"), user: "system:serviceaccount:team-a:ci", groups: saGroups, req: steps, want: false},
		{name: "another ServiceAccount of the release namespace", cur: step("[]"),
			user: "system:serviceaccount:" + releaseNS + ":variant-1", req: steps, want: false},
		{name: "the garbage collector updates", old: step("[]"), cur: step("[]"),
			user: "system:serviceaccount:kube-system:generic-garbage-collector", req: steps, want: true},
		{name: "the namespace controller updates", old: step("[]"), cur: step("[]"),
			user: "system:serviceaccount:kube-system:namespace-controller", req: steps, want: true},
		{name: "the garbage collector cannot create", cur: step("[]"),
			user: "system:serviceaccount:kube-system:generic-garbage-collector", req: steps, want: false},
		{name: "a user forces a recheck", old: step("[]"), cur: withAnnotations(step("[]"), map[string]interface{}{"kardinal.io/force-recheck": "1"}),
			user: "alice", req: steps, want: true},
		{name: "a user forces a recheck and edits the spec", old: step("[]"),
			cur: withAnnotations(step("[ok]"), map[string]interface{}{"kardinal.io/force-recheck": "1"}), user: "alice", req: steps, want: false},
		{name: "a user adds another annotation", old: step("[]"), cur: withAnnotations(step("[]"), map[string]interface{}{"x": "1"}),
			user: "alice", req: steps, want: false},
		{name: "a user forces a recheck through status", old: step("[]"),
			cur: withAnnotations(step("[]"), map[string]interface{}{"kardinal.io/force-recheck": "1"}), user: "alice", req: stepStatus, want: false},
		{name: "a user writes a MetricCheck", cur: mcTemplate(), user: "alice", req: mcs, want: true},
		{name: "a user edits their MetricCheck's status", old: mcTemplate(), cur: mcTemplate(), user: "alice", req: mcStatus, want: true},
		{name: "a user forges a MetricCheck instance", cur: mcInstance(), user: "alice", req: mcs, want: false},
		{name: "a user passes a MetricCheck instance", old: mcInstance(), cur: mcInstance(), user: "alice", req: mcStatus, want: false},
		{name: "a user strips an instance's label", old: mcInstance(), cur: mcTemplate(), user: "alice", req: mcs, want: false},
		{name: "kro creates a MetricCheck instance", cur: mcInstance(), user: graphSA, groups: saGroups, req: mcs, want: true},
		{name: "the controller writes an instance's status", old: mcInstance(), cur: mcInstance(), user: controller, req: mcStatus, want: true},
		{name: "kro adopts a user's MetricCheck", old: mcTemplate(), cur: mcInstance(), user: graphSA, groups: saGroups, req: mcs, want: false},
		{name: "a user creates kardinal's AnalysisRun", cur: obj(map[string]interface{}{"kardinal.io/bundle": "app-v1"}, nil),
			user: "alice", req: runs, want: false},
		{name: "a user creates their own AnalysisRun", cur: obj(nil, nil), user: "alice", req: runs, want: true},
		{name: "the controller creates kardinal's AnalysisRun", cur: obj(map[string]interface{}{"kardinal.io/bundle": "app-v1"}, nil),
			user: controller, req: runs, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			groups := tc.groups
			if groups == nil {
				groups = user
			}
			assert.Equal(t, tc.want, admitsGroups(t, vap, tc.cur, tc.old, tc.user, groups, tc.req))
		})
	}

	// Another kardinal controller, listed by exact username (no wildcards).
	listed := identityPolicy(t, "graph-objects", "--set",
		"admission.controllerUsernames={system:serviceaccount:"+releaseNS+":kp-b,system:serviceaccount:"+releaseNS+":variant-3}")
	for u, want := range map[string]bool{
		"system:serviceaccount:" + releaseNS + ":kp-b":      true,
		"system:serviceaccount:" + releaseNS + ":variant-3": true,
		"system:serviceaccount:" + releaseNS + ":variant-4": false,
		"system:serviceaccount:team-a:variant-3":            false,
	} {
		assert.Equal(t, want, admitsGroups(t, listed, step("[]"), nil, u, user, steps), u)
	}
}

// TestIdentityAdmission_GateAdoptionRefused: kro adopts an existing object
// of the name it applies. A PolicyGate made ahead of kro under an instance's
// name, without the kardinal.io/bundle label, cannot become the instance: the
// update that adds the label is refused for everyone, kro and the controller
// included, so the promotion waits until it is deleted.
func TestIdentityAdmission_GateAdoptionRefused(t *testing.T) {
	vap := identityPolicy(t, "gate-overrides")
	controller := "system:serviceaccount:" + releaseNS + ":kardinal-promoter"
	gate := func(instance bool, extra map[string]interface{}) map[string]interface{} {
		labels := map[string]interface{}{"kardinal.io/environment": "prod"}
		if instance {
			labels["kardinal.io/bundle"] = "app-v1"
		}
		spec := map[string]interface{}{"expression": "true"}
		for k, v := range extra {
			spec[k] = v
		}
		return map[string]interface{}{"metadata": map[string]interface{}{"name": "app-v1-hold-prod", "namespace": "team-a",
			"labels": labels}, "spec": spec}
	}
	squat := gate(false, map[string]interface{}{"overrides": []interface{}{
		map[string]interface{}{"reason": "pre-seeded", "expiresAt": "2027-01-01T00:00:00Z", "createdBy": "mallory"}}})
	for _, u := range []string{"system:serviceaccount:team-a:kardinal-graph", controller, "kubernetes-admin"} {
		assert.False(t, admits(t, vap, gate(true, nil), squat, u), "%s adopts a squatter", u)
	}
	assert.True(t, admits(t, vap, squat, nil, "mallory"), "a PolicyGate without the label is a template anyone may create")
	assert.True(t, admits(t, vap, gate(true, nil), nil, "system:serviceaccount:team-a:kardinal-graph"), "kro creates the instance")
	assert.True(t, admits(t, vap, gate(true, map[string]interface{}{"message": "m"}), gate(true, nil),
		"system:serviceaccount:team-a:kardinal-graph"), "kro updates its instance")
}

// TestIdentityAdmission_ShardMode: with controller.namespaceShard each
// release (one per shard, docs/sharding.md) binds its identity policies only
// to its shard's namespaces (label kardinal.io/shard), so a shard's policies
// never refuse another shard's controller; the default shard also binds the
// unlabelled namespaces, through a second binding per policy.
func TestIdentityAdmission_ShardMode(t *testing.T) {
	bindings := func(args ...string) map[string]admissionregistrationv1.ValidatingAdmissionPolicyBinding {
		out := map[string]admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
		for _, d := range render(t, "kardinal-promoter", args...) {
			if d.Kind == "ValidatingAdmissionPolicyBinding" {
				var b admissionregistrationv1.ValidatingAdmissionPolicyBinding
				decodeStrict(t, d, &b)
				out[b.Name] = b
			}
		}
		return out
	}
	policies := []string{}
	for _, obj := range identityAdmissionObjects("kardinal-promoter") {
		if name, ok := strings.CutPrefix(obj, "ValidatingAdmissionPolicyBinding/"); ok {
			policies = append(policies, name)
		}
	}
	// hold-writes (hold-admission.yaml, #1528) exempts this release's
	// controller too, so it is bound the same way.
	policies = append(policies, "kardinal-promoter-hold-writes")
	team := bindings("--set", "controller.namespaceShard=team-b")
	assert.Len(t, team, len(policies))
	for _, name := range policies {
		b, ok := team[name]
		require.True(t, ok, name)
		require.NotNil(t, b.Spec.MatchResources, name)
		assert.Equal(t, map[string]string{"kardinal.io/shard": "team-b"}, b.Spec.MatchResources.NamespaceSelector.MatchLabels, name)
	}
	def := bindings("--set", "controller.namespaceShard=default")
	assert.Len(t, def, 2*len(policies))
	for _, name := range policies {
		labelled, unlabelled := def[name], def[name+"-unlabelled"]
		assert.Equal(t, name, unlabelled.Spec.PolicyName, "the second binding binds the same policy")
		require.NotNil(t, labelled.Spec.MatchResources, name)
		assert.Equal(t, map[string]string{"kardinal.io/shard": "default"}, labelled.Spec.MatchResources.NamespaceSelector.MatchLabels)
		require.NotNil(t, unlabelled.Spec.MatchResources, name)
		require.Len(t, unlabelled.Spec.MatchResources.NamespaceSelector.MatchExpressions, 1)
		assert.Equal(t, "kardinal.io/shard", unlabelled.Spec.MatchResources.NamespaceSelector.MatchExpressions[0].Key)
		assert.Equal(t, metav1.LabelSelectorOpDoesNotExist, unlabelled.Spec.MatchResources.NamespaceSelector.MatchExpressions[0].Operator)
		assert.Equal(t, []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}, unlabelled.Spec.ValidationActions)
	}
}

// TestIdentityAdmission_GateOverridesImpersonated: the gate-overrides policy
// takes the Graph ServiceAccount for kro only without credential-id or
// pod-name in userInfo.extra: kro impersonates it, while a token minted for it
// (kubectl create token) or a Pod running as it carries one (ledger G16).
func TestIdentityAdmission_GateOverridesImpersonated(t *testing.T) {
	vap := identityPolicy(t, "gate-overrides")
	const graphSA = "system:serviceaccount:team-a:kardinal-graph"
	gate := func(expr string) map[string]interface{} {
		return map[string]interface{}{"metadata": map[string]interface{}{"namespace": "team-a",
			"labels": map[string]interface{}{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod"}},
			"spec": map[string]interface{}{"expression": expr}}
	}
	groups := []interface{}{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:team-a"}
	extra := func(k string) map[string]interface{} {
		return map[string]interface{}{"userInfoExtra": map[string]interface{}{k: []interface{}{"x"}}}
	}
	for name, tc := range map[string]struct {
		old, cur map[string]interface{}
		req      []map[string]interface{}
		want     bool
	}{
		"kro (impersonated) creates an instance":     {cur: gate("x"), want: true},
		"kro (impersonated) updates an instance":     {old: gate("x"), cur: gate("y"), want: true},
		"a minted Graph token creates an instance":   {cur: gate("true"), req: []map[string]interface{}{extra("authentication.kubernetes.io/credential-id")}},
		"a Pod as the Graph SA creates an instance":  {cur: gate("true"), req: []map[string]interface{}{extra("authentication.kubernetes.io/pod-name")}},
		"a minted Graph token edits the expression":  {old: gate("x"), cur: gate("true"), req: []map[string]interface{}{extra("authentication.kubernetes.io/credential-id")}},
		"a Pod as the Graph SA edits the expression": {old: gate("x"), cur: gate("true"), req: []map[string]interface{}{extra("authentication.kubernetes.io/pod-name")}},
		"another extra does not make it a token":     {old: gate("x"), cur: gate("y"), req: []map[string]interface{}{extra("example.com/team")}, want: true},
	} {
		assert.Equal(t, tc.want, admitsGroups(t, vap, tc.cur, tc.old, graphSA, groups, tc.req...), name)
	}
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
	assert.Contains(t, vars["mayPause"], `subresource('pause')`)
	assert.Contains(t, vars["mayHold"], `subresource('hold')`)
	assert.Contains(t, vars["mayOverride"], `subresource('override')`)
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
// exempt, mayPause, mayHold, mayOverride, limited) given: the CEL authorizer
// is the API server's, so the test decides who holds which subresource and
// checks the rest of the policy. perms are the subresources a limited caller
// holds (pause, hold, override); none means pause for pipelines and
// override for policygates.
func scopedAdmits(t *testing.T, vap *admissionregistrationv1.ValidatingAdmissionPolicy, resource string,
	object, oldObject map[string]interface{}, user string, limited bool, perms ...string) bool {
	t.Helper()
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
		cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType))
	require.NoError(t, err)
	if limited && len(perms) == 0 {
		perms = []string{map[bool]string{true: "pause", false: "override"}[resource == "pipelines"]}
	}
	has := func(p string) bool {
		for _, q := range perms {
			if q == p {
				return true
			}
		}
		return false
	}
	variables := map[string]interface{}{"limited": limited, "exempt": false,
		"mayPause": has("pause"), "mayHold": has("hold"), "mayOverride": has("override")}
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
	assert.Contains(t, vars["limited"], "variables.mayPause || variables.mayHold || variables.mayOverride")
	assert.Contains(t, vars["limited"], "!variables.check.subresource('edit').check('update').allowed()")
	assert.Contains(t, vars["mayPause"], "variables.check.subresource('pause').check('update').allowed()")
	assert.Contains(t, vars["mayHold"], "variables.check.subresource('hold').check('update').allowed()")
	assert.Contains(t, vars["mayOverride"], "variables.check.subresource('override').check('update').allowed()")
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
	held := func(p map[string]interface{}) map[string]interface{} {
		p["spec"].(map[string]interface{})["holds"] = []interface{}{
			map[string]interface{}{"environment": "prod", "bundle": "app-rollback-1", "reason": "incident", "createdBy": alice}}
		return p
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
		{"hold with pipelines/hold", "pipelines", pipeline(false, "main", nil), held(pipeline(false, "main", nil)), true, true},
		{"release a hold with pipelines/hold", "pipelines", held(pipeline(false, "main", nil)), pipeline(false, "main", nil), true, true},
		{"hold with only pipelines/pause", "pipelines", pipeline(false, "main", nil), held(pipeline(false, "main", nil)), true, false},
		{"pause with only pipelines/hold", "pipelines", pipeline(false, "main", nil), pipeline(true, "main", nil), true, false},
		{"pause and hold with both", "pipelines", pipeline(false, "main", nil), held(pipeline(true, "main", nil)), true, true},
		{"another spec field with both", "pipelines", pipeline(false, "main", nil), held(pipeline(true, "other", nil)), true, false},
	}
	// The subresources a limited caller holds, when not the default.
	perms := map[string][]string{
		"hold with pipelines/hold":           {"hold"},
		"release a hold with pipelines/hold": {"hold"},
		"hold with only pipelines/pause":     {"pause"},
		"pause with only pipelines/hold":     {"hold"},
		"pause and hold with both":           {"pause", "hold"},
		"another spec field with both":       {"pause", "hold"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, scopedAdmits(t, vap, tt.resource, tt.cur, tt.old, alice, tt.limited, perms[tt.name]...))
		})
	}

	// controller.gateOverrideMaxMinutes moves the cap.
	for _, d := range render(t, "kardinal-promoter", "--set", "controller.gateOverrideMaxMinutes=30") {
		if d.Kind == "ValidatingAdmissionPolicy" && d.Name == "kardinal-promoter-scoped-writes" {
			assert.Contains(t, string(d.raw), "duration('30m')")
		}
	}
}

// TestGateOverrideCapIsOneValue: controller.gateOverrideMaxMinutes sets both
// the controller flag the UI API and the PolicyGate reconciler read
// (--gate-override-max-minutes; without it the binary's default, 1440 as in
// ui_api.go and policygate.DefaultMaxOverride) and the scoped-writes
// policy's bound, so the three never differ.
func TestGateOverrideCapIsOneValue(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
		flag bool
	}{
		{nil, "1440", false},
		{[]string{"--set", "controller.gateOverrideMaxMinutes=30"}, "30", true},
		{[]string{"--set-string", "controller.gateOverrideMaxMinutes=45"}, "45", true},
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
		if tc.flag {
			assert.Contains(t, deploy, `--gate-override-max-minutes=`+tc.want)
		} else {
			assert.NotContains(t, deploy, `--gate-override-max-minutes`, "the binary default applies")
		}
	}
}
