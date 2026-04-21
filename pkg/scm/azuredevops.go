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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	azureDevOpsDefaultAPIURL = "https://dev.azure.com"
	azureDevOpsAPIVersion    = "7.1"
)

// AzureDevOpsProvider implements SCMProvider against the Azure DevOps REST API.
//
// Repository format: "org/project/repoName" — e.g. "myorg/myproject/myrepo".
// The token must be an Azure DevOps Personal Access Token (PAT). Authentication
// uses the standard ADO pattern: Authorization: Basic base64(:<PAT>).
//
// All methods are safe for concurrent use.
//
// Design ref: docs/design/15-production-readiness.md §Lens 1 (Kargo parity)
type AzureDevOpsProvider struct {
	// Token is the Azure DevOps Personal Access Token (PAT).
	Token string

	// APIURL is the Azure DevOps base URL. Defaults to "https://dev.azure.com" if empty.
	APIURL string

	// WebhookSecret is the shared secret for validating incoming ADO service hook payloads.
	// ADO service hooks do not natively sign payloads with HMAC; instead we validate
	// a shared token sent in the X-AzureDevOps-Token header using constant-time comparison.
	WebhookSecret string

	// circuit guards all outbound Azure DevOps API calls.
	circuit *CircuitBreaker

	client *http.Client
}

// NewAzureDevOpsProvider constructs an AzureDevOpsProvider with the given PAT and
// optional API URL override (for Azure DevOps Server/on-premise installations).
func NewAzureDevOpsProvider(token, apiURL, webhookSecret string) *AzureDevOpsProvider {
	if apiURL == "" {
		apiURL = azureDevOpsDefaultAPIURL
	}
	return &AzureDevOpsProvider{
		Token:         token,
		APIURL:        strings.TrimRight(apiURL, "/"),
		WebhookSecret: webhookSecret,
		circuit:       NewCircuitBreaker(),
		client:        &http.Client{},
	}
}

// splitADORepo splits "org/project/repo" into three parts.
// Azure DevOps PR operations require org, project, and repository name separately.
func splitADORepo(repo string) (org, project, repoName string, err error) {
	parts := strings.SplitN(repo, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("azuredevops: repo must be in org/project/repo format, got %q", repo)
	}
	return parts[0], parts[1], parts[2], nil
}

// adoAuthHeader returns the Authorization header value for ADO PAT authentication.
// ADO uses Basic auth with an empty username: base64(:<PAT>).
func (a *AzureDevOpsProvider) adoAuthHeader() string {
	encoded := base64.StdEncoding.EncodeToString([]byte(":" + a.Token))
	return "Basic " + encoded
}

// OpenPR creates an Azure DevOps pull request and returns the PR URL and PR ID.
// It is idempotent: if a PR already exists for the source branch (TF401179 or 409),
// returns the existing PR.
func (a *AzureDevOpsProvider) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return "", 0, err
	}

	payload := map[string]interface{}{
		"title":         title,
		"description":   body,
		"sourceRefName": "refs/heads/" + head,
		"targetRefName": "refs/heads/" + base,
	}

	var result struct {
		PullRequestID int    `json:"pullRequestId"`
		URL           string `json:"url"`
		RemoteURL     string `json:"remoteUrl"`
		Repository    struct {
			WebURL string `json:"webUrl"`
		} `json:"repository"`
	}

	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests?api-version=%s",
		org, project, repoName, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodPost, path, payload, &result); err != nil {
		if isADOExistingPRErr(err) {
			return a.findExistingPR(ctx, org, project, repoName, head)
		}
		return "", 0, fmt.Errorf("open Azure DevOps PR %s: %w", repo, err)
	}

	// ADO PR URL is repo.webUrl + "/pullrequest/" + id
	prURL := fmt.Sprintf("%s/_git/%s/pullrequest/%d", result.Repository.WebURL, repoName, result.PullRequestID)
	if result.Repository.WebURL == "" {
		// Fallback: construct from APIURL
		prURL = fmt.Sprintf("%s/%s/%s/_git/%s/pullrequest/%d",
			a.APIURL, org, project, repoName, result.PullRequestID)
	}
	return prURL, result.PullRequestID, nil
}

// findExistingPR returns the open PR for the given source ref name.
func (a *AzureDevOpsProvider) findExistingPR(ctx context.Context, org, project, repoName, head string) (string, int, error) {
	sourceRef := "refs/heads/" + head
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests?searchCriteria.sourceRefName=%s&searchCriteria.status=active&api-version=%s",
		org, project, repoName, sourceRef, azureDevOpsAPIVersion)

	var result struct {
		Value []struct {
			PullRequestID int `json:"pullRequestId"`
			Repository    struct {
				WebURL string `json:"webUrl"`
			} `json:"repository"`
		} `json:"value"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return "", 0, fmt.Errorf("list ADO PRs to find existing for %s: %w", head, err)
	}
	if len(result.Value) == 0 {
		return "", 0, fmt.Errorf("ADO PR already exists for branch %s but could not find it in active PRs", head)
	}
	pr := result.Value[0]
	prURL := fmt.Sprintf("%s/_git/%s/pullrequest/%d", pr.Repository.WebURL, repoName, pr.PullRequestID)
	if pr.Repository.WebURL == "" {
		prURL = fmt.Sprintf("%s/%s/%s/_git/%s/pullrequest/%d",
			a.APIURL, org, project, repoName, pr.PullRequestID)
	}
	return prURL, pr.PullRequestID, nil
}

// isADOExistingPRErr returns true when ADO rejected PR creation because one already exists.
// TF401179: "An active pull request for the source and target branch already exists."
func isADOExistingPRErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "TF401179") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "active pull request") ||
		strings.Contains(msg, "status 409")
}

// ClosePR abandons (closes) the Azure DevOps pull request without merging.
func (a *AzureDevOpsProvider) ClosePR(ctx context.Context, repo string, prNumber int) error {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return err
	}
	payload := map[string]string{"status": "abandoned"}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d?api-version=%s",
		org, project, repoName, prNumber, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodPatch, path, payload, nil); err != nil {
		return fmt.Errorf("close ADO PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// CommentOnPR posts a comment thread on the Azure DevOps pull request.
func (a *AzureDevOpsProvider) CommentOnPR(ctx context.Context, repo string, prNumber int, body string) error {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return err
	}
	// ADO requires creating a "thread" with at least one comment.
	payload := map[string]interface{}{
		"comments": []map[string]interface{}{
			{"content": body, "commentType": 1},
		},
		"status": 1, // active
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d/threads?api-version=%s",
		org, project, repoName, prNumber, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodPost, path, payload, nil); err != nil {
		return fmt.Errorf("comment on ADO PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// GetPRStatus returns whether the ADO pull request has been completed (merged) and
// whether it is still active (open).
func (a *AzureDevOpsProvider) GetPRStatus(ctx context.Context, repo string, prNumber int) (bool, bool, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return false, false, err
	}
	var result struct {
		// ADO PR status: "active", "abandoned", "completed"
		Status string `json:"status"`
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d?api-version=%s",
		org, project, repoName, prNumber, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return false, false, fmt.Errorf("get ADO PR status %s#%d: %w", repo, prNumber, err)
	}
	merged := result.Status == "completed"
	open := result.Status == "active"
	return merged, open, nil
}

// GetPRReviewStatus returns reviewer approval state for an Azure DevOps pull request.
// ADO uses numeric vote values: 10 = approved, 5 = approved with suggestions,
// 0 = no vote, -5 = waiting for author, -10 = rejected.
// approved is true when at least one reviewer has vote >= 10 and no reviewer has vote <= -10.
func (a *AzureDevOpsProvider) GetPRReviewStatus(ctx context.Context, repo string, prNumber int) (bool, int, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return false, 0, err
	}
	var reviewers []struct {
		Vote int `json:"vote"`
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d/reviewers?api-version=%s",
		org, project, repoName, prNumber, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodGet, path, nil, &reviewers); err != nil {
		return false, 0, fmt.Errorf("get ADO PR reviewers %s#%d: %w", repo, prNumber, err)
	}
	approved := 0
	for _, r := range reviewers {
		if r.Vote <= -10 {
			// Any rejection → not approved
			return false, 0, nil
		}
		if r.Vote >= 10 {
			approved++
		}
	}
	return approved > 0, approved, nil
}

// ParseWebhookEvent parses an Azure DevOps service hook payload and validates
// the X-AzureDevOps-Token header using constant-time comparison.
// The caller must pass the value of the X-AzureDevOps-Token header as signature.
func (a *AzureDevOpsProvider) ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error) {
	if a.WebhookSecret != "" {
		if subtle.ConstantTimeCompare([]byte(signature), []byte(a.WebhookSecret)) != 1 {
			return WebhookEvent{}, fmt.Errorf("azuredevops webhook: token mismatch")
		}
	}

	// ADO service hook payload for git.pullrequest.merged / git.pullrequest.created
	var raw struct {
		EventType string `json:"eventType"`
		Resource  struct {
			PullRequestID int    `json:"pullRequestId"`
			Status        string `json:"status"`
			MergeStatus   string `json:"mergeStatus"`
			Repository    struct {
				// ADO uses project.name/repository.name structure; full_name is not standard.
				// We construct a best-effort org/project/repo string from the remote URL.
				Name    string `json:"name"`
				Project struct {
					Name string `json:"name"`
				} `json:"project"`
				RemoteURL string `json:"remoteUrl"`
			} `json:"repository"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse Azure DevOps webhook payload: %w", err)
	}

	// Construct repoFullName from project/repo.
	repoFullName := raw.Resource.Repository.Project.Name + "/" + raw.Resource.Repository.Name

	// ADO event types: "git.pullrequest.created", "git.pullrequest.updated",
	// "git.pullrequest.merged" (completed), "git.pullrequest.merged" (abandoned)
	merged := raw.Resource.Status == "completed"
	action := raw.Resource.Status

	return WebhookEvent{
		EventType:    raw.EventType,
		PRNumber:     raw.Resource.PullRequestID,
		RepoFullName: repoFullName,
		Merged:       merged,
		Action:       action,
	}, nil
}

// AddLabelsToPR is not supported by Azure DevOps Pull Requests natively.
// ADO does not have a PR label concept equivalent to GitHub labels.
// This method is a no-op that returns nil to avoid breaking the promotion step.
func (a *AzureDevOpsProvider) AddLabelsToPR(_ context.Context, _ string, _ int, _ []string) error {
	return nil
}

// do executes an authenticated Azure DevOps API request using PAT Basic auth.
func (a *AzureDevOpsProvider) do(ctx context.Context, method, path string, body, result interface{}) error {
	if err := a.circuit.Allow(); err != nil {
		return fmt.Errorf("azuredevops scm: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.APIURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", a.adoAuthHeader())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.client.Do(req)
	if err != nil {
		a.circuit.RecordFailure(time.Time{})
		return fmt.Errorf("execute request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		if IsRateLimitError(resp.StatusCode) {
			retryAfter := RetryAfterFromResponse(resp)
			a.circuit.RecordFailure(retryAfter)
		} else {
			a.circuit.RecordSuccess()
		}
		return fmt.Errorf("azuredevops API %s %s: status %d: %s", method, path, resp.StatusCode, string(raw))
	}

	a.circuit.RecordSuccess()
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
