// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Commit status states, as kardinal posts them.
const (
	CommitStatusPending = "pending"
	CommitStatusSuccess = "success"
	CommitStatusFailure = "failure"
	// CommitStatusError says kardinal cannot tell whether the gates pass
	// (GitHub, Forgejo and Azure DevOps "error"; GitLab and Bitbucket show it
	// as failed).
	CommitStatusError = "error"
)

// GatesStatusContext is the default commit status context (GitHub, Forgejo,
// Gitea), pipeline name (GitLab), build key (Bitbucket) or status genre/name
// (Azure DevOps) kardinal posts the gate results under
// (--gates-status-context). Branch protection can require it; nothing else
// should post under it.
const GatesStatusContext = "kardinal/gates"

// CommitStatus is one status kardinal sets on a PR's head commit.
type CommitStatus struct {
	// Context names the status (GatesStatusContext).
	Context string
	// State is CommitStatusPending, CommitStatusSuccess or CommitStatusFailure.
	State string
	// Description is a one-line summary, shortened to the provider's limit.
	Description string
	// TargetURL links to details; empty for none.
	TargetURL string
}

// CommitStatusSetter is implemented by the providers that can set a status on
// a pull request's commit: GitHub, GitLab, Forgejo, Gitea, Bitbucket Cloud
// and Azure DevOps. It is optional, so a provider without it still serves
// promotions; the step then posts no status.
type CommitStatusSetter interface {
	// SetPRCommitStatus sets s on commit sha of PR prNumber: the commit
	// kardinal pushed to the PR branch, never whatever the head is now, so a
	// head someone else pushed is left without a status (branch protection
	// that requires it then holds the PR). Setting the same status again
	// replaces it.
	SetPRCommitStatus(ctx context.Context, repo string, prNumber int, sha string, s CommitStatus) error
}

// SetPRCommitStatus implements CommitStatusSetter when the current provider
// does.
func (d *DynamicProvider) SetPRCommitStatus(ctx context.Context, repo string, prNumber int, sha string, s CommitStatus) error {
	cs, ok := d.current().(CommitStatusSetter)
	if !ok {
		return fmt.Errorf("%s: %w", d.providerType, ErrCommitStatusUnsupported)
	}
	return cs.SetPRCommitStatus(ctx, repo, prNumber, sha, s)
}

// TokenIdentifier is implemented by providers whose token can change at run
// time (DynamicProvider, after a Secret rotation). TokenID identifies the
// current token without revealing it: a short prefix of its SHA-256. A
// caller that backs off after a permanent error (a token without the
// permission) tries again as soon as the token changes.
type TokenIdentifier interface {
	TokenID() string
}

// tokenIdentity is the TokenID of token.
func tokenIdentity(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:12]
}

var _ TokenIdentifier = (*DynamicProvider)(nil)

// ErrCommitStatusUnsupported is returned when the provider cannot set commit
// statuses.
var ErrCommitStatusUnsupported = errors.New("the SCM provider does not support commit statuses")

// ErrCommitNotInPR is returned when the PR does not contain the commit to
// set the status on (Azure DevOps: no iteration has it), for example because
// the PR head moved since kardinal pushed. Retrying cannot help until
// kardinal pushes again.
var ErrCommitNotInPR = errors.New("the pull request has no iteration with that commit")

// truncateDescription shortens a status description to n characters on a
// rune boundary (GitHub allows 140).
func truncateDescription(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// The providers that set commit statuses.
var (
	_ CommitStatusSetter = (*GitHubProvider)(nil)
	_ CommitStatusSetter = (*ForgejoProvider)(nil)
	_ CommitStatusSetter = (*GitLabProvider)(nil)
	_ CommitStatusSetter = (*BitbucketProvider)(nil)
	_ CommitStatusSetter = (*AzureDevOpsProvider)(nil)
	_ CommitStatusSetter = (*DynamicProvider)(nil)
)

// SetPRCommitStatus sets s on commit sha (POST /repos/{repo}/statuses/{sha}).
func (g *GitHubProvider) SetPRCommitStatus(ctx context.Context, repo string, _ int, sha string, s CommitStatus) error {
	body := map[string]string{"state": s.State, "context": s.Context, "description": truncateDescription(s.Description, 140)}
	if s.TargetURL != "" {
		body["target_url"] = s.TargetURL
	}
	if err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/statuses/%s", repo, sha), body, nil); err != nil {
		return fmt.Errorf("set commit status on %s@%s: %w", repo, sha, err)
	}
	return nil
}

// SetPRCommitStatus sets s on commit sha
// (POST /api/v1/repos/{owner}/{repo}/statuses/{sha}); Forgejo and Gitea.
func (f *ForgejoProvider) SetPRCommitStatus(ctx context.Context, repo string, _ int, sha string, s CommitStatus) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	body := map[string]string{"state": s.State, "context": s.Context, "description": truncateDescription(s.Description, 140)}
	if s.TargetURL != "" {
		body["target_url"] = s.TargetURL
	}
	if err := f.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/statuses/%s", owner, name, sha), body, nil); err != nil {
		return fmt.Errorf("set commit status on %s@%s: %w", repo, sha, err)
	}
	return nil
}

// SetPRCommitStatus sets s on commit sha (POST
// /api/v4/projects/:id/statuses/:sha, with name = s.Context). GitLab calls the
// states pending, success, failed and canceled. GitLab refuses to set a
// status to the state it already has ("Cannot transition status"): that is
// the status kardinal wants, so it counts as set.
func (g *GitLabProvider) SetPRCommitStatus(ctx context.Context, repo string, _ int, sha string, s CommitStatus) error {
	projectID := encodeProjectID(repo)
	state := map[string]string{CommitStatusFailure: "failed", CommitStatusError: "failed"}[s.State]
	if state == "" {
		state = s.State
	}
	body := map[string]string{"state": state, "name": s.Context, "description": truncateDescription(s.Description, 255)}
	if s.TargetURL != "" {
		body["target_url"] = s.TargetURL
	}
	err := g.do(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%s/statuses/%s", projectID, sha), body, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest &&
		strings.Contains(apiErr.Body, "Cannot transition status") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("set commit status on %s@%s: %w", repo, sha, err)
	}
	return nil
}

// SetPRCommitStatus sets s as a build status on commit sha
// (POST /2.0/repositories/{ws}/{repo}/commit/{sha}/statuses/build). Bitbucket
// calls the states INPROGRESS, SUCCESSFUL and FAILED, and requires a URL: s's,
// or the PR's page when s has none.
func (b *BitbucketProvider) SetPRCommitStatus(ctx context.Context, repo string, prNumber int, sha string, s CommitStatus) error {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return err
	}
	url := s.TargetURL
	if url == "" {
		var pr struct {
			Links struct {
				HTML struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
		}
		if err := b.do(ctx, http.MethodGet, fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d", workspace, repoSlug, prNumber), nil, &pr); err != nil {
			return fmt.Errorf("get Bitbucket PR %s#%d link: %w", repo, prNumber, err)
		}
		url = pr.Links.HTML.Href
	}
	state := map[string]string{CommitStatusPending: "INPROGRESS", CommitStatusSuccess: "SUCCESSFUL",
		CommitStatusFailure: "FAILED", CommitStatusError: "FAILED"}[s.State]
	body := map[string]string{"state": state, "key": s.Context, "name": s.Context, "url": url,
		"description": truncateDescription(s.Description, 255)}
	if err := b.do(ctx, http.MethodPost, fmt.Sprintf("/2.0/repositories/%s/%s/commit/%s/statuses/build", workspace, repoSlug, sha), body, nil); err != nil {
		return fmt.Errorf("set build status on %s@%s: %w", repo, sha, err)
	}
	return nil
}

// SetPRCommitStatus sets s as a pull request iteration status (POST
// .../pullRequests/{id}/iterations/{iteration}/statuses) on the iteration
// whose source commit is sha, which an Azure DevOps "status check" branch
// policy can require; with "reset on new changes" a later push needs a new
// status. Its context is genre and name from s.Context ("kardinal/gates":
// genre "kardinal", name "gates"); the states are pending, succeeded, failed
// and error. A PR with no iteration for sha (its head moved) gets nothing:
// ErrCommitNotInPR.
func (a *AzureDevOpsProvider) SetPRCommitStatus(ctx context.Context, repo string, prNumber int, sha string, s CommitStatus) error {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d", org, project, repoName, prNumber)
	var iterations struct {
		Value []struct {
			ID              int `json:"id"`
			SourceRefCommit struct {
				CommitID string `json:"commitId"`
			} `json:"sourceRefCommit"`
		} `json:"value"`
	}
	if err := a.do(ctx, http.MethodGet, base+"/iterations?api-version="+azureDevOpsAPIVersion, nil, &iterations); err != nil {
		return fmt.Errorf("list ADO PR %s#%d iterations: %w", repo, prNumber, err)
	}
	iteration := 0
	for _, it := range iterations.Value {
		if strings.EqualFold(it.SourceRefCommit.CommitID, sha) && it.ID > iteration {
			iteration = it.ID
		}
	}
	if iteration == 0 {
		return fmt.Errorf("ADO PR %s#%d commit %s: %w", repo, prNumber, sha, ErrCommitNotInPR)
	}
	state := map[string]string{CommitStatusPending: "pending", CommitStatusSuccess: "succeeded",
		CommitStatusFailure: "failed", CommitStatusError: "error"}[s.State]
	genre, name := "kardinal", "gates"
	if g, n, ok := strings.Cut(s.Context, "/"); ok {
		genre, name = g, n
	} else if s.Context != "" {
		genre, name = "", s.Context
	}
	body := map[string]interface{}{
		"state":       state,
		"description": truncateDescription(s.Description, 255),
		"context":     map[string]string{"genre": genre, "name": name},
	}
	if s.TargetURL != "" {
		body["targetUrl"] = s.TargetURL
	}
	path := fmt.Sprintf("%s/iterations/%d/statuses?api-version=%s", base, iteration, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("set PR iteration status on %s#%d: %w", repo, prNumber, err)
	}
	return nil
}
