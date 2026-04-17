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
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitLabProvider implements SCMProvider against the GitLab REST API v4.
// Requests are authenticated with a private token (PRIVATE-TOKEN header).
// All methods are safe for concurrent use.
type GitLabProvider struct {
	// Token is the GitLab private token (e.g., glpat-...) or project access token.
	Token string

	// APIURL is the GitLab API base URL. Defaults to "https://gitlab.com" if empty.
	APIURL string

	// WebhookSecret is the secret token for validating incoming GitLab webhook payloads.
	// GitLab sends the token in the X-Gitlab-Token header (plaintext comparison).
	WebhookSecret string

	// circuit guards all outbound GitLab API calls.
	circuit *CircuitBreaker

	client *http.Client
}

// NewGitLabProvider constructs a GitLabProvider with the given token and optional
// API URL override (for self-managed GitLab instances).
func NewGitLabProvider(token, apiURL, webhookSecret string) *GitLabProvider {
	if apiURL == "" {
		apiURL = "https://gitlab.com"
	}
	return &GitLabProvider{
		Token:         token,
		APIURL:        strings.TrimRight(apiURL, "/"),
		WebhookSecret: webhookSecret,
		circuit:       NewCircuitBreaker(),
		client:        &http.Client{},
	}
}

// encodeProjectID URL-encodes the repo path (owner/repo → owner%2Frepo) for use
// in GitLab API paths that require the project ID or URL-encoded namespace/path.
func encodeProjectID(repo string) string {
	return url.PathEscape(repo)
}

// OpenPR creates a GitLab merge request and returns the MR web URL and IID.
// It is idempotent: if an MR already exists for the source branch, it returns the
// existing MR's URL and IID rather than failing.
func (g *GitLabProvider) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	projectID := encodeProjectID(repo)
	payload := map[string]string{
		"title":         title,
		"description":   body,
		"source_branch": head,
		"target_branch": base,
	}
	var result struct {
		IID    int    `json:"iid"`
		WebURL string `json:"web_url"`
		State  string `json:"state"`
	}
	if err := g.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests", projectID), payload, &result); err != nil {
		// GitLab returns 409 Conflict when an MR already exists for the source branch.
		if isGitLabExistingMRErr(err) {
			return g.findExistingMR(ctx, repo, head)
		}
		return "", 0, fmt.Errorf("open MR %s: %w", repo, err)
	}
	return result.WebURL, result.IID, nil
}

// findExistingMR lists open MRs for the project and returns the one with the matching
// source branch.
func (g *GitLabProvider) findExistingMR(ctx context.Context, repo, sourceBranch string) (string, int, error) {
	projectID := encodeProjectID(repo)
	var mrs []struct {
		IID          int    `json:"iid"`
		WebURL       string `json:"web_url"`
		SourceBranch string `json:"source_branch"`
		State        string `json:"state"`
	}
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests?state=opened&per_page=100", projectID),
		nil, &mrs); err != nil {
		return "", 0, fmt.Errorf("list MRs to find existing %s: %w", sourceBranch, err)
	}
	for _, mr := range mrs {
		if mr.SourceBranch == sourceBranch {
			return mr.WebURL, mr.IID, nil
		}
	}
	return "", 0, fmt.Errorf("MR already exists for %s but could not find it in open MRs", sourceBranch)
}

// isGitLabExistingMRErr returns true when GitLab rejected the MR creation with 409
// because an open MR already exists for the source branch.
func isGitLabExistingMRErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "409") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "open merge request")
}

// ClosePR closes the merge request by setting state_event to "close".
func (g *GitLabProvider) ClosePR(ctx context.Context, repo string, prNumber int) error {
	projectID := encodeProjectID(repo)
	payload := map[string]string{"state_event": "close"}
	if err := g.do(ctx, http.MethodPut,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", projectID, prNumber), payload, nil); err != nil {
		return fmt.Errorf("close MR %s!%d: %w", repo, prNumber, err)
	}
	return nil
}

// CommentOnPR posts a note (comment) on the merge request.
func (g *GitLabProvider) CommentOnPR(ctx context.Context, repo string, prNumber int, body string) error {
	projectID := encodeProjectID(repo)
	payload := map[string]string{"body": body}
	if err := g.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/notes", projectID, prNumber), payload, nil); err != nil {
		return fmt.Errorf("comment on MR %s!%d: %w", repo, prNumber, err)
	}
	return nil
}

// GetPRStatus returns whether the MR has been merged and whether it is still open.
func (g *GitLabProvider) GetPRStatus(ctx context.Context, repo string, prNumber int) (bool, bool, error) {
	projectID := encodeProjectID(repo)
	var result struct {
		State string `json:"state"`
	}
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", projectID, prNumber), nil, &result); err != nil {
		return false, false, fmt.Errorf("get MR status %s!%d: %w", repo, prNumber, err)
	}
	merged := result.State == "merged"
	open := result.State == "opened"
	return merged, open, nil
}

// GetPRReviewStatus returns review approval state for a GitLab merge request.
// GitLab MR approvals require the Approvals API (available on all tiers).
// approved is true when the MR has at least one approval.
// approvalCount is the number of distinct approvers.
func (g *GitLabProvider) GetPRReviewStatus(ctx context.Context, repo string, prNumber int) (bool, int, error) {
	projectID := encodeProjectID(repo)
	var result struct {
		ApprovedBy []struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"approved_by"`
	}
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/approvals", projectID, prNumber), nil, &result); err != nil {
		return false, 0, fmt.Errorf("get MR approvals %s!%d: %w", repo, prNumber, err)
	}
	count := len(result.ApprovedBy)
	return count > 0, count, nil
}

// ParseWebhookEvent parses a GitLab merge request webhook payload and validates
// the X-Gitlab-Token header using constant-time comparison.
// The caller must pass the value of the X-Gitlab-Token header as signature.
func (g *GitLabProvider) ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error) {
	if g.WebhookSecret != "" {
		// GitLab uses a plaintext token, not HMAC. Constant-time comparison prevents timing attacks.
		if subtle.ConstantTimeCompare([]byte(signature), []byte(g.WebhookSecret)) != 1 {
			return WebhookEvent{}, fmt.Errorf("webhook token mismatch")
		}
	}

	var raw struct {
		ObjectKind string `json:"object_kind"`
		ObjectAttr struct {
			IID    int    `json:"iid"`
			State  string `json:"state"`
			Action string `json:"action"`
		} `json:"object_attributes"`
		Project struct {
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"project"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse GitLab webhook payload: %w", err)
	}

	merged := raw.ObjectAttr.State == "merged"
	return WebhookEvent{
		EventType:    raw.ObjectKind,
		PRNumber:     raw.ObjectAttr.IID,
		RepoFullName: raw.Project.PathWithNamespace,
		Merged:       merged,
		Action:       raw.ObjectAttr.Action,
	}, nil
}

// AddLabelsToPR applies labels to the merge request by updating the MR with the
// comma-separated label list.
func (g *GitLabProvider) AddLabelsToPR(ctx context.Context, repo string, prNumber int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	projectID := encodeProjectID(repo)
	payload := map[string]string{
		"labels": strings.Join(labels, ","),
	}
	if err := g.do(ctx, http.MethodPut,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", projectID, prNumber), payload, nil); err != nil {
		return fmt.Errorf("add labels to MR %s!%d: %w", repo, prNumber, err)
	}
	return nil
}

// do executes an authenticated GitLab API request.
func (g *GitLabProvider) do(ctx context.Context, method, path string, body, result interface{}) error {
	if err := g.circuit.Allow(); err != nil {
		return fmt.Errorf("gitlab scm: %w", err)
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
	req.Header.Set("PRIVATE-TOKEN", g.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.client.Do(req)
	if err != nil {
		g.circuit.RecordFailure(time.Time{})
		return fmt.Errorf("execute request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		if IsRateLimitError(resp.StatusCode) {
			retryAfter := RetryAfterFromResponse(resp)
			g.circuit.RecordFailure(retryAfter)
		} else {
			g.circuit.RecordSuccess()
		}
		return fmt.Errorf("GitLab API %s %s: status %d: %s", method, path, resp.StatusCode, string(raw))
	}

	g.circuit.RecordSuccess()
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
