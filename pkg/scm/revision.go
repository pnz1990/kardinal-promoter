// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"fmt"
	"net/http"

	gogit "github.com/go-git/go-git/v5"
)

// HeadCommitReader is implemented by git clients that can report the commit a
// local working tree is at. The PromotionStep reconciler uses it after
// git-push to record the commit it delivered, so health checks can require the
// GitOps tool to have synced that commit (E2E-01).
type HeadCommitReader interface {
	HeadCommit(ctx context.Context, dir string) (string, error)
}

// MergeCommitGetter is implemented by SCM providers that can report the commit
// a merged pull request produced on its base branch (merge, squash or rebase
// commit). The PRStatus reconciler records it in status.mergeCommitSHA.
type MergeCommitGetter interface {
	GetPRMergeCommit(ctx context.Context, repo string, prNumber int) (string, error)
}

// HeadCommit returns the full SHA of HEAD in the repository at dir.
func (c *GoGitClient) HeadCommit(_ context.Context, dir string) (string, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("open repository %s: %w", dir, err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("read HEAD in %s: %w", dir, err)
	}
	return head.Hash().String(), nil
}

// GetPRMergeCommit returns merge_commit_sha of a merged pull request.
func (g *GitHubProvider) GetPRMergeCommit(ctx context.Context, repo string, prNumber int) (string, error) {
	var result struct {
		MergeCommitSHA string `json:"merge_commit_sha"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, prNumber), nil, &result); err != nil {
		return "", fmt.Errorf("get PR merge commit %s#%d: %w", repo, prNumber, err)
	}
	return result.MergeCommitSHA, nil
}

// GetPRMergeCommit returns the commit a merged GitLab merge request produced:
// merge_commit_sha, or squash_commit_sha for squash merges, or the MR head sha
// for fast-forward merges.
func (g *GitLabProvider) GetPRMergeCommit(ctx context.Context, repo string, prNumber int) (string, error) {
	var result struct {
		MergeCommitSHA  string `json:"merge_commit_sha"`
		SquashCommitSHA string `json:"squash_commit_sha"`
		SHA             string `json:"sha"`
	}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", encodeProjectID(repo), prNumber)
	if err := g.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return "", fmt.Errorf("get MR merge commit %s!%d: %w", repo, prNumber, err)
	}
	for _, sha := range []string{result.MergeCommitSHA, result.SquashCommitSHA, result.SHA} {
		if sha != "" {
			return sha, nil
		}
	}
	return "", nil
}

// GetPRMergeCommit returns merge_commit_sha of a merged Forgejo/Gitea pull request.
func (f *ForgejoProvider) GetPRMergeCommit(ctx context.Context, repo string, prNumber int) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	var result struct {
		MergeCommitSHA string `json:"merge_commit_sha"`
	}
	if err := f.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", owner, name, prNumber), nil, &result); err != nil {
		return "", fmt.Errorf("get PR merge commit %s#%d: %w", repo, prNumber, err)
	}
	return result.MergeCommitSHA, nil
}

// GetPRMergeCommit returns merge_commit.hash of a merged Bitbucket pull
// request. Bitbucket reports an abbreviated hash; health checks compare
// revisions by prefix.
func (b *BitbucketProvider) GetPRMergeCommit(ctx context.Context, repo string, prNumber int) (string, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return "", err
	}
	var result struct {
		MergeCommit *struct {
			Hash string `json:"hash"`
		} `json:"merge_commit"`
	}
	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d", workspace, repoSlug, prNumber)
	if err := b.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return "", fmt.Errorf("get Bitbucket PR merge commit %s#%d: %w", repo, prNumber, err)
	}
	if result.MergeCommit == nil {
		return "", nil
	}
	return result.MergeCommit.Hash, nil
}

// GetPRMergeCommit returns lastMergeCommit.commitId of a completed Azure DevOps pull request.
func (a *AzureDevOpsProvider) GetPRMergeCommit(ctx context.Context, repo string, prNumber int) (string, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return "", err
	}
	var result struct {
		LastMergeCommit *struct {
			CommitID string `json:"commitId"`
		} `json:"lastMergeCommit"`
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d?api-version=%s",
		org, project, repoName, prNumber, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return "", fmt.Errorf("get ADO PR merge commit %s#%d: %w", repo, prNumber, err)
	}
	if result.LastMergeCommit == nil {
		return "", nil
	}
	return result.LastMergeCommit.CommitID, nil
}

// GetPRMergeCommit forwards to the active provider.
func (d *DynamicProvider) GetPRMergeCommit(ctx context.Context, repo string, prNumber int) (string, error) {
	g, ok := d.current().(MergeCommitGetter)
	if !ok {
		return "", nil
	}
	return g.GetPRMergeCommit(ctx, repo, prNumber)
}

var (
	_ HeadCommitReader  = (*GoGitClient)(nil)
	_ MergeCommitGetter = (*GitHubProvider)(nil)
	_ MergeCommitGetter = (*GitLabProvider)(nil)
	_ MergeCommitGetter = (*ForgejoProvider)(nil)
	_ MergeCommitGetter = (*BitbucketProvider)(nil)
	_ MergeCommitGetter = (*AzureDevOpsProvider)(nil)
	_ MergeCommitGetter = (*DynamicProvider)(nil)
)
