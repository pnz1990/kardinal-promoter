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
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

const bitbucketDefaultAPIURL = "https://api.bitbucket.org"

// BitbucketProvider implements SCMProvider against the Bitbucket Cloud REST API v2.0.
// Requests are authenticated with a Bearer token, so use a repository, project
// or workspace access token. App passwords need Basic auth and do not work here.
// All methods are safe for concurrent use.
type BitbucketProvider struct {
	// Token is the Bitbucket Cloud access token or app password.
	Token string

	// APIURL is the Bitbucket API base URL. Defaults to "https://api.bitbucket.org" if empty.
	APIURL string

	// WebhookSecret is the shared secret for validating incoming Bitbucket webhook payloads.
	// Bitbucket signs payloads with HMAC-SHA256 and sends the signature in
	// X-Hub-Signature as "sha256=<hex>".
	// Empty refuses every event (ErrNoWebhookSecret).
	WebhookSecret string

	// circuits guards all outbound API calls: one circuit per repository
	// owner and one for the token's rate limit (CircuitRegistry). A
	// DynamicProvider shares its registry with every provider it builds.
	circuits *CircuitRegistry

	client *http.Client
}

// NewBitbucketProvider constructs a BitbucketProvider with the given token and optional
// API URL override.
func NewBitbucketProvider(token, apiURL, webhookSecret string) *BitbucketProvider {
	if apiURL == "" {
		apiURL = bitbucketDefaultAPIURL
	}
	return &BitbucketProvider{
		Token:         token,
		APIURL:        strings.TrimRight(apiURL, "/"),
		WebhookSecret: webhookSecret,
		circuits:      NewCircuitRegistry(),
		client:        &http.Client{Timeout: providerHTTPTimeout, Transport: tracing.Transport(nil, false)},
	}
}

// splitBitbucketRepo splits "workspace/repo_slug" into two parts.
// Bitbucket Cloud uses workspace + repo_slug, not owner/repo like GitHub.
func splitBitbucketRepo(repo string) (workspace, repoSlug string, err error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("bitbucket: repo must be in workspace/repo_slug format, got %q", repo)
	}
	return parts[0], parts[1], nil
}

// OpenPR creates a Bitbucket pull request and returns the PR URL and PR ID.
// It is idempotent: if a PR already exists for the source branch, returns the existing PR.
func (b *BitbucketProvider) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return "", 0, err
	}

	payload := map[string]interface{}{
		"title":       title,
		"description": body,
		"source": map[string]interface{}{
			"branch": map[string]string{"name": head},
		},
		"destination": map[string]interface{}{
			"branch": map[string]string{"name": base},
		},
		"close_source_branch": false,
	}

	var result struct {
		ID    int `json:"id"`
		Links struct {
			HTML struct {
				Href string `json:"href"`
			} `json:"html"`
		} `json:"links"`
	}

	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests", workspace, repoSlug)
	if err := b.do(ctx, http.MethodPost, path, payload, &result); err != nil {
		// Bitbucket returns 400 with "There are already open pull requests" when a PR exists.
		if isBitbucketExistingPRErr(err) {
			return b.findExistingPR(ctx, workspace, repoSlug, head, base)
		}
		return "", 0, fmt.Errorf("open Bitbucket PR %s: %w", repo, err)
	}
	return result.Links.HTML.Href, result.ID, nil
}

// bitbucketPRBranch is the source or destination of a Bitbucket pullrequest.
type bitbucketPRBranch struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
}

// findExistingPR finds the open PR from sourceBranch into destBranch. A PR
// from the same branch into another destination is not the one Bitbucket
// refused to duplicate.
func (b *BitbucketProvider) findExistingPR(ctx context.Context, workspace, repoSlug, sourceBranch, destBranch string) (string, int, error) {
	var result struct {
		Values []struct {
			ID          int               `json:"id"`
			State       string            `json:"state"`
			Source      bitbucketPRBranch `json:"source"`
			Destination bitbucketPRBranch `json:"destination"`
			Links       struct {
				HTML struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
		} `json:"values"`
	}
	// Filter on the server by source and destination branch (C06-scm-health-15).
	quote := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
	q := fmt.Sprintf(`source.branch.name="%s" AND destination.branch.name="%s" AND state="OPEN"`,
		quote(sourceBranch), quote(destBranch))
	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests?pagelen=50&q=%s", workspace, repoSlug, url.QueryEscape(q))
	if err := b.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return "", 0, fmt.Errorf("list Bitbucket PRs for %s/%s: %w", workspace, repoSlug, err)
	}
	for _, pr := range result.Values {
		if pr.Source.Branch.Name == sourceBranch && pr.Destination.Branch.Name == destBranch {
			return pr.Links.HTML.Href, pr.ID, nil
		}
	}
	return "", 0, fmt.Errorf("bitbucket PR exists for branch %s into %s but could not find it in open PRs", sourceBranch, destBranch)
}

// isBitbucketExistingPRErr returns true when Bitbucket rejected PR creation because
// one already exists for the source branch.
func isBitbucketExistingPRErr(err error) bool {
	if err == nil {
		return false
	}
	// Only the duplicate message: any other 400 (missing branch, validation
	// error) must surface as the real error (C06-scm-health-14).
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already") && strings.Contains(msg, "pull request")
}

// ClosePR declines (closes) the pull request without merging.
func (b *BitbucketProvider) ClosePR(ctx context.Context, repo string, prNumber int) error {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d/decline", workspace, repoSlug, prNumber)
	if err := b.do(ctx, http.MethodPost, path, nil, nil); err != nil {
		return fmt.Errorf("close Bitbucket PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// CommentOnPR posts a comment on the pull request.
func (b *BitbucketProvider) CommentOnPR(ctx context.Context, repo string, prNumber int, body string) error {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return err
	}
	payload := map[string]interface{}{
		"content": map[string]string{"raw": body},
	}
	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d/comments", workspace, repoSlug, prNumber)
	if err := b.do(ctx, http.MethodPost, path, payload, nil); err != nil {
		return fmt.Errorf("comment on Bitbucket PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// GetPRStatus returns whether the pull request has been merged and whether it is open.
func (b *BitbucketProvider) GetPRStatus(ctx context.Context, repo string, prNumber int) (bool, bool, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return false, false, err
	}
	var result struct {
		State string `json:"state"`
	}
	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d", workspace, repoSlug, prNumber)
	if err := b.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return false, false, fmt.Errorf("get Bitbucket PR status %s#%d: %w", repo, prNumber, err)
	}
	// Bitbucket states: OPEN, MERGED, DECLINED, SUPERSEDED
	merged := result.State == "MERGED"
	open := result.State == "OPEN"
	return merged, open, nil
}

// GetPRReviewStatus returns review approval state for a Bitbucket pull request.
// approved is true when at least one reviewer has approved and no reviewer has
// requested changes.
func (b *BitbucketProvider) GetPRReviewStatus(ctx context.Context, repo string, prNumber int) (bool, int, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return false, 0, err
	}
	var result struct {
		Participants []struct {
			Role     string `json:"role"`
			Approved bool   `json:"approved"`
			State    string `json:"state"`
		} `json:"participants"`
	}
	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d", workspace, repoSlug, prNumber)
	if err := b.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return false, 0, fmt.Errorf("get Bitbucket PR reviewers %s#%d: %w", repo, prNumber, err)
	}
	approveCount, changesRequested := 0, false
	for _, p := range result.Participants {
		if p.State == "changes_requested" {
			changesRequested = true
		}
		if p.Approved {
			approveCount++
		}
	}
	return approveCount > 0 && !changesRequested, approveCount, nil
}

// ParseWebhookEvent parses a Bitbucket webhook payload and validates the
// X-Hub-Signature HMAC-SHA256 header. The caller must pass the value of the
// X-Hub-Signature header as signature.
func (b *BitbucketProvider) ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error) {
	if b.WebhookSecret == "" {
		return WebhookEvent{}, ErrNoWebhookSecret
	}
	mac := hmac.New(sha256.New, []byte(b.WebhookSecret))
	mac.Write(payload)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return WebhookEvent{}, fmt.Errorf("bitbucket webhook: invalid HMAC-SHA256 signature")
	}

	// Bitbucket Cloud webhooks use pullrequest:fulfilled (merged), pullrequest:rejected (declined)
	// pullrequest:created (opened), pullrequest:updated, etc.
	var raw struct {
		// Bitbucket sends the event key in X-Event-Key header; the JSON body uses
		// {"pullrequest": {...}} structure.
		PullRequest struct {
			ID          int    `json:"id"`
			State       string `json:"state"`
			Destination struct {
				Repository struct {
					FullName string `json:"full_name"`
				} `json:"repository"`
			} `json:"destination"`
		} `json:"pullrequest"`
		// Repository is the repository the webhook belongs to: the PR's
		// destination, never the fork it comes from.
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse Bitbucket webhook payload: %w", err)
	}

	pr := raw.PullRequest
	repo := raw.Repository.FullName
	if repo == "" {
		repo = pr.Destination.Repository.FullName
	}
	if pr.State == "MERGED" {
		return mergedPREvent(repo, pr.ID), nil
	}
	// Bitbucket doesn't embed an "action" in the body; report the state.
	return WebhookEvent{
		EventType:    "pullrequest",
		PRNumber:     pr.ID,
		RepoFullName: repo,
		Action:       strings.ToLower(pr.State),
	}, nil
}

// AddLabelsToPR is a no-op for Bitbucket Cloud — Bitbucket does not support PR labels.
// Labels applied to Bitbucket PRs are not visible to reviewers; this method returns
// nil to avoid breaking the promotion step.
func (b *BitbucketProvider) AddLabelsToPR(_ context.Context, _ string, _ int, _ []string) error {
	return nil
}

// do executes an authenticated Bitbucket API request using Bearer token auth.
func (b *BitbucketProvider) do(ctx context.Context, method, path string, body, result interface{}) error {
	owner := ownerFromPath(path, "/2.0/repositories/")
	call := startSCMCall("bitbucket", owner, method, path)
	if err := b.circuits.Allow(owner); err != nil {
		call.circuitOpen(b.circuits, owner)
		return fmt.Errorf("bitbucket scm: %w", err)
	}
	// When the call started: a failure of a call that started before the
	// circuit opened is not counted (CircuitBreaker.RecordFailureFrom).
	started := time.Now()

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, b.APIURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.client.Do(req)
	// Arguments are taken now; the circuit states are read at return, after Record.
	defer call.done(resp, err, b.circuits, owner)
	if err != nil {
		b.circuits.Record(owner, started, nil, err)
		return fmt.Errorf("execute request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		apiErr := newAPIError("bitbucket", method, path, resp, raw)
		b.circuits.RecordAPIError(owner, started, resp, apiErr)
		return apiErr
	}

	b.circuits.Record(owner, started, resp, nil)
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
