// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"slices"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// variantServiceAccount creates ServiceAccount name in ControllerNamespace for
// a controller variant. It binds it, with one ClusterRoleBinding each, to the
// ClusterRoles that the chart's ServiceAccount chartSA is bound to through
// ClusterRoleBindings, and to nothing else. In particular it gets no
// RoleBinding to the chart's <fullname>-leader-election Role (Leases and the
// kardinal-version ConfigMap). The variant then cannot read or write the
// leader election Lease, so it stays a standby even when the chart's
// controller misses a renewal. The test fails if the ServiceAccount can still
// get, create or update Leases in ControllerNamespace. The ServiceAccount and
// its bindings are deleted when the test ends.
func (e *Env) variantServiceAccount(t *testing.T, name, chartSA string, labels map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	crbs, err := e.Kube.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list ClusterRoleBindings: %v", err)
	}
	var roles []string
	for _, b := range crbs.Items {
		if b.RoleRef.Kind != "ClusterRole" {
			continue
		}
		for _, s := range b.Subjects {
			if s.Kind == rbacv1.ServiceAccountKind && s.Name == chartSA && s.Namespace == ControllerNamespace {
				roles = append(roles, b.RoleRef.Name)
				break
			}
		}
	}
	slices.Sort(roles)
	roles = slices.Compact(roles)
	if len(roles) == 0 {
		t.Fatalf("no ClusterRoleBinding binds ServiceAccount %s/%s; a controller variant would have no access",
			ControllerNamespace, chartSA)
	}

	chart, err := e.Kube.CoreV1().ServiceAccounts(ControllerNamespace).Get(ctx, chartSA, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read the controller ServiceAccount: %v", err)
	}
	sa := &corev1.ServiceAccount{
		ObjectMeta:       metav1.ObjectMeta{Name: name, Namespace: ControllerNamespace, Labels: labels},
		ImagePullSecrets: chart.ImagePullSecrets,
	}
	var bindings []string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, b := range bindings {
			if err := e.Kube.RbacV1().ClusterRoleBindings().Delete(ctx, b, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("delete ClusterRoleBinding %s: %v", b, err)
			}
		}
		if err := e.Kube.CoreV1().ServiceAccounts(ControllerNamespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete ServiceAccount %s: %v", name, err)
		}
	})
	if _, err := e.Kube.CoreV1().ServiceAccounts(ControllerNamespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ServiceAccount %s: %v", name, err)
	}
	for _, r := range roles {
		b := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-" + r, Labels: labels},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: r},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: ControllerNamespace}},
		}
		if _, err := e.Kube.RbacV1().ClusterRoleBindings().Create(ctx, b, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create ClusterRoleBinding %s: %v", b.Name, err)
		}
		bindings = append(bindings, b.Name)
	}

	user := "system:serviceaccount:" + ControllerNamespace + ":" + name
	groups := []string{"system:serviceaccounts", "system:serviceaccounts:" + ControllerNamespace, "system:authenticated"}
	for _, verb := range []string{"get", "create", "update"} {
		review, err := e.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
			Spec: authorizationv1.SubjectAccessReviewSpec{User: user, Groups: groups,
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Namespace: ControllerNamespace, Verb: verb, Group: "coordination.k8s.io", Resource: "leases"}},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("SubjectAccessReview %s leases for %s: %v", verb, user, err)
		}
		if review.Status.Allowed {
			t.Fatalf("%s may %s Leases in %s (%s); a controller variant could take the leader lease",
				user, verb, ControllerNamespace, review.Status.Reason)
		}
	}
	t.Logf("controller variant ServiceAccount %s/%s: ClusterRoles %v, no Lease access", ControllerNamespace, name, roles)
}
