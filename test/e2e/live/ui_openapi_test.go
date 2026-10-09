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
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestUI_APIConformsToOpenAPI checks the OpenAPI document against the live
// controller. Both listeners serve it without credentials, byte for byte as
// docs/reference/openapi.json. Then a promotion held at a prod gate gives
// every read real data, and each documented UI operation is called and its
// response checked against the document's schema, which allows no
// undocumented property: the pipeline list, the Pipeline's Bundles, the
// Bundle graph and steps, the gate list (before and after a gate override
// through the API), a step's events, validate-cel (valid and not), pause
// and resume. The webhook listener's health endpoint is checked too.
//
// Covers API-OPENAPI-01.
func TestUI_APIConformsToOpenAPI(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	spec := framework.LoadOpenAPI(t)
	c := mainUI(t, e)

	r := c.Get(t, "/api/v1/openapi.json")
	require.Equal(t, http.StatusOK, r.Status, r.String())
	assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
	assert.Equal(t, string(spec.Raw), r.Body, "the UI listener serves docs/reference/openapi.json")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proxy := func(suffix ...string) []byte {
		t.Helper()
		raw, err := e.Kube.CoreV1().RESTClient().Get().Namespace(framework.ControllerNamespace).
			Resource("services").Name(framework.ControllerName + ":webhook").SubResource("proxy").
			Suffix(suffix...).DoRaw(ctx)
		require.NoError(t, err, "GET %v on the webhook listener", suffix)
		return raw
	}
	assert.Equal(t, string(spec.Raw), string(proxy("api", "v1", "openapi.json")), "the webhook listener serves it too")
	assert.Empty(t, spec.Validate(spec.ResponseSchema(t, "GET", "/webhook/scm/health", 200), proxy("webhook", "scm", "health")))

	a := newArgoApp(t, e, "test", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "needs-open-label", "prod", openExpr, recheck))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2, "--commit", "0123abc", "--author", "e2e-bot")
	step := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	gate := e.WaitGateReady(t, a.ns, bundle, "prod", "needs-open-label", false, "= false", gateTimeout)

	called := map[string]bool{}
	check := func(method, tmpl, path string, status int, body interface{}) {
		t.Helper()
		var res framework.UIResponse
		if method == http.MethodGet {
			res = c.Get(t, path)
		} else {
			res = c.Post(t, path, body)
		}
		require.Equal(t, status, res.Status, "%s %s: %s", method, path, res)
		errs := spec.Validate(spec.ResponseSchema(t, method, tmpl, status), []byte(res.Body))
		assert.Empty(t, errs, "%s %s answers what the document describes:\n%s", method, path, res.Body)
		called[method+" "+tmpl] = true
	}
	q := "?namespace=" + a.ns
	check("GET", "/api/v1/ui/pipelines", uiAPI+"/pipelines", 200, nil)
	check("GET", "/api/v1/ui/pipelines/{pipeline}/bundles", uiAPI+"/pipelines/"+pipelineName+"/bundles"+q, 200, nil)
	check("GET", "/api/v1/ui/bundles/{bundle}/graph", uiAPI+"/bundles/"+bundle+"/graph"+q, 200, nil)
	check("GET", "/api/v1/ui/bundles/{bundle}/steps", uiAPI+"/bundles/"+bundle+"/steps"+q, 200, nil)
	check("GET", "/api/v1/ui/gates", uiAPI+"/gates", 200, nil)
	check("GET", "/api/v1/ui/steps/{namespace}/{step}/events", uiAPI+"/steps/"+a.ns+"/"+step.Name+"/events", 200, nil)
	check("POST", "/api/v1/ui/validate-cel", uiAPI+"/validate-cel", 200, map[string]string{"expression": openExpr})
	check("POST", "/api/v1/ui/validate-cel", uiAPI+"/validate-cel", 200, map[string]string{"expression": "bundle.nope("})
	check("POST", "/api/v1/ui/gates/{namespace}/{gate}/approve", uiAPI+"/gates/"+a.ns+"/"+gate.Name+"/approve", 200,
		map[string]interface{}{"reason": "openapi conformance", "expiresInMinutes": 5})
	framework.Eventually(t, gateTimeout, "the override to reach the gate list", func(context.Context) (bool, string) {
		res := c.Get(t, uiAPI+"/gates")
		return strings.Contains(res.Body, "openapi conformance"), fmt.Sprintf("HTTP %d", res.Status)
	})
	check("GET", "/api/v1/ui/gates", uiAPI+"/gates", 200, nil)
	check("POST", "/api/v1/ui/pause", uiAPI+"/pause", 200, map[string]string{"pipeline": pipelineName, "namespace": a.ns})
	check("POST", "/api/v1/ui/resume", uiAPI+"/resume", 200, map[string]string{"pipeline": pipelineName, "namespace": a.ns})
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", promoteTimeout)
	check("GET", "/api/v1/ui/bundles/{bundle}/graph", uiAPI+"/bundles/"+bundle+"/graph"+q, 200, nil)

	// Every read and the writes that change nothing lasting are covered
	// here; promote, rollback and create-bundle are TestUI_APIPromoteAndRollback
	// and TestUI_APICreateBundle, whose bodies this document also describes.
	for _, op := range []string{"GET /api/v1/ui/pipelines", "GET /api/v1/ui/pipelines/{pipeline}/bundles",
		"GET /api/v1/ui/bundles/{bundle}/graph", "GET /api/v1/ui/bundles/{bundle}/steps", "GET /api/v1/ui/gates",
		"GET /api/v1/ui/steps/{namespace}/{step}/events"} {
		assert.True(t, called[op], "%s was checked", op)
	}
}

// TestUI_APIServiceAccountToken follows docs/reference/rest-api.md §API
// tokens for automation on the TokenReview release: a ServiceAccount bound
// to the documented viewer rules gets a TokenRequest token and lists
// Pipelines with it; a token minted for another audience is refused; the
// OpenAPI document needs no token.
//
// Covers API-SATOKEN-01.
func TestUI_APIServiceAccountToken(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	trNS := framework.MustEnv(t, framework.EnvUITRNamespace)
	url := framework.MustEnv(t, framework.EnvUITRURL)
	ctx := context.Background()

	r := framework.UIClient{BaseURL: url}.Get(t, "/api/v1/openapi.json")
	require.Equal(t, http.StatusOK, r.Status, "no token needed for the document: %s", r)
	assert.Equal(t, string(framework.LoadOpenAPI(t).Raw), r.Body)

	sa := "release-dashboard"
	_, err := e.Kube.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa}}, metav1.CreateOptions{})
	require.NoError(t, err)
	bindViewer(t, e, trNS, ns, sa)
	mint := func(audiences ...string) string {
		t.Helper()
		tr, err := e.Kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, sa, &authnv1.TokenRequest{
			Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](3600), Audiences: audiences}}, metav1.CreateOptions{})
		require.NoError(t, err)
		return tr.Status.Token
	}

	framework.Eventually(t, time.Minute, "the ServiceAccount token to list Pipelines", func(context.Context) (bool, string) {
		res := framework.UIClient{BaseURL: url, Token: mint()}.Get(t, uiAPI+"/pipelines")
		return res.Status == http.StatusOK, res.String()
	})
	res := framework.UIClient{BaseURL: url, Token: mint("https://not-the-api-server.example")}.Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusUnauthorized, res.Status, "another audience is refused: %s", res)
}

// bindViewer binds ServiceAccount saNS/sa to the documented UI viewer rules
// (docs/guides/security.md) with a Role and RoleBinding in ns, deleted at the
// end of the test.
func bindViewer(t *testing.T, e *framework.Env, ns, saNS, sa string) {
	t.Helper()
	ctx := context.Background()
	name := "viewer-" + saNS
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"kardinal.io"}, Resources: []string{"pipelines", "bundles", "policygates", "promotionsteps"}, Verbs: []string{"get", "list"}},
			{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"list"}},
		},
	}
	_, err := e.Kube.RbacV1().Roles(ns).Create(ctx, role, metav1.CreateOptions{})
	require.NoError(t, err)
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa, Namespace: saNS}},
	}
	_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = e.Kube.RbacV1().RoleBindings(ns).Delete(context.Background(), name, metav1.DeleteOptions{})
		_ = e.Kube.RbacV1().Roles(ns).Delete(context.Background(), name, metav1.DeleteOptions{})
	})
}
