// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Brancher is a Server on which the test runner can make branches and open
// PRs itself, for example a second PR from kardinal's branch into another
// base. Every Server implements it.
type Brancher interface {
	// CreateBranch creates branch at the head of from.
	CreateBranch(ctx context.Context, r Repo, branch, from string) error
	// DeleteBranch deletes branch. A missing branch is not an error.
	DeleteBranch(ctx context.Context, r Repo, branch string) error
	// BranchHead returns the commit at the head of branch.
	BranchHead(ctx context.Context, r Repo, branch string) (string, error)
	// OpenPR opens a PR from head into base as the test runner.
	OpenPR(ctx context.Context, r Repo, head, base, title string) (PR, error)
}

var (
	_ Brancher = (*forgejo)(nil)
	_ Brancher = (*gitlab)(nil)
	_ Brancher = (*github)(nil)
)

func (f *forgejo) CreateBranch(ctx context.Context, r Repo, branch, from string) error {
	return f.do(ctx, http.MethodPost, f.repoPath(r)+"/branches",
		map[string]string{"new_branch_name": branch, "old_branch_name": from}, nil)
}

// DeleteBranch: Forgejo answers 500 "object does not exist", not 404, to the
// delete of a branch that is not there (B90), so after a failed delete the
// branch is read, and a branch that reads 404 is gone.
func (f *forgejo) DeleteBranch(ctx context.Context, r Repo, branch string) error {
	path := f.repoPath(r) + "/branches/" + segments(branch)
	err := f.do(ctx, http.MethodDelete, path, nil, nil)
	if err == nil || IsNotFound(err) {
		return nil
	}
	if IsNotFound(f.do(ctx, http.MethodGet, path, nil, nil)) {
		return nil
	}
	return err
}

func (f *forgejo) BranchHead(ctx context.Context, r Repo, branch string) (string, error) {
	r.Branch = branch
	return f.BranchSHA(ctx, r)
}

func (f *forgejo) OpenPR(ctx context.Context, r Repo, head, base, title string) (PR, error) {
	var p forgejoPR
	err := f.do(ctx, http.MethodPost, f.repoPath(r)+"/pulls",
		map[string]string{"head": head, "base": base, "title": title}, &p)
	return p.pr(), err
}

// segments escapes each segment of a branch name, keeping the slashes:
// Forgejo routes /branches/* by path, and GitLab wants the name as one
// escaped segment instead.
func segments(branch string) string {
	parts := strings.Split(branch, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func (g *gitlab) CreateBranch(ctx context.Context, r Repo, branch, from string) error {
	return g.do(ctx, http.MethodPost, g.projectPath(r)+"/repository/branches?"+
		url.Values{"branch": {branch}, "ref": {from}}.Encode(), nil, nil)
}

func (g *gitlab) DeleteBranch(ctx context.Context, r Repo, branch string) error {
	err := g.do(ctx, http.MethodDelete, g.projectPath(r)+"/repository/branches/"+url.PathEscape(branch), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (g *gitlab) BranchHead(ctx context.Context, r Repo, branch string) (string, error) {
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	err := g.do(ctx, http.MethodGet, g.projectPath(r)+"/repository/branches/"+url.PathEscape(branch), nil, &b)
	return b.Commit.ID, err
}

func (g *gitlab) OpenPR(ctx context.Context, r Repo, head, base, title string) (PR, error) {
	var m gitlabMR
	err := g.do(ctx, http.MethodPost, g.projectPath(r)+"/merge_requests",
		map[string]string{"source_branch": head, "target_branch": base, "title": title}, &m)
	return m.pr(), err
}

// CreateBranch on GitHub only makes branches under BranchPrefix, so a test
// can never create (or later delete) a branch it does not own.
func (g *github) CreateBranch(ctx context.Context, _ Repo, branch, from string) error {
	if !strings.HasPrefix(branch, BranchPrefix) {
		return fmt.Errorf("refusing to create %s: not under %s", branch, BranchPrefix)
	}
	sha, err := g.BranchHead(ctx, Repo{}, from)
	if err != nil {
		return err
	}
	return g.do(ctx, http.MethodPost, g.repoPath()+"/git/refs",
		map[string]string{"ref": "refs/heads/" + branch, "sha": sha}, nil)
}

// DeleteBranch on GitHub only deletes branches under BranchPrefix.
func (g *github) DeleteBranch(ctx context.Context, _ Repo, branch string) error {
	if !strings.HasPrefix(branch, BranchPrefix) {
		return fmt.Errorf("refusing to delete %s: not under %s", branch, BranchPrefix)
	}
	return g.deleteBranch(ctx, branch)
}

func (g *github) BranchHead(ctx context.Context, _ Repo, branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := g.do(ctx, http.MethodGet, g.repoPath()+"/git/ref/heads/"+branch, nil, &ref)
	return ref.Object.SHA, err
}

func (g *github) OpenPR(ctx context.Context, _ Repo, head, base, title string) (PR, error) {
	var p githubPR
	err := g.do(ctx, http.MethodPost, g.repoPath()+"/pulls",
		map[string]string{"head": head, "base": base, "title": title}, &p)
	return p.pr(), err
}
