// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"fmt"
	"net/http"
)

// PRCommit is one commit of a pull request: its SHA and its author as the
// SCM names them (the SCM account when it knows one, else the commit's
// author name).
type PRCommit struct {
	SHA    string `json:"sha"`
	Author string `json:"author,omitempty"`
}

// prCommitLister lists the commits of one pull request, as the SCM keeps
// them: after a squash or rebase merge, and after the head branch is
// deleted (#2092). complete is false when the SCM has more than the page
// read: the list is then not used, so a PR is never adopted on a partial
// read.
type prCommitLister interface {
	listPRCommits(ctx context.Context, repo string, number int) (commits []PRCommit, complete bool, err error)
}

// prCommitPage is the most commits read of one pull request.
const prCommitPage = 100

// --- GitHub ---

func (g *GitHubProvider) listPRCommits(ctx context.Context, repo string, number int) ([]PRCommit, bool, error) {
	var commits []struct {
		SHA    string `json:"sha"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
		Commit struct {
			Author struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=%d", repo, number, prCommitPage),
		nil, &commits); err != nil {
		return nil, false, fmt.Errorf("list the commits of PR #%d: %w", number, err)
	}
	out := make([]PRCommit, 0, len(commits))
	for _, c := range commits {
		by := c.Commit.Author.Name
		if c.Author != nil && c.Author.Login != "" {
			by = c.Author.Login
		}
		out = append(out, PRCommit{SHA: c.SHA, Author: by})
	}
	return out, len(commits) < prCommitPage, nil
}

// --- GitLab ---

func (g *GitLabProvider) listPRCommits(ctx context.Context, repo string, number int) ([]PRCommit, bool, error) {
	var commits []struct {
		ID         string `json:"id"`
		AuthorName string `json:"author_name"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/commits?per_page=%d",
		encodeProjectID(repo), number, prCommitPage), nil, &commits); err != nil {
		return nil, false, fmt.Errorf("list the commits of MR !%d: %w", number, err)
	}
	out := make([]PRCommit, 0, len(commits))
	for _, c := range commits {
		out = append(out, PRCommit{SHA: c.ID, Author: c.AuthorName})
	}
	return out, len(commits) < prCommitPage, nil
}

// --- Forgejo and Gitea ---

func (f *ForgejoProvider) listPRCommits(ctx context.Context, repo string, number int) ([]PRCommit, bool, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, false, err
	}
	var commits []struct {
		SHA    string `json:"sha"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
		Commit struct {
			Author struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/commits?limit=%d&page=1",
		owner, name, number, forgejoPageSize), nil, &commits); err != nil {
		return nil, false, fmt.Errorf("list the commits of PR #%d: %w", number, err)
	}
	out := make([]PRCommit, 0, len(commits))
	for _, c := range commits {
		by := c.Commit.Author.Name
		if c.Author != nil && c.Author.Login != "" {
			by = c.Author.Login
		}
		out = append(out, PRCommit{SHA: c.SHA, Author: by})
	}
	return out, len(commits) < forgejoPageSize, nil
}

// --- Bitbucket Cloud ---

func (b *BitbucketProvider) listPRCommits(ctx context.Context, repo string, number int) ([]PRCommit, bool, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return nil, false, err
	}
	var res struct {
		Values []struct {
			Hash   string `json:"hash"`
			Author struct {
				Raw  string `json:"raw"`
				User *struct {
					Nickname string `json:"nickname"`
				} `json:"user"`
			} `json:"author"`
		} `json:"values"`
		Next string `json:"next"`
	}
	if err := b.do(ctx, http.MethodGet, fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d/commits?pagelen=%d",
		workspace, repoSlug, number, prCommitPage), nil, &res); err != nil {
		return nil, false, fmt.Errorf("list the commits of PR #%d: %w", number, err)
	}
	out := make([]PRCommit, 0, len(res.Values))
	for _, c := range res.Values {
		by := c.Author.Raw
		if c.Author.User != nil && c.Author.User.Nickname != "" {
			by = c.Author.User.Nickname
		}
		out = append(out, PRCommit{SHA: c.Hash, Author: by})
	}
	return out, res.Next == "", nil
}

// --- Bitbucket Data Center ---

func (b *BitbucketDCProvider) listPRCommits(ctx context.Context, repo string, number int) ([]PRCommit, bool, error) {
	path, err := b.prPath(repo)
	if err != nil {
		return nil, false, err
	}
	var res struct {
		Values []struct {
			ID     string `json:"id"`
			Author struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"values"`
		IsLastPage bool `json:"isLastPage"`
	}
	if err := b.do(ctx, http.MethodGet, fmt.Sprintf("%s/%d/commits?limit=%d", path, number, prCommitPage), nil, &res); err != nil {
		return nil, false, fmt.Errorf("list the commits of PR #%d: %w", number, err)
	}
	out := make([]PRCommit, 0, len(res.Values))
	for _, c := range res.Values {
		out = append(out, PRCommit{SHA: c.ID, Author: c.Author.Name})
	}
	return out, res.IsLastPage, nil
}

// --- Azure DevOps ---

func (a *AzureDevOpsProvider) listPRCommits(ctx context.Context, repo string, number int) ([]PRCommit, bool, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return nil, false, err
	}
	var res struct {
		Value []struct {
			CommitID string `json:"commitId"`
			Author   struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"value"`
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullRequests/%d/commits?$top=%d&api-version=%s",
		org, project, repoName, number, prCommitPage, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodGet, path, nil, &res); err != nil {
		return nil, false, fmt.Errorf("list the commits of PR %d: %w", number, err)
	}
	out := make([]PRCommit, 0, len(res.Value))
	for _, c := range res.Value {
		out = append(out, PRCommit{SHA: c.CommitID, Author: c.Author.Name})
	}
	return out, len(res.Value) < prCommitPage, nil
}
