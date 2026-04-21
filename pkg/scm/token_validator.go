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
	"fmt"
	"net/http"
	"strings"
	"time"
)

// TokenScopeWarning describes a missing or insufficient token scope found during
// startup validation. It is a warning, not an error — the controller continues
// to run but will likely fail when it attempts the operation that needs the scope.
type TokenScopeWarning struct {
	// MissingScope is the required scope that is absent.
	MissingScope string

	// Consequence is a human-readable description of what will fail without this scope.
	Consequence string
}

// String returns a formatted warning message.
func (w TokenScopeWarning) String() string {
	return fmt.Sprintf("missing scope %q: %s", w.MissingScope, w.Consequence)
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

	httpClient := &http.Client{Timeout: 10 * time.Second}
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
	// For fine-grained PATs, this header may be absent; GitHub-Actions tokens also
	// use a different mechanism. We treat an absent header as "fine-grained PAT or
	// GitHub App token" and warn only if specific write endpoints are known to fail.
	rawScopes := resp.Header.Get("X-OAuth-Scopes")

	// Fine-grained PAT or GitHub App: no X-OAuth-Scopes header.
	// We cannot inspect their scopes from the /user endpoint; skip classic scope check.
	if rawScopes == "" {
		return nil, nil
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

	httpClient := &http.Client{Timeout: 10 * time.Second}
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

	// Parse JSON response for scopes.
	// We only need to check for "api" scope.
	body, _ := readBody(resp)
	if !strings.Contains(body, `"api"`) {
		return []TokenScopeWarning{
			{MissingScope: "api",
				Consequence: "cannot create merge requests or push branches. Add the 'api' scope to the GitLab personal access token. " +
					"Without this scope, promotions will fail when the open-pr step runs.",
			},
		}, nil
	}

	return nil, nil
}

// ValidateForgejoTokenScopes calls the Forgejo/Gitea /user endpoint to verify
// the token is valid. Forgejo uses fine-grained tokens and does not expose scope
// information in a queryable way from the /user endpoint; we verify only that the
// token is valid (200 OK from /user).
func ValidateForgejoTokenScopes(ctx context.Context, token, apiURL string) ([]TokenScopeWarning, error) {
	if token == "" {
		return []TokenScopeWarning{
			{MissingScope: "<token>", Consequence: "no Forgejo token configured — all SCM operations will fail"},
		}, nil
	}

	if apiURL == "" {
		return nil, nil // no API URL configured — skip check
	}
	apiURL = strings.TrimRight(apiURL, "/")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/api/v1/user", nil)
	if err != nil {
		return nil, fmt.Errorf("construct Forgejo /user request: %w", err)
	}
	req.Header.Set("Authorization", "token "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call Forgejo /user: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return []TokenScopeWarning{
			{MissingScope: "<valid token>", Consequence: "token rejected by Forgejo API (401) — token may be expired"},
		}, nil
	}

	return nil, nil
}

// readBody reads up to 4096 bytes from the response body as a string.
func readBody(resp *http.Response) (string, error) {
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n]), nil
}
