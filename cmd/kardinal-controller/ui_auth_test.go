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
	"crypto/subtle"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// uiAuthMiddleware mirrors the inline handler built in main() for testability.
// Returns an http.Handler that requires a Bearer token on /api/v1/ui/* routes
// and serves uiMux directly for all other paths.
func uiAuthMiddleware(uiMux *http.ServeMux, token string) http.Handler {
	if token == "" {
		return uiMux
	}
	tokenBytes := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/ui/") {
			authHeader := r.Header.Get("Authorization")
			provided := strings.TrimPrefix(authHeader, "Bearer ")
			if !strings.HasPrefix(authHeader, "Bearer ") ||
				subtle.ConstantTimeCompare([]byte(provided), tokenBytes) != 1 {
				w.Header().Set("Www-Authenticate", `Bearer realm="kardinal-ui"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		uiMux.ServeHTTP(w, r)
	})
}

// TestUIAuth_NoToken verifies open mode: all /api/v1/ui/* routes accessible without auth.
func TestUIAuth_NoToken(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	uiMux := http.NewServeMux()
	srv.RegisterRoutes(uiMux)

	handler := uiAuthMiddleware(uiMux, "") // no token → open mode

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should NOT be 401 — open mode returns 200 (empty list).
	assert.Equal(t, http.StatusOK, w.Code,
		"open mode: /api/v1/ui/pipelines must return 200 without auth header")
}

// TestUIAuth_CorrectToken verifies that a correct Bearer token grants access.
func TestUIAuth_CorrectToken(t *testing.T) {
	const secret = "supersecrettoken"
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	uiMux := http.NewServeMux()
	srv.RegisterRoutes(uiMux)

	handler := uiAuthMiddleware(uiMux, secret)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code,
		"correct Bearer token must return 200")
}

// TestUIAuth_WrongToken verifies that a wrong token returns 401.
func TestUIAuth_WrongToken(t *testing.T) {
	const secret = "supersecrettoken"
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	uiMux := http.NewServeMux()
	srv.RegisterRoutes(uiMux)

	handler := uiAuthMiddleware(uiMux, secret)

	cases := []struct {
		name   string
		header string
	}{
		{"wrong token", "Bearer wrongtoken"},
		{"no header", ""},
		{"malformed — no bearer prefix", "Token " + secret},
		{"empty bearer", "Bearer "},
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
				"must return 401 for unauthenticated request (case: %s)", tc.name)
			assert.Contains(t, w.Header().Get("Www-Authenticate"), `Bearer realm="kardinal-ui"`,
				"must set Www-Authenticate header on 401")
		})
	}
}

// TestUIAuth_StaticAssetsUnprotected verifies that /ui/* static assets bypass auth (O4).
func TestUIAuth_StaticAssetsUnprotected(t *testing.T) {
	const secret = "supersecrettoken"
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	uiMux := http.NewServeMux()
	srv.RegisterRoutes(uiMux)
	// Register a trivial /ui/ handler to simulate the static file server.
	uiMux.HandleFunc("/ui/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := uiAuthMiddleware(uiMux, secret)

	req := httptest.NewRequest(http.MethodGet, "/ui/index.html", nil)
	// Deliberately omit auth header.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code,
		"static /ui/* assets must not be gated by auth middleware (O4)")
}
