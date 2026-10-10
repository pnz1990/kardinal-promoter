//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestUI_APIApprovalsTokenReview (E6): a ServiceAccount authenticated by
// the UI's TokenReview mode approves a Bundle from the UI API. The Approval
// names the ServiceAccount and its groups and carries
// kardinal.io/recorded-via: ui, the chart's approvals policy admits the
// controller's write, the approval gate counts it and prod promotes; the
// same user revokes it through the UI. A caller without create on approvals
// is refused and writes nothing.
//
// Covers UI-APPROVE-01.
func TestUI_APIApprovalsTokenReview(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	ctx := context.Background()
	g := framework.Gate(a.ns, "one-approver", "prod", "true", recheck)
	g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedGroups: []string{"system:serviceaccounts:" + a.ns}}
	e.CreateGate(t, g)
	a.apply(t, a.pipeline(nil))

	v := e.ControllerVariant(t, a.ns, []string{"--ui-tokenreview-auth=true"})
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: v.Name + "-tokenreview"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: tokenReviewClusterRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: v.Name, Namespace: framework.ControllerNamespace}},
	}
	_, err := e.Kube.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = e.Kube.RbacV1().ClusterRoleBindings().Delete(context.Background(), crb.Name, metav1.DeleteOptions{})
	})

	tokenFor := func(sa string, rules []rbacv1.PolicyRule) string {
		t.Helper()
		_, err := e.Kube.CoreV1().ServiceAccounts(a.ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa}}, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = e.Kube.RbacV1().Roles(a.ns).Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: sa, Namespace: a.ns}, Rules: rules},
			metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = e.Kube.RbacV1().RoleBindings(a.ns).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: sa, Namespace: a.ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: sa},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: a.ns}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		tr, err := e.Kube.CoreV1().ServiceAccounts(a.ns).CreateToken(ctx, sa, &authnv1.TokenRequest{
			Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](1800)}}, metav1.CreateOptions{})
		require.NoError(t, err)
		return tr.Status.Token
	}
	read := []rbacv1.PolicyRule{
		{APIGroups: []string{"kardinal.io"}, Resources: []string{"pipelines", "bundles"}, Verbs: []string{"get", "list"}},
		{APIGroups: []string{"kardinal.io"}, Resources: []string{"approvals"}, Verbs: []string{"list"}},
	}
	approver := tokenFor("approver", append(read, rbacv1.PolicyRule{APIGroups: []string{"kardinal.io"},
		Resources: []string{"approvals"}, Verbs: []string{"create", "delete"}}))
	watcher := tokenFor("watcher", read)
	user := "system:serviceaccount:" + a.ns + ":approver"
	framework.Eventually(t, time.Minute, "the API server to grant the bindings", func(ctx context.Context) (bool, string) {
		for _, check := range []struct {
			user, ns string
			ra       authzv1.ResourceAttributes
		}{
			{"system:serviceaccount:" + framework.ControllerNamespace + ":" + v.Name, "",
				authzv1.ResourceAttributes{Verb: "create", Group: "authentication.k8s.io", Resource: "tokenreviews"}},
			{user, a.ns, authzv1.ResourceAttributes{Namespace: a.ns, Verb: "create", Group: "kardinal.io", Resource: "approvals"}},
		} {
			sar, err := e.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{
				Spec: authzv1.SubjectAccessReviewSpec{User: check.user, ResourceAttributes: &check.ra,
					Groups: []string{"system:serviceaccounts", "system:authenticated"}}}, metav1.CreateOptions{})
			if err != nil {
				return false, err.Error()
			}
			if !sar.Status.Allowed {
				return false, fmt.Sprintf("%s cannot %s %s", check.user, check.ra.Verb, check.ra.Resource)
			}
		}
		return true, ""
	})

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)

	body := map[string]any{"bundle": bundle, "environment": "prod", "namespace": a.ns, "decision": "approve", "comment": "from the UI"}
	r := framework.UIClient{BaseURL: v.UIURL, Token: watcher}.Post(t, uiAPI+"/approvals", body)
	require.Equal(t, http.StatusForbidden, r.Status, "no create on approvals: %s", r)
	r = framework.UIClient{BaseURL: v.UIURL, Token: approver}.Post(t, uiAPI+"/approvals", body)
	require.Equal(t, http.StatusOK, r.Status, r.String())
	assert.Contains(t, r.String(), "Recorded: "+user+" approves "+bundle+" for prod")

	var list v1alpha1.ApprovalList
	require.NoError(t, e.Client.List(ctx, &list, client.InNamespace(a.ns)))
	require.Len(t, list.Items, 1, "only the approver's Approval")
	ap := list.Items[0]
	assert.Equal(t, user, ap.Spec.User)
	assert.Contains(t, ap.Spec.Groups, "system:serviceaccounts:"+a.ns, "the TokenReview groups")
	assert.Equal(t, "ui", ap.Annotations[lifecycle.AnnotationRecordedVia])
	assert.Equal(t, "from the UI", ap.Spec.Comment)

	rbVerified(t, a, bundle, "prod")
	assertEnvAt(t, a, "prod", fixtures.V2)

	r = framework.UIClient{BaseURL: v.UIURL, Token: approver}.Post(t, uiAPI+"/approvals",
		map[string]any{"bundle": bundle, "environment": "prod", "namespace": a.ns, "revoke": true})
	require.Equal(t, http.StatusOK, r.Status, "the approvals policy lets the controller revoke a UI Approval: %s", r)
	require.NoError(t, e.Client.List(ctx, &list, client.InNamespace(a.ns)))
	assert.Empty(t, list.Items)
}
