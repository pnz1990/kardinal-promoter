// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"fmt"
	"net/http"
	"sort"
)

// CommitStatus is one commit status as the git server reports it.
type CommitStatus struct {
	Context     string
	State       string // pending, success, failure (GitLab: failed)
	Description string
}

// StatusReader is a Server that lists a commit's statuses.
type StatusReader interface {
	// CommitStatuses returns the statuses of commit sha, newest first.
	CommitStatuses(ctx context.Context, r Repo, sha string) ([]CommitStatus, error)
}

// CommitStatuses lists GET /repos/{owner}/{repo}/commits/{sha}/statuses
// (Forgejo and Gitea).
func (f *forgejo) CommitStatuses(ctx context.Context, r Repo, sha string) ([]CommitStatus, error) {
	var raw []struct {
		ID          int64  `json:"id"`
		Context     string `json:"context"`
		Status      string `json:"status"`
		Description string `json:"description"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("%s/commits/%s/statuses?sort=newest", f.repoPath(r), sha), nil, &raw); err != nil {
		return nil, fmt.Errorf("list statuses of %s: %w", sha, err)
	}
	// sort=newest orders by the creation second; two statuses set within
	// one second come back in either order, so the ID decides.
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].ID > raw[j].ID })
	out := make([]CommitStatus, len(raw))
	for i, s := range raw {
		out[i] = CommitStatus{Context: s.Context, State: s.Status, Description: s.Description}
	}
	return out, nil
}

// CommitStatuses lists GET /projects/:id/repository/commits/:sha/statuses
// (GitLab; the context is the status name).
func (g *gitlab) CommitStatuses(ctx context.Context, r Repo, sha string) ([]CommitStatus, error) {
	var raw []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Status      string `json:"status"`
		Description string `json:"description"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s/repository/commits/%s/statuses", g.projectPath(r), sha), nil, &raw); err != nil {
		return nil, fmt.Errorf("list statuses of %s: %w", sha, err)
	}
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].ID > raw[j].ID })
	out := make([]CommitStatus, len(raw))
	for i, s := range raw {
		out[i] = CommitStatus{Context: s.Name, State: s.Status, Description: s.Description}
	}
	return out, nil
}
