// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package accesslog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lines(t *testing.T, buf *bytes.Buffer) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(l), &m), l)
		out = append(out, m)
	}
	return out
}

// handler answers like the APIs: it authenticates "Bearer good" as alice
// (a login when the query says so), refuses others, and serves the rest.
func handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := FromContext(r.Context())
		switch r.Header.Get("Authorization") {
		case "Bearer good":
			e.User, e.Groups, e.Auth = "alice", []string{"devs"}, "tokenreview"
			e.Login = r.URL.Query().Get("login") == "1"
		case "Bearer s3cret-static":
			e.Auth = "static-token"
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/forbidden" {
			http.Error(w, `forbidden: user "alice" cannot update pipelines/pause.kardinal.io in namespace team-a`, http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
}

func TestMiddleware_WhatIsLogged(t *testing.T) {
	tests := []struct {
		name, method, path, auth string
		all                      bool
		want                     map[string]interface{} // nil: no line
	}{
		{"read by a known caller is not logged", "GET", "/api/v1/ui/pipelines", "Bearer good", false, nil},
		{"login", "GET", "/api/v1/ui/pipelines?login=1", "Bearer good", false,
			map[string]interface{}{"access": "login", "user": "alice", "auth": "tokenreview", "status": float64(200)}},
		{"bad token", "GET", "/api/v1/ui/pipelines", "Bearer nope", false,
			map[string]interface{}{"access": "denied", "status": float64(401), "reason": "unauthorized"}},
		{"forbidden", "GET", "/forbidden", "Bearer good", false,
			map[string]interface{}{"access": "denied", "status": float64(403), "user": "alice",
				"reason": `forbidden: user "alice" cannot update pipelines/pause.kardinal.io in namespace team-a`}},
		{"write", "POST", "/api/v1/ui/pause", "Bearer good", false,
			map[string]interface{}{"access": "write", "method": "POST", "path": "/api/v1/ui/pause", "user": "alice"}},
		{"static token write", "POST", "/api/v1/bundles", "Bearer s3cret-static", false,
			map[string]interface{}{"access": "write", "auth": "static-token"}},
		{"every request when asked", "GET", "/api/v1/ui/pipelines", "Bearer good", true,
			map[string]interface{}{"access": "request", "user": "alice"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := New(Config{AllRequests: tt.all}, zerolog.New(&buf))
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{"token":"body-s3cret"}`))
			req.Header.Set("Authorization", tt.auth)
			l.Middleware("ui", handler()).ServeHTTP(httptest.NewRecorder(), req)
			got := lines(t, &buf)
			if tt.want == nil {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			for k, v := range tt.want {
				assert.Equal(t, v, got[0][k], k)
			}
			assert.Equal(t, "ui", got[0]["server"])
			assert.NotContains(t, got[0], "sourceIP", "only with SourceIP")
			assert.NotContains(t, buf.String(), "s3cret", "no credential or body is logged")
		})
	}
}

func TestSourceIP(t *testing.T) {
	proxies, err := ParseCIDRs([]string{"10.0.0.0/8", "192.168.1.1"})
	require.NoError(t, err)
	l := New(Config{SourceIP: true, TrustedProxies: proxies}, zerolog.Nop())
	tests := []struct {
		name, remote, xff, want string
	}{
		{"direct client", "203.0.113.7:5000", "", "203.0.113.7"},
		{"untrusted peer's header is ignored", "203.0.113.7:5000", "198.51.100.1", "203.0.113.7"},
		{"trusted proxy", "10.1.2.3:443", "198.51.100.1", "198.51.100.1"},
		{"chain of trusted proxies", "10.1.2.3:443", "198.51.100.1, 192.168.1.1, 10.9.9.9", "198.51.100.1"},
		{"spoofed left entries are not believed", "10.1.2.3:443", "1.1.1.1, 198.51.100.1", "198.51.100.1"},
		{"garbage header", "10.1.2.3:443", "not-an-ip", "10.1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remote
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			assert.Equal(t, tt.want, l.sourceIP(r))
		})
	}
	_, err = ParseCIDRs([]string{"10.0.0.0/33"})
	assert.Error(t, err)
}

// TestRateBound: past PerSecond lines in a second, lines are dropped and one
// summary line says how many.
func TestRateBound(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(1_000, 0)
	l := New(Config{PerSecond: 3}, zerolog.New(&buf))
	l.now = func() time.Time { return now }
	h := l.Middleware("ui", handler())
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("POST", "/api/v1/ui/pause", nil)
		req.Header.Set("Authorization", "Bearer nope")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	assert.Len(t, lines(t, &buf), 3)
	now = now.Add(time.Second)
	req := httptest.NewRequest("POST", "/api/v1/ui/pause", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	got := lines(t, &buf)
	require.Len(t, got, 5)
	assert.Equal(t, float64(7), got[3]["dropped"])
	assert.Equal(t, "denied", got[4]["access"])
}
