// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCredentialRedirects: with credentials attached, a redirect to another
// host is refused (Helm index, Git), except a registry blob download, which
// follows without the Authorization header; a redirect from https to http is
// always refused. Without credentials the watchers follow redirects as before.
func TestCredentialRedirects(t *testing.T) {
	var gotAuth []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("ok"))
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer origin.Close()
	get := func(c *http.Client, path string) error {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL+path, nil)
		require.NoError(t, err)
		req.SetBasicAuth("u", "secret")
		resp, err := c.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}

	err := get(credentialRedirects(origin.Client(), false), "/charts/index.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing a redirect to another host")
	assert.Empty(t, gotAuth, "nothing reached the other host")

	require.NoError(t, get(credentialRedirects(origin.Client(), true), "/v2/a/b/blobs/sha256:x"))
	assert.Equal(t, []string{""}, gotAuth, "a blob redirect follows without the credentials")
	require.Error(t, get(credentialRedirects(origin.Client(), true), "/v2/a/b/manifests/v1"),
		"only blob downloads may leave the registry host")

	require.NoError(t, get(origin.Client(), "/charts/index.yaml"), "without the policy redirects are followed")

	tlsOrigin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+r.URL.Path, http.StatusFound)
	}))
	defer tlsOrigin.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, tlsOrigin.URL+"/v2/a/b/blobs/x", nil)
	_, err = credentialRedirects(tlsOrigin.Client(), true).Do(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing a redirect from https to http")
}
