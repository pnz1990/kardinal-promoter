// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestIsPermanentError drives every provider's GetPRStatus against a server
// that answers with one status and checks which responses are permanent: a
// rejected token, a token without access, or a missing repository or pull
// request. Rate limits, 5xx and network errors must stay retryable, and the
// error message format must not change.
func TestIsPermanentError(t *testing.T) {
	providers := []struct {
		name   string
		prefix string
		repo   string
		newFn  func(apiURL string) scm.SCMProvider
	}{
		{"github", "GitHub", "o/r", func(u string) scm.SCMProvider { return scm.NewGitHubProvider("t", u, "") }},
		{"gitlab", "GitLab", "g/p", func(u string) scm.SCMProvider { return scm.NewGitLabProvider("t", u, "") }},
		{"forgejo", "forgejo", "o/r", func(u string) scm.SCMProvider { return scm.NewForgejoProvider("t", u, "") }},
		{"bitbucket", "bitbucket", "ws/r", func(u string) scm.SCMProvider { return scm.NewBitbucketProvider("t", u, "") }},
		{"azuredevops", "azuredevops", "org/proj/r", func(u string) scm.SCMProvider { return scm.NewAzureDevOpsProvider("t", u, "") }},
	}
	responses := []struct {
		name          string
		status        int
		header        map[string]string
		body          string
		wantPermanent bool
	}{
		{name: "401 token rejected", status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`, wantPermanent: true},
		{name: "403 token lacks access", status: http.StatusForbidden, body: `{"message":"Resource not accessible by integration"}`, wantPermanent: true},
		{name: "403 primary rate limit", status: http.StatusForbidden, header: map[string]string{"X-RateLimit-Remaining": "0"}, body: `{}`},
		{name: "403 with Retry-After", status: http.StatusForbidden, header: map[string]string{"Retry-After": "60"}, body: `{}`},
		{name: "403 secondary rate limit without headers", status: http.StatusForbidden, body: `{"message":"You have exceeded a secondary rate limit."}`},
		{name: "404 not found", status: http.StatusNotFound, body: `{"message":"Not Found"}`, wantPermanent: true},
		{name: "410 gone", status: http.StatusGone, body: `{}`, wantPermanent: true},
		{name: "409 conflict", status: http.StatusConflict, body: `{}`},
		{name: "429 too many requests", status: http.StatusTooManyRequests, body: `{}`},
		{name: "500", status: http.StatusInternalServerError, body: `oops`},
		{name: "502", status: http.StatusBadGateway, body: `bad gateway`},
	}
	for _, p := range providers {
		for _, r := range responses {
			t.Run(p.name+"/"+r.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					for k, v := range r.header {
						w.Header().Set(k, v)
					}
					w.WriteHeader(r.status)
					_, _ = w.Write([]byte(r.body))
				}))
				defer srv.Close()

				_, _, err := p.newFn(srv.URL).GetPRStatus(context.Background(), p.repo, 7)
				require.Error(t, err)
				assert.Equal(t, r.wantPermanent, scm.IsPermanentError(err), "error: %v", err)

				var apiErr *scm.APIError
				require.True(t, errors.As(err, &apiErr), "GetPRStatus must wrap an *scm.APIError: %v", err)
				assert.Equal(t, r.status, apiErr.StatusCode)
				assert.Contains(t, err.Error(), p.prefix+" API GET ")
				assert.Contains(t, err.Error(), ": status "+strconv.Itoa(r.status)+": "+r.body)
			})
		}
		t.Run(p.name+"/network error", func(t *testing.T) {
			srv := httptest.NewServer(http.NotFoundHandler())
			url := srv.URL
			srv.Close()
			_, _, err := p.newFn(url).GetPRStatus(context.Background(), p.repo, 7)
			require.Error(t, err)
			assert.False(t, scm.IsPermanentError(err), "error: %v", err)
		})
	}

	t.Run("open circuit breaker", func(t *testing.T) {
		err := fmt.Errorf("github scm: %w", &scm.ErrCircuitOpen{RetryAfter: time.Now().Add(time.Minute)})
		assert.False(t, scm.IsPermanentError(err))
	})
	t.Run("deadline exceeded", func(t *testing.T) {
		assert.False(t, scm.IsPermanentError(fmt.Errorf("execute request: %w", context.DeadlineExceeded)))
	})
	t.Run("nil", func(t *testing.T) {
		assert.False(t, scm.IsPermanentError(nil))
	})
}
