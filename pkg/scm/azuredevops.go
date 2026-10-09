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
	"net/url"
	"strings"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
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
	// Empty refuses every event (ErrNoWebhookSecret).
	WebhookSecret string

	// circuits guards all outbound API calls: one circuit per repository
	// owner and one for the token's rate limit (CircuitRegistry). A
	// DynamicProvider shares its registry with every provider it builds.
	circuits *CircuitRegistry

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
		circuits:      NewCircuitRegistry(),
		client:        &http.Client{Timeout: providerHTTPTimeout, Transport: tracing.Transport(nil, false)},
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
			return a.findExistingPR(ctx, org, project, repoName, head, base)
		}
		return "", 0, fmt.Errorf("open Azure DevOps PR %s: %w", repo, err)
	}

	return a.prWebURL(result.Repository.WebURL, org, project, repoName, result.PullRequestID), result.PullRequestID, nil
}

// prWebURL returns the web URL of an Azure DevOps pull request. repository.webUrl
// is already ".../{org}/{project}/_git/{repo}", so only "/pullrequest/{id}" is
// appended (C06-scm-health-06). Without a webUrl the URL is built from APIURL.
func (a *AzureDevOpsProvider) prWebURL(webURL, org, project, repoName string, id int) string {
	if webURL != "" {
		return fmt.Sprintf("%s/pullrequest/%d", strings.TrimSuffix(webURL, "/"), id)
	}
	return fmt.Sprintf("%s/%s/%s/_git/%s/pullrequest/%d", a.APIURL, org, project, repoName, id)
}

// findExistingPR returns the active PR from head into base. TF401179 refuses a
// second PR for the same source and target branch; a PR from head into
// another branch is not that one.
func (a *AzureDevOpsProvider) findExistingPR(ctx context.Context, org, project, repoName, head, base string) (string, int, error) {
	query := url.Values{
		"searchCriteria.sourceRefName": {"refs/heads/" + head},
		"searchCriteria.targetRefName": {"refs/heads/" + base},
		"searchCriteria.status":        {"active"},
		"api-version":                  {azureDevOpsAPIVersion},
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests?%s", org, project, repoName, query.Encode())

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
		return "", 0, fmt.Errorf("ADO PR already exists for branch %s into %s but could not find it in active PRs", head, base)
	}
	pr := result.Value[0]
	return a.prWebURL(pr.Repository.WebURL, org, project, repoName, pr.PullRequestID), pr.PullRequestID, nil
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
// approved is true when at least one reviewer approved (vote >= 5) and no
// reviewer is waiting for the author or rejected (vote <= -5). The count is
// the number of approving reviewers.
func (a *AzureDevOpsProvider) GetPRReviewStatus(ctx context.Context, repo string, prNumber int) (bool, int, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return false, 0, err
	}
	// ADO list APIs wrap the items in a {"count": N, "value": [...]} envelope
	// (C06-scm-health-05).
	var reviewers struct {
		Value []struct {
			Vote int `json:"vote"`
		} `json:"value"`
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d/reviewers?api-version=%s",
		org, project, repoName, prNumber, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodGet, path, nil, &reviewers); err != nil {
		return false, 0, fmt.Errorf("get ADO PR reviewers %s#%d: %w", repo, prNumber, err)
	}
	approved, blocked := 0, false
	for _, r := range reviewers.Value {
		switch {
		case r.Vote <= -5:
			blocked = true
		case r.Vote >= 5:
			approved++
		}
	}
	return approved > 0 && !blocked, approved, nil
}

// ParseWebhookEvent parses an Azure DevOps service hook payload and validates
// the X-AzureDevOps-Token header using constant-time comparison.
// The caller must pass the value of the X-AzureDevOps-Token header as signature.
func (a *AzureDevOpsProvider) ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error) {
	if a.WebhookSecret == "" {
		return WebhookEvent{}, ErrNoWebhookSecret
	}
	if subtle.ConstantTimeCompare([]byte(signature), []byte(a.WebhookSecret)) != 1 {
		return WebhookEvent{}, fmt.Errorf("azuredevops webhook: token mismatch")
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

	// The provider APIs take "org/project/repo"; the organization is only in
	// the remote URL. Without one, fall back to "project/repo", which will
	// not match a PRStatus but still identifies the event in logs.
	repoFullName, repoErr := RepoFromURL(raw.Resource.Repository.RemoteURL)
	if repoErr != nil {
		repoFullName = raw.Resource.Repository.Project.Name + "/" + raw.Resource.Repository.Name
	}

	// ADO event types: "git.pullrequest.created", "git.pullrequest.updated"
	// (also sent when a PR is completed or abandoned) and
	// "git.pullrequest.merged" ("merge attempted"). Only status "completed"
	// means the PR was merged into the target branch.
	if raw.Resource.Status == "completed" {
		return mergedPREvent(repoFullName, raw.Resource.PullRequestID), nil
	}
	return WebhookEvent{
		EventType:    raw.EventType,
		PRNumber:     raw.Resource.PullRequestID,
		RepoFullName: repoFullName,
		Action:       raw.Resource.Status,
	}, nil
}

// AddLabelsToPR adds labels (ADO "tags") to the pull request with the
// Pull Request Labels - Create API, one call per label. Adding a label that is
// already on the PR is not an error.
func (a *AzureDevOpsProvider) AddLabelsToPR(ctx context.Context, repo string, prNumber int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullRequests/%d/labels?api-version=%s",
		org, project, repoName, prNumber, azureDevOpsAPIVersion)
	for _, l := range labels {
		if err := a.do(ctx, http.MethodPost, path, map[string]string{"name": l}, nil); err != nil {
			return fmt.Errorf("add label %q to ADO PR %s#%d: %w", l, repo, prNumber, err)
		}
	}
	return nil
}

// do executes an authenticated Azure DevOps API request using PAT Basic auth.
func (a *AzureDevOpsProvider) do(ctx context.Context, method, path string, body, result interface{}) error {
	owner := ownerFromPath(path, "/")
	call := startSCMCall("azuredevops", owner, method, path)
	if err := a.circuits.Allow(owner); err != nil {
		call.circuitOpen(a.circuits, owner)
		return fmt.Errorf("azuredevops scm: %w", err)
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
	// Arguments are taken now; the circuit states are read at return, after Record.
	defer call.done(resp, err, a.circuits, owner)
	if err != nil {
		a.circuits.Record(owner, started, nil, err)
		return fmt.Errorf("execute request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		a.circuits.Record(owner, started, resp, nil)
		return newAPIError("azuredevops", method, path, resp, raw)
	}

	a.circuits.Record(owner, started, resp, nil)
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
