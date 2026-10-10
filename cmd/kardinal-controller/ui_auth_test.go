// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	policygaterecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// uiTestAssets stands in for the embedded React build.
var uiTestAssets = fstest.MapFS{
	"index.html":           {Data: []byte("<html>kardinal</html>")},
	"assets/index-abc.js":  {Data: []byte("console.log(1)")},
	"assets/index-abc.css": {Data: []byte("body{}")},
}

func uiAuthDo(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	// The UI is reached through kubectl port-forward: a loopback Host and a
	// loopback peer.
	req.Host = "localhost:8082"
	req.RemoteAddr = "127.0.0.1:54321"
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestUIHandler_StaticToken exercises the real UI handler in static-token
// mode. Replaces a test that exercised a copy of the middleware defined in
// the test file (C07-controller-34).
func TestUIHandler_StaticToken(t *testing.T) {
	const secret = "supersecrettoken"
	tests := []struct {
		name     string
		token    string
		path     string
		header   string
		wantCode int
	}{
		{name: "open mode, no header", path: "/api/v1/ui/pipelines", wantCode: http.StatusOK},
		{name: "correct token", token: secret, path: "/api/v1/ui/pipelines", header: "Bearer " + secret, wantCode: http.StatusOK},
		{name: "wrong token", token: secret, path: "/api/v1/ui/pipelines", header: "Bearer wrongtoken", wantCode: http.StatusUnauthorized},
		{name: "no header", token: secret, path: "/api/v1/ui/pipelines", wantCode: http.StatusUnauthorized},
		{name: "no bearer prefix", token: secret, path: "/api/v1/ui/pipelines", header: "Token " + secret, wantCode: http.StatusUnauthorized},
		{name: "empty bearer", token: secret, path: "/api/v1/ui/pipelines", header: "Bearer ", wantCode: http.StatusUnauthorized},
		{name: "static assets are public", token: secret, path: "/ui/assets/index-abc.js", wantCode: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
			h := newUIHandler(c, uiTestAssets, uiAuthConfig{staticToken: tt.token}, "", nil, zerolog.Nop())
			rec := uiAuthDo(t, h, http.MethodGet, tt.path, tt.header, "")
			require.Equal(t, tt.wantCode, rec.Code)
			if tt.wantCode == http.StatusUnauthorized {
				assert.Contains(t, rec.Header().Get("Www-Authenticate"), `Bearer realm="kardinal-ui"`)
			}
		})
	}
}

// TestUIHandler_StaticTokenTakesPrecedenceOverTokenReview verifies O4 (spec
// issue-975): with both modes configured, only the static token is accepted
// and TokenReview is never called.
func TestUIHandler_StaticTokenTakesPrecedenceOverTokenReview(t *testing.T) {
	const staticToken = "static-secret-token"
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	tr := &uiTestTokens{users: map[string]string{"kube-token": "alice"}}
	h := newUIHandler(c, nil, uiAuthConfig{staticToken: staticToken, tokens: tr, access: &uiTestAccess{}}, "", nil, zerolog.Nop())

	assert.Equal(t, http.StatusOK, uiAuthDo(t, h, http.MethodGet, "/api/v1/ui/pipelines", "Bearer "+staticToken, "").Code)
	assert.Equal(t, http.StatusUnauthorized, uiAuthDo(t, h, http.MethodGet, "/api/v1/ui/pipelines", "Bearer kube-token", "").Code)
	assert.Zero(t, tr.calls, "TokenReview must not be called when a static token is set")
}

// uiTestTokens maps bearer tokens to usernames; unknown tokens are rejected.
type uiTestTokens struct {
	users map[string]string
	calls int
}

func (r *uiTestTokens) Review(_ context.Context, token string) (*authv1.TokenReviewStatus, error) {
	r.calls++
	u, ok := r.users[token]
	if !ok {
		return &authv1.TokenReviewStatus{Authenticated: false}, nil
	}
	return &authv1.TokenReviewStatus{Authenticated: true, User: authv1.UserInfo{Username: u}}, nil
}

// uiTestAccess is a tiny RBAC table: user -> namespace -> allowed verbs on
// kardinal.io resources ("*" namespace means cluster-wide).
type uiTestAccess struct {
	rules map[string]map[string][]string
	calls []authzv1.ResourceAttributes
}

func (a *uiTestAccess) Allowed(_ context.Context, u authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error) {
	a.calls = append(a.calls, attrs)
	for _, ns := range []string{attrs.Namespace, "*"} {
		for _, v := range a.rules[u.Username][ns] {
			if v == attrs.Verb && attrs.Group == "kardinal.io" {
				return true, "", nil
			}
		}
	}
	return false, "", nil
}

// TestUIHandler_TokenReviewAuthorizesActions runs the real UI API behind
// TokenReview mode. Regression test for C13b-design-04 and C07-controller-07:
// any authenticated token (for example a ServiceAccount in an unrelated
// namespace) could pause pipelines and override gates.
func TestUIHandler_TokenReviewAuthorizesActions(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{
		"deployer-token": "system:serviceaccount:team-a:deployer",
		"random-token":   "system:serviceaccount:random-ns:default",
		"viewer-token":   "viewer",
	}}
	rbac := map[string]map[string][]string{
		"system:serviceaccount:team-a:deployer": {"team-a": {"get", "list", "update", "create"}},
		"viewer":                                {"*": {"get", "list"}},
	}

	tests := []struct {
		name       string
		token      string
		method     string
		path       string
		body       string
		wantCode   int
		wantBody   string
		wantPaused bool
		wantGateBy string
	}{
		{name: "deployer pauses its pipeline", token: "deployer-token", method: http.MethodPost, path: "/api/v1/ui/pause",
			body: `{"pipeline":"app","namespace":"team-a"}`, wantCode: http.StatusOK, wantPaused: true},
		{name: "unrelated service account cannot pause", token: "random-token", method: http.MethodPost, path: "/api/v1/ui/pause",
			body: `{"pipeline":"app","namespace":"team-a"}`, wantCode: http.StatusForbidden},
		{name: "viewer cannot pause", token: "viewer-token", method: http.MethodPost, path: "/api/v1/ui/pause",
			body: `{"pipeline":"app","namespace":"team-a"}`, wantCode: http.StatusForbidden},
		{name: "deployer approves its gate, recorded as the user", token: "deployer-token", method: http.MethodPost,
			path: "/api/v1/ui/gates/team-a/no-weekend/approve", body: `{"reason":"hotfix"}`,
			wantCode: http.StatusOK, wantGateBy: "system:serviceaccount:team-a:deployer"},
		{name: "unrelated service account cannot approve", token: "random-token", method: http.MethodPost,
			path: "/api/v1/ui/gates/team-a/no-weekend/approve", body: `{"reason":"x"}`, wantCode: http.StatusForbidden},
		{name: "viewer cannot approve", token: "viewer-token", method: http.MethodPost,
			path: "/api/v1/ui/gates/team-a/no-weekend/approve", body: `{"reason":"x"}`, wantCode: http.StatusForbidden},
		{name: "viewer lists pipelines", token: "viewer-token", method: http.MethodGet, path: "/api/v1/ui/pipelines",
			wantCode: http.StatusOK},
		// Lists follow namespace RBAC: a namespace-scoped user sees its
		// namespace, an unrelated one an empty list; neither is a 403.
		{name: "namespace-scoped deployer lists its namespace", token: "deployer-token", method: http.MethodGet,
			path: "/api/v1/ui/pipelines", wantCode: http.StatusOK, wantBody: `"namespace":"team-a"`},
		{name: "unrelated service account lists nothing", token: "random-token", method: http.MethodGet,
			path: "/api/v1/ui/pipelines", wantCode: http.StatusOK, wantBody: "[]"},
		{name: "unrelated service account cannot read a Bundle graph", token: "random-token", method: http.MethodGet,
			path: "/api/v1/ui/bundles/b/graph?namespace=team-a", wantCode: http.StatusForbidden},
		{name: "unknown token", token: "nope", method: http.MethodGet, path: "/api/v1/ui/pipelines",
			wantCode: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
				&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"}},
				&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "no-weekend", Namespace: "team-a"}},
			).Build()
			h := newUIHandler(c, nil, uiAuthConfig{tokens: tokens, access: &uiTestAccess{rules: rbac}}, "", nil, zerolog.Nop())

			rec := uiAuthDo(t, h, tt.method, tt.path, "Bearer "+tt.token, tt.body)
			require.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
			if tt.wantBody != "" {
				assert.Contains(t, rec.Body.String(), tt.wantBody)
			}

			var pl v1alpha1.Pipeline
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "app"}, &pl))
			assert.Equal(t, tt.wantPaused, pl.Spec.Paused)

			var gate v1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "no-weekend"}, &gate))
			if tt.wantGateBy == "" {
				assert.Empty(t, gate.Spec.Overrides)
			} else {
				require.Len(t, gate.Spec.Overrides, 1)
				assert.Equal(t, tt.wantGateBy, gate.Spec.Overrides[0].CreatedBy)
			}
		})
	}
}

// TestUIHandler_TokenReviewScopeNamespace verifies that in namespace-scoped
// mode (--watch-namespace) all-namespace reads are checked against the
// watched namespace, so a namespace-scoped user can use the dashboard.
func TestUIHandler_TokenReviewScopeNamespace(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"t": "alice"}}
	access := &uiTestAccess{rules: map[string]map[string][]string{"alice": {"team-a": {"get", "list"}}}}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	h := newUIHandler(c, nil, uiAuthConfig{tokens: tokens, access: access, scopeNamespace: "team-a"}, "", nil, zerolog.Nop())

	rec := uiAuthDo(t, h, http.MethodGet, "/api/v1/ui/pipelines", "Bearer t", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotEmpty(t, access.calls)
	for _, a := range access.calls {
		assert.Equal(t, "team-a", a.Namespace)
	}
}

// TestBuildUIAuth covers C13b-design-09: when the TokenReview client could
// not be built, the controller logged "UI API will be open (no auth)" and
// served the UI without auth although the operator asked for it.
func TestBuildUIAuth(t *testing.T) {
	good := &rest.Config{Host: "https://127.0.0.1:6443"}
	// client-go refuses a QPS limit without a burst.
	bad := &rest.Config{Host: "https://127.0.0.1:6443", QPS: 5, Burst: 0}
	review := reviewOptions{audiences: []string{"kardinal-promoter"}}
	tests := []struct {
		name       string
		cfg        *rest.Config
		flags      uiAuthFlags
		wantErr    string
		wantReview bool
	}{
		{name: "open", cfg: bad},
		{name: "static token and TokenReview refused", cfg: bad,
			flags: uiAuthFlags{staticToken: "s3cret", tokenReview: true, review: review}, wantErr: "--ui-auth-static-overrides-tokenreview"},
		{name: "static token wins over TokenReview when allowed", cfg: bad,
			flags: uiAuthFlags{staticToken: "s3cret", tokenReview: true, allowStaticWithTokenReview: true, review: review}},
		{name: "static token", cfg: bad, flags: uiAuthFlags{staticToken: "s3cret"}},
		{name: "TokenReview", cfg: good, flags: uiAuthFlags{tokenReview: true, review: review}, wantReview: true},
		{name: "TokenReview needs an audience", cfg: good, flags: uiAuthFlags{tokenReview: true}, wantErr: "audience"},
		{name: "TokenReview client cannot be built", cfg: bad, flags: uiAuthFlags{tokenReview: true, review: review}, wantErr: "token reviewer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.flags.scopeNamespace = "team-a"
			auth, err := buildUIAuth(tt.cfg, tt.flags)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.flags.staticToken, auth.staticToken)
			assert.Equal(t, tt.wantReview, auth.tokens != nil)
			assert.Equal(t, tt.wantReview, auth.access != nil)
			assert.Equal(t, "team-a", auth.scopeNamespace)
		})
	}
}

// TestUIHandler_GateApproveBounds covers C07-controller-23: expiry is bounded
// to 1..1440 minutes (an overflowing value used to produce an already-expired
// override with 200), and the path namespace wins over the body.
func TestUIHandler_GateApproveBounds(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		body        string
		wantCode    int
		wantNS      string
		wantMinutes int
	}{
		{name: "default expiry", path: "/api/v1/ui/gates/team-a/g/approve", body: `{"reason":"r"}`,
			wantCode: http.StatusOK, wantNS: "team-a", wantMinutes: 60},
		{name: "max expiry", path: "/api/v1/ui/gates/team-a/g/approve", body: `{"reason":"r","expiresInMinutes":1440}`,
			wantCode: http.StatusOK, wantNS: "team-a", wantMinutes: 1440},
		{name: "overflowing expiry", path: "/api/v1/ui/gates/team-a/g/approve",
			body: `{"reason":"r","expiresInMinutes":` + strconv.FormatInt(math.MaxInt64, 10) + `}`, wantCode: http.StatusBadRequest},
		{name: "above max", path: "/api/v1/ui/gates/team-a/g/approve", body: `{"reason":"r","expiresInMinutes":1441}`,
			wantCode: http.StatusBadRequest},
		{name: "negative", path: "/api/v1/ui/gates/team-a/g/approve", body: `{"reason":"r","expiresInMinutes":-5}`,
			wantCode: http.StatusBadRequest},
		{name: "body namespace does not override the path", path: "/api/v1/ui/gates/team-a/g/approve",
			body: `{"reason":"r","namespace":"team-b"}`, wantCode: http.StatusOK, wantNS: "team-a", wantMinutes: 60},
		{name: "body namespace used with the short path", path: "/api/v1/ui/gates/g/approve",
			body: `{"reason":"r","namespace":"team-b"}`, wantCode: http.StatusOK, wantNS: "team-b", wantMinutes: 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
				&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "team-a"}},
				&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "team-b"}},
			).Build()
			h := newUIHandler(c, nil, uiAuthConfig{}, "", nil, zerolog.Nop())
			before := time.Now()
			rec := uiAuthDo(t, h, http.MethodPost, tt.path, "", tt.body)
			require.Equal(t, tt.wantCode, rec.Code, rec.Body.String())

			for _, ns := range []string{"team-a", "team-b"} {
				var gate v1alpha1.PolicyGate
				require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "g"}, &gate))
				if ns != tt.wantNS {
					assert.Empty(t, gate.Spec.Overrides, "gate in %s must not change", ns)
					continue
				}
				require.Len(t, gate.Spec.Overrides, 1)
				o := gate.Spec.Overrides[0]
				assert.Equal(t, "kardinal-ui", o.CreatedBy, "no UI auth: the UI's requester")
				assert.True(t, o.ExpiresAt.After(before))
				assert.WithinDuration(t, before.Add(time.Duration(tt.wantMinutes)*time.Minute), o.ExpiresAt.Time, time.Minute)
			}
		})
	}
}

// TestUIHandler_StaticAssets covers C07-controller-27: files and the index
// are served, directories are not listed.
func TestUIHandler_StaticAssets(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	h := newUIHandler(c, uiTestAssets, uiAuthConfig{}, "", nil, zerolog.Nop())

	tests := []struct {
		path     string
		wantCode int
		contains string
	}{
		{path: "/ui/", wantCode: http.StatusOK, contains: "kardinal"},
		{path: "/ui/assets/index-abc.js", wantCode: http.StatusOK, contains: "console.log"},
		{path: "/ui/assets/", wantCode: http.StatusNotFound},
		{path: "/ui/assets", wantCode: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := uiAuthDo(t, h, http.MethodGet, tt.path, "", "")
			assert.Equal(t, tt.wantCode, rec.Code)
			assert.NotContains(t, rec.Body.String(), "index-abc.css", "directory listing")
			if tt.contains != "" {
				assert.Contains(t, rec.Body.String(), tt.contains)
			}
		})
	}
}

// TestUIHandler_BodyLimit covers the UI half of C07-controller-25: request
// bodies over maxUIRequestBody are rejected instead of read into memory.
func TestUIHandler_BodyLimit(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"}},
	).Build()
	h := newUIHandler(c, nil, uiAuthConfig{}, "", nil, zerolog.Nop())

	pad := strings.Repeat(" ", maxUIRequestBody)
	rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/pause", "", `{"pipeline":"app"`+pad+`}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var pl v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "app"}, &pl))
	assert.False(t, pl.Spec.Paused)

	rec = uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/pause", "", `{"pipeline":"app"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
}

// exactAccess allows "user verb resource[/subresource] namespace" tuples.
type exactAccess map[string]bool

func (a exactAccess) Allowed(_ context.Context, u authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error) {
	res := attrs.Resource
	if attrs.Subresource != "" {
		res += "/" + attrs.Subresource
	}
	return a[u.Username+" "+attrs.Verb+" "+res+" "+attrs.Namespace], "", nil
}

// TestUIHandler_ActionsUseVirtualSubresources: in TokenReview mode pausing
// needs update on pipelines/pause and overriding a gate update on
// policygates/override, not update on the object: a caller with only the
// action permission succeeds (the controller writes, createdBy is the
// caller); one with update on the object but not the action is refused.
func TestUIHandler_ActionsUseVirtualSubresources(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"p": "promoter", "a": "approver", "e": "editor"}}
	access := exactAccess{
		"promoter update pipelines/pause team-a":      true,
		"approver update policygates/override team-a": true,
		"editor update pipelines team-a":              true,
		"editor update policygates team-a":            true,
		"editor get pipelines team-a":                 true,
		"editor get policygates team-a":               true,
	}
	tests := []struct {
		name, token, path, body string
		want                    int
	}{
		{"pause with pipelines/pause only", "p", "/api/v1/ui/pause", `{"pipeline":"app","namespace":"team-a"}`, http.StatusOK},
		{"pause with update pipelines but no pipelines/pause", "e", "/api/v1/ui/pause", `{"pipeline":"app","namespace":"team-a"}`, http.StatusForbidden},
		{"override with policygates/override only", "a", "/api/v1/ui/gates/team-a/g/approve", `{"reason":"r"}`, http.StatusOK},
		{"override with update policygates but no override", "e", "/api/v1/ui/gates/team-a/g/approve", `{"reason":"r"}`, http.StatusForbidden},
		{"promoter cannot override", "p", "/api/v1/ui/gates/team-a/g/approve", `{"reason":"r"}`, http.StatusForbidden},
		{"approver cannot pause", "a", "/api/v1/ui/pause", `{"pipeline":"app","namespace":"team-a"}`, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
				&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"}},
				&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "team-a"}},
			).Build()
			h := newUIHandler(c, nil, uiAuthConfig{tokens: tokens, access: access}, "", nil, zerolog.Nop())
			rec := uiAuthDo(t, h, http.MethodPost, tt.path, "Bearer "+tt.token, tt.body)
			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			var p v1alpha1.Pipeline
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "app"}, &p))
			var g v1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "g"}, &g))
			if tt.want != http.StatusOK {
				assert.False(t, p.Spec.Paused, "nothing written")
				assert.Empty(t, g.Spec.Overrides, "nothing written")
				return
			}
			if strings.Contains(tt.path, "pause") {
				assert.True(t, p.Spec.Paused)
			} else {
				require.Len(t, g.Spec.Overrides, 1)
				assert.Equal(t, "approver", g.Spec.Overrides[0].CreatedBy)
			}
		})
	}
}

// TestGateOverrideCapDefault: the UI's default cap is the chart's
// controller.gateOverrideMaxMinutes default (test/helm TestGateOverrideCapIsOneValue).
func TestGateOverrideCapDefault(t *testing.T) {
	assert.Equal(t, 1440, maxGateOverrideMinutes)
	assert.Equal(t, policygaterecon.DefaultMaxOverride, time.Duration(maxGateOverrideMinutes)*time.Minute,
		"the UI default is the reconciler default")
}

// TestApplyGateOverrideCap (#1511 QA): --gate-override-max-minutes sets the
// reconciler's cap and the UI API's bound together: with 30 the UI accepts a
// 30-minute override and refuses 31 minutes.
func TestApplyGateOverrideCap(t *testing.T) {
	saved := maxGateOverrideMinutes
	t.Cleanup(func() { maxGateOverrideMinutes = saved })
	r := &policygaterecon.Reconciler{}
	applyGateOverrideCap(30, r)
	assert.Equal(t, 30*time.Minute, r.MaxOverride)
	assert.Equal(t, 30, maxGateOverrideMinutes)

	for minutes, want := range map[int]int{30: http.StatusOK, 31: http.StatusBadRequest} {
		c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
			&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "team-a"}}).Build()
		h := newUIHandler(c, nil, uiAuthConfig{}, "", nil, zerolog.Nop())
		rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/gates/team-a/g/approve", "",
			`{"reason":"r","expiresInMinutes":`+strconv.Itoa(minutes)+`}`)
		assert.Equal(t, want, rec.Code, "%d minutes: %s", minutes, rec.Body.String())
	}
}

// holdAccess allows every kardinal.io verb in team-a, and the hold
// subresource only to holdUser.
type holdAccess struct{ holdUser string }

func (a holdAccess) Allowed(_ context.Context, u authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error) {
	if attrs.Subresource == "hold" {
		return u.Username == a.holdUser && attrs.Verb == "update" && attrs.Resource == "pipelines", "", nil
	}
	return attrs.Group == "kardinal.io" && attrs.Namespace == "team-a" && attrs.Subresource == "", "", nil
}

// TestUIHandler_HoldNeedsHoldSubresource (#1528 QA): holding or releasing
// through the UI needs update on pipelines/hold, the subresource the
// hold-writes admission policy asks of a direct write: plain update on the
// Pipeline is not enough, and a refused request writes nothing.
func TestUIHandler_HoldNeedsHoldSubresource(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"d": "deployer", "h": "holder"}}
	held := func() *v1alpha1.Pipeline {
		p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}}}}
		p.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "app-rollback-1", Reason: "x"}}
		return p
	}
	for _, tc := range []struct {
		token    string
		wantCode int
		wantHeld bool
	}{
		{token: "d", wantCode: http.StatusForbidden, wantHeld: true},
		{token: "h", wantCode: http.StatusOK, wantHeld: false},
	} {
		c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(held()).Build()
		h := newUIHandler(c, nil, uiAuthConfig{tokens: tokens, access: holdAccess{holdUser: "holder"}}, "", nil, zerolog.Nop())
		rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/release-hold", "Bearer "+tc.token,
			`{"pipeline":"app","namespace":"team-a","environment":"prod"}`)
		require.Equal(t, tc.wantCode, rec.Code, rec.Body.String())
		var p v1alpha1.Pipeline
		require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "app"}, &p))
		assert.Equal(t, tc.wantHeld, len(p.Spec.Holds) == 1, tc.token)
	}

	// A rollback with a hold by a user without pipelines/hold creates
	// nothing.
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}}}}).Build()
	h := newUIHandler(c, nil, uiAuthConfig{tokens: tokens, access: holdAccess{holdUser: "holder"}}, "", nil, zerolog.Nop())
	rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/rollback", "Bearer d",
		`{"pipeline":"app","namespace":"team-a","environment":"prod","hold":true,"holdReason":"x"}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	assert.Empty(t, bundles.Items)
}

// groupTokens authenticates every token as a user with groups.
type groupTokens map[string]authv1.UserInfo

func (g groupTokens) Review(_ context.Context, token string) (*authv1.TokenReviewStatus, error) {
	u, ok := g[token]
	return &authv1.TokenReviewStatus{Authenticated: ok, User: u}, nil
}

// TestUIHandler_Approvals (E6): the UI approves, rejects and revokes as the
// authenticated user: the Approval names the TokenReview user and groups and
// carries kardinal.io/recorded-via: ui; the same decision again changes
// nothing, another replaces it, and revoke deletes it. A user without create
// on approvals is refused, and without TokenReview mode there is no
// identity to record, so the request is refused before anything is written.
func TestUIHandler_Approvals(t *testing.T) {
	ctx := context.Background()
	objs := func() []client.Object {
		p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}}}}
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "team-a", UID: "uid-v2"},
			Spec:   v1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
			Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
		return []client.Object{p, b}
	}
	tokens := groupTokens{
		"alice":  {Username: "alice@example.com", Groups: []string{"release-managers", "system:authenticated"}},
		"viewer": {Username: "viewer"},
		"bob":    {Username: "bob@example.com"},
	}
	all := []string{"get", "list", "create", "delete", "update"}
	access := &uiTestAccess{rules: map[string]map[string][]string{
		"alice@example.com": {"team-a": all}, "bob@example.com": {"team-a": all}, "viewer": {"team-a": {"get", "list"}}}}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(objs()...).Build()
	h := newUIHandler(c, nil, uiAuthConfig{tokens: tokens, access: access}, "", nil, zerolog.Nop())
	approvals := func() []v1alpha1.Approval {
		var l v1alpha1.ApprovalList
		require.NoError(t, c.List(ctx, &l))
		return l.Items
	}
	post := func(token, body string) *httptest.ResponseRecorder {
		return uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/approvals", "Bearer "+token, body)
	}

	rec := post("alice", `{"bundle":"app-v2","namespace":"team-a","environment":"prod","decision":"approve","comment":"LGTM"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"outcome":"Recorded"`)
	assert.Contains(t, rec.Body.String(), "Recorded: alice@example.com approves app-v2 for prod")
	got := approvals()
	require.Len(t, got, 1)
	assert.Equal(t, "alice@example.com", got[0].Spec.User)
	assert.Equal(t, []string{"release-managers", "system:authenticated"}, got[0].Spec.Groups)
	assert.Equal(t, "approve", got[0].Spec.Decision)
	assert.Equal(t, "LGTM", got[0].Spec.Comment)
	assert.Equal(t, "uid-v2", got[0].Spec.BundleUID)
	assert.Equal(t, "ui", got[0].Annotations["kardinal.io/recorded-via"])

	rec = post("alice", `{"bundle":"app-v2","namespace":"team-a","environment":"prod","decision":"approve","comment":"LGTM"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"outcome":"Already recorded"`)

	rec = post("alice", `{"bundle":"app-v2","namespace":"team-a","environment":"prod","decision":"reject","comment":"CVE"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got = approvals()
	require.Len(t, got, 1, "a new decision replaces the old one")
	assert.Equal(t, "reject", got[0].Spec.Decision)

	rec = post("viewer", `{"bundle":"app-v2","namespace":"team-a","environment":"prod","decision":"approve"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, "no create on approvals")
	assert.Len(t, approvals(), 1)

	rec = post("alice", `{"bundle":"app-v2","namespace":"team-a","environment":"prod","decision":"maybe"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = post("alice", `{"bundle":"app-v2","namespace":"team-a","environment":"staging","decision":"approve"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "unknown environment")

	// Bob may delete approvals in team-a, but the UI revokes only his own:
	// he has none, so nothing is revoked and alice's reject stays (#1593 QA).
	rec = post("bob", `{"bundle":"app-v2","namespace":"team-a","environment":"prod","revoke":true}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "bob@example.com has no Approval of app-v2 for prod to revoke")
	got = approvals()
	require.Len(t, got, 1)
	assert.Equal(t, "alice@example.com", got[0].Spec.User)

	rec = post("alice", `{"bundle":"app-v2","namespace":"team-a","environment":"prod","revoke":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Revoked: alice@example.com no longer rejects app-v2 for prod")
	assert.Empty(t, approvals())

	// No verified identity: refused, nothing written.
	c2 := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(objs()...).Build()
	static := newUIHandler(c2, nil, uiAuthConfig{staticToken: "static-token"}, "", nil, zerolog.Nop())
	rec = uiAuthDo(t, static, http.MethodPost, "/api/v1/ui/approvals", "Bearer static-token",
		`{"bundle":"app-v2","namespace":"team-a","environment":"prod","decision":"approve"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "TokenReview")
	var l v1alpha1.ApprovalList
	require.NoError(t, c2.List(ctx, &l))
	assert.Empty(t, l.Items)
}

// holderAccess is a promoter: it may read every kardinal.io kind in team-a,
// create Bundles and update pipelines/hold, but not update Pipelines.
type holderAccess struct{}

func (holderAccess) Allowed(_ context.Context, _ authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error) {
	if attrs.Group != "kardinal.io" || attrs.Namespace != "team-a" {
		return false, "", nil
	}
	switch {
	case attrs.Subresource == "hold":
		return attrs.Resource == "pipelines" && attrs.Verb == "update", "", nil
	case attrs.Subresource != "":
		return false, "", nil
	case attrs.Verb == "get", attrs.Verb == "list", attrs.Verb == "watch":
		return true, "", nil
	case attrs.Verb == "create":
		return attrs.Resource == "bundles", "", nil
	}
	return false, "", nil
}

// TestUIHandler_HoldWithoutPipelineUpdate (#1511 QA): the promoter role holds
// pipelines/hold but not update on Pipelines. Rolling back with a hold and
// releasing it through the UI work: the controller writes spec.holds, the
// plan's reads and the Bundle create go through the caller's client.
func TestUIHandler_HoldWithoutPipelineUpdate(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"p": "promoter"}}
	ctx := context.Background()
	t0 := time.Now().Add(-2 * time.Hour)
	bundle := func(name, tag string, minute int) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a",
				CreationTimestamp: metav1.NewTime(t0.Add(time.Duration(minute) * time.Minute))},
			Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "app",
				Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: tag}}},
			Status: v1alpha1.BundleStatus{Phase: "Verified"},
		}
	}
	// The PromotionSteps that say v1, then v2, were Verified in prod.
	step := func(b string, minute int) *v1alpha1.PromotionStep {
		at := metav1.NewTime(t0.Add(time.Duration(minute) * time.Minute))
		return &v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: b + "-prod", Namespace: "team-a", CreationTimestamp: at,
				Labels: map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": b, "kardinal.io/environment": "prod"}},
			Spec: v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: b, Environment: "prod"},
			Status: v1alpha1.PromotionStepStatus{State: "Verified", Conditions: []metav1.Condition{
				{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified", LastTransitionTime: at}}},
		}
	}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}}}},
		bundle("v1", "1.0", 0), bundle("v2", "2.0", 10), step("v1", 1), step("v2", 11),
	).Build()
	h := newUIHandler(c, nil, uiAuthConfig{tokens: tokens, access: holderAccess{}}, "", nil, zerolog.Nop())

	rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/rollback", "Bearer p",
		`{"pipeline":"app","namespace":"team-a","environment":"prod","hold":true,"holdReason":"incident"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "app"}, &p))
	require.Len(t, p.Spec.Holds, 1, "the controller wrote the hold")
	assert.Equal(t, "promoter", p.Spec.Holds[0].CreatedBy)
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(ctx, &bundles, client.InNamespace("team-a")))
	assert.Len(t, bundles.Items, 3, "the rollback Bundle was created")

	rec = uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/release-hold", "Bearer p",
		`{"pipeline":"app","namespace":"team-a","environment":"prod"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "app"}, &p))
	assert.Empty(t, p.Spec.Holds, "the controller released the hold")
}

// recordingAccess is holderAccess without create on bundles, recording each
// check.
type recordingAccess struct{ checked *[]authzv1.ResourceAttributes }

func (a recordingAccess) Allowed(ctx context.Context, u authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error) {
	*a.checked = append(*a.checked, attrs)
	if attrs.Verb == "create" {
		return false, "", nil
	}
	return holderAccess{}.Allowed(ctx, u, attrs)
}

// TestUIHandler_HoldNeedsBundleCreateFirst (#1511 QA): a caller with
// pipelines/hold who cannot create Bundles is refused before the hold is
// checked or written, so no hold appears on the Pipeline even for a moment.
func TestUIHandler_HoldNeedsBundleCreateFirst(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"h": "holder"}}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}}}},
	).Build()
	var checked []authzv1.ResourceAttributes
	h := newUIHandler(c, nil, uiAuthConfig{tokens: tokens, access: recordingAccess{checked: &checked}}, "", nil, zerolog.Nop())
	rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/rollback", "Bearer h",
		`{"pipeline":"app","namespace":"team-a","environment":"prod","hold":true,"holdReason":"incident"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	for _, a := range checked {
		assert.NotEqual(t, "hold", a.Subresource, "pipelines/hold is not checked after create bundles is denied")
	}
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "app"}, &p))
	assert.Empty(t, p.Spec.Holds)
}
