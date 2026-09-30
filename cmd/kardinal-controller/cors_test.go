// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestCORS_Middleware verifies the CORS and Host policy for /api/v1/ui/*:
//   - while UI auth is off, every /api/ request to an unknown Host gets 403,
//     reads and requests without Origin included;
//   - other requests with no Origin pass;
//   - same-origin requests (Origin names the Host) pass only when the Host is
//     loopback or in --ui-allowed-hosts;
//   - cross-origin requests pass only when allow-listed (or "*");
//   - non-UI paths are never filtered.
//
// Regression test for C07-controller-05 and C10b-web-01 (the default policy
// rejected the embedded UI's own same-origin POSTs) and for the #1240 review
// (Origin == Host alone let a DNS-rebound page through).
func TestCORS_Middleware(t *testing.T) {
	const svc = "kardinal-promoter.kardinal-system.svc"
	tests := []struct {
		name       string
		allowed    string // --cors-allowed-origins
		hosts      string // --ui-allowed-hosts
		auth       bool   // UI auth on
		method     string
		path       string
		host       string
		origin     string
		wantCode   int
		wantACAO   string
		wantCalled bool
	}{
		{name: "no origin, allowed host", hosts: "k.example", method: http.MethodPost, path: "/api/v1/ui/pause",
			host: "k.example:8082", wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin POST, default policy", hosts: "k.example", method: http.MethodPost, path: "/api/v1/ui/pause",
			host: "k.example:8082", origin: "http://k.example:8082", wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin, https, host case differs", hosts: "k.example", method: http.MethodPost,
			path: "/api/v1/ui/promote", host: "K.Example", origin: "https://k.example",
			wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin with allow-list set", allowed: "https://dash.example", hosts: "k.example",
			method: http.MethodPost, path: "/api/v1/ui/rollback", host: "k.example:8082", origin: "http://k.example:8082",
			wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin, localhost", method: http.MethodPost, path: "/api/v1/ui/pause", host: "localhost:8082",
			origin: "http://localhost:8082", wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin, 127.0.0.1", method: http.MethodPost, path: "/api/v1/ui/pause", host: "127.0.0.1:8082",
			origin: "http://127.0.0.1:8082", wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin, [::1]", method: http.MethodPost, path: "/api/v1/ui/pause", host: "[::1]:8082",
			origin: "http://[::1]:8082", wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin, localhost with trailing dot", method: http.MethodPost, path: "/api/v1/ui/pause",
			host: "localhost.:8082", origin: "http://localhost.:8082", wantCode: http.StatusOK, wantCalled: true},
		{name: "same origin, Service DNS name", hosts: "kardinal-promoter," + svc, method: http.MethodPost,
			path: "/api/v1/ui/pause", host: svc + ":8082", origin: "http://" + svc + ":8082",
			wantCode: http.StatusOK, wantCalled: true},

		// DNS rebinding: the Host is the attacker's name.
		{name: "rebinding, auth off", method: http.MethodPost, path: "/api/v1/ui/pause", host: "evil.example:8082",
			origin: "http://evil.example:8082", wantCode: http.StatusForbidden},
		{name: "rebinding, auth on: no CORS grant", auth: true, method: http.MethodPost, path: "/api/v1/ui/pause",
			host: "evil.example:8082", origin: "http://evil.example:8082", wantCode: http.StatusForbidden},
		{name: "rebinding, suffix of an allowed host", hosts: "k.example", method: http.MethodPost,
			path: "/api/v1/ui/pause", host: "k.example.evil.example:8082", origin: "http://k.example.evil.example:8082",
			wantCode: http.StatusForbidden},
		{name: "rebinding, form post without Origin, auth off", method: http.MethodPost, path: "/api/v1/ui/pause",
			host: "evil.example:8082", wantCode: http.StatusForbidden},
		{name: "unknown host, PUT without Origin, auth off", method: http.MethodPut, path: "/api/v1/ui/pause",
			host: "evil.example:8082", wantCode: http.StatusForbidden},
		{name: "unknown host, PATCH without Origin, auth off", method: http.MethodPatch, path: "/api/v1/ui/pause",
			host: "evil.example:8082", wantCode: http.StatusForbidden},
		{name: "unknown host, DELETE without Origin, auth off", method: http.MethodDelete, path: "/api/v1/ui/pause",
			host: "evil.example:8082", wantCode: http.StatusForbidden},
		{name: "unknown host, POST without Origin, auth on", auth: true, method: http.MethodPost,
			path: "/api/v1/ui/pause", host: "evil.example:8082", wantCode: http.StatusOK, wantCalled: true},
		{name: "unknown host, GET without Origin, auth off", method: http.MethodGet, path: "/api/v1/ui/pipelines",
			host: "evil.example:8082", wantCode: http.StatusForbidden},
		{name: "unknown host, HEAD without Origin, auth off", method: http.MethodHead, path: "/api/v1/ui/pipelines",
			host: "evil.example:8082", wantCode: http.StatusForbidden},
		{name: "unknown host, OPTIONS, auth off", method: http.MethodOptions, path: "/api/v1/ui/pipelines",
			host: "evil.example:8082", origin: "http://evil.example:8082", wantCode: http.StatusForbidden},
		{name: "unknown host, other /api/ path, auth off", method: http.MethodGet, path: "/api/v1/bundles",
			host: "evil.example:8082", wantCode: http.StatusForbidden},
		{name: "unknown host, GET without Origin, auth on", auth: true, method: http.MethodGet,
			path: "/api/v1/ui/pipelines", host: "evil.example:8082", wantCode: http.StatusOK, wantCalled: true},
		{name: "allowed host, GET without Origin, auth off", hosts: "k.example", method: http.MethodGet,
			path: "/api/v1/ui/pipelines", host: "k.example:8082", wantCode: http.StatusOK, wantCalled: true},

		{name: "cross origin, default policy", method: http.MethodPost, path: "/api/v1/ui/pause", host: "localhost:8082",
			origin: "https://evil.example", wantCode: http.StatusForbidden},
		{name: "cross origin, different port", hosts: "k.example", method: http.MethodPost, path: "/api/v1/ui/pause",
			host: "k.example:8082", origin: "http://k.example:9999", wantCode: http.StatusForbidden},
		{name: "null origin", method: http.MethodPost, path: "/api/v1/ui/pause", host: "localhost:8082",
			origin: "null", wantCode: http.StatusForbidden},
		{name: "non-http scheme with same host", method: http.MethodPost, path: "/api/v1/ui/pause",
			host: "localhost:8082", origin: "chrome-extension://localhost:8082", wantCode: http.StatusForbidden},
		{name: "cross origin, allow-listed", allowed: "https://dash.example, https://other.example", hosts: "k.example",
			method: http.MethodPost, path: "/api/v1/ui/pause", host: "k.example:8082", origin: "https://dash.example",
			wantCode: http.StatusOK, wantACAO: "https://dash.example", wantCalled: true},
		{name: "cross origin, allow-listed, unknown host, auth off", allowed: "https://dash.example",
			method: http.MethodPost, path: "/api/v1/ui/pause", host: "k.example:8082", origin: "https://dash.example",
			wantCode: http.StatusForbidden},
		{name: "cross origin, allow-listed, unknown host, auth on", allowed: "https://dash.example", auth: true,
			method: http.MethodPost, path: "/api/v1/ui/pause", host: "k.example:8082", origin: "https://dash.example",
			wantCode: http.StatusOK, wantACAO: "https://dash.example", wantCalled: true},
		{name: "cross origin, not in allow-list", allowed: "https://dash.example", method: http.MethodPost,
			path: "/api/v1/ui/pause", host: "localhost:8082", origin: "https://evil.example", wantCode: http.StatusForbidden},
		{name: "cross origin preflight, allow-listed", allowed: "https://dash.example", hosts: "k.example",
			method: http.MethodOptions, path: "/api/v1/ui/pause", host: "k.example:8082", origin: "https://dash.example",
			wantCode: http.StatusOK, wantACAO: "https://dash.example"},
		{name: "wildcard", allowed: "*", method: http.MethodPost, path: "/api/v1/ui/pause", host: "localhost:8082",
			origin: "https://any.example", wantCode: http.StatusOK, wantACAO: "https://any.example", wantCalled: true},
		{name: "static assets are not filtered", method: http.MethodGet, path: "/ui/index.html", host: "evil.example:8082",
			origin: "https://evil.example", wantCode: http.StatusOK, wantCalled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hosts, err := parseUIAllowedHosts(tt.hosts)
			require.NoError(t, err)
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			h := applyCORSMiddleware(next, tt.allowed, hosts, tt.auth, zerolog.Nop())
			req := httptest.NewRequest(tt.method, "http://localhost"+tt.path, strings.NewReader(`{}`))
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

// TestParseUIAllowedHosts covers --ui-allowed-hosts parsing: ports, case,
// brackets and trailing dots are normalised away; schemes, paths and
// wildcards are rejected rather than silently never matching.
func TestParseUIAllowedHosts(t *testing.T) {
	tests := []struct {
		name    string
		csv     string
		want    []string
		wantErr bool
	}{
		{name: "empty", csv: "", want: []string{}},
		{name: "names", csv: "kardinal-promoter, kardinal-promoter.kardinal-system.svc ,,",
			want: []string{"kardinal-promoter", "kardinal-promoter.kardinal-system.svc"}},
		{name: "port, case, trailing dot", csv: "UI.Example.com.:443", want: []string{"ui.example.com"}},
		{name: "IPv6 literal", csv: "[fd00::1]:8082,fd00::2", want: []string{"fd00::1", "fd00::2"}},
		{name: "scheme", csv: "https://ui.example.com", wantErr: true},
		{name: "path", csv: "ui.example.com/ui", wantErr: true},
		{name: "wildcard", csv: "*.example.com", wantErr: true},
		{name: "port only", csv: ":8082", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUIAllowedHosts(tt.csv)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, got.names())
		})
	}
}

// TestUIHandler_DNSRebinding is the lead's repro for the #1240 review: with UI
// auth off, a page on a rebound name (evil.example resolving to the
// controller) sends a matching Origin and Host. The request must not be
// treated as same-origin and must not pause the pipeline.
func TestUIHandler_DNSRebinding(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"}},
	).Build()
	h := newUIHandler(c, nil, uiAuthConfig{}, "", nil, zerolog.Nop())

	req := httptest.NewRequest(http.MethodPost, "http://evil.example:8082/api/v1/ui/pause",
		strings.NewReader(`{"pipeline":"app"}`))
	req.Host = "evil.example:8082"
	// A loopback peer (a browser on the port-forward machine), so the Host
	// check is what refuses the request.
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Origin", "http://evil.example:8082")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "--ui-allowed-hosts")
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	var pl v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "app"}, &pl))
	assert.False(t, pl.Spec.Paused, "a rebound page must not pause the pipeline")
}

// TestUIHandler_DNSRebindingReads covers the read side of DNS rebinding: with
// UI auth off, a rebound page's same-origin GET carries no Origin header, so
// only the Host check stands between it and pipeline state. A rebound Host
// gets 403; the same GET on localhost is served.
func TestUIHandler_DNSRebindingReads(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"}},
	).Build()
	h := newUIHandler(c, nil, uiAuthConfig{}, "", nil, zerolog.Nop())

	tests := []struct {
		name     string
		host     string
		wantCode int
		wantBody string
	}{
		{name: "rebound host", host: "evil.example:8082", wantCode: http.StatusForbidden,
			wantBody: "--ui-allowed-hosts"},
		{name: "localhost", host: "localhost:8082", wantCode: http.StatusOK, wantBody: `"app"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/api/v1/ui/pipelines", nil)
			req.Host = tt.host
			req.RemoteAddr = "127.0.0.1:54321" // a browser on the port-forward machine
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tt.wantBody)
			assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
		})
	}
}
