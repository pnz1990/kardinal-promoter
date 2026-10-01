// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// CommitFiles (forgejo_flow.go) on GitLab and GitHub.

// filePaths are the paths of files, sorted.
func filePaths(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// commitFiles commits files to branch, or to newBranch made from branch,
// with one call to the commits API: each file is created or updated as it
// exists on branch.
func (g *gitlab) commitFiles(ctx context.Context, r Repo, branch, newBranch, message string, files map[string][]byte) (string, error) {
	actions := make([]map[string]string, 0, len(files))
	for _, p := range filePaths(files) {
		action := "update"
		err := g.do(ctx, http.MethodGet,
			g.projectPath(r)+"/repository/files/"+url.PathEscape(p)+"?ref="+url.QueryEscape(branch), nil, nil)
		switch {
		case IsNotFound(err):
			action = "create"
		case err != nil:
			return "", fmt.Errorf("stat %s: %w", p, err)
		}
		actions = append(actions, map[string]string{"action": action, "file_path": p, "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString(files[p])})
	}
	body := map[string]interface{}{"branch": branch, "commit_message": message, "actions": actions}
	if newBranch != "" {
		body["branch"], body["start_branch"] = newBranch, branch
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := g.do(ctx, http.MethodPost, g.projectPath(r)+"/repository/commits", body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("commit to %s: no commit SHA in the response", r.Name)
	}
	return out.ID, nil
}

// commitFiles commits files on top of branch through the git data API and
// moves branch, or creates newBranch, to the commit. Like CreateBranch, it
// writes only branches under BranchPrefix.
func (g *github) commitFiles(ctx context.Context, branch, newBranch, message string, files map[string][]byte) (string, error) {
	target := branch
	if newBranch != "" {
		target = newBranch
	}
	if !strings.HasPrefix(target, BranchPrefix) {
		return "", fmt.Errorf("refusing to commit to %s: not under %s", target, BranchPrefix)
	}
	parent, err := g.BranchHead(ctx, Repo{}, branch)
	if err != nil {
		return "", err
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := g.do(ctx, http.MethodGet, g.repoPath()+"/git/commits/"+parent, nil, &commit); err != nil {
		return "", err
	}
	tree := make([]map[string]string, 0, len(files))
	for _, p := range filePaths(files) {
		tree = append(tree, map[string]string{"path": p, "mode": "100644", "type": "blob", "content": string(files[p])})
	}
	var sha struct {
		SHA string `json:"sha"`
	}
	if err := g.do(ctx, http.MethodPost, g.repoPath()+"/git/trees",
		map[string]interface{}{"base_tree": commit.Tree.SHA, "tree": tree}, &sha); err != nil {
		return "", err
	}
	if err := g.do(ctx, http.MethodPost, g.repoPath()+"/git/commits", map[string]interface{}{
		"message": message, "tree": sha.SHA, "parents": []string{parent},
	}, &sha); err != nil {
		return "", err
	}
	if newBranch != "" {
		err = g.do(ctx, http.MethodPost, g.repoPath()+"/git/refs",
			map[string]string{"ref": "refs/heads/" + newBranch, "sha": sha.SHA}, nil)
	} else {
		err = g.do(ctx, http.MethodPatch, g.repoPath()+"/git/refs/heads/"+branch,
			map[string]interface{}{"sha": sha.SHA, "force": false}, nil)
	}
	return sha.SHA, err
}
