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
// error for any other server.

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

// RepoFor returns the Repo CreateRepo would return for name, without
// creating it: a test can point a Pipeline at a repo that does not exist yet.
func RepoFor(s Server, name string) (Repo, error) {
	f, err := asForgejo(s)
	if err != nil {
		return Repo{}, err
	}
	return Repo{Owner: f.owner, Name: name, Branch: "main",
		CloneURL: fmt.Sprintf("%s/%s/%s.git", f.cloneBase, f.owner, name)}, nil
}

// Commits returns the newest limit commits of branch, newest first.
func Commits(ctx context.Context, s Server, r Repo, branch string, limit int) ([]Commit, error) {
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
	path := fmt.Sprintf("%s/commits?sha=%s&limit=%d&stat=false&verification=false&files=false",
		f.repoPath(r), url.QueryEscape(branch), limit)
	if err := f.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Commit, 0, len(raw))
	for _, c := range raw {
		out = append(out, Commit{SHA: c.SHA, Message: c.Commit.Message,
			AuthorName: c.Commit.Author.Name, AuthorEmail: c.Commit.Author.Email})
	}
	return out, nil
}

// CommitFiles commits files (path to content) in one commit on branch, or on
// newBranch created from branch when newBranch is set, and returns the commit
// SHA. Existing files are updated, new ones created.
func CommitFiles(ctx context.Context, s Server, r Repo, branch, newBranch, message string, files map[string][]byte) (string, error) {
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
	f, err := asForgejo(s)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%s/%s/%s.git", f.api, r.Owner, r.Name), f.token, nil
}
