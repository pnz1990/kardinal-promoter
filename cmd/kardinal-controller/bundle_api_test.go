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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func bundleAPIScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

func bundleAPIPipeline(ns, name string) *v1alpha1.Pipeline {
	return &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

// bundleAPIClient returns a fake client that already holds the Pipelines the
// tests create Bundles for.
func bundleAPIClient() client.Client {
	return fake.NewClientBuilder().WithScheme(bundleAPIScheme()).WithObjects(
		bundleAPIPipeline("default", "nginx-demo"),
		bundleAPIPipeline("my-ns", "my-app"),
		bundleAPIPipeline("default", "app"),
		bundleAPIPipeline("team-a", "app"),
	).Build()
}

func bundleAPIPost(t *testing.T, srv *bundleAPIServer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	srv.Handler()(w, req)
	return w
}

// TestBundleAPI_CreateBundle verifies that a valid POST creates a Bundle CRD.
func TestBundleAPI_CreateBundle(t *testing.T) {
	c := bundleAPIClient()

	srv := newBundleAPIServer(c, "test-token", "default")
	handler := srv.Handler()

	body := `{
		"pipeline": "nginx-demo",
		"type": "image",
		"images": [{"repository": "ghcr.io/nginx/nginx", "tag": "1.29.0"}],
		"provenance": {
			"commitSHA": "abc123",
			"ciRunURL": "https://github.com/org/repo/actions/runs/1",
			"author": "alice"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	handler(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp bundleCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Name)
	assert.Equal(t, "default", resp.Namespace)

	// Verify Bundle was created in the fake client.
	var bundleList v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	assert.Len(t, bundleList.Items, 1)
	assert.Equal(t, "nginx-demo", bundleList.Items[0].Spec.Pipeline)
	assert.Equal(t, "image", bundleList.Items[0].Spec.Type)
	assert.Equal(t, "abc123", bundleList.Items[0].Spec.Provenance.CommitSHA)
}

// TestBundleAPI_RejectsInvalidToken verifies that missing or wrong Bearer token returns 401.
func TestBundleAPI_RejectsInvalidToken(t *testing.T) {
	c := bundleAPIClient()
	srv := newBundleAPIServer(c, "secret-token", "default")
	handler := srv.Handler()

	tests := []struct {
		name   string
		header string
	}{
		{"no token", ""},
		{"wrong token", "Bearer wrong-token"},
		{"malformed bearer", "Basic dXNlcjpwYXNz"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			handler(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code)
		})
	}
}

// TestBundleAPI_RateLimits verifies that >60 requests/minute from same token returns 429.
func TestBundleAPI_RateLimits(t *testing.T) {
	c := bundleAPIClient()
	srv := newBundleAPIServer(c, "test-token", "default")
	handler := srv.Handler()

	body := `{"pipeline":"nginx-demo","type":"image","images":[{"repository":"ghcr.io/nginx/nginx","tag":"1.29.0"}]}`

	var lastCode int
	// Send 65 requests — the first 60 should succeed, the 61st should get 429.
	for i := 0; i < 65; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		handler(w, req)
		lastCode = w.Code
	}
	assert.Equal(t, http.StatusTooManyRequests, lastCode)
}

// TestBundleAPI_RejectsMalformedBody verifies that a malformed JSON body returns 400.
func TestBundleAPI_RejectsMalformedBody(t *testing.T) {
	c := bundleAPIClient()
	srv := newBundleAPIServer(c, "token", "default")
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader("{not valid json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestBundleAPI_RejectsOversizedBody verifies that a body >1 MB returns 400.
func TestBundleAPI_RejectsOversizedBody(t *testing.T) {
	c := bundleAPIClient()
	srv := newBundleAPIServer(c, "token", "default")
	handler := srv.Handler()

	// Build a body >1 MB.
	large := strings.Repeat("x", 1<<20+1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(large))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestBundleAPI_TwoBundlesDifferentNames verifies that two requests produce distinct Bundle names.
func TestBundleAPI_TwoBundlesDifferentNames(t *testing.T) {
	c := bundleAPIClient()
	srv := newBundleAPIServer(c, "token", "default")
	handler := srv.Handler()

	body := `{"pipeline":"nginx-demo","type":"image","images":[{"repository":"ghcr.io/nginx/nginx","tag":"1.29.0"}]}`

	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer token")
	w1 := httptest.NewRecorder()
	handler(w1, req1)
	require.Equal(t, http.StatusCreated, w1.Code)

	time.Sleep(2 * time.Millisecond)

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer token")
	w2 := httptest.NewRecorder()
	handler(w2, req2)
	require.Equal(t, http.StatusCreated, w2.Code)

	var resp1, resp2 bundleCreateResponse
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &resp1))
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
	assert.NotEqual(t, resp1.Name, resp2.Name)
}

// TestBundleAPI_SetsProvenance verifies that provenance fields from the request
// are stored on the Bundle.
func TestBundleAPI_SetsProvenance(t *testing.T) {
	c := bundleAPIClient()
	srv := newBundleAPIServer(c, "tok", "my-ns")
	handler := srv.Handler()

	body := `{"pipeline":"my-app","type":"image","images":[{"repository":"ghcr.io/org/app","tag":"v2"}],"provenance":{"commitSHA":"sha1","author":"bob","ciRunURL":"https://ci.example.com/run/1"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	handler(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var bundleList v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	require.Len(t, bundleList.Items, 1)
	b := bundleList.Items[0]
	assert.Equal(t, "my-app", b.Spec.Pipeline)
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, "sha1", b.Spec.Provenance.CommitSHA)
	assert.Equal(t, "bob", b.Spec.Provenance.Author)
	assert.Equal(t, "https://ci.example.com/run/1", b.Spec.Provenance.CIRunURL)
}

// TestBundleAPI_CarriesIntentAndConfigRef verifies that intent and configRef
// from the request reach the Bundle spec. Regression test for
// C07-controller-02 (intent dropped, a staging-only bundle promoted to prod)
// and C07-controller-03 (configRef dropped, config bundles were no-ops).
func TestBundleAPI_CarriesIntentAndConfigRef(t *testing.T) {
	c := bundleAPIClient()
	srv := newBundleAPIServer(c, "tok", "default")

	w := bundleAPIPost(t, srv, `{"pipeline":"app","type":"mixed",
	  "images":[{"repository":"ghcr.io/org/app","tag":"v1"}],
	  "configRef":{"gitRepo":"https://github.com/org/cfg","commitSHA":"abc123"},
	  "intent":{"targetEnvironment":"staging","skipEnvironments":["uat"]}}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp bundleCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	var b v1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: resp.Namespace, Name: resp.Name}, &b))
	require.NotNil(t, b.Spec.Intent)
	assert.Equal(t, "staging", b.Spec.Intent.TargetEnvironment)
	assert.Equal(t, []string{"uat"}, b.Spec.Intent.SkipEnvironments)
	require.NotNil(t, b.Spec.ConfigRef)
	assert.Equal(t, "https://github.com/org/cfg", b.Spec.ConfigRef.GitRepo)
	assert.Equal(t, "abc123", b.Spec.ConfigRef.CommitSHA)
	assert.Equal(t, "mixed", b.Spec.Type)
}

// TestBundleAPI_ValidatesRequest verifies that requests the controller cannot
// act on are rejected with a clear status instead of creating a Bundle that
// silently does nothing. Regression test for C07-controller-02/03/16/29.
func TestBundleAPI_ValidatesRequest(t *testing.T) {
	image := `"images":[{"repository":"ghcr.io/org/app","tag":"v1"}]`
	tests := []struct {
		name          string
		onlyNamespace string
		body          string
		wantCode      int
		wantBody      string
	}{
		{name: "unknown field", body: `{"pipeline":"app",` + image + `,"intnet":{"targetEnvironment":"staging"}}`,
			wantCode: http.StatusBadRequest, wantBody: "unknown field"},
		{name: "unknown type", body: `{"pipeline":"app","type":"helm",` + image + `}`,
			wantCode: http.StatusBadRequest, wantBody: "type must be one of"},
		{name: "image bundle without images", body: `{"pipeline":"app","type":"image"}`,
			wantCode: http.StatusBadRequest, wantBody: "requires at least one entry in images"},
		{name: "config bundle without configRef", body: `{"pipeline":"app","type":"config"}`,
			wantCode: http.StatusBadRequest, wantBody: "requires configRef.commitSHA"},
		{name: "config bundle with empty commitSHA", body: `{"pipeline":"app","type":"config","configRef":{"gitRepo":"https://x"}}`,
			wantCode: http.StatusBadRequest, wantBody: "requires configRef.commitSHA"},
		{name: "mixed bundle without images", body: `{"pipeline":"app","type":"mixed","configRef":{"commitSHA":"abc"}}`,
			wantCode: http.StatusBadRequest, wantBody: "requires at least one entry in images"},
		{name: "invalid pipeline name", body: `{"pipeline":"___",` + image + `}`,
			wantCode: http.StatusBadRequest, wantBody: "valid Kubernetes object name"},
		{name: "pipeline name over 63 characters", body: `{"pipeline":"` + strings.Repeat("a", 64) + `",` + image + `}`,
			wantCode: http.StatusBadRequest, wantBody: "valid Kubernetes object name"},
		{name: "pipeline not found", body: `{"pipeline":"missing",` + image + `}`,
			wantCode: http.StatusNotFound, wantBody: "not found"},
		{name: "namespace without the pipeline", body: `{"pipeline":"app","namespace":"kube-system",` + image + `}`,
			wantCode: http.StatusNotFound, wantBody: "not found"},
		{name: "namespace outside watch namespace", onlyNamespace: "default",
			body:     `{"pipeline":"app","namespace":"team-a",` + image + `}`,
			wantCode: http.StatusForbidden, wantBody: "not watched"},
		{name: "ciRunURL with another scheme", body: `{"pipeline":"app",` + image + `,"provenance":{"ciRunURL":"javascript:alert(1)"}}`,
			wantCode: http.StatusBadRequest, wantBody: "provenance.ciRunURL must be an absolute http or https URL"},
		{name: "relative ciRunURL", body: `{"pipeline":"app",` + image + `,"provenance":{"ciRunURL":"/o/r/actions/runs/1"}}`,
			wantCode: http.StatusBadRequest, wantBody: "provenance.ciRunURL must be an absolute http or https URL"},
		{name: "ciRunURL with a newline", body: `{"pipeline":"app",` + image + `,"provenance":{"ciRunURL":"https://ci.example.com/1\n| x |"}}`,
			wantCode: http.StatusBadRequest, wantBody: "provenance.ciRunURL must not contain whitespace"},
		{name: "ciRunURL with user info", body: `{"pipeline":"app",` + image + `,"provenance":{"ciRunURL":"https://u:s3cret@ci.example.com/1"}}`,
			wantCode: http.StatusBadRequest, wantBody: "provenance.ciRunURL must not contain user info"},
		{name: "ciRunURL with a credential parsed as a port", body: `{"pipeline":"app",` + image + `,"provenance":{"ciRunURL":"https://user:s3cret/x"}}`,
			wantCode: http.StatusBadRequest, wantBody: "provenance.ciRunURL is not a valid URL"},
		{name: "https ciRunURL", body: `{"pipeline":"app",` + image + `,"provenance":{"ciRunURL":"https://github.com/o/r/actions/runs/1","commitSHA":"abc"}}`,
			wantCode: http.StatusCreated},
		{name: "provenance without ciRunURL", body: `{"pipeline":"app",` + image + `,"provenance":{"commitSHA":"abc"}}`,
			wantCode: http.StatusCreated},
		{name: "config bundle", body: `{"pipeline":"app","type":"config","configRef":{"commitSHA":"abc"}}`,
			wantCode: http.StatusCreated},
		{name: "chart bundle", body: `{"pipeline":"app","type":"chart","chart":{"name":"podinfo","version":"6.15.0"}}`,
			wantCode: http.StatusCreated},
		{name: "chart bundle without a version", body: `{"pipeline":"app","type":"chart","chart":{"name":"podinfo"}}`,
			wantCode: http.StatusBadRequest, wantBody: `type "chart" requires chart.name and chart.version`},
		{name: "other namespace with the pipeline, cluster-wide mode", body: `{"pipeline":"app","namespace":"team-a",` + image + `}`,
			wantCode: http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := bundleAPIClient()
			srv := newBundleAPIServer(c, "tok", "default")
			srv.onlyNamespace = tt.onlyNamespace

			w := bundleAPIPost(t, srv, tt.body)
			assert.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantBody != "" {
				assert.Contains(t, w.Body.String(), tt.wantBody)
			}
			assert.NotContains(t, w.Body.String(), "s3cret", "a rejected ciRunURL must not be echoed")
			var list v1alpha1.BundleList
			require.NoError(t, c.List(context.Background(), &list))
			if tt.wantCode == http.StatusCreated {
				assert.Len(t, list.Items, 1)
			} else {
				assert.Empty(t, list.Items, "no Bundle may be created for a rejected request")
			}
		})
	}
}

// TestBundleAPI_DefaultsToServerNamespace verifies that a request without a
// namespace lands in the server's namespace (the watched namespace in
// namespace-scoped mode). Regression test for C07-controller-16.
func TestBundleAPI_DefaultsToServerNamespace(t *testing.T) {
	c := bundleAPIClient()
	srv := newBundleAPIServer(c, "tok", "team-a")
	srv.onlyNamespace = "team-a"

	w := bundleAPIPost(t, srv, `{"pipeline":"app","images":[{"repository":"ghcr.io/org/app","tag":"v1"}]}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var resp bundleCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "team-a", resp.Namespace)
	assert.True(t, strings.HasPrefix(resp.Name, "app-"), resp.Name)
}

// TestBundleAPI_EmptyTokenRejectsEverything verifies that a server built with
// no token cannot be authenticated with an empty bearer token.
func TestBundleAPI_EmptyTokenRejectsEverything(t *testing.T) {
	srv := newBundleAPIServer(bundleAPIClient(), "", "default")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(`{"pipeline":"app"}`))
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	srv.Handler()(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestBundleAPI_PipelineNotInCacheYet checks that a Pipeline the cache has not
// seen yet is read from the API server before the API answers 404: a CI job
// may apply a Pipeline and post a Bundle for it right after.
func TestBundleAPI_PipelineNotInCacheYet(t *testing.T) {
	image := `"images":[{"repository":"ghcr.io/x/app","tag":"1.0.0"}]`
	cache := fake.NewClientBuilder().WithScheme(bundleAPIScheme()).Build()
	live := fake.NewClientBuilder().WithScheme(bundleAPIScheme()).WithObjects(bundleAPIPipeline("default", "app")).Build()

	srv := newBundleAPIServer(cache, "tok", "default")
	w := bundleAPIPost(t, srv, `{"pipeline":"app",`+image+`}`)
	assert.Equal(t, http.StatusNotFound, w.Code, "without a reader the cache is trusted: %s", w.Body.String())

	srv.reader = live
	w = bundleAPIPost(t, srv, `{"pipeline":"app",`+image+`}`)
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var list v1alpha1.BundleList
	require.NoError(t, cache.List(context.Background(), &list))
	assert.Len(t, list.Items, 1, "the Bundle is created through the controller's client")

	w = bundleAPIPost(t, srv, `{"pipeline":"missing",`+image+`}`)
	assert.Equal(t, http.StatusNotFound, w.Code, "a Pipeline neither has is still 404: %s", w.Body.String())
}
