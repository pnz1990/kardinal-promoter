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
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ForgejoProvider implements SCMProvider against the Forgejo/Gitea REST API v1.
// The Forgejo API is compatible with Gitea and uses the same endpoint paths.
// Requests are authenticated with a token in the Authorization header.
// All methods are safe for concurrent use.
type ForgejoProvider struct {
	// Token is the Forgejo/Gitea API token.
	Token string

	// APIURL is the Forgejo/Gitea base URL (e.g., "https://forgejo.example.com").
	// The /api/v1 prefix is appended automatically.
	APIURL string

	// WebhookSecret is the secret for validating incoming webhook payloads.
	// Forgejo/Gitea signs payloads with HMAC-SHA256 and sends the result in
	// the X-Gitea-Signature header (same scheme as GitHub's X-Hub-Signature-256).
	WebhookSecret string

	client *http.Client
}

// NewForgejoProvider constructs a ForgejoProvider for the given base URL and token.
// The apiURL must point to the Forgejo/Gitea instance root (e.g., "https://forgejo.example.com").
func NewForgejoProvider(token, apiURL, webhookSecret string) *ForgejoProvider {
	if apiURL == "" {
		apiURL = "https://codeberg.org" // Codeberg is the primary public Forgejo instance
	}
	return &ForgejoProvider{
		Token:         token,
		APIURL:        strings.TrimRight(apiURL, "/"),
		WebhookSecret: webhookSecret,
		client:        &http.Client{},
	}
}

// OpenPR creates a Forgejo pull request and returns the PR URL and number.
// It is idempotent: if a PR already exists for the head branch, returns the existing PR.
func (f *ForgejoProvider) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", 0, err
	}

	payload := map[string]interface{}{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}
	var result struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := f.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/repos/%s/%s/pulls", owner, name), payload, &result); err != nil {
		// Forgejo/Gitea returns 409 when a PR already exists for the same head/base.
		if isForgejoExistingPRErr(err) {
			return f.findExistingPR(ctx, owner, name, head)
		}
		return "", 0, fmt.Errorf("open PR %s: %w", repo, err)
	}
	return result.HTMLURL, result.Number, nil
}

// findExistingPR lists open PRs for the repository and returns the one with the
// matching head branch.
func (f *ForgejoProvider) findExistingPR(ctx context.Context, owner, repo, head string) (string, int, error) {
	var prs []struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Label string `json:"label"`
		} `json:"head"`
	}
	if err := f.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/repos/%s/%s/pulls?state=open&limit=50", owner, repo),
		nil, &prs); err != nil {
		return "", 0, fmt.Errorf("list PRs to find existing %s: %w", head, err)
	}
	for _, pr := range prs {
		// The head label is "owner:branch" in Forgejo
		if pr.Head.Label == head || strings.HasSuffix(pr.Head.Label, ":"+head) {
			return pr.HTMLURL, pr.Number, nil
		}
	}
	return "", 0, fmt.Errorf("PR already exists for %s but could not find it in open PRs", head)
}

// isForgejoExistingPRErr returns true when Forgejo rejected the PR creation
// because one already exists.
func isForgejoExistingPRErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "409") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "pull request already exists")
}

// splitRepo splits "owner/repo" into owner and repo name.
func splitRepo(repo string) (string, string, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo format %q: expected \"owner/repo\"", repo)
	}
	return parts[0], parts[1], nil
}

// ClosePR closes the pull request.
func (f *ForgejoProvider) ClosePR(ctx context.Context, repo string, prNumber int) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	payload := map[string]string{"state": "closed"}
	if err := f.do(ctx, http.MethodPatch,
		fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", owner, name, prNumber), payload, nil); err != nil {
		return fmt.Errorf("close PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// CommentOnPR posts a comment on the pull request.
// Forgejo/Gitea uses the issues endpoint for PR comments.
func (f *ForgejoProvider) CommentOnPR(ctx context.Context, repo string, prNumber int, body string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	payload := map[string]string{"body": body}
	if err := f.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments", owner, name, prNumber), payload, nil); err != nil {
		return fmt.Errorf("comment on PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// GetPRStatus returns whether the PR has been merged and whether it is still open.
func (f *ForgejoProvider) GetPRStatus(ctx context.Context, repo string, prNumber int) (bool, bool, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return false, false, err
	}
	var result struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	if err := f.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", owner, name, prNumber), nil, &result); err != nil {
		return false, false, fmt.Errorf("get PR status %s#%d: %w", repo, prNumber, err)
	}
	open := result.State == "open"
	return result.Merged, open, nil
}

// GetPRReviewStatus returns review approval state for a Forgejo/Gitea pull request.
// Forgejo's review API returns all reviews; we take the latest state per user.
// approved is true when at least one approving review exists and no
// change-request review is outstanding. approvalCount is distinct approvers.
func (f *ForgejoProvider) GetPRReviewStatus(ctx context.Context, repo string, prNumber int) (bool, int, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return false, 0, err
	}
	var reviews []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State string `json:"state"`
	}
	if err := f.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/reviews", owner, name, prNumber), nil, &reviews); err != nil {
		return false, 0, fmt.Errorf("get PR reviews %s#%d: %w", repo, prNumber, err)
	}

	// Take the most recent review per user.
	latestByUser := make(map[string]string, len(reviews))
	for _, r := range reviews {
		if r.User.Login == "" {
			continue
		}
		switch r.State {
		case "APPROVED", "REQUEST_CHANGES":
			latestByUser[r.User.Login] = r.State
		}
	}

	approvalCount := 0
	hasChangeRequest := false
	for _, state := range latestByUser {
		switch state {
		case "APPROVED":
			approvalCount++
		case "REQUEST_CHANGES":
			hasChangeRequest = true
		}
	}
	return approvalCount > 0 && !hasChangeRequest, approvalCount, nil
}

// ParseWebhookEvent parses a Forgejo/Gitea pull request webhook payload.
// Forgejo signs payloads with HMAC-SHA256 and sends the hex digest in
// the X-Gitea-Signature header (same algorithm as GitHub's X-Hub-Signature-256).
func (f *ForgejoProvider) ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error) {
	if f.WebhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(f.WebhookSecret))
		mac.Write(payload)
		expected := hex.EncodeToString(mac.Sum(nil))
		if subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) != 1 {
			return WebhookEvent{}, fmt.Errorf("webhook HMAC mismatch")
		}
	}

	var raw struct {
		Action      string `json:"action"`
		Number      int    `json:"number"`
		PullRequest struct {
			HTMLURL string `json:"html_url"`
			Merged  bool   `json:"merged"`
			State   string `json:"state"`
		} `json:"pull_request"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse Forgejo webhook payload: %w", err)
	}

	merged := raw.PullRequest.Merged || (raw.Action == "closed" && raw.PullRequest.State == "closed" && raw.PullRequest.Merged)
	return WebhookEvent{
		EventType:    "pull_request",
		PRNumber:     raw.Number,
		RepoFullName: raw.Repository.FullName,
		Merged:       merged,
		Action:       raw.Action,
	}, nil
}

// AddLabelsToPR replaces the label set on the pull request issue.
// Forgejo/Gitea uses the issues/labels endpoint with a label ID list.
// Since we only have label names, we first create labels (idempotent), then apply.
func (f *ForgejoProvider) AddLabelsToPR(ctx context.Context, repo string, prNumber int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	// Resolve label names to IDs (create if missing).
	ids, err := f.ensureLabels(ctx, owner, name, labels)
	if err != nil {
		return fmt.Errorf("ensure labels for %s#%d: %w", repo, prNumber, err)
	}

	payload := map[string]interface{}{"labels": ids}
	if err := f.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/labels", owner, name, prNumber), payload, nil); err != nil {
		return fmt.Errorf("add labels to PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// ensureLabels returns the IDs of the given label names, creating them if they do not exist.
func (f *ForgejoProvider) ensureLabels(ctx context.Context, owner, repo string, names []string) ([]int, error) {
	// List existing labels.
	var existing []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := f.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/repos/%s/%s/labels?limit=50", owner, repo), nil, &existing); err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	labelIDs := make(map[string]int, len(existing))
	for _, l := range existing {
		labelIDs[l.Name] = l.ID
	}

	ids := make([]int, 0, len(names))
	for _, n := range names {
		if id, ok := labelIDs[n]; ok {
			ids = append(ids, id)
			continue
		}
		// Create label with a default color.
		var created struct {
			ID int `json:"id"`
		}
		if err := f.do(ctx, http.MethodPost,
			fmt.Sprintf("/api/v1/repos/%s/%s/labels", owner, repo),
			map[string]string{"name": n, "color": "#0075ca"}, &created); err != nil {
			return nil, fmt.Errorf("create label %q: %w", n, err)
		}
		ids = append(ids, created.ID)
	}
	return ids, nil
}

// do executes an authenticated Forgejo/Gitea API request.
func (f *ForgejoProvider) do(ctx context.Context, method, path string, body, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, f.APIURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "token "+f.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("execute request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("forgejo API %s %s: status %d: %s", method, path, resp.StatusCode, string(raw))
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
