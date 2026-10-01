// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// maxTokenInfoBytes caps how much of a token introspection response is read.
const maxTokenInfoBytes = 1 << 20

// tokenCheckTimeout bounds one token check request. cmd/kardinal-controller
// also bounds the whole check through its context.
const tokenCheckTimeout = 10 * time.Second

// newTokenCheckClient returns the HTTP client for one startup token check and
// the function that releases what the check opened, to call once the response
// body is closed.
//
// The client has a Transport of its own, cloned from http.DefaultTransport so
// that the proxy and TLS settings stay the same, with keep-alives disabled.
// go-git's HTTP transport, like every *http.Client without a Transport, pools
// its connections in http.DefaultTransport. A check through that pool left its
// connection idle there whenever net/http could reuse it, and the controller's
// first clone or push to the same host reused it: after a response read to its
// end or a bodiless 401, 403 or 404 on every Go version, and since Go 1.27
// after any response body of up to 256 KiB, which Close now drains (Go 1.26
// closed the connection). A CNI that never re-checks an established
// connection (kindnet) then let git traffic through a NetworkPolicy without
// git egress until the pod restarted, because the check runs before the
// policy is enforced for the new pod (B78).
//
// DisableKeepAlives makes the request carry Connection: close, so the
// connection never enters a pool and the server closes it after the response.
// CloseIdleConnections then drops anything the transport could still hold,
// such as an HTTP/2 connection the server has not closed yet. Both are cheap:
// the check sends one request.
func newTokenCheckClient() (client *http.Client, release func()) {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		tr = tr.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	tr.DisableKeepAlives = true
	return &http.Client{Timeout: tokenCheckTimeout, Transport: tr}, tr.CloseIdleConnections
}

// TokenScopeWarning describes a missing or insufficient token scope found during
// startup validation. It is a warning, not an error — the controller continues
// to run but will likely fail when it attempts the operation that needs the scope.
type TokenScopeWarning struct {
	// MissingScope is the required scope that is absent.
	MissingScope string

	// Consequence is a human-readable description of what will fail without this scope.
	Consequence string
}

// ValidateGitHubTokenScopes calls the GitHub /user endpoint and inspects the
// X-OAuth-Scopes response header to verify that the token has the scopes required
// for kardinal-promoter to open and manage pull requests.
//
// Required scopes:
//   - repo   (classic PAT) OR contents:write + pull_requests:write (fine-grained PAT)
//
// Returns a list of warnings if required scopes are absent. An empty list means
// the token appears correctly scoped. Errors (network, 401, etc.) are returned
// directly — the caller should log these as warnings, not fatal errors, since
// a transient network issue at startup should not prevent the controller from starting.
//
// This is a startup preflight check only — it is NOT called in the reconciler hot path.
func ValidateGitHubTokenScopes(ctx context.Context, token, apiURL string) ([]TokenScopeWarning, error) {
	if token == "" {
		return []TokenScopeWarning{
			{MissingScope: "<token>", Consequence: "no GitHub token configured — all SCM operations will fail with 401"},
		}, nil
	}

	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	apiURL = strings.TrimRight(apiURL, "/")

	httpClient, release := newTokenCheckClient()
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/user", nil)
	if err != nil {
		return nil, fmt.Errorf("construct /user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call GitHub /user: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return []TokenScopeWarning{
			{MissingScope: "<valid token>", Consequence: "token rejected by GitHub API (401) — token may be expired or malformed"},
		}, nil
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GitHub /user returned HTTP %d — cannot validate scopes", resp.StatusCode)
	}

	// X-OAuth-Scopes header: comma-separated list of classic PAT scopes.
	// Fine-grained PATs and GitHub App tokens do not send it, and their
	// permissions cannot be read from /user, so say that the scopes were not
	// checked instead of passing silently.
	rawScopes := resp.Header.Get("X-OAuth-Scopes")
	if rawScopes == "" {
		return []TokenScopeWarning{{
			MissingScope: "<unverified>",
			Consequence: "cannot verify the scopes of a fine-grained PAT or GitHub App token; " +
				"make sure it has contents:write and pull_requests:write on the GitOps repository",
		}}, nil
	}

	// Classic PAT: parse scopes.
	scopes := make(map[string]struct{})
	for _, s := range strings.Split(rawScopes, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			scopes[s] = struct{}{}
		}
	}

	var warnings []TokenScopeWarning

	// "repo" covers all repository operations including PR creation and branch push.
	// "public_repo" covers public repositories only.
	_, hasRepo := scopes["repo"]
	_, hasPublicRepo := scopes["public_repo"]

	if !hasRepo && !hasPublicRepo {
		warnings = append(warnings, TokenScopeWarning{
			MissingScope: "repo",
			Consequence: "cannot open pull requests or push branches. Add the 'repo' scope to the GitHub PAT " +
				"(or 'public_repo' for public-repository-only pipelines). " +
				"Without this scope, promotions will fail when the open-pr step runs.",
		})
	}

	return warnings, nil
}

// ValidateGitLabTokenScopes calls the GitLab /personal_access_tokens/self endpoint
// (or /oauth/token/info for OAuth tokens) to inspect the token's scopes.
//
// Required scopes for kardinal-promoter: api (covers all REST operations including MR creation).
//
// Returns warnings for missing scopes. Errors indicate transient failures.
func ValidateGitLabTokenScopes(ctx context.Context, token, apiURL string) ([]TokenScopeWarning, error) {
	if token == "" {
		return []TokenScopeWarning{
			{MissingScope: "<token>", Consequence: "no GitLab token configured — all SCM operations will fail"},
		}, nil
	}

	if apiURL == "" {
		apiURL = "https://gitlab.com"
	}
	apiURL = strings.TrimRight(apiURL, "/")

	httpClient, release := newTokenCheckClient()
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/api/v4/personal_access_tokens/self", nil)
	if err != nil {
		return nil, fmt.Errorf("construct GitLab token introspection request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call GitLab token introspection: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return []TokenScopeWarning{
			{MissingScope: "<valid token>", Consequence: "token rejected by GitLab API (401) — token may be expired"},
		}, nil
	}
	// GitLab returns 404 for OAuth tokens (no personal_access_tokens/self endpoint).
	// Treat this as unknown — skip scope check.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GitLab token introspection returned HTTP %d", resp.StatusCode)
	}

	// The token's own name is in the same response, so match the scopes
	// list, not the raw body.
	var info struct {
		Scopes []string `json:"scopes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTokenInfoBytes)).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode GitLab token introspection: %w", err)
	}
	if !slices.Contains(info.Scopes, "api") {
		return []TokenScopeWarning{
			{MissingScope: "api",
				Consequence: "cannot create merge requests or push branches. Add the 'api' scope to the GitLab personal access token. " +
					"Without this scope, promotions will fail when the open-pr step runs.",
			},
		}, nil
	}

	return nil, nil
}

// ErrTokenScopesNotChecked is wrapped by a validator that reached the SCM but
// could not see the token's scopes. The token is not known to be bad, so the
// caller should log it at info level, not as a warning or a network error.
var ErrTokenScopesNotChecked = errors.New("token scopes not checked")

// ValidateForgejoTokenScopes checks a Forgejo token. See
// validateGiteaFamilyTokenScopes.
func ValidateForgejoTokenScopes(ctx context.Context, token, apiURL string) ([]TokenScopeWarning, error) {
	return validateGiteaFamilyTokenScopes(ctx, "Forgejo", token, apiURL)
}

// ValidateGiteaTokenScopes checks a Gitea token. See
// validateGiteaFamilyTokenScopes.
func ValidateGiteaTokenScopes(ctx context.Context, token, apiURL string) ([]TokenScopeWarning, error) {
	return validateGiteaFamilyTokenScopes(ctx, "Gitea", token, apiURL)
}

// validateGiteaFamilyTokenScopes calls the Forgejo/Gitea /api/v1/user endpoint.
// name ("Forgejo" or "Gitea") is used in the messages. These SCMs do not expose
// a token's scopes, so the call shows only whether the token is accepted:
//   - 401: the token is rejected, which is a warning.
//   - 403: the token is accepted but lacks read:user, which /user needs. The
//     documented scopes (write:repository, write:issue) do not include it, so
//     this is the normal answer. It wraps ErrTokenScopesNotChecked.
//   - 200: the token is accepted. Its scopes are still not known.
//
// No repository is probed instead: none is configured at startup (each Pipeline
// names its own), and a repository read needs only read:repository, so it
// would not show write:issue either.
func validateGiteaFamilyTokenScopes(ctx context.Context, name, token, apiURL string) ([]TokenScopeWarning, error) {
	if token == "" {
		return []TokenScopeWarning{
			{MissingScope: "<token>", Consequence: "no " + name + " token configured — all SCM operations will fail"},
		}, nil
	}

	if apiURL == "" {
		return nil, nil // no API URL configured — skip check
	}
	apiURL = strings.TrimRight(apiURL, "/")

	httpClient, release := newTokenCheckClient()
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/api/v1/user", nil)
	if err != nil {
		return nil, fmt.Errorf("construct %s /user request: %w", name, err)
	}
	req.Header.Set("Authorization", "token "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s /user: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return []TokenScopeWarning{
			{MissingScope: "<valid token>", Consequence: "token rejected by the " + name + " API (401) — token may be expired"},
		}, nil
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: /user returned 403 (the documented scopes don't include read:user)", ErrTokenScopesNotChecked)
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("%s /user returned HTTP %d — cannot validate token", name, resp.StatusCode)
	}

	return nil, nil
}
