// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package renderjob

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLockNetwork (QA round 2 on #1515, H1): in the render process every
// HTTP request through net/http's defaults is refused, so a generator or
// transformer that fetches a URL (kustomize's remote loader) reaches
// nothing; go-git keeps only the transport of spec.git.url, and its dialer
// reaches only that host and port, through the egress guard.
func TestLockNetwork(t *testing.T) {
	transport, client, protocols := http.DefaultTransport, http.DefaultClient, maps.Clone(gitclient.Protocols)
	t.Cleanup(func() {
		http.DefaultTransport, http.DefaultClient = transport, client
		for k := range gitclient.Protocols {
			delete(gitclient.Protocols, k)
		}
		maps.Copy(gitclient.Protocols, protocols)
	})
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("AKIA-stolen"))
	}))
	defer metadata.Close()

	require.NoError(t, LockNetwork("https://git.example.com/org/repo.git"))
	for _, get := range []func() (*http.Response, error){
		func() (*http.Response, error) { return http.Get(metadata.URL) },
		func() (*http.Response, error) {
			return http.DefaultClient.Get("http://169.254.169.254/latest/meta-data/")
		},
		func() (*http.Response, error) { return (&http.Client{}).Get(metadata.URL) },
	} {
		resp, err := get()
		if resp != nil {
			_ = resp.Body.Close()
		}
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNetworkRefused), "%v", err)
	}
	assert.Equal(t, []string{"https"}, keys(gitclient.Protocols), "only the git URL's transport is left")

	dial := gitTransport("git.example.com:443").DialContext
	_, err := dial(context.Background(), "tcp", "evil.example.com:443")
	assert.ErrorIs(t, err, ErrNetworkRefused, "another host")
	_, err = dial(context.Background(), "tcp", "git.example.com:22")
	assert.ErrorIs(t, err, ErrNetworkRefused, "another port")
	_, err = gitTransport(metadata.Listener.Addr().String()).DialContext(context.Background(), "tcp", metadata.Listener.Addr().String())
	require.Error(t, err, "the git host itself goes through the egress guard: loopback is refused")
	assert.NotErrorIs(t, err, ErrNetworkRefused)

	_, _, err = gitHostPort("ssh://git@github.com/org/repo.git")
	assert.Error(t, err, "http(s) only")
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
