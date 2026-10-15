// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"fmt"
	"net/http"
)

// PRMergerGetter is implemented by SCM providers that can report who merged
// a pull request. The PRStatus reconciler records it in status.mergedBy when
// the merge was found by polling (a webhook's payload names the merger
// itself, WebhookEvent.MergedBy), and the PRMerged AuditEvent names them
// (#1939). An empty name means the SCM did not say.
type PRMergerGetter interface {
	GetPRMerger(ctx context.Context, repo string, prNumber int) (string, error)
}

// GetPRMerger returns merged_by.login of a merged GitHub pull request.
func (g *GitHubProvider) GetPRMerger(ctx context.Context, repo string, prNumber int) (string, error) {
	var result struct {
		MergedBy *struct {
			Login string `json:"login"`
		} `json:"merged_by"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, prNumber), nil, &result); err != nil {
		return "", fmt.Errorf("get PR merger %s#%d: %w", repo, prNumber, err)
	}
	if result.MergedBy == nil {
		return "", nil
	}
	return result.MergedBy.Login, nil
}

// GetPRMerger returns merge_user.username of a merged GitLab merge request,
// or the older merged_by.username.
func (g *GitLabProvider) GetPRMerger(ctx context.Context, repo string, prNumber int) (string, error) {
	type user struct {
		Username string `json:"username"`
	}
	var result struct {
		MergeUser *user `json:"merge_user"`
		MergedBy  *user `json:"merged_by"`
	}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", encodeProjectID(repo), prNumber)
	if err := g.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return "", fmt.Errorf("get MR merger %s!%d: %w", repo, prNumber, err)
	}
	for _, u := range []*user{result.MergeUser, result.MergedBy} {
		if u != nil && u.Username != "" {
			return u.Username, nil
		}
	}
	return "", nil
}

// GetPRMerger returns merged_by.login of a merged Forgejo/Gitea pull request.
func (f *ForgejoProvider) GetPRMerger(ctx context.Context, repo string, prNumber int) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	var result struct {
		MergedBy *struct {
			Login string `json:"login"`
		} `json:"merged_by"`
	}
	if err := f.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", owner, name, prNumber), nil, &result); err != nil {
		return "", fmt.Errorf("get PR merger %s#%d: %w", repo, prNumber, err)
	}
	if result.MergedBy == nil {
		return "", nil
	}
	return result.MergedBy.Login, nil
}

// GetPRMerger returns closed_by of a merged Bitbucket Cloud pull request:
// its nickname, else its display name.
func (b *BitbucketProvider) GetPRMerger(ctx context.Context, repo string, prNumber int) (string, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return "", err
	}
	var result struct {
		ClosedBy *struct {
			Nickname    string `json:"nickname"`
			DisplayName string `json:"display_name"`
		} `json:"closed_by"`
	}
	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d", workspace, repoSlug, prNumber)
	if err := b.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return "", fmt.Errorf("get Bitbucket PR merger %s#%d: %w", repo, prNumber, err)
	}
	if result.ClosedBy == nil {
		return "", nil
	}
	return firstNonEmpty(result.ClosedBy.Nickname, result.ClosedBy.DisplayName), nil
}

// GetPRMerger returns closedBy.uniqueName of a completed Azure DevOps pull
// request.
func (a *AzureDevOpsProvider) GetPRMerger(ctx context.Context, repo string, prNumber int) (string, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return "", err
	}
	var result struct {
		ClosedBy *struct {
			UniqueName string `json:"uniqueName"`
		} `json:"closedBy"`
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d?api-version=%s",
		org, project, repoName, prNumber, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return "", fmt.Errorf("get ADO PR merger %s#%d: %w", repo, prNumber, err)
	}
	if result.ClosedBy == nil {
		return "", nil
	}
	return result.ClosedBy.UniqueName, nil
}

// GetPRMerger forwards to the active provider.
func (d *DynamicProvider) GetPRMerger(ctx context.Context, repo string, prNumber int) (string, error) {
	g, ok := d.current().(PRMergerGetter)
	if !ok {
		return "", nil
	}
	return g.GetPRMerger(ctx, repo, prNumber)
}

// GetPRMerger implements PRMergerGetter.
func (g *guardedProvider) GetPRMerger(ctx context.Context, repo string, prNumber int) (string, error) {
	if err := g.check("get PR merger in", repo); err != nil {
		return "", err
	}
	mg, ok := g.inner.(PRMergerGetter)
	if !ok {
		return "", nil
	}
	return mg.GetPRMerger(ctx, repo, prNumber)
}

// firstNonEmpty is the first of values that is not empty, or "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var (
	_ PRMergerGetter = (*GitHubProvider)(nil)
	_ PRMergerGetter = (*GitLabProvider)(nil)
	_ PRMergerGetter = (*ForgejoProvider)(nil)
	_ PRMergerGetter = (*BitbucketProvider)(nil)
	_ PRMergerGetter = (*AzureDevOpsProvider)(nil)
	_ PRMergerGetter = (*DynamicProvider)(nil)
	_ PRMergerGetter = (*guardedProvider)(nil)
)
