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
