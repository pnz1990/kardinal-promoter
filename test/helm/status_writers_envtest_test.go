//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestStatusWritersOnAPIServer runs the <release>-status-writers rule on a
// real kube-apiserver (envtest, the oldest and newest supported Kubernetes),
// with real RBAC:
//
//   - a role with resources ["*"] on kardinal.io reaches every status, since
//     RBAC's "*" covers subresources (SubjectAccessReviews say so), and
//     without the policy its holder forges every covered kind's status;
//   - the chart's <release>-admin-extra names its kinds instead, and grants
//     no status, bound in a namespace or cluster-wide (cluster-scoped kinds
//     included), while still granting every verb on the kinds;
//   - with the policy, that holder's status update is refused, for every
//     kind it covers, and a cluster admin's too; the controller's is
//     admitted; a per-promotion instance is left to graph-objects.
//
// Covers STATUS-WRITERS-01.
func TestStatusWritersOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"))
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(repoRoot(t), "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))
	admin, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	ctx := context.Background()

	// The user roles; the policy is installed once the hole is shown.
	var policy []*unstructured.Unstructured
	for _, d := range render(t, "kardinal-promoter") {
		var obj unstructured.Unstructured
		switch {
		case (d.Kind == "ValidatingAdmissionPolicy" || d.Kind == "ValidatingAdmissionPolicyBinding") &&
			(strings.HasSuffix(d.Name, "-status-writers") || strings.HasSuffix(d.Name, "-graph-objects")):
			require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
			policy = append(policy, &obj)
		case d.Kind == "ClusterRole" && d.Name == "kardinal-promoter-admin-extra":
			require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
			require.NoError(t, admin.Create(ctx, &obj), d.Name)
		}
	}

	const ns = "payments"
	require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	// alice is payments' namespace admin, as the chart's user roles make one;
	// carol has the same role cluster-wide (docs/guides/security.md allows
	// both); bob has a role with resources ["*"] on kardinal.io in payments
	// (v0.13's admin-extra, or any hand-written role like it).
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-wildcard"},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"*"}, Verbs: []string{"*"}}}}))
	for _, b := range []struct{ user, role string }{{"alice", "kardinal-promoter-admin-extra"}, {"bob", "kardinal-wildcard"}} {
		require.NoError(t, admin.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: b.user, Namespace: ns},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: b.role},
			Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: b.user}}}))
	}
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "carol"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "kardinal-promoter-admin-extra"},
		Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: "carol"}}}))
	// The controller gets cluster-admin here: the test is about admission, not
	// the controller's RBAC.
	controllerUser := "system:serviceaccount:" + releaseNS + ":kardinal-promoter"
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "controller-admin"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: controllerUser}}}))
	as := func(user string, groups ...string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: append(groups, "system:authenticated")}
		cl, err := client.New(c, client.Options{Scheme: sch})
		require.NoError(t, err)
		return cl
	}
	alice, bob := as("alice"), as("bob")
	controller := as(controllerUser, "system:serviceaccounts", "system:serviceaccounts:"+releaseNS)

	// RBAC, with SubjectAccessReviews: a role with resources ["*"] reaches
	// every status (bob), so RBAC alone cannot keep it the controller's;
	// the chart's admin-extra names its kinds and reaches none (alice in
	// payments, carol cluster-wide, cluster-scoped kinds included), while
	// still granting every verb on the kinds themselves.
	can := func(user, namespace, resource, sub string) bool {
		sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{User: user,
			ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: namespace, Verb: "update",
				Group: v1alpha1.GroupVersion.Group, Resource: resource, Subresource: sub}}}
		require.NoError(t, admin.Create(ctx, sar))
		return sar.Status.Allowed
	}
	for _, kind := range statusWriterKinds {
		assert.True(t, can("bob", ns, kind, "status"), "a resources [\"*\"] role may update %s/status", kind)
		assert.False(t, can("alice", ns, kind, "status"), "admin-extra may not update %s/status", kind)
		assert.True(t, can("alice", ns, kind, ""), "admin-extra may update %s", kind)
	}
	for _, kind := range clusterStatusKinds {
		assert.False(t, can("carol", "", kind, "status"), "admin-extra bound cluster-wide may not update %s/status", kind)
		assert.True(t, can("carol", "", kind, ""), "admin-extra bound cluster-wide may update %s", kind)
	}

	target := func(kind string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion(v1alpha1.GroupVersion.String())
		u.SetKind(statusFixtures[kind].kind)
		u.SetNamespace(ns)
		u.SetName("s-" + kind)
		return u
	}
	patch := func(kind string) client.Patch {
		return client.RawPatch(types.MergePatchType, []byte(`{"status":`+statusFixtures[kind].status+`}`))
	}
	// Without the policy, bob forges the status of every kind: the hole.
	for _, kind := range statusWriterKinds {
		f := statusFixtures[kind]
		obj := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": v1alpha1.GroupVersion.String(), "kind": f.kind,
			"metadata": map[string]interface{}{"name": "s-" + kind, "namespace": ns},
			"spec":     f.spec}}
		require.NoError(t, admin.Create(ctx, obj), kind)
		require.NoError(t, bob.Status().Patch(ctx, target(kind), patch(kind)), "%s: without the policy bob writes status", kind)
	}

	require.Len(t, policy, 4, "status-writers and graph-objects, each with its binding, are rendered")
	for _, o := range policy {
		require.NoError(t, admin.Create(ctx, o), o.GetName())
	}
	for _, kind := range statusWriterKinds {
		// Wait for the policy to load, then: bob and the cluster admin are
		// refused by the policy, alice by RBAC; the controller is admitted.
		require.Eventually(t, func() bool { return bob.Status().Patch(ctx, target(kind), patch(kind)) != nil },
			30*time.Second, 300*time.Millisecond, "%s: bob's status patch is refused", kind)
		err := bob.Status().Patch(ctx, target(kind), patch(kind))
		require.Error(t, err, kind)
		assert.Contains(t, err.Error(), "only kardinal's controller writes", "%s: refused by the policy", kind)
		err = alice.Status().Patch(ctx, target(kind), patch(kind))
		require.Error(t, err, kind)
		assert.True(t, apierrors.IsForbidden(err), "%s: admin-extra grants no status: %v", kind, err)
		err = admin.Status().Patch(ctx, target(kind), patch(kind))
		require.Error(t, err, "%s: the cluster admin too", kind)
		assert.Contains(t, err.Error(), "only kardinal's controller writes", kind)
		require.NoError(t, controller.Status().Patch(ctx, target(kind), patch(kind)), "%s: the controller writes status", kind)
	}

	// A per-promotion MetricCheck instance (kardinal.io/bundle) is
	// graph-objects': it refuses bob, with its own message only.
	f := statusFixtures["metricchecks"]
	inst := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": v1alpha1.GroupVersion.String(), "kind": f.kind,
		"metadata": map[string]interface{}{"name": "inst", "namespace": ns, "labels": map[string]interface{}{"kardinal.io/bundle": "b1"}},
		"spec":     f.spec}}
	require.NoError(t, controller.Create(ctx, inst))
	instTarget := target("metricchecks")
	instTarget.SetName("inst")
	err = bob.Status().Patch(ctx, instTarget, patch("metricchecks"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only kardinal (the promotion Graph or the controller) creates or changes this object")
	assert.NotContains(t, err.Error(), "only kardinal's controller writes", "one policy answers for an instance")
	require.NoError(t, controller.Status().Patch(ctx, instTarget, patch("metricchecks")))
}
