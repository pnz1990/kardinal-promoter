// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"fmt"
	"net/http"
)

// prCommitLister lists the commits of one pull request, as the SCM keeps
// them: after a squash or rebase merge, and after the head branch is
// deleted (#2092). complete is false when the SCM has more than the page
// read: the list is then not used, so a PR is never adopted on a partial
// read.
type prCommitLister interface {
	listPRCommits(ctx context.Context, repo string, number int) (shas []string, complete bool, err error)
}

// prCommitPage is the most commits read of one pull request.
const prCommitPage = 100

// --- GitHub ---

func (g *GitHubProvider) listPRCommits(ctx context.Context, repo string, number int) ([]string, bool, error) {
	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=%d", repo, number, prCommitPage),
		nil, &commits); err != nil {
		return nil, false, fmt.Errorf("list the commits of PR #%d: %w", number, err)
	}
	out := make([]string, 0, len(commits))
	for _, c := range commits {
		out = append(out, c.SHA)
	}
	return out, len(commits) < prCommitPage, nil
}

// --- GitLab ---

func (g *GitLabProvider) listPRCommits(ctx context.Context, repo string, number int) ([]string, bool, error) {
	var commits []struct {
		ID string `json:"id"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/commits?per_page=%d",
		encodeProjectID(repo), number, prCommitPage), nil, &commits); err != nil {
		return nil, false, fmt.Errorf("list the commits of MR !%d: %w", number, err)
	}
	out := make([]string, 0, len(commits))
	for _, c := range commits {
		out = append(out, c.ID)
	}
	return out, len(commits) < prCommitPage, nil
}

// --- Forgejo and Gitea ---

func (f *ForgejoProvider) listPRCommits(ctx context.Context, repo string, number int) ([]string, bool, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, false, err
	}
	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/commits?limit=%d&page=1",
		owner, name, number, forgejoPageSize), nil, &commits); err != nil {
		return nil, false, fmt.Errorf("list the commits of PR #%d: %w", number, err)
	}
	out := make([]string, 0, len(commits))
	for _, c := range commits {
		out = append(out, c.SHA)
	}
	return out, len(commits) < forgejoPageSize, nil
}

// --- Bitbucket Cloud ---

func (b *BitbucketProvider) listPRCommits(ctx context.Context, repo string, number int) ([]string, bool, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return nil, false, err
	}
	var res struct {
		Values []struct {
			Hash string `json:"hash"`
		} `json:"values"`
		Next string `json:"next"`
	}
	if err := b.do(ctx, http.MethodGet, fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d/commits?pagelen=%d",
		workspace, repoSlug, number, prCommitPage), nil, &res); err != nil {
		return nil, false, fmt.Errorf("list the commits of PR #%d: %w", number, err)
	}
	out := make([]string, 0, len(res.Values))
	for _, c := range res.Values {
		out = append(out, c.Hash)
	}
	return out, res.Next == "", nil
}

// --- Bitbucket Data Center ---

func (b *BitbucketDCProvider) listPRCommits(ctx context.Context, repo string, number int) ([]string, bool, error) {
	path, err := b.prPath(repo)
	if err != nil {
		return nil, false, err
	}
	var res struct {
		Values []struct {
			ID string `json:"id"`
		} `json:"values"`
		IsLastPage bool `json:"isLastPage"`
	}
	if err := b.do(ctx, http.MethodGet, fmt.Sprintf("%s/%d/commits?limit=%d", path, number, prCommitPage), nil, &res); err != nil {
		return nil, false, fmt.Errorf("list the commits of PR #%d: %w", number, err)
	}
	out := make([]string, 0, len(res.Values))
	for _, c := range res.Values {
		out = append(out, c.ID)
	}
	return out, res.IsLastPage, nil
}

// --- Azure DevOps ---

func (a *AzureDevOpsProvider) listPRCommits(ctx context.Context, repo string, number int) ([]string, bool, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return nil, false, err
	}
	var res struct {
		Value []struct {
			CommitID string `json:"commitId"`
		} `json:"value"`
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullRequests/%d/commits?$top=%d&api-version=%s",
		org, project, repoName, number, prCommitPage, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodGet, path, nil, &res); err != nil {
		return nil, false, fmt.Errorf("list the commits of PR %d: %w", number, err)
	}
	out := make([]string, 0, len(res.Value))
	for _, c := range res.Value {
		out = append(out, c.CommitID)
	}
	return out, len(res.Value) < prCommitPage, nil
}
