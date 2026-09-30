// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package source

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseRegistryRef covers Docker Hub normalisation and short references (C05-steps-40).
func TestParseRegistryRef(t *testing.T) {
	tests := []struct {
		ref      string
		wantBase string
		wantName string
		wantErr  string
	}{
		{ref: "ghcr.io/myorg/myapp", wantBase: "https://ghcr.io", wantName: "myorg/myapp"},
		{ref: "http://localhost:5000/myapp", wantBase: "http://localhost:5000", wantName: "myapp"},
		{ref: "https://127.0.0.1:5000/a/b", wantBase: "https://127.0.0.1:5000", wantName: "a/b"},
		{ref: "my.registry:5000/ns/app", wantBase: "https://my.registry:5000", wantName: "ns/app"},
		{ref: "localhost/app", wantBase: "https://localhost", wantName: "app"},
		{ref: "docker.io/library/nginx", wantBase: "https://registry-1.docker.io", wantName: "library/nginx"},
		{ref: "index.docker.io/nginx", wantBase: "https://registry-1.docker.io", wantName: "library/nginx"},
		{ref: "nginx", wantBase: "https://registry-1.docker.io", wantName: "library/nginx"},
		{ref: "myorg/app", wantBase: "https://registry-1.docker.io", wantName: "myorg/app"},
		{ref: "", wantErr: "empty"},
		{ref: "ghcr.io", wantErr: "must include image name"},
		{ref: "https://ghcr.io", wantErr: "must include image name"},
		{ref: "ftp://ghcr.io/a", wantErr: "unsupported scheme"},
		{ref: "ghcr.io/org/app:v1", wantErr: "must not include a tag"},
		{ref: "ghcr.io/org/app@sha256:abc", wantErr: "must not include credentials or a digest"},
		{ref: "https://user:pw@ghcr.io/org/app", wantErr: "must not include credentials or a digest"},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			base, name, err := parseRegistryRef(tt.ref)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), "pw@", "credentials must not be echoed")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantBase, base.String())
			assert.Equal(t, tt.wantName, name)
		})
	}
}

func TestParseChallenge(t *testing.T) {
	scheme, params := parseChallenge(
		`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:a/b:pull,push"`)
	assert.Equal(t, "Bearer", scheme)
	assert.Equal(t, map[string]string{
		"realm":   "https://ghcr.io/token",
		"service": "ghcr.io",
		"scope":   "repository:a/b:pull,push",
	}, params)

	scheme, params = parseChallenge(`Basic realm=registry`)
	assert.Equal(t, "Basic", scheme)
	assert.Equal(t, "registry", params["realm"])
}

func TestNextLink(t *testing.T) {
	assert.Equal(t, "/v2/a/tags/list?last=x&n=1", nextLink(`</v2/a/tags/list?last=x&n=1>; rel="next"`))
	assert.Equal(t, "/v2/a/tags/list?n=2", nextLink(`</first>; rel="prev", </v2/a/tags/list?n=2>; rel=next`))
	assert.Empty(t, nextLink(""))
	assert.Empty(t, nextLink(`</v2/a/tags/list>; rel="prev"`))
}

// TestWatchersHaveTimeouts verifies that neither watcher uses a client without
// a timeout, which would let a hung host block the reconcile worker (C05-steps-28).
func TestWatchersHaveTimeouts(t *testing.T) {
	oci := NewOCIWatcher("ghcr.io/a/b", "")
	require.NotNil(t, oci.httpClient)
	assert.Equal(t, defaultHTTPTimeout, oci.httpClient.Timeout)

	git := NewGitWatcher("https://example.com/a", "main", "")
	require.NotNil(t, git.httpClient)
	assert.Equal(t, defaultHTTPTimeout, git.httpClient.Timeout)
}

func TestRealmMustBeHTTPS(t *testing.T) {
	base, name, err := parseRegistryRef("ghcr.io/a/b")
	require.NoError(t, err)
	s := &registrySession{client: newHTTPClient(), base: base, name: name}
	err = s.fetchAnonymousToken(t.Context(), `Bearer realm="http://ghcr.io/token",service="ghcr.io"`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not https")
	assert.Empty(t, s.token)
}

func TestRedactURL(t *testing.T) {
	assert.Equal(t, "https://x:xxxxx@github.com/org/repo", redactURL("https://x:secret@github.com/org/repo"))
	assert.Equal(t, "https://github.com/org/repo", redactURL("https://github.com/org/repo"))
	assert.Equal(t, "<invalid URL>", redactURL("https://x:secret@github.com/%zz"))
}
