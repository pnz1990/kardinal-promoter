// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError is an error response (status 400 or higher) from an SCM
// provider's REST API. Every provider's request helper returns one, wrapped
// by the calling method, so callers can tell a rejected token or a missing
// repository from an outage with errors.As or IsPermanentError.
type APIError struct {
	// Provider names the API in the message: "GitHub", "GitLab", "forgejo",
	// "bitbucket" or "azuredevops".
	Provider   string
	Method     string
	Path       string
	StatusCode int
	// Body is the response body, as the provider sent it.
	Body string
	// Transient is true for a response that is expected to succeed on retry:
	// 429, a 5xx, or a 403 that is a rate limit.
	Transient bool
}

// newAPIError builds the APIError for resp, whose body has been read into raw.
// The rate-limit headers decide first (IsTransientResponse); only for GitHub
// (and GitHub Enterprise Server, the same provider), whose secondary rate
// limit can come as a 403 with neither header, the JSON message decides
// next.
func newAPIError(provider, method, path string, resp *http.Response, raw []byte) *APIError {
	body := string(raw)
	return &APIError{
		Provider:   provider,
		Method:     method,
		Path:       path,
		StatusCode: resp.StatusCode,
		Body:       body,
		Transient: IsTransientResponse(resp) ||
			(provider == githubAPIName && resp.StatusCode == http.StatusForbidden && isGitHubRateLimitMessage(raw)),
	}
}

// githubAPIName is the Provider of GitHub's APIErrors.
const githubAPIName = "GitHub"

// Error keeps the message format the providers have always used.
func (e *APIError) Error() string {
	return fmt.Sprintf("%s API %s %s: status %d: %s", e.Provider, e.Method, e.Path, e.StatusCode, e.Body)
}

// isGitHubRateLimitMessage reports whether a GitHub 403 body is a JSON error
// whose message is a rate limit ("You have exceeded a secondary rate
// limit", "API rate limit exceeded", abuse detection). Text elsewhere in the
// body, or a body that is not JSON, does not count: a permission error that
// quotes the words must stay permanent.
func isGitHubRateLimitMessage(raw []byte) bool {
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return false
	}
	m := strings.ToLower(e.Message)
	return strings.Contains(m, "rate limit") || strings.Contains(m, "abuse detection")
}

// IsPermanentError reports whether err carries an SCM API response that
// retrying the same request cannot fix: the token was rejected (401), the
// token lacks access (403 that is not a rate limit), or the repository or
// pull request does not exist or is not visible to the token (404, 410).
// A call the repository allowlist refused (ErrRepositoryNotAllowed) is
// permanent too.
//
// Network errors, timeouts, an open circuit breaker, 429 and 5xx responses
// are not permanent.
func IsPermanentError(err error) bool {
	if errors.Is(err, ErrRepositoryNotAllowed) {
		return true // a call the allowlist refused (#1332)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Transient {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return true
	}
	return false
}
