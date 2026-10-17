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

const controllerUser = "system:serviceaccount:" + releaseNS + ":kardinal-promoter"

func gitopsWritesPolicy(t *testing.T, args ...string) (*admissionregistrationv1.ValidatingAdmissionPolicy, *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	t.Helper()
	var vap *admissionregistrationv1.ValidatingAdmissionPolicy
	var binding *admissionregistrationv1.ValidatingAdmissionPolicyBinding
	for _, d := range render(t, "kardinal-promoter", args...) {
		if d.Name != "kardinal-promoter-gitops-writes" {
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
	require.NotNil(t, vap, "gitops-writes policy rendered")
	require.NotNil(t, binding, "gitops-writes binding rendered")
	return vap, binding
}

// gitopsAdmits evaluates the policy as the API server would for an UPDATE by
// user of an object of group/resource from oldObj to newObj, with mayUpdate
// the authorizer's answer for update on it (cel-go has no authorizer). It
// reports whether the policy matched and the failed validations.
func gitopsAdmits(t *testing.T, vap *admissionregistrationv1.ValidatingAdmissionPolicy, user, group, resource string,
	mayUpdate bool, oldObj, newObj map[string]interface{}) (matched bool, failed []string) {
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
	variables := map[string]interface{}{}
	vars := map[string]interface{}{
		"object": newObj, "oldObject": oldObj, "variables": variables,
		"request": map[string]interface{}{"operation": "UPDATE", "namespace": "argocd", "name": "web",
			"resource": map[string]interface{}{"group": group, "resource": resource},
			"userInfo": map[string]interface{}{"username": user}},
	}
	for _, c := range vap.Spec.MatchConditions {
		if ok, _ := eval(c.Expression, vars).(bool); !ok {
			return false, nil
		}
	}
	for _, v := range vap.Spec.Variables {
		if v.Name == "mayUpdate" {
			assert.Contains(t, v.Expression, "check('update')")
			variables[v.Name] = mayUpdate
			continue
		}
		variables[v.Name] = eval(v.Expression, vars)
	}
	for _, v := range vap.Spec.Validations {
		if ok, _ := eval(v.Expression, vars).(bool); !ok {
			failed = append(failed, v.Expression)
		}
	}
	return true, failed
}

// TestHelmTemplateGitOpsWritesPolicy: the controller may set only the
// refresh annotation of an Application (argocd.argoproj.io/refresh) or a
// Flux object (reconcile.fluxcd.io/requestedAt); any other change to spec,
// status, operation, labels, annotations, finalizers or owner references is
// denied unless it may update the object. Other users are not matched.
func TestHelmTemplateGitOpsWritesPolicy(t *testing.T) {
	t.Parallel()
	vap, binding := gitopsWritesPolicy(t)
	assert.Equal(t, admissionregistrationv1.Fail, *vap.Spec.FailurePolicy)
	assert.Equal(t, []admissionregistrationv1.ValidationAction{"Deny"}, binding.Spec.ValidationActions)
	assert.Nil(t, binding.Spec.MatchResources, "cluster-wide: the objects are in the GitOps tools' namespaces")
	var kinds []string
	for _, r := range vap.Spec.MatchConstraints.ResourceRules {
		assert.Equal(t, []admissionregistrationv1.OperationType{"UPDATE"}, r.Operations)
		kinds = append(kinds, r.APIGroups[0]+"/"+r.Resources[0])
	}
	assert.ElementsMatch(t, []string{"argoproj.io/applications", "kustomize.toolkit.fluxcd.io/kustomizations",
		"helm.toolkit.fluxcd.io/helmreleases", "source.toolkit.fluxcd.io/gitrepositories"}, kinds)

	obj := func(mutate func(m map[string]interface{})) map[string]interface{} {
		m := map[string]interface{}{
			"metadata": map[string]interface{}{"name": "web", "namespace": "argocd",
				"labels":      map[string]interface{}{"team": "a"},
				"annotations": map[string]interface{}{"owner": "a"}},
			"spec":   map[string]interface{}{"project": "default", "source": map[string]interface{}{"targetRevision": "main"}},
			"status": map[string]interface{}{"sync": map[string]interface{}{"status": "Synced"}},
		}
		if mutate != nil {
			mutate(m)
		}
		return m
	}
	ann := func(k, v string) func(map[string]interface{}) {
		return func(m map[string]interface{}) {
			m["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})[k] = v
		}
	}
	argo := [2]string{"argoproj.io", "applications"}
	ks := [2]string{"kustomize.toolkit.fluxcd.io", "kustomizations"}
	repo := [2]string{"source.toolkit.fluxcd.io", "gitrepositories"}
	tests := []struct {
		name      string
		user      string
		kind      [2]string
		mayUpdate bool
		old, new  func(map[string]interface{})
		unmatched bool
		denied    bool
	}{
		{name: "Application refresh annotation", kind: argo, new: ann("argocd.argoproj.io/refresh", "normal")},
		{name: "Application refresh annotation removed", kind: argo, old: ann("argocd.argoproj.io/refresh", "normal")},
		{name: "Kustomization requestedAt", kind: ks, new: ann("reconcile.fluxcd.io/requestedAt", "2026-10-11T12:00:00Z ns/step")},
		{name: "GitRepository requestedAt changed", kind: repo, old: ann("reconcile.fluxcd.io/requestedAt", "a"),
			new: ann("reconcile.fluxcd.io/requestedAt", "b")},
		{name: "Argo CD key on a Kustomization", kind: ks, new: ann("argocd.argoproj.io/refresh", "normal"), denied: true},
		{name: "Flux key on an Application", kind: argo, new: ann("reconcile.fluxcd.io/requestedAt", "x"), denied: true},
		{name: "another annotation changed", kind: argo, new: ann("owner", "b"), denied: true},
		{name: "another annotation added", kind: argo, new: ann("argocd.argoproj.io/sync-options", "Prune=false"), denied: true},
		{name: "another annotation removed", kind: argo, new: func(m map[string]interface{}) {
			delete(m["metadata"].(map[string]interface{})["annotations"].(map[string]interface{}), "owner")
		}, denied: true},
		{name: "spec changed", kind: argo, new: func(m map[string]interface{}) {
			m["spec"].(map[string]interface{})["source"] = map[string]interface{}{"targetRevision": "evil"}
		}, denied: true},
		{name: "spec changed with update allowed (rbac.argocdApplicationsWrite)", kind: argo, mayUpdate: true,
			new: func(m map[string]interface{}) { m["spec"].(map[string]interface{})["project"] = "other" }},
		{name: "Kustomization spec changed", kind: ks, new: func(m map[string]interface{}) {
			m["spec"].(map[string]interface{})["path"] = "./evil"
		}, denied: true},
		{name: "status changed", kind: argo, new: func(m map[string]interface{}) { m["status"] = map[string]interface{}{} }, denied: true},
		{name: "Application sync operation added", kind: argo, new: func(m map[string]interface{}) {
			m["operation"] = map[string]interface{}{"sync": map[string]interface{}{}}
		}, denied: true},
		{name: "label changed", kind: argo, new: func(m map[string]interface{}) {
			m["metadata"].(map[string]interface{})["labels"] = map[string]interface{}{"team": "b"}
		}, denied: true},
		{name: "finalizer added", kind: argo, new: func(m map[string]interface{}) {
			m["metadata"].(map[string]interface{})["finalizers"] = []interface{}{"x"}
		}, denied: true},
		{name: "another top-level field added", kind: argo, new: func(m map[string]interface{}) {
			m["extra"] = map[string]interface{}{"x": "y"}
		}, denied: true},
		{name: "another metadata field changed", kind: argo, new: func(m map[string]interface{}) {
			m["metadata"].(map[string]interface{})["generateName"] = "x-"
		}, denied: true},
		{name: "fields the API server maintains change with the refresh", kind: argo, new: func(m map[string]interface{}) {
			md := m["metadata"].(map[string]interface{})
			md["resourceVersion"], md["generation"] = "2", int64(2)
			md["managedFields"] = []interface{}{map[string]interface{}{"manager": "kardinal"}}
			md["annotations"].(map[string]interface{})["argocd.argoproj.io/refresh"] = "normal"
		}},
		{name: "another user is not matched", user: "alice", kind: argo, new: func(m map[string]interface{}) {
			m["spec"].(map[string]interface{})["project"] = "other"
		}, unmatched: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := tt.user
			if user == "" {
				user = controllerUser
			}
			matched, failed := gitopsAdmits(t, vap, user, tt.kind[0], tt.kind[1], tt.mayUpdate, obj(tt.old), obj(tt.new))
			assert.Equal(t, !tt.unmatched, matched)
			assert.Equal(t, tt.denied, len(failed) > 0, "failed: %v", failed)
		})
	}

	// admission.controllerUsernames are confined too.
	other := "system:serviceaccount:kardinal-system:kardinal-promoter-b"
	vapB, _ := gitopsWritesPolicy(t, "--set", "admission.controllerUsernames[0]="+other)
	matched, failed := gitopsAdmits(t, vapB, other, "argoproj.io", "applications", false, obj(nil),
		obj(func(m map[string]interface{}) { m["spec"].(map[string]interface{})["project"] = "other" }))
	assert.True(t, matched)
	assert.NotEmpty(t, failed)
}
