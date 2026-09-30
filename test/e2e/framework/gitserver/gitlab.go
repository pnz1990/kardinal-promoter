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

// gitlab drives GitLab CE through /api/v4.
type gitlab struct {
	client
}

func (g *gitlab) Kind() string { return "gitlab" }

func (g *gitlab) CreateRepo(ctx context.Context, name string, files map[string][]byte) (Repo, error) {
	r := Repo{Owner: g.owner, Name: name, Branch: "main",
		CloneURL: fmt.Sprintf("%s/%s/%s.git", g.cloneBase, g.owner, name)}
	var ns struct {
		ID int `json:"id"`
	}
	if err := g.do(ctx, http.MethodGet, "/api/v4/namespaces/"+url.PathEscape(g.owner), nil, &ns); err != nil {
		return Repo{}, err
	}
	err := g.do(ctx, http.MethodPost, "/api/v4/projects", map[string]interface{}{
		"name": name, "path": name, "namespace_id": ns.ID, "default_branch": "main",
		"initialize_with_readme": true, "visibility": "public",
	}, nil)
	if err != nil {
		return Repo{}, err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	actions := make([]map[string]string, 0, len(paths))
	for _, p := range paths {
		actions = append(actions, map[string]string{
			"action": "create", "file_path": p, "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString(files[p]),
		})
	}
	err = g.do(ctx, http.MethodPost, g.projectPath(r)+"/repository/commits", map[string]interface{}{
		"branch": "main", "commit_message": "seed e2e fixture", "actions": actions,
	}, nil)
	return r, err
}

func (g *gitlab) DeleteRepo(ctx context.Context, r Repo) error {
	err := g.do(ctx, http.MethodDelete, g.projectPath(r), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (g *gitlab) ReadFile(ctx context.Context, r Repo, ref, path string) ([]byte, error) {
	var raw []byte
	err := g.do(ctx, http.MethodGet,
		g.projectPath(r)+"/repository/files/"+url.PathEscape(path)+"/raw?ref="+url.QueryEscape(ref), nil, &raw)
	return raw, err
}

type gitlabMR struct {
	IID          int      `json:"iid"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	State        string   `json:"state"`
	SourceBranch string   `json:"source_branch"`
	TargetBranch string   `json:"target_branch"`
	Labels       []string `json:"labels"`
	WebURL       string   `json:"web_url"`
}

func (g *gitlab) PullRequests(ctx context.Context, r Repo) ([]PR, error) {
	var out []PR
	for page := 1; ; page++ {
		var mrs []gitlabMR
		if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests?state=all&target_branch=%s&per_page=100&page=%d",
			g.projectPath(r), url.QueryEscape(r.Branch), page), nil, &mrs); err != nil {
			return nil, err
		}
		for _, m := range mrs {
			state := m.State
			if state == "opened" {
				state = "open"
			}
			out = append(out, PR{Number: m.IID, Title: m.Title, Body: m.Description, Head: m.SourceBranch,
				Base: m.TargetBranch, State: state, Labels: m.Labels, URL: m.WebURL})
		}
		if len(mrs) < 100 {
			return out, nil
		}
	}
}

func (g *gitlab) MergePR(ctx context.Context, r Repo, number int) error {
	return g.do(ctx, http.MethodPut, fmt.Sprintf("%s/merge_requests/%d/merge", g.projectPath(r), number), nil, nil)
}

func (g *gitlab) ClosePR(ctx context.Context, r Repo, number int) error {
	return g.do(ctx, http.MethodPut, fmt.Sprintf("%s/merge_requests/%d", g.projectPath(r), number),
		map[string]string{"state_event": "close"}, nil)
}

// AddWebhook needs the instance setting allow_local_requests_from_web_hooks_and_services,
// which hack/e2e/components/gitlab.sh turns on, to deliver to an in-cluster URL.
func (g *gitlab) AddWebhook(ctx context.Context, r Repo, hookURL, secret string) error {
	return g.do(ctx, http.MethodPost, g.projectPath(r)+"/hooks", map[string]interface{}{
		"url": hookURL, "token": secret, "merge_requests_events": true, "push_events": false,
		"enable_ssl_verification": false,
	}, nil)
}

// projectPath addresses the project by its URL-encoded full path, so no
// numeric ID has to be looked up.
func (g *gitlab) projectPath(r Repo) string {
	return "/api/v4/projects/" + url.PathEscape(r.Owner+"/"+r.Name)
}
