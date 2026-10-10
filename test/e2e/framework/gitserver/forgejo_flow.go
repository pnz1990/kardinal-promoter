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
)

// This file holds the git server operations the flow suites (graph, bundle,
// pipeline, step) need beyond the Server interface: reading commits, adding
// commits the way a developer or CI would, and changing repo visibility.
// They work on Forgejo and Gitea, the core suite's git servers, and return an
// error for any other server. CommitFiles also works on GitLab and GitHub
// (commit_files.go), whose suites run the TestCore_ tests too.

// Commit is one commit as the git server reports it.
type Commit struct {
	SHA         string
	Message     string
	AuthorName  string
	AuthorEmail string
}

func asForgejo(s Server) (*forgejo, error) {
	f, ok := s.(*forgejo)
	if !ok {
		return nil, fmt.Errorf("%s: only Forgejo and Gitea are supported", s.Kind())
	}
	return f, nil
}

// Commits returns the newest limit commits of branch, newest first.
// Forgejo, Gitea, GitLab and GitHub are supported.
func Commits(ctx context.Context, s Server, r Repo, branch string, limit int) ([]Commit, error) {
	switch g := s.(type) {
	case *gitlab:
		return g.commits(ctx, r, branch, limit)
	case *github:
		return g.commits(ctx, branch, limit)
	}
	f, err := asForgejo(s)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"author"`
		} `json:"commit"`
	}
	// The server returns at most 50 commits a page.
	const pageSize = 50
	var out []Commit
	for page := 1; len(out) < limit; page++ {
		raw = raw[:0]
		path := fmt.Sprintf("%s/commits?sha=%s&limit=%d&page=%d&stat=false&verification=false&files=false",
			f.repoPath(r), url.QueryEscape(branch), min(pageSize, limit), page)
		if err := f.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		for _, c := range raw {
			out = append(out, Commit{SHA: c.SHA, Message: c.Commit.Message,
				AuthorName: c.Commit.Author.Name, AuthorEmail: c.Commit.Author.Email})
		}
		if len(raw) < min(pageSize, limit) {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CommitFiles commits files (path to content) in one commit on branch, or on
// newBranch created from branch when newBranch is set, and returns the commit
// SHA. Existing files are updated, new ones created. On GitHub the branch
// written must be under BranchPrefix.
func CommitFiles(ctx context.Context, s Server, r Repo, branch, newBranch, message string, files map[string][]byte) (string, error) {
	switch g := s.(type) {
	case *gitlab:
		return g.commitFiles(ctx, r, branch, newBranch, message, files)
	case *github:
		return g.commitFiles(ctx, branch, newBranch, message, files)
	}
	f, err := asForgejo(s)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	changes := make([]map[string]string, 0, len(paths))
	for _, p := range paths {
		change := map[string]string{"operation": "create", "path": p,
			"content": base64.StdEncoding.EncodeToString(files[p])}
		var existing struct {
			SHA string `json:"sha"`
		}
		err := f.do(ctx, http.MethodGet, f.repoPath(r)+"/contents/"+p+"?ref="+url.QueryEscape(branch), nil, &existing)
		switch {
		case err == nil:
			change["operation"], change["sha"] = "update", existing.SHA
		case !IsNotFound(err):
			return "", fmt.Errorf("stat %s: %w", p, err)
		}
		changes = append(changes, change)
	}
	body := map[string]interface{}{"branch": branch, "message": message, "files": changes}
	if newBranch != "" {
		body["new_branch"] = newBranch
	}
	var resp struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := f.do(ctx, http.MethodPost, f.repoPath(r)+"/contents", body, &resp); err != nil {
		return "", err
	}
	if resp.Commit.SHA == "" {
		return "", fmt.Errorf("commit to %s: no commit SHA in the response", r.Name)
	}
	return resp.Commit.SHA, nil
}

// SetPrivate makes the repo private (or public again): cloning it then needs
// credentials.
func SetPrivate(ctx context.Context, s Server, r Repo, private bool) error {
	f, err := asForgejo(s)
	if err != nil {
		return err
	}
	return f.do(ctx, http.MethodPatch, f.repoPath(r), map[string]interface{}{"private": private}, nil)
}

// PushRemote returns the URL the test runner pushes r to over HTTP and the
// token it authenticates with (the suite's admin token; never log it).
func PushRemote(s Server, r Repo) (remote, token string, err error) {
	if g, ok := s.(*github); ok {
		remote, token = g.pushRemote()
		return remote, token, nil
	}
	if g, ok := s.(*gitlab); ok {
		// GitLab takes a personal access token as the password with any
		// user name.
		return fmt.Sprintf("%s/%s/%s.git", g.api, r.Owner, r.Name), g.token, nil
	}
	f, err := asForgejo(s)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%s/%s/%s.git", f.api, r.Owner, r.Name), f.token, nil
}

// AddDeployKey adds a read-only deploy key (an authorized_keys line) to r,
// so a client with the private key can fetch r over SSH.
func AddDeployKey(ctx context.Context, s Server, r Repo, title, publicKey string) error {
	f, err := asForgejo(s)
	if err != nil {
		return err
	}
	return f.do(ctx, http.MethodPost, f.repoPath(r)+"/keys",
		map[string]interface{}{"title": title, "key": publicKey, "read_only": true}, nil)
}

// RawURL is the URL Forgejo serves r's files at on branch, in the cluster:
// <RawURL>/<path> is the file's content. Basic auth with a token reads a
// private repo's files.
func RawURL(s Server, r Repo, branch string) (string, error) {
	f, err := asForgejo(s)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/%s/raw/branch/%s", f.cloneBase, r.Owner, r.Name, branch), nil
}

// UpdateFile replaces the content of an existing file on r.Branch in one
// commit and returns its SHA.
func UpdateFile(ctx context.Context, s Server, r Repo, path, message string, content []byte) (string, error) {
	f, err := asForgejo(s)
	if err != nil {
		return "", err
	}
	var cur struct {
		SHA string `json:"sha"`
	}
	if err := f.do(ctx, http.MethodGet, f.repoPath(r)+"/contents/"+path+"?ref="+url.QueryEscape(r.Branch), nil, &cur); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := f.do(ctx, http.MethodPut, f.repoPath(r)+"/contents/"+path, map[string]interface{}{
		"branch": r.Branch, "message": message, "sha": cur.SHA, "content": base64.StdEncoding.EncodeToString(content),
	}, &out); err != nil {
		return "", fmt.Errorf("update %s: %w", path, err)
	}
	if out.Commit.SHA == "" {
		return "", fmt.Errorf("update %s: no commit SHA in the response", path)
	}
	return out.Commit.SHA, nil
}
