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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Audit C10b-web-09: the UI must not be frameable by another site, and the
// browser must not guess content types.
func TestUISecurityHeaders(t *testing.T) {
	inner := http.NewServeMux()
	inner.HandleFunc("/ui/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html>"))
	})
	inner.HandleFunc("/api/v1/ui/pipelines", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	})
	inner.HandleFunc("/api/v1/ui/denied", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	handler := withUISecurityHeaders(inner)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "UI page", method: http.MethodGet, path: "/ui/", wantStatus: http.StatusOK},
		{name: "UI API read", method: http.MethodGet, path: "/api/v1/ui/pipelines", wantStatus: http.StatusOK},
		{name: "rejected request", method: http.MethodPost, path: "/api/v1/ui/denied", wantStatus: http.StatusUnauthorized},
		{name: "unknown path", method: http.MethodGet, path: "/nope", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			require.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
			assert.Equal(t, uiContentSecurityPolicy, rec.Header().Get("Content-Security-Policy"))
		})
	}
}

func assertUISecurityHeaders(t *testing.T, h http.Header) {
	t.Helper()
	assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	assert.Equal(t, uiContentSecurityPolicy, h.Get("Content-Security-Policy"))
}

// TestNewUIHandler_SecurityHeaders checks the handler main actually serves:
// every response carries the headers, including refusals from the auth,
// Host, CORS and body-limit layers.
func TestNewUIHandler_SecurityHeaders(t *testing.T) {
	const secret = "ui-test-token"
	static := uiAuthConfig{staticToken: secret}
	tokenReview := func() uiAuthConfig {
		return uiAuthConfig{
			tokens: &uiTestTokens{users: map[string]string{"viewer-token": "viewer"}},
			access: &uiTestAccess{},
		}
	}
	tests := []struct {
		name       string
		auth       uiAuthConfig
		method     string
		path       string
		host       string
		origin     string
		token      string
		body       string
		wantStatus int
	}{
		{name: "UI page", method: http.MethodGet, path: "/ui/", wantStatus: http.StatusOK},
		{name: "UI script", method: http.MethodGet, path: "/ui/assets/index-abc.js", wantStatus: http.StatusOK},
		{name: "UI API read", method: http.MethodGet, path: "/api/v1/ui/pipelines", wantStatus: http.StatusOK},
		{name: "unknown path", method: http.MethodGet, path: "/nope", wantStatus: http.StatusNotFound},
		{name: "static token missing", auth: static, method: http.MethodGet, path: "/api/v1/ui/pipelines",
			wantStatus: http.StatusUnauthorized},
		{name: "static token accepted", auth: static, method: http.MethodGet, path: "/api/v1/ui/pipelines",
			token: "Bearer " + secret, wantStatus: http.StatusOK},
		{name: "TokenReview rejects the token", auth: tokenReview(), method: http.MethodGet,
			path: "/api/v1/ui/pipelines", token: "Bearer unknown", wantStatus: http.StatusUnauthorized},
		{name: "TokenReview user without RBAC", auth: tokenReview(), method: http.MethodPost,
			path: "/api/v1/ui/pause", token: "Bearer viewer-token", body: `{"pipeline":"app"}`, wantStatus: http.StatusForbidden},
		{name: "unknown Host (DNS rebinding)", method: http.MethodGet, path: "/api/v1/ui/pipelines",
			host: "evil.example:8082", wantStatus: http.StatusForbidden},
		{name: "cross-origin request", method: http.MethodPost, path: "/api/v1/ui/pause",
			origin: "https://evil.example", body: "{}", wantStatus: http.StatusForbidden},
		{name: "body over the limit", method: http.MethodPost, path: "/api/v1/ui/pause",
			body: `{"pipeline":"app"` + strings.Repeat(" ", maxUIRequestBody) + `}`, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
			h := newUIHandler(c, uiTestAssets, tt.auth, "", nil, zerolog.Nop())

			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Host = "localhost:8082"
			req.RemoteAddr = "127.0.0.1:54321" // kubectl port-forward
			if tt.host != "" {
				req.Host = tt.host
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.token != "" {
				req.Header.Set("Authorization", tt.token)
			}
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assertUISecurityHeaders(t, rec.Header())
		})
	}
}

func TestUIContentSecurityPolicy(t *testing.T) {
	tests := []struct {
		name      string
		directive string
	}{
		{name: "no framing by other sites", directive: "frame-ancestors 'none'"},
		{name: "scripts from the UI origin only", directive: "script-src 'self';"},
		{name: "API calls to the UI origin only", directive: "connect-src 'self'"},
		{name: "no plugins", directive: "object-src 'none'"},
		{name: "no base tag hijack", directive: "base-uri 'self'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Contains(t, uiContentSecurityPolicy, tt.directive)
		})
	}
	assert.NotContains(t, uiContentSecurityPolicy, "unsafe-eval")
}
