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
	"strings"
)

// GitHubProvider implements SCMProvider against the GitHub REST API.
// Requests are authenticated with a personal access token.
type GitHubProvider struct {
	// Token is the GitHub personal access token or fine-grained PAT.
	Token string

	// APIURL is the GitHub API base URL. Defaults to "https://api.github.com" if empty.
	APIURL string

	// WebhookSecret is the HMAC secret for validating incoming webhook payloads.
	WebhookSecret string

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
		client:        &http.Client{},
	}
}

// OpenPR creates a pull request and returns the PR URL and number.
// It is idempotent: if a PR already exists for the head branch, it returns the
// existing PR's URL and number rather than failing with 422.
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
		// GitHub returns 422 when a PR for this head branch already exists.
		// In that case, find the existing open PR and return it.
		if isExistingPRErr(err) {
			return g.findExistingPR(ctx, repo, head)
		}
		return "", 0, fmt.Errorf("open PR %s: %w", repo, err)
	}
	return result.HTMLURL, result.Number, nil
}

// findExistingPR lists open PRs for the repo and finds the one with the matching head branch.
func (g *GitHubProvider) findExistingPR(ctx context.Context, repo, head string) (string, int, error) {
	var prs []struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/pulls?state=open&per_page=100", repo), nil, &prs); err != nil {
		return "", 0, fmt.Errorf("list PRs to find existing %s: %w", head, err)
	}
	for _, pr := range prs {
		if pr.Head.Ref == head {
			return pr.HTMLURL, pr.Number, nil
		}
	}
	return "", 0, fmt.Errorf("PR already exists for %s but could not find it in open PRs", head)
}

// isExistingPRErr returns true when the GitHub API rejected the PR creation with 422
// because a pull request already exists for the head branch.
func isExistingPRErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsStr(msg, "422") && (containsStr(msg, "already exists") || containsStr(msg, "A pull request"))
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
	var reviews []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State string `json:"state"`
	}
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, prNumber), nil, &reviews); err != nil {
		return false, 0, fmt.Errorf("get PR reviews %s#%d: %w", repo, prNumber, err)
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
	if g.WebhookSecret != "" {
		if err := g.validateSignature(payload, signature); err != nil {
			return WebhookEvent{}, fmt.Errorf("webhook signature invalid: %w", err)
		}
	}

	var raw struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number  int    `json:"number"`
			Merged  bool   `json:"merged"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse webhook payload: %w", err)
	}

	return WebhookEvent{
		EventType:    "pull_request",
		PRNumber:     raw.PullRequest.Number,
		RepoFullName: raw.Repository.FullName,
		Merged:       raw.PullRequest.Merged,
		Action:       raw.Action,
	}, nil
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

// Label represents a GitHub label with a name and color.
type Label struct {
	// Name is the label name.
	Name string

	// Color is the 6-digit hex color code (without #).
	Color string
}

// DefaultKardinalLabels returns the standard set of kardinal labels.
func DefaultKardinalLabels() []Label {
	return []Label{
		{Name: "kardinal", Color: "0075ca"},
		{Name: "kardinal/promotion", Color: "2196f3"},
		{Name: "kardinal/rollback", Color: "e91e63"},
		{Name: "kardinal/emergency", Color: "f44336"},
	}
}

// EnsureLabels ensures that the given labels exist in the repository, creating any
// that are missing. It is safe to call concurrently and is idempotent.
func (g *GitHubProvider) EnsureLabels(ctx context.Context, repo string, labels []Label) error {
	for _, label := range labels {
		payload := map[string]string{
			"name":  label.Name,
			"color": label.Color,
		}
		if err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/labels", repo), payload, nil); err != nil {
			// 422 Unprocessable Entity means the label already exists — not an error.
			if isAlreadyExistsErr(err) {
				continue
			}
			return fmt.Errorf("ensure label %q on %s: %w", label.Name, repo, err)
		}
	}
	return nil
}

// AddLabelsToPR applies labels to the pull request. Labels that do not exist are
// created on-demand via EnsureLabels before applying.
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

// isAlreadyExistsErr returns true when the GitHub API rejected the request with 422
// because the label already exists.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	// GitHub returns HTTP 422 with "already_exists" when creating a duplicate label.
	msg := err.Error()
	return containsStr(msg, "422") && containsStr(msg, "already_exists")
}

// containsStr returns true if s contains substr.
func containsStr(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// do executes an authenticated GitHub API request.
func (g *GitHubProvider) do(ctx context.Context, method, path string, body, result interface{}) error {
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
		return fmt.Errorf("execute request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API %s %s: status %d: %s", method, path, resp.StatusCode, string(raw))
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
