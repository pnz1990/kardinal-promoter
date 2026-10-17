// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/web"
)

func TestParseUIBasePath(t *testing.T) {
	tests := []struct {
		raw, want, err string
	}{
		{raw: "", want: ""},
		{raw: "/", want: ""},
		{raw: "/kardinal", want: "/kardinal"},
		{raw: "/kardinal/", want: "/kardinal"},
		{raw: " /tools/kardinal ", want: "/tools/kardinal"},
		{raw: "/a.b_c~d-e", want: "/a.b_c~d-e"},
		{raw: "kardinal", err: "must start with /"},
		{raw: "/kar dinal", err: "each segment"},
		{raw: "/a//b", err: "each segment"},
		{raw: "/a/../b", err: "each segment"},
		{raw: "/a/%2e", err: "each segment"},
		{raw: "/a?x=1", err: "each segment"},
		{raw: "/ui", err: "must not start with /ui"},
		{raw: "/ui/x", err: "must not start with /ui"},
		{raw: "/api", err: "must not start with /api"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseUIBasePath(tt.raw)
			if tt.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The base path only adds paths: authentication and the routes see the
// unprefixed path, and the unprefixed UI keeps working (#1933).
func TestWithUIBasePath(t *testing.T) {
	const secret = "s3cret"
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	h := withUIBasePath("/kardinal", newUIHandler(c, nil, uiTestAssets, uiAuthConfig{staticToken: secret}, "", nil, zerolog.Nop()))
	bearer := "Bearer " + secret

	tests := []struct {
		name, path, token string
		want              int
		location          string
	}{
		{name: "the prefix redirects to the UI", path: "/kardinal", want: http.StatusFound, location: "/kardinal/ui/"},
		{name: "the prefix with a slash", path: "/kardinal/", want: http.StatusFound, location: "/kardinal/ui/"},
		{name: "ui without a slash", path: "/kardinal/ui", want: http.StatusFound, location: "/kardinal/ui/"},
		{name: "the app", path: "/kardinal/ui/", want: http.StatusOK},
		{name: "an asset", path: "/kardinal/ui/assets/index-abc.js", want: http.StatusOK},
		{name: "the API needs the token", path: "/kardinal/api/v1/ui/pipelines", want: http.StatusUnauthorized},
		{name: "the API with the token", path: "/kardinal/api/v1/ui/pipelines", token: bearer, want: http.StatusOK},
		{name: "the unprefixed app", path: "/ui/", want: http.StatusOK},
		{name: "the unprefixed API needs the token", path: "/api/v1/ui/pipelines", want: http.StatusUnauthorized},
		{name: "the unprefixed API with the token", path: "/api/v1/ui/pipelines", token: bearer, want: http.StatusOK},
		{name: "a longer first segment is not the prefix", path: "/kardinalx/ui/", want: http.StatusNotFound},
		{name: "other paths under the prefix", path: "/kardinal/metrics", want: http.StatusNotFound},
		{name: "a doubled prefix", path: "/kardinal/kardinal/ui/", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := uiAuthDo(t, h, http.MethodGet, tt.path, tt.token, "")
			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			if tt.location != "" {
				assert.Equal(t, tt.location, rec.Header().Get("Location"))
			}
			assert.Equal(t, uiContentSecurityPolicy, rec.Header().Get("Content-Security-Policy"), "security headers on every answer")
			if tt.want == http.StatusUnauthorized {
				assert.Contains(t, rec.Header().Get("Www-Authenticate"), `Bearer realm="kardinal-ui"`)
			}
		})
	}
}

// With auth off, the prefixed API still answers loopback peers only.
func TestWithUIBasePath_LoopbackOnlyWithoutAuth(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	h := withUIBasePath("/kardinal", newUIHandler(c, nil, uiTestAssets, uiAuthConfig{}, "", nil, zerolog.Nop()))
	req := httptest.NewRequest(http.MethodGet, "/kardinal/api/v1/ui/pipelines", nil)
	req.Host = "localhost:8082"
	req.RemoteAddr = "10.244.1.7:41000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	rec = uiAuthDo(t, h, http.MethodGet, "/kardinal/api/v1/ui/pipelines", "", "")
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestWithUIBasePath_NoBase(t *testing.T) {
	var seen string
	h := withUIBasePath("", http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.URL.Path }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/kardinal/ui/", nil))
	assert.Equal(t, "/kardinal/ui/", seen, "no base: paths pass unchanged")
}

// The built UI must load under any prefix: index.html refers to its scripts,
// styles and icons relatively (Vite base "./"), never from /ui/ or the root.
func TestUIBuild_RelativeAssetURLs(t *testing.T) {
	dist, err := fs.Sub(web.Assets, "dist")
	require.NoError(t, err)
	index, err := fs.ReadFile(dist, "index.html")
	require.NoError(t, err)
	refs := regexp.MustCompile(`(?:href|src)="([^"]+)"`).FindAllStringSubmatch(string(index), -1)
	require.NotEmpty(t, refs)
	for _, m := range refs {
		u := m[1]
		require.True(t, strings.HasPrefix(u, "./"), "index.html refers to %q; build the UI with base './'", u)
		_, err := fs.Stat(dist, strings.TrimPrefix(u, "./"))
		assert.NoError(t, err, "index.html refers to %q, which the build lacks", u)
	}
}
