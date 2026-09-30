// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
)

// Committer is a Server that can commit to a repo's branch and report its
// head, as a developer pushing to it would. Forgejo and Gitea implement it.
type Committer interface {
	// CommitFiles adds files (paths that do not exist yet) to r.Branch in one
	// commit and returns its SHA.
	CommitFiles(ctx context.Context, r Repo, message string, files map[string][]byte) (string, error)
	// BranchSHA is the commit r.Branch points at.
	BranchSHA(ctx context.Context, r Repo) (string, error)
}

// HookTester is a Server that can register a webhook and have the server
// send it a test delivery (a signed push event for the branch head).
// Forgejo and Gitea implement it.
type HookTester interface {
	TestWebhook(ctx context.Context, r Repo, url, secret string) error
}

func (f *forgejo) CommitFiles(ctx context.Context, r Repo, message string, files map[string][]byte) (string, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	changes := make([]map[string]string, 0, len(paths))
	for _, p := range paths {
		changes = append(changes, map[string]string{
			"operation": "create", "path": p, "content": base64.StdEncoding.EncodeToString(files[p]),
		})
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := f.do(ctx, http.MethodPost, f.repoPath(r)+"/contents", map[string]interface{}{
		"branch": r.Branch, "message": message, "files": changes,
	}, &out); err != nil {
		return "", err
	}
	if out.Commit.SHA == "" {
		return "", fmt.Errorf("commit to %s/%s: no commit SHA in the response", r.Name, r.Branch)
	}
	return out.Commit.SHA, nil
}

func (f *forgejo) BranchSHA(ctx context.Context, r Repo) (string, error) {
	var out struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := f.do(ctx, http.MethodGet, f.repoPath(r)+"/branches/"+r.Branch, nil, &out); err != nil {
		return "", err
	}
	return out.Commit.ID, nil
}

func (f *forgejo) TestWebhook(ctx context.Context, r Repo, url, secret string) error {
	var hook struct {
		ID int64 `json:"id"`
	}
	if err := f.do(ctx, http.MethodPost, f.repoPath(r)+"/hooks", map[string]interface{}{
		"type":   f.kind,
		"active": true,
		"events": []string{"push"},
		"config": map[string]string{"url": url, "content_type": "json", "secret": secret},
	}, &hook); err != nil {
		return err
	}
	return f.do(ctx, http.MethodPost, fmt.Sprintf("%s/hooks/%d/tests", f.repoPath(r), hook.ID), nil, nil)
}
