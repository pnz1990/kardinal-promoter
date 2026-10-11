// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package source

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingTransport fails every request with an error that, like a dial
// error, does not mention the URL: net/http's *url.Error adds it. It records
// the request it saw.
type failingTransport struct{ seen *http.Request }

func (f *failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.seen = r
	return nil, errors.New("connection refused")
}

// TestWatchers_URLCredentialsNotInErrors (#1721 QA): a repoURL whose user
// name is a token ("https://ghp_SECRET@host/...") is never quoted in a
// watcher's error, which becomes the Subscription status, its Ready condition
// and a log line. The git watcher sends the URL's credentials as basic auth
// and not in the request URL.
func TestWatchers_URLCredentialsNotInErrors(t *testing.T) {
	ctx := context.Background()
	for _, repo := range []string{"https://ghp_SECRET@git.example/o/r", "https://u:ghp_SECRET@git.example/o/r"} {
		tr := &failingTransport{}
		w := &GitWatcher{RepoURL: repo}
		w.WithHTTPClient(&http.Client{Transport: tr})
		_, err := w.Watch(ctx, "")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "SECRET", repo)
		require.NotNil(t, tr.seen)
		assert.Nil(t, tr.seen.URL.User, "no userinfo in the request URL")
		_, pass, ok := tr.seen.BasicAuth()
		assert.True(t, ok, "the URL's credentials are sent as basic auth")
		if repo == "https://u:ghp_SECRET@git.example/o/r" {
			assert.Equal(t, "ghp_SECRET", pass)
		}
	}

	// The helm watcher refuses credentials in repoURL; an HTTP error that
	// quotes a URL with a token in its query (a redirect) is redacted too.
	h := &HelmWatcher{RepoURL: "https://charts.example/repo", Chart: "app",
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New(`Get "https://ghp_SECRET@charts.example/index.yaml?access_token=ghp_SECRET": refused`)
		})}}
	_, err := h.Watch(ctx, "")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
