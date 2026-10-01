// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// ControllerServiceAccount is the controller's ServiceAccount in
// ControllerNamespace (the chart's release name in hack/e2e).
const ControllerServiceAccount = "kardinal-promoter"

// GrantArgoPatch lets the controller patch only the named Argo CD
// Applications, the RBAC the argocd update strategy needs (the chart grants
// get/list/watch; rbac.argocdApplicationsWrite adds patch on every
// Application). name names the Role and RoleBinding in ArgoCDNamespace; both
// are deleted when the test ends.
func (e *Env) GrantArgoPatch(t *testing.T, name string, apps ...string) {
	t.Helper()
	e.BindArgoRules(t, name, []rbacv1.PolicyRule{{
		APIGroups: []string{"argoproj.io"}, Resources: []string{"applications"},
		ResourceNames: apps, Verbs: []string{"get", "patch"},
	}})
}

// BindArgoRules binds rules on Argo CD Applications to the controller in
// ArgoCDNamespace, as a Role and RoleBinding named name that are deleted when
// the test ends.
func (e *Env) BindArgoRules(t *testing.T, name string, rules []rbacv1.PolicyRule) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	meta := metav1.ObjectMeta{Name: name, Namespace: ArgoCDNamespace, Labels: map[string]string{"kardinal.io/e2e": "true"}}
	rbac := e.Kube.RbacV1()
	if _, err := rbac.Roles(ArgoCDNamespace).Create(ctx, &rbacv1.Role{ObjectMeta: meta, Rules: rules}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Role %s/%s: %v", ArgoCDNamespace, name, err)
	}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: meta,
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: ControllerServiceAccount,
			Namespace: ControllerNamespace}},
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, err := range []error{
			rbac.RoleBindings(ArgoCDNamespace).Delete(ctx, name, metav1.DeleteOptions{}),
			rbac.Roles(ArgoCDNamespace).Delete(ctx, name, metav1.DeleteOptions{}),
		} {
			if err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("delete RBAC %s/%s: %v", ArgoCDNamespace, name, err)
			}
		}
	})
	if _, err := rbac.RoleBindings(ArgoCDNamespace).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create RoleBinding %s/%s: %v", ArgoCDNamespace, name, err)
	}
}

// ControllerCan reports whether the controller's ServiceAccount may do verb
// on the Argo CD Application name in ArgoCDNamespace, or on every
// Application when name is "" (a SubjectAccessReview).
func (e *Env) ControllerCan(t *testing.T, verb, name string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	review, err := e.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authv1.SubjectAccessReview{
		Spec: authv1.SubjectAccessReviewSpec{
			User: "system:serviceaccount:" + ControllerNamespace + ":" + ControllerServiceAccount,
			ResourceAttributes: &authv1.ResourceAttributes{Namespace: ArgoCDNamespace, Verb: verb,
				Group: ApplicationGVR.Group, Resource: ApplicationGVR.Resource, Name: name},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("SubjectAccessReview %s applications: %v", verb, err)
	}
	return review.Status.Allowed
}

// ChartApplicationRules renders the repo's chart with helm template and the
// given --set values and returns the rules on Argo CD Applications in the
// controller's ClusterRole (the chart's manager role; its graph-reader and
// kro-watch roles also read Applications). helm must be on PATH; it runs with
// an empty kubeconfig and never contacts a cluster.
func ChartApplicationRules(t *testing.T, set ...string) []rbacv1.PolicyRule {
	t.Helper()
	args := []string{"template", "kardinal-promoter", filepath.Join(repoRoot(t), "chart", "kardinal-promoter"),
		"--namespace", ControllerNamespace}
	for _, s := range set {
		args = append(args, "--set", s)
	}
	cmd := exec.Command("helm", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+os.DevNull)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm %v: %v\n%s", args, err, stderr.String())
	}
	var rules []rbacv1.PolicyRule
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	for {
		var doc struct {
			Kind     string              `json:"kind"`
			Metadata metav1.ObjectMeta   `json:"metadata"`
			Rules    []rbacv1.PolicyRule `json:"rules"`
		}
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode helm template output: %v", err)
		}
		if doc.Kind != "ClusterRole" || doc.Metadata.Name != ControllerServiceAccount+"-manager-role" {
			continue
		}
		for _, r := range doc.Rules {
			if slices.Contains(r.APIGroups, ApplicationGVR.Group) && slices.Contains(r.Resources, ApplicationGVR.Resource) {
				rules = append(rules, r)
			}
		}
	}
	return rules
}
