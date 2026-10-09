//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestUI_AccessLog reads the access log a controller in TokenReview mode
// writes for a ServiceAccount: its first request is a login (the TokenReview)
// with the user and groups, a pause it may not make is a denial with the RBAC
// reason, a request with a bad token is a denial, and the client address is
// added with --access-log-source-ip. No token appears in the log.
//
// Covers UIAPI-ACCESSLOG-01.
func TestUI_AccessLog(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))

	v := e.ControllerVariant(t, a.ns, []string{"--ui-tokenreview-auth=true", "--access-log-source-ip=true"})
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

	const sa = "dashboard"
	_, err = e.Kube.CoreV1().ServiceAccounts(a.ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa}}, metav1.CreateOptions{})
	require.NoError(t, err)
	user := "system:serviceaccount:" + a.ns + ":" + sa
	// The UI lists read every namespace, so the viewer reads cluster-wide;
	// it may not update anything.
	viewer := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: a.ns + "-viewer"}, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"kardinal.io"}, Resources: []string{"pipelines", "bundles"}, Verbs: []string{"get", "list"}}}}
	_, err = e.Kube.RbacV1().ClusterRoles().Create(ctx, viewer, metav1.CreateOptions{})
	require.NoError(t, err)
	vb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: a.ns + "-viewer"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: viewer.Name},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: a.ns}}}
	_, err = e.Kube.RbacV1().ClusterRoleBindings().Create(ctx, vb, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = e.Kube.RbacV1().ClusterRoleBindings().Delete(context.Background(), vb.Name, metav1.DeleteOptions{})
		_ = e.Kube.RbacV1().ClusterRoles().Delete(context.Background(), viewer.Name, metav1.DeleteOptions{})
	})
	framework.Eventually(t, time.Minute, "the variant may create TokenReviews and the viewer may get Pipelines", func(context.Context) (bool, string) {
		return e.Can(t, framework.ServiceAccountUser(framework.ControllerNamespace, v.Name),
				framework.Access{Verb: "create", Group: "authentication.k8s.io", Resource: "tokenreviews"}) &&
				e.Can(t, user, framework.Access{Verb: "list", Group: "kardinal.io", Resource: "bundles"}),
			"not yet"
	})
	tr, err := e.Kube.CoreV1().ServiceAccounts(a.ns).CreateToken(ctx, sa, &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](600)}}, metav1.CreateOptions{})
	require.NoError(t, err)
	token := tr.Status.Token

	since := time.Now().Add(-time.Second)
	ui := framework.UIClient{BaseURL: v.UIURL, Token: token}
	framework.Eventually(t, time.Minute, "the viewer reads its Pipeline", func(context.Context) (bool, string) {
		r := ui.Get(t, uiAPI+"/pipelines/"+pipelineName+"/bundles?namespace="+a.ns)
		return r.Status == http.StatusOK, r.String()
	})
	r := ui.Post(t, uiAPI+"/pause", map[string]string{"pipeline": pipelineName, "namespace": a.ns})
	require.Equal(t, http.StatusForbidden, r.Status, r.String())
	bad := framework.UIClient{BaseURL: v.UIURL, Token: "not-a-token"}.Get(t, uiAPI+"/pipelines")
	require.Equal(t, http.StatusUnauthorized, bad.Status)

	type line struct {
		Component, Access, Server, Method, Path, User, Auth, Reason, SourceIP string
		Status                                                                int
		Groups                                                                []string
	}
	var got []line
	framework.Eventually(t, time.Minute, "the access log lines", func(context.Context) (bool, string) {
		got = nil
		logs := e.VariantLogs(t, v, since)
		for _, l := range strings.Split(logs, "\n") {
			var ln line
			if json.Unmarshal([]byte(l), &ln) == nil && ln.Component == "access" {
				got = append(got, ln)
			}
		}
		require.NotContains(t, logs, token, "the token is never logged")
		var login, denied, unauth bool
		for _, ln := range got {
			login = login || ln.Access == "login" && ln.User == user
			denied = denied || ln.Access == "denied" && ln.Status == http.StatusForbidden && ln.Path == uiAPI+"/pause"
			unauth = unauth || ln.Access == "denied" && ln.Status == http.StatusUnauthorized
		}
		return login && denied && unauth, logs
	})
	for _, ln := range got {
		assert.Equal(t, "ui", ln.Server)
		assert.NotEmpty(t, ln.SourceIP, "--access-log-source-ip")
		switch {
		case ln.Access == "login":
			assert.Equal(t, "tokenreview", ln.Auth)
			assert.Contains(t, ln.Groups, "system:serviceaccounts:"+a.ns)
		case ln.Access == "denied" && ln.Status == http.StatusForbidden:
			assert.Equal(t, user, ln.User)
			assert.Equal(t, http.MethodPost, ln.Method)
			assert.Contains(t, ln.Reason, "cannot update")
		case ln.Access == "denied" && ln.Status == http.StatusUnauthorized:
			assert.Equal(t, "unauthorized", ln.Reason)
		}
	}
}
