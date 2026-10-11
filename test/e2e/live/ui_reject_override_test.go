//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// uiRejectOverrideSetup is a controller in TokenReview mode and a Pipeline
// in its own namespace whose test environment is held by gate "hold", with
// a Bundle waiting on it (its gate instance exists), and the chart's viewer,
// promoter and approver roles bound in the namespace.
func uiRejectOverrideSetup(t *testing.T, e *framework.Env) (ns, bundle string, ui func(roleUser) framework.UIClient,
	viewer, promoter, approver roleUser) {
	t.Helper()
	ctx := context.Background()
	ns = e.Namespace(t)
	require.NoError(t, e.Client.Create(ctx, &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: pipelineName, Namespace: ns},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://git.example/kardinal/" + ns + ".git", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test", Path: fixtures.Path("test"), Approval: "auto",
				Update: v1alpha1.UpdateConfig{Strategy: "kustomize"}}},
		}}))
	e.CreateGate(t, framework.Gate(ns, "hold", "test", "false", recheck))
	bundle = e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	framework.Eventually(t, 2*time.Minute, "the Bundle's instance of gate hold", func(ctx context.Context) (bool, string) {
		var gates v1alpha1.PolicyGateList
		if err := e.Client.List(ctx, &gates, client.InNamespace(ns),
			client.MatchingLabels{"kardinal.io/bundle": bundle, "kardinal.io/gate-template": "hold"}); err != nil {
			return false, err.Error()
		}
		return len(gates.Items) == 1, fmt.Sprintf("%d instances", len(gates.Items))
	})

	v := e.ControllerVariant(t, ns, []string{"--ui-tokenreview-auth=true"})
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
	role := func(r string) string { return framework.ControllerDeployment + "-" + r }
	viewer = newRoleUser(t, e, ns, "viewer", role("viewer"))
	promoter = newRoleUser(t, e, ns, "promoter", role("promoter"))
	approver = newRoleUser(t, e, ns, "approver", role("approver"))
	framework.Eventually(t, time.Minute, "the bindings to take effect", func(ctx context.Context) (bool, string) {
		for _, c := range []struct {
			who  string
			attr authzv1.ResourceAttributes
		}{
			{promoter.username(), authzv1.ResourceAttributes{Namespace: ns, Verb: "update", Group: "kardinal.io", Resource: "bundles", Subresource: "reject"}},
			{approver.username(), authzv1.ResourceAttributes{Namespace: ns, Verb: "update", Group: "kardinal.io", Resource: "policygates", Subresource: "override"}},
			{viewer.username(), authzv1.ResourceAttributes{Namespace: ns, Verb: "list", Group: "kardinal.io", Resource: "pipelines"}},
		} {
			if !e.Can(t, c.who, framework.Access{Verb: c.attr.Verb, Group: c.attr.Group, Resource: c.attr.Resource,
				Subresource: c.attr.Subresource, Namespace: c.attr.Namespace}) {
				return false, fmt.Sprintf("%s cannot %s %s/%s", c.who, c.attr.Verb, c.attr.Resource, c.attr.Subresource)
			}
		}
		return true, ""
	})
	ui = func(u roleUser) framework.UIClient { return framework.UIClient{BaseURL: v.UIURL, Token: u.token} }
	return ns, bundle, ui, viewer, promoter, approver
}

// TestUI_APIPipelineOverride checks POST /api/v1/ui/override live, in
// TokenReview mode with the chart's roles: the viewer and the promoter are
// refused (no policygates/override) and nothing is written; the approver
// overrides gate hold in environment test for the in-progress Bundle, as
// kardinal override --stage does: its instance gets one override in the
// approver's name, the gate passes, and the controller writes a
// GateOverridden AuditEvent naming the approver.
//
// Covers UI-OVERRIDE-ENV-01.
func TestUI_APIPipelineOverride(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns, bundle, ui, viewer, promoter, approver := uiRejectOverrideSetup(t, e)
	body := map[string]interface{}{"pipeline": pipelineName, "namespace": ns, "gate": "hold", "environment": "test",
		"reason": "e2e: P0 hotfix", "expiresInMinutes": 10}
	for _, u := range []roleUser{viewer, promoter} {
		r := ui(u).Post(t, uiAPI+"/override", body)
		assert.Equal(t, http.StatusForbidden, r.Status, "%s may not override: %s", u.name, r)
	}
	instance := func() v1alpha1.PolicyGate {
		var gates v1alpha1.PolicyGateList
		require.NoError(t, e.Client.List(ctx, &gates, client.InNamespace(ns),
			client.MatchingLabels{"kardinal.io/bundle": bundle, "kardinal.io/gate-template": "hold"}))
		require.Len(t, gates.Items, 1)
		return gates.Items[0]
	}
	assert.Empty(t, instance().Spec.Overrides, "nothing written by the refused callers")

	r := ui(approver).Post(t, uiAPI+"/override", body)
	require.Equal(t, http.StatusOK, r.Status, "the approver overrides: %s", r)
	var resp struct {
		Gates     []string `json:"gates"`
		ExpiresAt string   `json:"expiresAt"`
	}
	r.JSON(t, &resp)
	g := instance()
	assert.Equal(t, []string{g.Name}, resp.Gates)
	require.Len(t, g.Spec.Overrides, 1)
	o := g.Spec.Overrides[0]
	assert.Equal(t, approver.username(), o.CreatedBy, "the verified requester")
	assert.Equal(t, "test", o.Stage)
	assert.Equal(t, "e2e: P0 hotfix", o.Reason)
	framework.Eventually(t, time.Minute, "the gate to pass and its GateOverridden record", func(ctx context.Context) (bool, string) {
		var cur v1alpha1.PolicyGate
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(&g), &cur); err != nil {
			return false, err.Error()
		}
		events, err := e.AuditEvents(ctx, ns, bundle)
		if err != nil {
			return false, err.Error()
		}
		for _, ae := range events {
			if ae.Spec.Action == "GateOverridden" && strings.Contains(ae.Spec.Message, approver.username()) {
				return cur.Status.Ready, fmt.Sprintf("ready=%v", cur.Status.Ready)
			}
		}
		return false, fmt.Sprintf("ready=%v, no GateOverridden record yet", cur.Status.Ready)
	})
}

// TestUI_APIReject checks POST /api/v1/ui/reject live, in TokenReview mode
// with the chart's roles: the viewer is refused (no bundles/reject) and
// nothing is written; the promoter rejects the Bundle: spec.rejected names
// the promoter (the controller wrote it, the bundle-rejection policy admits
// that only from the controller), the Bundle turns Rejected, a second
// rejection is a conflict, and the BundleRejected AuditEvent names the
// promoter and the reason. A tenant with update on Bundles cannot write a
// rejection in someone else's name itself.
//
// Covers UI-REJECT-01.
func TestUI_APIReject(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns, bundle, ui, viewer, promoter, approver := uiRejectOverrideSetup(t, e)
	body := map[string]string{"bundle": bundle, "namespace": ns, "reason": "e2e: CVE-2026-1 in the base image"}
	r := ui(viewer).Post(t, uiAPI+"/reject", body)
	assert.Equal(t, http.StatusForbidden, r.Status, "viewer may not reject: %s", r)
	get := func() v1alpha1.Bundle {
		var b v1alpha1.Bundle
		require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundle}, &b))
		return b
	}
	assert.Nil(t, get().Spec.Rejected, "nothing written by the refused caller")

	// A tenant who may update Bundles still cannot reject in another name.
	admin := newRoleUser(t, e, ns, "tenant-admin", framework.ControllerDeployment+"-admin")
	framework.Eventually(t, time.Minute, "the admin binding", func(context.Context) (bool, string) {
		return e.Can(t, admin.username(), framework.Access{Verb: "patch", Group: "kardinal.io", Resource: "bundles", Namespace: ns}), ""
	})
	forged := get()
	at := metav1.Now()
	forged.Spec.Rejected = &v1alpha1.BundleRejection{By: promoter.username(), Reason: "forged", At: &at}
	// A token for the API server's audience (roleUser tokens are kardinal's).
	tr, err := e.Kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, admin.name, &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](600)}}, metav1.CreateOptions{})
	require.NoError(t, err)
	cfg := rest.AnonymousClientConfig(e.Config)
	cfg.BearerToken = tr.Status.Token
	tenant, err := client.New(cfg, client.Options{Scheme: framework.Scheme()})
	require.NoError(t, err)
	err = tenant.Update(ctx, &forged)
	require.Error(t, err, "a rejection in someone else's name")
	assert.True(t, apierrors.IsForbidden(err) || apierrors.IsInvalid(err), err.Error())
	assert.Contains(t, err.Error(), "spec.rejected.by must be your own username")
	assert.Nil(t, get().Spec.Rejected)

	r = ui(promoter).Post(t, uiAPI+"/reject", body)
	require.Equal(t, http.StatusOK, r.Status, "the promoter rejects: %s", r)
	b := get()
	require.NotNil(t, b.Spec.Rejected)
	assert.Equal(t, promoter.username(), b.Spec.Rejected.By, "the verified requester")
	assert.Equal(t, "e2e: CVE-2026-1 in the base image", b.Spec.Rejected.Reason)
	e.WaitBundlePhase(t, ns, bundle, "Rejected", time.Minute)
	r = ui(approver).Post(t, uiAPI+"/reject", body)
	assert.Equal(t, http.StatusConflict, r.Status, "already rejected: %s", r)

	framework.Eventually(t, time.Minute, "the BundleRejected record", func(ctx context.Context) (bool, string) {
		var list v1alpha1.AuditEventList
		if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/action": "BundleRejected"}); err != nil {
			return false, err.Error()
		}
		if len(list.Items) != 1 {
			return false, fmt.Sprintf("%d records", len(list.Items))
		}
		ae := list.Items[0]
		want := promoter.username() + " rejected bundle " + bundle + ": e2e: CVE-2026-1 in the base image"
		return ae.Spec.Message == want && ae.Spec.BundleName == bundle && ae.Spec.Environment == "*", ae.Spec.Message
	})
}
