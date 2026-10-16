//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// waitBoundNamed waits until the RBAC authorizer grants what the RoleBinding
// ns/name binds (ns "": the ClusterRoleBinding name): a SubjectAccessReview
// for each of its user and ServiceAccount subjects allows the first rule of
// the role it refers to. The API server's authorizer reads bindings from an
// informer, so a request made right after the binding is created can still
// be forbidden; TestGraph_GateSquatterRefused failed that way. A rule with
// resourceNames is asked for its first name; a role with no resource rule
// (only nonResourceURLs) is not waited for.
func waitBoundNamed(t *testing.T, e *framework.Env, ns, name string) {
	t.Helper()
	ctx := context.Background()
	var ref rbacv1.RoleRef
	var subjects []rbacv1.Subject
	if ns == "" {
		b, err := e.Kube.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err)
		ref, subjects = b.RoleRef, b.Subjects
	} else {
		b, err := e.Kube.RbacV1().RoleBindings(ns).Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err)
		ref, subjects = b.RoleRef, b.Subjects
	}
	var rules []rbacv1.PolicyRule
	if ref.Kind == "Role" {
		r, err := e.Kube.RbacV1().Roles(ns).Get(ctx, ref.Name, metav1.GetOptions{})
		require.NoError(t, err)
		rules = r.Rules
	} else {
		r, err := e.Kube.RbacV1().ClusterRoles().Get(ctx, ref.Name, metav1.GetOptions{})
		require.NoError(t, err)
		rules = r.Rules
	}
	a, ok := firstAccess(rules, ns)
	if !ok {
		return
	}
	for _, s := range subjects {
		var user string
		switch s.Kind {
		case rbacv1.UserKind:
			user = s.Name
		case rbacv1.ServiceAccountKind:
			user = framework.ServiceAccountUser(s.Namespace, s.Name)
		default:
			continue // a Group: SubjectAccessReview needs a user in it
		}
		framework.Eventually(t, 30*time.Second, fmt.Sprintf("RBAC of binding %s/%s in force for %s (%s)", ns, name, user, a),
			func(context.Context) (bool, string) { return e.Can(t, user, a), "not yet allowed" })
	}
}

// firstAccess is a request the first resource rule of rules allows in ns.
func firstAccess(rules []rbacv1.PolicyRule, ns string) (framework.Access, bool) {
	for _, r := range rules {
		if len(r.Resources) == 0 || len(r.Verbs) == 0 || len(r.APIGroups) == 0 {
			continue
		}
		a := framework.Access{Namespace: ns, Verb: r.Verbs[0], Group: r.APIGroups[0], Resource: r.Resources[0]}
		if a.Verb == "*" {
			a.Verb = "get"
		}
		if a.Group == "*" {
			a.Group = ""
		}
		if a.Resource == "*" {
			a.Resource = "configmaps"
		}
		if res, sub, ok := cutSubresource(a.Resource); ok {
			a.Resource, a.Subresource = res, sub
		}
		if len(r.ResourceNames) > 0 {
			a.Name = r.ResourceNames[0]
		}
		return a, true
	}
	return framework.Access{}, false
}

// cutSubresource splits "bundles/reject" into the resource and subresource.
func cutSubresource(r string) (string, string, bool) {
	for i := 0; i < len(r); i++ {
		if r[i] == '/' {
			return r[:i], r[i+1:], true
		}
	}
	return r, "", false
}
