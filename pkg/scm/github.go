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

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// GitHubProvider implements SCMProvider against the GitHub REST API.
// Requests are authenticated with a personal access token.
type GitHubProvider struct {
	// Token is the GitHub personal access token or fine-grained PAT.
	Token string

	// APIURL is the GitHub API base URL. Defaults to "https://api.github.com" if empty.
	APIURL string

	// WebhookSecret is the HMAC secret for validating incoming webhook payloads.
	// Empty refuses every event (ErrNoWebhookSecret).
	WebhookSecret string

	// circuits guards all outbound API calls: one circuit per repository
	// owner and one for the token's rate limit (CircuitRegistry). A
	// DynamicProvider shares its registry with every provider it builds.
	circuits *CircuitRegistry

	client *http.Client
}

// NewGitHubProvider constructs a GitHubProvider with the given token and optional
// API URL override (for GitHub Enterprise).
func NewGitHubProvider(token, apiURL, webhookSecret string) *GitHubProvider {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	return &GitHubProvider{
		Token:         token,
		APIURL:        strings.TrimRight(apiURL, "/"),
		WebhookSecret: webhookSecret,
		circuits:      NewCircuitRegistry(),
		client:        &http.Client{Timeout: providerHTTPTimeout, Transport: tracing.Transport(nil, false)},
	}
}

// OpenPR creates a pull request and returns the PR URL and number.
// It is idempotent: if an open PR from head into base already exists, it
// returns that PR's URL and number rather than failing with 422.
func (g *GitHubProvider) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	payload := map[string]string{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}
	var result struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls", repo), payload, &result); err != nil {
		// GitHub returns 422 when an open PR from head into base already
		// exists. In that case, find that PR and return it.
		if isExistingPRErr(err) {
			return g.findExistingPR(ctx, repo, head, base)
		}
		return "", 0, fmt.Errorf("open PR %s: %w", repo, err)
	}
	return result.HTMLURL, result.Number, nil
}

// findExistingPR finds the open PR from head into base. GitHub refuses a
// duplicate only for the same head and base, so an open PR from head into
// another branch is not the one it refused and must not be reused.
func (g *GitHubProvider) findExistingPR(ctx context.Context, repo, head, base string) (string, int, error) {
	var prs []struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	// Filter on the server by head and base branch: in a busy repository the
	// PR may not be on the first page of all open PRs (C06-scm-health-15).
	owner, _, _ := strings.Cut(repo, "/")
	q := url.Values{"state": {"open"}, "per_page": {"100"}, "head": {owner + ":" + head}, "base": {base}}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls?%s", repo, q.Encode()), nil, &prs); err != nil {
		return "", 0, fmt.Errorf("list PRs to find existing %s: %w", head, err)
	}
	for _, pr := range prs {
		if pr.Head.Ref == head && pr.Base.Ref == base {
			return pr.HTMLURL, pr.Number, nil
		}
	}
	return "", 0, fmt.Errorf("PR already exists for %s into %s but could not find it in open PRs", head, base)
}

// isExistingPRErr returns true when the GitHub API rejected the PR creation with 422
// because a pull request already exists for the head branch.
func isExistingPRErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "422") && (strings.Contains(msg, "already exists") || strings.Contains(msg, "A pull request"))
}

// ClosePR closes the pull request without merging.
func (g *GitHubProvider) ClosePR(ctx context.Context, repo string, prNumber int) error {
	payload := map[string]string{"state": "closed"}
	if err := g.do(ctx, http.MethodPatch,
		fmt.Sprintf("/repos/%s/pulls/%d", repo, prNumber), payload, nil); err != nil {
		return fmt.Errorf("close PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// CommentOnPR posts a comment on the pull request.
func (g *GitHubProvider) CommentOnPR(ctx context.Context, repo string, prNumber int, body string) error {
	payload := map[string]string{"body": body}
	if err := g.do(ctx, http.MethodPost,
		fmt.Sprintf("/repos/%s/issues/%d/comments", repo, prNumber), payload, nil); err != nil {
		return fmt.Errorf("comment on PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// GetPRStatus returns whether the PR has been merged and whether it is still open.
func (g *GitHubProvider) GetPRStatus(ctx context.Context, repo string, prNumber int) (bool, bool, error) {
	var result struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/pulls/%d", repo, prNumber), nil, &result); err != nil {
		return false, false, fmt.Errorf("get PR status %s#%d: %w", repo, prNumber, err)
	}
	return result.Merged, result.State == "open", nil
}

// GetPRReviewStatus returns review approval state for a pull request.
// approved is true when at least one approving review exists and no change-request
// review is outstanding. approvalCount counts distinct approving reviewers.
//
// The GitHub Reviews API returns all reviews in submission order; we process
// them chronologically so the last review from each user wins.
func (g *GitHubProvider) GetPRReviewStatus(ctx context.Context, repo string, prNumber int) (bool, int, error) {
	type review struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State string `json:"state"`
	}
	// Read every page: a CHANGES_REQUESTED after the first page must still
	// block (C06-scm-health-16).
	var reviews []review
	for page := 1; page <= maxListPages; page++ {
		var batch []review
		if err := g.do(ctx, http.MethodGet,
			fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=%d&page=%d", repo, prNumber, githubPageSize, page), nil, &batch); err != nil {
			return false, 0, fmt.Errorf("get PR reviews %s#%d: %w", repo, prNumber, err)
		}
		reviews = append(reviews, batch...)
		if len(batch) < githubPageSize {
			break
		}
	}

	// Track the most recent state per reviewer login.
	latestByUser := make(map[string]string, len(reviews))
	for _, r := range reviews {
		if r.User.Login == "" {
			continue
		}
		// Only APPROVED and CHANGES_REQUESTED affect the approval decision.
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED":
			latestByUser[r.User.Login] = r.State
		}
	}

	approvalCount := 0
	hasChangeRequest := false
	for _, state := range latestByUser {
		switch state {
		case "APPROVED":
			approvalCount++
		case "CHANGES_REQUESTED":
			hasChangeRequest = true
		}
	}

	approved := approvalCount > 0 && !hasChangeRequest
	return approved, approvalCount, nil
}

// ParseWebhookEvent parses a GitHub webhook payload and validates the HMAC-SHA256 signature.
func (g *GitHubProvider) ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error) {
	return g.parseWebhookEvent(payload, signature, "")
}

// parseWebhookEvent parses a GitHub webhook. eventType is the X-GitHub-Event
// header ("pull_request", "push", "issue_comment", "ping", ...); the payload
// does not name its event. Only a "pull_request" event reports Merged.
// Without the header the payload is read as a pull_request event.
func (g *GitHubProvider) parseWebhookEvent(payload []byte, signature, eventType string) (WebhookEvent, error) {
	if g.WebhookSecret == "" {
		return WebhookEvent{}, ErrNoWebhookSecret
	}
	if err := g.validateSignature(payload, signature); err != nil {
		return WebhookEvent{}, fmt.Errorf("webhook signature invalid: %w", err)
	}

	var raw struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number         int    `json:"number"`
			Merged         bool   `json:"merged"`
			HTMLURL        string `json:"html_url"`
			MergeCommitSHA string `json:"merge_commit_sha"`
		} `json:"pull_request"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse webhook payload: %w", err)
	}

	if eventType == "" {
		eventType = "pull_request"
	}
	event := WebhookEvent{
		EventType:    eventType,
		PRNumber:     raw.PullRequest.Number,
		RepoFullName: raw.Repository.FullName,
		Merged:       eventType == "pull_request" && raw.PullRequest.Merged,
		Action:       raw.Action,
	}
	if event.Merged {
		// Before the merge it is the test merge commit, not the promoted one.
		event.MergeCommitSHA = raw.PullRequest.MergeCommitSHA
	}
	return event, nil
}

// validateSignature checks that the payload matches the HMAC-SHA256 signature.
func (g *GitHubProvider) validateSignature(payload []byte, signature string) error {
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return fmt.Errorf("signature missing sha256= prefix")
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return fmt.Errorf("decode signature hex: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(g.WebhookSecret))
	if _, err := mac.Write(payload); err != nil {
		return fmt.Errorf("compute HMAC: %w", err)
	}
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// AddLabelsToPR applies labels to the pull request. GitHub creates labels that
// do not exist yet in the repository.
func (g *GitHubProvider) AddLabelsToPR(ctx context.Context, repo string, prNumber int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	payload := map[string][]string{"labels": labels}
	if err := g.do(ctx, http.MethodPost,
		fmt.Sprintf("/repos/%s/issues/%d/labels", repo, prNumber), payload, nil); err != nil {
		return fmt.Errorf("add labels to PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// do executes an authenticated GitHub API request.
func (g *GitHubProvider) do(ctx context.Context, method, path string, body, result interface{}) error {
	// Check circuit breaker before making the call.
	owner := ownerFromPath(path, "/repos/")
	if err := g.circuits.Allow(owner); err != nil {
		return fmt.Errorf("github scm: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, g.APIURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.client.Do(req)
	if err != nil {
		// Network error — record as failure with no retry-after hint.
		g.circuits.Record(owner, nil, err)
		return fmt.Errorf("execute request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		g.circuits.Record(owner, resp, nil)
		return newAPIError("GitHub", method, path, resp, raw)
	}

	g.circuits.Record(owner, resp, nil)
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
