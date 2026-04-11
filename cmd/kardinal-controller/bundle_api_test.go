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
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func bundleAPIScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestBundleAPI_CreateBundle verifies that a valid POST creates a Bundle CRD.
func TestBundleAPI_CreateBundle(t *testing.T) {
	s := bundleAPIScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

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
	s := bundleAPIScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
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
	s := bundleAPIScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
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
	s := bundleAPIScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
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
	s := bundleAPIScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
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
	s := bundleAPIScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
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
	s := bundleAPIScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
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
