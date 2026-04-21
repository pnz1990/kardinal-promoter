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

package uiauth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	authv1 "k8s.io/api/authentication/v1"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockReviewer is a test double for TokenReviewer.
type mockReviewer struct {
	authenticated bool
	err           error
}

func (m *mockReviewer) Review(_ context.Context, _ string) (*authv1.TokenReviewStatus, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &authv1.TokenReviewStatus{Authenticated: m.authenticated}, nil
}

// okHandler is a trivial next handler used to confirm pass-through.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestTokenReviewMiddleware_NoAuthHeader(t *testing.T) {
	// O2: Missing Authorization header → 401
	reviewer := &mockReviewer{authenticated: false}
	handler := uiauth.Middleware(okHandler, reviewer)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Header().Get("Www-Authenticate"), `Bearer realm="kardinal-ui"`)
}

func TestTokenReviewMiddleware_ValidToken(t *testing.T) {
	// O3 (pass case): TokenReview returns Authenticated=true → 200
	reviewer := &mockReviewer{authenticated: true}
	handler := uiauth.Middleware(okHandler, reviewer)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
	req.Header.Set("Authorization", "Bearer valid-kubeconfig-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestTokenReviewMiddleware_InvalidToken(t *testing.T) {
	// O3 (fail case): TokenReview returns Authenticated=false → 401
	reviewer := &mockReviewer{authenticated: false}
	handler := uiauth.Middleware(okHandler, reviewer)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
	req.Header.Set("Authorization", "Bearer expired-or-invalid-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Header().Get("Www-Authenticate"), `Bearer realm="kardinal-ui"`)
}

func TestTokenReviewMiddleware_APIError(t *testing.T) {
	// O6: TokenReview API failure → 503 (fail-closed)
	reviewer := &mockReviewer{err: errors.New("apiserver unreachable")}
	handler := uiauth.Middleware(okHandler, reviewer)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestTokenReviewMiddleware_StaticAssetsUnprotected(t *testing.T) {
	// O5: /ui/* static assets pass through without auth check
	reviewer := &mockReviewer{authenticated: false}
	handler := uiauth.Middleware(okHandler, reviewer)

	req := httptest.NewRequest(http.MethodGet, "/ui/index.html", nil)
	// Deliberately no Authorization header
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Must not be 401 — static assets bypass auth
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestTokenReviewMiddleware_MalformedHeader(t *testing.T) {
	// O2: Malformed header (not "Bearer ..." prefix) → 401
	reviewer := &mockReviewer{authenticated: true}
	handler := uiauth.Middleware(okHandler, reviewer)

	cases := []struct {
		name   string
		header string
	}{
		{"token without bearer prefix", "Token kubetoken"},
		{"basic auth", "Basic dXNlcjpwYXNz"},
		{"empty header", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			require.Equal(t, http.StatusUnauthorized, w.Code,
				"malformed header should return 401 (case: %s)", tc.name)
		})
	}
}

func TestTokenReviewMiddleware_NonUIPath(t *testing.T) {
	// Non-UI API paths (e.g. /api/v1/bundles) should pass through even without auth.
	// The TokenReview middleware only guards /api/v1/ui/* — bundle API has its own auth.
	reviewer := &mockReviewer{authenticated: false}
	handler := uiauth.Middleware(okHandler, reviewer)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bundles", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
