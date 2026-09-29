// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// TestCORS_Middleware verifies the CORS policy for /api/v1/ui/*: requests with
// no Origin and same-origin requests (Origin host equals the Host header) pass,
// cross-origin requests pass only when allow-listed (or "*"), and non-UI paths
// are never filtered. Regression test for C07-controller-05 and C10b-web-01:
// the default policy rejected the embedded UI's own same-origin POSTs.
func TestCORS_Middleware(t *testing.T) {
	tests := []struct {
		name       string
		allowed    string
		method     string
		path       string
		host       string
		origin     string
		wantCode   int
		wantACAO   string
		wantCalled bool
	}{
		{name: "no origin", method: http.MethodPost, path: "/api/v1/ui/pause", host: "k.example:8082",
			wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin POST, default policy", method: http.MethodPost, path: "/api/v1/ui/pause", host: "k.example:8082",
			origin: "http://k.example:8082", wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin, https, host case differs", method: http.MethodPost, path: "/api/v1/ui/promote", host: "K.Example",
			origin: "https://k.example", wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin with allow-list set", allowed: "https://dash.example", method: http.MethodPost,
			path: "/api/v1/ui/rollback", host: "k.example:8082", origin: "http://k.example:8082",
			wantCode: http.StatusOK, wantCalled: true},
		{name: "cross origin, default policy", method: http.MethodPost, path: "/api/v1/ui/pause", host: "k.example:8082",
			origin: "https://evil.example", wantCode: http.StatusForbidden},
		{name: "cross origin, different port", method: http.MethodPost, path: "/api/v1/ui/pause", host: "k.example:8082",
			origin: "http://k.example:9999", wantCode: http.StatusForbidden},
		{name: "null origin", method: http.MethodPost, path: "/api/v1/ui/pause", host: "k.example:8082",
			origin: "null", wantCode: http.StatusForbidden},
		{name: "non-http scheme with same host", method: http.MethodPost, path: "/api/v1/ui/pause", host: "k.example:8082",
			origin: "chrome-extension://k.example:8082", wantCode: http.StatusForbidden},
		{name: "cross origin, allow-listed", allowed: "https://dash.example, https://other.example", method: http.MethodPost,
			path: "/api/v1/ui/pause", host: "k.example:8082", origin: "https://dash.example",
			wantCode: http.StatusOK, wantACAO: "https://dash.example", wantCalled: true},
		{name: "cross origin, not in allow-list", allowed: "https://dash.example", method: http.MethodPost,
			path: "/api/v1/ui/pause", host: "k.example:8082", origin: "https://evil.example", wantCode: http.StatusForbidden},
		{name: "cross origin preflight, allow-listed", allowed: "https://dash.example", method: http.MethodOptions,
			path: "/api/v1/ui/pause", host: "k.example:8082", origin: "https://dash.example",
			wantCode: http.StatusOK, wantACAO: "https://dash.example"},
		{name: "wildcard", allowed: "*", method: http.MethodPost, path: "/api/v1/ui/pause", host: "k.example:8082",
			origin: "https://any.example", wantCode: http.StatusOK, wantACAO: "https://any.example", wantCalled: true},
		{name: "non-UI path is not filtered", method: http.MethodGet, path: "/ui/index.html", host: "k.example:8082",
			origin: "https://evil.example", wantCode: http.StatusOK, wantCalled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			h := applyCORSMiddleware(next, tt.allowed, zerolog.Nop())
			req := httptest.NewRequest(tt.method, "http://"+tt.host+tt.path, strings.NewReader(`{}`))
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantCode, rec.Code)
			assert.Equal(t, tt.wantACAO, rec.Header().Get("Access-Control-Allow-Origin"))
			assert.Equal(t, tt.wantCalled, called)
		})
	}
}
