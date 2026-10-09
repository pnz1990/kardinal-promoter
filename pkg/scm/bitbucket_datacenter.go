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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// BitbucketDCProvider implements SCMProvider against the REST API 1.0 of
// Bitbucket Data Center (and Bitbucket Server, its former name).
//
// The repository is "<project key>/<repo slug>". RepoFromURL gives
// "scm/<KEY>/<slug>" for an HTTP clone URL, "projects/<KEY>/repos/<slug>"
// for a browse URL and "<key>/<slug>" for an ssh URL; the provider reads all
// of them (splitBitbucketDCRepo), with or without a context path. Requests
// are authenticated with an HTTP access token (personal, project or
// repository) as a Bearer token. All methods are safe for concurrent use.
type BitbucketDCProvider struct {
	// Token is the HTTP access token.
	Token string

	// APIURL is the server's base URL, with its context path if any
	// (https://bitbucket.example.com or https://example.com/bitbucket).
	APIURL string

	// WebhookSecret is the secret of the repository webhooks. Bitbucket Data
	// Center signs payloads with HMAC-SHA256 in X-Hub-Signature
	// ("sha256=<hex>"). Empty refuses every event (ErrNoWebhookSecret).
	WebhookSecret string

	// circuits guards the API calls, one circuit per project (key, in upper
	// case: keys are case-insensitive), as the other providers have one per
	// owner (#1274).
	circuits *CircuitRegistry
	client   *http.Client
}

// NewBitbucketDCProvider constructs a BitbucketDCProvider. apiURL is
// required: Bitbucket Data Center is always self-hosted.
func NewBitbucketDCProvider(token, apiURL, webhookSecret string) *BitbucketDCProvider {
	return &BitbucketDCProvider{
		Token:         token,
		APIURL:        strings.TrimRight(apiURL, "/"),
		WebhookSecret: webhookSecret,
		circuits:      NewCircuitRegistry(),
		client:        &http.Client{Timeout: providerHTTPTimeout},
	}
}

// splitBitbucketDCRepo returns the project key and repository slug of repo:
// "KEY/slug", "scm/KEY/slug", "projects/KEY/repos/slug", any of them after
// a context path. A personal repository's key is "~user".
func splitBitbucketDCRepo(repo string) (project, slug string, err error) {
	segs := strings.Split(strings.Trim(repo, "/"), "/")
	// A browse URL may go on past the slug (/browse, /pull-requests/12).
	for i := 0; i+3 < len(segs); i++ {
		if !strings.EqualFold(segs[i+2], "repos") {
			continue
		}
		switch {
		case strings.EqualFold(segs[i], "projects"):
			return segs[i+1], segs[i+3], nil
		case strings.EqualFold(segs[i], "users"):
			return "~" + segs[i+1], segs[i+3], nil
		}
	}
	if len(segs) >= 3 && strings.EqualFold(segs[len(segs)-3], "scm") {
		return segs[len(segs)-2], segs[len(segs)-1], nil
	}
	if len(segs) == 2 && segs[0] != "" && segs[1] != "" {
		return segs[0], segs[1], nil
	}
	return "", "", fmt.Errorf("bitbucket-datacenter: repo must be <project key>/<repo slug> (or an /scm/ or /projects/.../repos/ path), got %q", repo)
}

// CanonicalRepo implements RepoCanonicalizer: "KEY/slug" in lower case, so
// an https, ssh or browse URL of the repository and a webhook payload name
// the same repository. Project keys and slugs are case-insensitive.
func (b *BitbucketDCProvider) CanonicalRepo(repo string) string {
	project, slug, err := splitBitbucketDCRepo(repo)
	if err != nil {
		return strings.ToLower(repo)
	}
	return strings.ToLower(project + "/" + slug)
}

// prPath returns the REST path of the repository's pull requests.
func (b *BitbucketDCProvider) prPath(repo string) (string, error) {
	project, slug, err := splitBitbucketDCRepo(repo)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/rest/api/1.0/projects/%s/repos/%s/pull-requests", url.PathEscape(project), url.PathEscape(slug)), nil
}

// bbdcRef is the fromRef or toRef of a pull request.
type bbdcRef struct {
	ID         string `json:"id"`
	Repository struct {
		Slug    string `json:"slug"`
		Project struct {
			Key string `json:"key"`
		} `json:"project"`
	} `json:"repository"`
}

// bbdcPR is a pull request as the REST API returns it.
type bbdcPR struct {
	ID      int     `json:"id"`
	Version int     `json:"version"`
	State   string  `json:"state"`
	Title   string  `json:"title"`
	FromRef bbdcRef `json:"fromRef"`
	ToRef   bbdcRef `json:"toRef"`
	Links   struct {
		Self []struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
	Reviewers []struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
		Status string `json:"status"`
	} `json:"reviewers"`
	Properties struct {
		MergeCommit *struct {
			ID string `json:"id"`
		} `json:"mergeCommit"`
	} `json:"properties"`
}

func (p bbdcPR) webURL() string {
	if len(p.Links.Self) > 0 {
		return p.Links.Self[0].Href
	}
	return ""
}

// OpenPR creates a pull request from head into base. A pull request that is
// open between the same branches already (409 with a
// DuplicatePullRequestException) is returned instead.
func (b *BitbucketDCProvider) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	project, slug, err := splitBitbucketDCRepo(repo)
	if err != nil {
		return "", 0, err
	}
	path, _ := b.prPath(repo)
	ref := func(branch string) map[string]interface{} {
		return map[string]interface{}{
			"id":         "refs/heads/" + branch,
			"repository": map[string]interface{}{"slug": slug, "project": map[string]string{"key": project}},
		}
	}
	payload := map[string]interface{}{
		"title": title, "description": body, "fromRef": ref(head), "toRef": ref(base),
	}
	var pr bbdcPR
	if err := b.do(ctx, http.MethodPost, path, payload, &pr); err != nil {
		// 409 has several causes (reviewers that do not resolve, the same
		// branch twice, an archived repository); only a duplicate is reused.
		if apiErr, ok := statusIs(err, http.StatusConflict); ok &&
			(strings.Contains(apiErr.Body, "DuplicatePullRequestException") || strings.Contains(apiErr.Body, "existingPullRequest")) {
			return b.findExistingPR(ctx, repo, head, base)
		}
		return "", 0, fmt.Errorf("open Bitbucket Data Center PR %s: %w", repo, err)
	}
	return pr.webURL(), pr.ID, nil
}

// findExistingPR finds the open pull request from head into base.
func (b *BitbucketDCProvider) findExistingPR(ctx context.Context, repo, head, base string) (string, int, error) {
	path, err := b.prPath(repo)
	if err != nil {
		return "", 0, err
	}
	start := 0
	for page := 0; page < maxListPages; page++ {
		q := url.Values{"state": {"OPEN"}, "direction": {"OUTGOING"}, "at": {"refs/heads/" + head},
			"limit": {"100"}, "start": {fmt.Sprint(start)}}
		var res struct {
			Values        []bbdcPR `json:"values"`
			IsLastPage    bool     `json:"isLastPage"`
			NextPageStart int      `json:"nextPageStart"`
		}
		if err := b.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &res); err != nil {
			return "", 0, fmt.Errorf("list PRs to find existing %s: %w", head, err)
		}
		for _, pr := range res.Values {
			if pr.FromRef.ID == "refs/heads/"+head && pr.ToRef.ID == "refs/heads/"+base {
				return pr.webURL(), pr.ID, nil
			}
		}
		if res.IsLastPage {
			break
		}
		start = res.NextPageStart
	}
	return "", 0, fmt.Errorf("PR already exists for %s into %s but could not find it in open PRs", head, base)
}

// getPR reads one pull request.
func (b *BitbucketDCProvider) getPR(ctx context.Context, repo string, prNumber int) (bbdcPR, error) {
	path, err := b.prPath(repo)
	if err != nil {
		return bbdcPR{}, err
	}
	var pr bbdcPR
	if err := b.do(ctx, http.MethodGet, fmt.Sprintf("%s/%d", path, prNumber), nil, &pr); err != nil {
		return bbdcPR{}, err
	}
	return pr, nil
}

// withVersion runs a request that needs the pull request's current version
// (decline, merge), reading it first and once more if another change
// bumped it in between (409 with a version mismatch).
func (b *BitbucketDCProvider) withVersion(ctx context.Context, repo string, prNumber int, run func(version int) error) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var pr bbdcPR
		if pr, err = b.getPR(ctx, repo, prNumber); err != nil {
			return err
		}
		err = run(pr.Version)
		apiErr, conflict := statusIs(err, http.StatusConflict)
		if !conflict || !strings.Contains(apiErr.Body, "OutOfDate") {
			return err
		}
	}
	return err
}

// ClosePR declines the pull request.
func (b *BitbucketDCProvider) ClosePR(ctx context.Context, repo string, prNumber int) error {
	path, err := b.prPath(repo)
	if err != nil {
		return err
	}
	err = b.withVersion(ctx, repo, prNumber, func(version int) error {
		return b.do(ctx, http.MethodPost, fmt.Sprintf("%s/%d/decline?version=%d", path, prNumber, version), map[string]string{}, nil)
	})
	if err != nil {
		return fmt.Errorf("decline Bitbucket Data Center PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// CommentOnPR adds a comment to the pull request.
func (b *BitbucketDCProvider) CommentOnPR(ctx context.Context, repo string, prNumber int, body string) error {
	path, err := b.prPath(repo)
	if err != nil {
		return err
	}
	if err := b.do(ctx, http.MethodPost, fmt.Sprintf("%s/%d/comments", path, prNumber), map[string]string{"text": body}, nil); err != nil {
		return fmt.Errorf("comment on Bitbucket Data Center PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// GetPRStatus returns whether the pull request is merged and whether it is
// open. A declined pull request is neither.
func (b *BitbucketDCProvider) GetPRStatus(ctx context.Context, repo string, prNumber int) (bool, bool, error) {
	pr, err := b.getPR(ctx, repo, prNumber)
	if err != nil {
		return false, false, fmt.Errorf("get Bitbucket Data Center PR status %s#%d: %w", repo, prNumber, err)
	}
	return pr.State == "MERGED", pr.State == "OPEN", nil
}

// GetPRReviewStatus counts the reviewers whose status is APPROVED; a
// reviewer with NEEDS_WORK blocks the approval.
func (b *BitbucketDCProvider) GetPRReviewStatus(ctx context.Context, repo string, prNumber int) (bool, int, error) {
	pr, err := b.getPR(ctx, repo, prNumber)
	if err != nil {
		return false, 0, fmt.Errorf("get Bitbucket Data Center PR reviews %s#%d: %w", repo, prNumber, err)
	}
	approvals, needsWork := 0, false
	for _, r := range pr.Reviewers {
		switch r.Status {
		case "APPROVED":
			approvals++
		case "NEEDS_WORK":
			needsWork = true
		}
	}
	return approvals > 0 && !needsWork, approvals, nil
}

// GetPRMergeCommit implements MergeCommitGetter: the merge commit Bitbucket
// records in the pull request's properties once it is merged.
func (b *BitbucketDCProvider) GetPRMergeCommit(ctx context.Context, repo string, prNumber int) (string, error) {
	pr, err := b.getPR(ctx, repo, prNumber)
	if err != nil {
		return "", fmt.Errorf("get Bitbucket Data Center PR merge commit %s#%d: %w", repo, prNumber, err)
	}
	if pr.Properties.MergeCommit == nil {
		return "", nil
	}
	return pr.Properties.MergeCommit.ID, nil
}

// AddLabelsToPR is a no-op: Bitbucket Data Center pull requests have no
// labels, so kardinal's PRs there get none (find rollbacks by their
// "[kardinal] Rollback" title).
func (b *BitbucketDCProvider) AddLabelsToPR(_ context.Context, _ string, _ int, _ []string) error {
	return nil
}

// DeleteBranch implements BranchDeleter with the branch-utils REST API. A
// branch that is not there is already deleted.
func (b *BitbucketDCProvider) DeleteBranch(ctx context.Context, repo, branch string) error {
	project, slug, err := splitBitbucketDCRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/rest/branch-utils/1.0/projects/%s/repos/%s/branches", url.PathEscape(project), url.PathEscape(slug))
	err = b.do(ctx, http.MethodDelete, path, map[string]interface{}{"name": "refs/heads/" + branch, "dryRun": false}, nil)
	if apiErr, ok := statusIs(err, http.StatusNotFound, http.StatusBadRequest); ok &&
		(apiErr.StatusCode == http.StatusNotFound || strings.Contains(apiErr.Body, "NoSuchBranch") || strings.Contains(apiErr.Body, "does not exist")) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete Bitbucket Data Center branch %s in %s: %w", branch, repo, err)
	}
	return nil
}

// ParseWebhookEvent validates the X-Hub-Signature HMAC-SHA256 and parses a
// pull request event (pr:merged, pr:declined, pr:opened, ...). The event key
// is in the body as well as in the X-Event-Key header. A merged pull request
// is reported the same way as every provider's (mergedPREvent), with the
// merge commit when the payload has it.
func (b *BitbucketDCProvider) ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error) {
	return b.parseWebhookEvent(payload, signature, "")
}

func (b *BitbucketDCProvider) parseWebhookEvent(payload []byte, signature, eventType string) (WebhookEvent, error) {
	if b.WebhookSecret == "" {
		return WebhookEvent{}, ErrNoWebhookSecret
	}
	mac := hmac.New(sha256.New, []byte(b.WebhookSecret))
	mac.Write(payload)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return WebhookEvent{}, errors.New("bitbucket-datacenter webhook: invalid HMAC-SHA256 signature")
	}
	var raw struct {
		EventKey    string `json:"eventKey"`
		PullRequest *struct {
			ID         int     `json:"id"`
			State      string  `json:"state"`
			ToRef      bbdcRef `json:"toRef"`
			Properties struct {
				MergeCommit *struct {
					ID string `json:"id"`
				} `json:"mergeCommit"`
			} `json:"properties"`
		} `json:"pullRequest"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse Bitbucket Data Center webhook payload: %w", err)
	}
	if raw.EventKey == "" {
		raw.EventKey = eventType
	}
	if raw.PullRequest == nil {
		// diagnostics:ping, repo:refs_changed and other events.
		return WebhookEvent{EventType: raw.EventKey}, nil
	}
	pr := raw.PullRequest
	repo := pr.ToRef.Repository.Project.Key + "/" + pr.ToRef.Repository.Slug
	if raw.EventKey == "pr:merged" || pr.State == "MERGED" {
		ev := mergedPREvent(repo, pr.ID)
		if mc := pr.Properties.MergeCommit; mc != nil {
			ev.MergeCommitSHA = mc.ID
		}
		return ev, nil
	}
	return WebhookEvent{EventType: raw.EventKey, PRNumber: pr.ID, RepoFullName: repo, Action: strings.ToLower(pr.State)}, nil
}

// do executes an authenticated Bitbucket Data Center REST request.
func (b *BitbucketDCProvider) do(ctx context.Context, method, path string, body, result interface{}) error {
	if b.APIURL == "" {
		return errors.New("bitbucket-datacenter scm: --scm-api-url (the server's base URL) is not set")
	}
	owner := dcProjectOf(path)
	if err := b.circuits.Allow(owner); err != nil {
		return fmt.Errorf("bitbucket-datacenter scm: %w", err)
	}
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
	req.Header.Set("Accept", "application/json")
	// Bitbucket refuses a state-changing request without this header
	// (XSRF check) when it comes from a browser-like client.
	req.Header.Set("X-Atlassian-Token", "no-check")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.circuits.Record(owner, nil, err)
		return fmt.Errorf("execute request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		b.circuits.Record(owner, resp, nil)
		return newAPIError("bitbucket-datacenter", method, path, resp, raw)
	}
	b.circuits.Record(owner, resp, nil)
	if result != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

var (
	_ SCMProvider       = (*BitbucketDCProvider)(nil)
	_ BranchDeleter     = (*BitbucketDCProvider)(nil)
	_ MergeCommitGetter = (*BitbucketDCProvider)(nil)
	_ eventTypeParser   = (*BitbucketDCProvider)(nil)
)

// dcProjectOf is the circuit owner of a request path: the project key of
// /rest/api/1.0/projects/{key}/... and /rest/branch-utils/1.0/projects/{key}/...,
// in upper case.
func dcProjectOf(path string) string {
	for _, prefix := range []string{"/rest/api/1.0/projects/", "/rest/branch-utils/1.0/projects/"} {
		if owner := ownerFromPath(path, prefix); owner != "" {
			return strings.ToUpper(owner)
		}
	}
	return ""
}
