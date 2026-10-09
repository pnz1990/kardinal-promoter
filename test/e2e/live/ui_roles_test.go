//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// roleUser is a ServiceAccount in ns bound with a RoleBinding to
// ClusterRole role, with a TokenRequest token.
type roleUser struct {
	name, ns, token string
}

func (u roleUser) username() string { return "system:serviceaccount:" + u.ns + ":" + u.name }

func newRoleUser(t *testing.T, e *framework.Env, ns, name, clusterRole string) roleUser {
	t.Helper()
	ctx := context.Background()
	_, err := e.Kube.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: ns}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	tr, err := e.Kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, name, &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](1800), Audiences: []string{"kardinal-promoter"}}}, metav1.CreateOptions{})
	require.NoError(t, err)
	return roleUser{name: name, ns: ns, token: tr.Status.Token}
}

// postBundle calls POST /api/v1/bundles on base with token.
func postBundle(t *testing.T, base, token, ns string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"pipeline": pipelineName, "namespace": ns, "type": "image",
		"images": []map[string]string{{"repository": fixtures.Image, "tag": fixtures.V2}},
		"intent": map[string]string{"targetEnvironment": "test"}})
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/bundles", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestUI_UserRoles checks the chart's user roles (rbac.userRoles) through a
// controller in TokenReview mode for both the UI API and the Bundle API,
// with ServiceAccounts bound by RoleBinding in namespace A only:
//   - viewer lists A's Pipeline and not B's (namespace RBAC, no 403), and
//     may not pause (403) or create Bundles (403);
//   - promoter creates a Bundle through the Bundle API in A (201, recorded
//     in kardinal.io/requested-by), not in B (403), pauses and resumes A,
//     and may not override a gate (403);
//   - approver overrides A's gate and may not create Bundles (403);
//   - a binding to Kubernetes' built-in view role grants the viewer rules
//     (aggregation);
//   - promoter and approver hold pipelines/pause and policygates/override,
//     not update on Pipelines or PolicyGates; the UI writes as the
//     controller and records the caller;
//   - a token for the API server's audience gets 401 from both APIs: they
//     accept only --audience kardinal-promoter tokens by default.
//
// Covers RBAC-ROLES-01, BUNDLEAPI-TR-01, TOKENREVIEW-AUD-01.
func TestUI_UserRoles(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	nsA, nsB := e.Namespace(t), e.Namespace(t)
	for _, ns := range []string{nsA, nsB} {
		p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: pipelineName, Namespace: ns},
			Spec: v1alpha1.PipelineSpec{
				Git: v1alpha1.PipelineGit{URL: "https://git.example/kardinal/" + ns + ".git", Branch: "main"},
				Environments: []v1alpha1.EnvironmentSpec{{Name: "test", Path: fixtures.Path("test"), Approval: "auto",
					Update: v1alpha1.UpdateConfig{Strategy: "kustomize"}}},
			}}
		require.NoError(t, e.Client.Create(ctx, p))
	}
	gate := framework.Gate(nsA, "hold", "test", "false", recheck)
	e.CreateGate(t, gate)

	v := e.ControllerVariant(t, nsA, []string{"--ui-tokenreview-auth=true", "--bundle-api-tokenreview-auth=true"})
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
	viewer := newRoleUser(t, e, nsA, "viewer", role("viewer"))
	promoter := newRoleUser(t, e, nsA, "promoter", role("promoter"))
	approver := newRoleUser(t, e, nsA, "approver", role("approver"))
	builtin := newRoleUser(t, e, nsA, "builtin-view", "view")

	// Wait until the API server grants the bindings (the UI caches denials).
	framework.Eventually(t, time.Minute, "the bindings to take effect", func(ctx context.Context) (bool, string) {
		for _, c := range []struct {
			who  string
			attr authzv1.ResourceAttributes
		}{
			{"system:serviceaccount:" + framework.ControllerNamespace + ":" + v.Name, authzv1.ResourceAttributes{Verb: "create", Group: "authentication.k8s.io", Resource: "tokenreviews"}},
			{promoter.username(), authzv1.ResourceAttributes{Namespace: nsA, Verb: "create", Group: "kardinal.io", Resource: "bundles"}},
			{approver.username(), authzv1.ResourceAttributes{Namespace: nsA, Verb: "update", Group: "kardinal.io", Resource: "policygates", Subresource: "override"}},
			{builtin.username(), authzv1.ResourceAttributes{Namespace: nsA, Verb: "list", Group: "kardinal.io", Resource: "pipelines"}},
		} {
			sar, err := e.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{
				Spec: authzv1.SubjectAccessReviewSpec{User: c.who, ResourceAttributes: &c.attr}}, metav1.CreateOptions{})
			if err != nil {
				return false, err.Error()
			}
			if !sar.Status.Allowed {
				return false, fmt.Sprintf("%s cannot %s %s", c.who, c.attr.Verb, c.attr.Resource)
			}
		}
		return true, ""
	})

	// Least privilege: the action roles grant the virtual subresources, not
	// update on the objects.
	for _, c := range []struct {
		who                   roleUser
		resource, subresource string
		want                  bool
	}{
		{promoter, "pipelines", "pause", true},
		{promoter, "pipelines", "", false},
		{approver, "policygates", "override", true},
		{approver, "policygates", "", false},
		{promoter, "policygates", "override", false},
	} {
		assert.Equal(t, c.want, e.Can(t, c.who.username(), framework.Access{Verb: "update", Group: "kardinal.io",
			Resource: c.resource, Subresource: c.subresource, Namespace: nsA}), "%s update %s/%s", c.who.name, c.resource, c.subresource)
	}

	// A token for the API server's audience (no --audience) is refused.
	apiToken, err := e.Kube.CoreV1().ServiceAccounts(nsA).CreateToken(ctx, viewer.name, &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](600)}}, metav1.CreateOptions{})
	require.NoError(t, err)
	r := framework.UIClient{BaseURL: v.UIURL, Token: apiToken.Status.Token}.Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusUnauthorized, r.Status, "API server audience token on the UI: %s", r)
	code, body := postBundle(t, v.URL, apiToken.Status.Token, nsA)
	assert.Equal(t, http.StatusUnauthorized, code, "API server audience token on the Bundle API: %s", body)

	ui := func(u roleUser) framework.UIClient { return framework.UIClient{BaseURL: v.UIURL, Token: u.token} }
	listed := func(u roleUser) map[string]bool {
		t.Helper()
		r := ui(u).Get(t, uiAPI+"/pipelines")
		require.Equal(t, http.StatusOK, r.Status, "%s lists Pipelines: %s", u.name, r)
		var list []uiPipeline
		r.JSON(t, &list)
		out := map[string]bool{}
		for _, p := range list {
			out[p.Namespace] = true
		}
		return out
	}
	for _, u := range []roleUser{viewer, builtin} {
		got := listed(u)
		assert.True(t, got[nsA], "%s sees its namespace", u.name)
		assert.False(t, got[nsB], "%s does not see a namespace it is not bound in", u.name)
	}

	pause := map[string]string{"pipeline": pipelineName, "namespace": nsA}
	r = ui(viewer).Post(t, uiAPI+"/pause", pause)
	assert.Equal(t, http.StatusForbidden, r.Status, "viewer may not pause: %s", r)
	code, body = postBundle(t, v.URL, viewer.token, nsA)
	assert.Equal(t, http.StatusForbidden, code, "viewer may not create Bundles: %s", body)
	assert.Contains(t, body, fmt.Sprintf(`forbidden: user %q cannot create bundles.kardinal.io in namespace %s`, viewer.username(), nsA))

	code, body = postBundle(t, v.URL, promoter.token, nsA)
	require.Equal(t, http.StatusCreated, code, "promoter creates a Bundle: %s", body)
	var created struct{ Name, Namespace string }
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: nsA, Name: created.Name}, &b))
	assert.Equal(t, promoter.username(), b.Annotations[lifecycle.AnnotationRequestedBy])
	code, body = postBundle(t, v.URL, promoter.token, nsB)
	assert.Equal(t, http.StatusForbidden, code, "promoter is not bound in B: %s", body)
	for _, action := range []string{"pause", "resume"} {
		r = ui(promoter).Post(t, uiAPI+"/"+action, pause)
		assert.Equal(t, http.StatusOK, r.Status, "promoter may %s: %s", action, r)
	}
	approve := map[string]interface{}{"reason": "e2e role check", "expiresInMinutes": 5}
	r = ui(promoter).Post(t, uiAPI+"/gates/"+nsA+"/hold/approve", approve)
	assert.Equal(t, http.StatusForbidden, r.Status, "promoter may not override a gate: %s", r)

	r = ui(approver).Post(t, uiAPI+"/gates/"+nsA+"/hold/approve", approve)
	assert.Equal(t, http.StatusOK, r.Status, "approver overrides the gate: %s", r)
	var g v1alpha1.PolicyGate
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: nsA, Name: "hold"}, &g))
	require.NotEmpty(t, g.Spec.Overrides)
	assert.Equal(t, approver.username(), g.Spec.Overrides[len(g.Spec.Overrides)-1].CreatedBy)
	code, body = postBundle(t, v.URL, approver.token, nsA)
	assert.Equal(t, http.StatusForbidden, code, "approver may not create Bundles: %s", body)
}

// TestUI_ScopedWritesAdmission checks the chart's scoped-writes
// ValidatingAdmissionPolicy (identity-admission.yaml) with kubeconfig-style
// writes, as rbac.userRoles.directWrites grants them: a ServiceAccount with
// the promoter role plus update on Pipelines may change spec.paused and
// nothing else; one with the approver role plus update on PolicyGates may
// add spec.overrides entries and not change the expression; one that also
// holds pipelines/edit may change the rest. A limited caller may not touch
// metadata (an ownerReference would have the garbage collector delete the
// gate), edit existing overrides, override in another name, or past the cap.
//
// Covers RBAC-SCOPED-WRITES-01.
func TestUI_ScopedWritesAdmission(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	require.NoError(t, e.Client.Create(ctx, &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: pipelineName, Namespace: ns},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://git.example/kardinal/" + ns + ".git", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test", Path: fixtures.Path("test"), Approval: "auto",
				Update: v1alpha1.UpdateConfig{Strategy: "kustomize"}}},
		}}))
	e.CreateGate(t, framework.Gate(ns, "hold", "test", "false", recheck))

	direct := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "direct-writes", Namespace: ns}, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"kardinal.io"}, Resources: []string{"pipelines", "policygates"}, Verbs: []string{"update", "patch"}}}}
	edit := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "pipelines-edit", Namespace: ns}, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"kardinal.io"}, Resources: []string{"pipelines/edit"}, Verbs: []string{"update"}}}}
	for _, r := range []*rbacv1.Role{direct, edit} {
		_, err := e.Kube.RbacV1().Roles(ns).Create(ctx, r, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	bind := func(u roleUser, role string) {
		_, err := e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: u.name + "-" + role, Namespace: ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: u.name, Namespace: ns}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	role := func(r string) string { return framework.ControllerDeployment + "-" + r }
	pauser := newRoleUser(t, e, ns, "pauser", role("promoter"))
	bind(pauser, direct.Name)
	gater := newRoleUser(t, e, ns, "gater", role("approver"))
	bind(gater, direct.Name)
	editor := newRoleUser(t, e, ns, "editor", role("promoter"))
	bind(editor, direct.Name)
	bind(editor, edit.Name)

	// These callers talk to the API server, so their tokens are for its
	// audience, not kardinal's.
	as := func(u roleUser) client.Client {
		tr, err := e.Kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, u.name, &authnv1.TokenRequest{
			Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](600)}}, metav1.CreateOptions{})
		require.NoError(t, err)
		cfg := rest.AnonymousClientConfig(e.Config)
		cfg.BearerToken = tr.Status.Token
		c, err := client.New(cfg, client.Options{Scheme: framework.Scheme()})
		require.NoError(t, err)
		return c
	}
	framework.Eventually(t, time.Minute, "the bindings to take effect", func(context.Context) (bool, string) {
		return e.Can(t, pauser.username(), framework.Access{Verb: "update", Group: "kardinal.io", Resource: "pipelines", Namespace: ns}) &&
			e.Can(t, gater.username(), framework.Access{Verb: "update", Group: "kardinal.io", Resource: "policygates", Subresource: "override", Namespace: ns}) &&
			e.Can(t, editor.username(), framework.Access{Verb: "update", Group: "kardinal.io", Resource: "pipelines", Subresource: "edit", Namespace: ns}), "not yet"
	})

	key := types.NamespacedName{Namespace: ns, Name: pipelineName}
	// The controller writes the Pipeline too: retry on conflict, as kubectl
	// edit would.
	updatePipeline := func(u roleUser, change func(*v1alpha1.Pipeline)) error {
		c := as(u)
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var p v1alpha1.Pipeline
			if err := c.Get(ctx, key, &p); err != nil {
				return err
			}
			change(&p)
			return c.Update(ctx, &p)
		})
	}
	tests := []struct {
		name   string
		who    roleUser
		change func(*v1alpha1.Pipeline)
		wantOK bool
		msg    string
	}{
		{"pauser pauses", pauser, func(p *v1alpha1.Pipeline) { p.Spec.Paused = true }, true, ""},
		{"pauser resumes", pauser, func(p *v1alpha1.Pipeline) { p.Spec.Paused = false }, true, ""},
		{"pauser may not change the branch", pauser, func(p *v1alpha1.Pipeline) { p.Spec.Git.Branch = "other" }, false,
			"you may change only spec.paused"},
		{"pauser may not change labels", pauser, func(p *v1alpha1.Pipeline) { p.Labels = map[string]string{"x": "y"} }, false,
			"you may not change the metadata"},
		{"pauser may not add a finalizer", pauser, func(p *v1alpha1.Pipeline) { p.Finalizers = append(p.Finalizers, "e2e.kardinal.io/hold") }, false,
			"you may not change the metadata"},
		{"editor changes the branch", editor, func(p *v1alpha1.Pipeline) { p.Spec.Git.Branch = "other" }, true, ""},
	}
	for _, tt := range tests {
		err := updatePipeline(tt.who, tt.change)
		if tt.wantOK {
			assert.NoError(t, err, tt.name)
			continue
		}
		require.Error(t, err, tt.name)
		assert.True(t, apierrors.IsForbidden(err) || apierrors.IsInvalid(err), "%s: %v", tt.name, err)
		assert.Contains(t, err.Error(), tt.msg, tt.name)
	}

	gk := types.NamespacedName{Namespace: ns, Name: "hold"}
	gc := as(gater)
	updateGate := func(change func(*v1alpha1.PolicyGate)) error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var g v1alpha1.PolicyGate
			if err := gc.Get(ctx, gk, &g); err != nil {
				return err
			}
			change(&g)
			return gc.Update(ctx, &g)
		})
	}
	assert.NoError(t, updateGate(func(g *v1alpha1.PolicyGate) {
		g.Spec.Overrides = append(g.Spec.Overrides, v1alpha1.PolicyGateOverride{
			Reason: "e2e scoped write", CreatedBy: gater.username(),
			ExpiresAt: metav1.NewTime(time.Now().Add(5 * time.Minute)), CreatedAt: ptr.To(metav1.Now())})
	}), "gater adds an override")
	refused := []struct {
		name, msg string
		change    func(*v1alpha1.PolicyGate)
	}{
		{"change the expression", "you may change only spec.overrides", func(g *v1alpha1.PolicyGate) { g.Spec.Expression = "true" }},
		{"add an ownerReference (garbage collection)", "you may not change the metadata", func(g *v1alpha1.PolicyGate) {
			g.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "gone", UID: "0000-dead"}}
		}},
		{"edit an existing override", "append-only", func(g *v1alpha1.PolicyGate) {
			g.Spec.Overrides[0].ExpiresAt = metav1.NewTime(time.Now().Add(48 * time.Hour))
		}},
		{"override in someone else's name", "createdBy", func(g *v1alpha1.PolicyGate) {
			g.Spec.Overrides = append(g.Spec.Overrides, v1alpha1.PolicyGateOverride{Reason: "r", CreatedBy: "someone-else",
				ExpiresAt: metav1.NewTime(time.Now().Add(time.Minute)), CreatedAt: ptr.To(metav1.Now())})
		}},
		{"override past the cap", "at most 1440 minutes", func(g *v1alpha1.PolicyGate) {
			g.Spec.Overrides = append(g.Spec.Overrides, v1alpha1.PolicyGateOverride{Reason: "r", CreatedBy: gater.username(),
				ExpiresAt: metav1.NewTime(time.Now().Add(25 * time.Hour)), CreatedAt: ptr.To(metav1.Now())})
		}},
	}
	for _, r := range refused {
		err := updateGate(r.change)
		require.Error(t, err, "gater may not %s", r.name)
		assert.Contains(t, err.Error(), r.msg, r.name)
	}
}
