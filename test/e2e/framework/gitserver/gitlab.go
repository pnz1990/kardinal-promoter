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
	"time"
)

// gitlab drives GitLab CE through /api/v4.
type gitlab struct {
	client
	// retry is the MergePR retry interval; zero means 2s.
	retry time.Duration
}

func (g *gitlab) Kind() string { return "gitlab" }

func (g *gitlab) CreateRepo(ctx context.Context, name string, files map[string][]byte) (Repo, error) {
	return g.createIn(ctx, g.owner, name, files)
}

// createIn creates project name in namespace owner (a group path).
func (g *gitlab) createIn(ctx context.Context, owner, name string, files map[string][]byte) (Repo, error) {
	r := Repo{Owner: owner, Name: name, Branch: "main",
		CloneURL: fmt.Sprintf("%s/%s/%s.git", g.cloneBase, owner, name)}
	var ns struct {
		ID int `json:"id"`
	}
	if err := g.do(ctx, http.MethodGet, "/api/v4/namespaces/"+url.PathEscape(owner), nil, &ns); err != nil {
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

// gitlabDeleteTries is how many times DeleteRepo asks.
const gitlabDeleteTries = 3

// DeleteRepo deletes the project. GitLab can answer 500 when the delete
// races a post-receive job of the last push (NoRepository "repository not
// found" while it expires the project's caches), so a 5xx is retried.
func (g *gitlab) DeleteRepo(ctx context.Context, r Repo) error {
	for try := 1; ; try++ {
		err := g.do(ctx, http.MethodDelete, g.projectPath(r), nil, nil)
		if IsNotFound(err) {
			return nil
		}
		se, ok := err.(*StatusError)
		if err == nil || !ok || se.Code < 500 || try == gitlabDeleteTries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(g.retryEvery()):
		}
	}
}

func (g *gitlab) ReadFile(ctx context.Context, r Repo, ref, path string) ([]byte, error) {
	var raw []byte
	err := g.do(ctx, http.MethodGet,
		g.projectPath(r)+"/repository/files/"+url.PathEscape(path)+"/raw?ref="+url.QueryEscape(ref), nil, &raw)
	return raw, err
}

type gitlabMR struct {
	IID             int      `json:"iid"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	State           string   `json:"state"`
	SourceBranch    string   `json:"source_branch"`
	TargetBranch    string   `json:"target_branch"`
	Labels          []string `json:"labels"`
	WebURL          string   `json:"web_url"`
	SHA             string   `json:"sha"`
	MergeCommitSHA  string   `json:"merge_commit_sha"`
	SquashCommitSHA string   `json:"squash_commit_sha"`
	Author          struct {
		Username string `json:"username"`
	} `json:"author"`
}

// pr converts the API's MR. A fast-forward merge leaves no merge commit:
// the head commit is then what the merge put on the target branch.
func (m gitlabMR) pr() PR {
	pr := PR{Number: m.IID, Title: m.Title, Body: m.Description, Head: m.SourceBranch, Base: m.TargetBranch,
		State: m.State, Labels: m.Labels, URL: m.WebURL, Author: m.Author.Username, HeadSHA: m.SHA}
	switch pr.State {
	case "opened":
		pr.State = "open"
	case "merged":
		pr.MergeCommit = m.MergeCommitSHA
		if pr.MergeCommit == "" {
			pr.MergeCommit = m.SquashCommitSHA
		}
		if pr.MergeCommit == "" {
			pr.MergeCommit = m.SHA
		}
	}
	return pr
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
			out = append(out, m.pr())
		}
		if len(mrs) < 100 {
			return out, nil
		}
	}
}

// gitlabMergeRetry is how long MergePR retries while GitLab is still checking
// whether a new MR can be merged.
const gitlabMergeRetry = time.Minute

// MergePR merges the MR. GitLab computes a new MR's merge status in the
// background and refuses the merge until it has (405, 406 or 422 "Branch
// cannot be merged"; 409 while the head is still being updated), so those
// answers are retried for up to gitlabMergeRetry.
func (g *gitlab) MergePR(ctx context.Context, r Repo, number int) error {
	path := fmt.Sprintf("%s/merge_requests/%d/merge", g.projectPath(r), number)
	deadline := time.Now().Add(gitlabMergeRetry)
	for {
		err := g.do(ctx, http.MethodPut, path, nil, nil)
		se, ok := err.(*StatusError)
		if err == nil || !ok || !retryableMerge(se.Code) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(g.retryEvery()):
		}
	}
}

func retryableMerge(code int) bool {
	switch code {
	case http.StatusMethodNotAllowed, http.StatusNotAcceptable, http.StatusConflict, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

func (g *gitlab) retryEvery() time.Duration {
	if g.retry > 0 {
		return g.retry
	}
	return 2 * time.Second
}

func (g *gitlab) ClosePR(ctx context.Context, r Repo, number int) error {
	return g.do(ctx, http.MethodPut, fmt.Sprintf("%s/merge_requests/%d", g.projectPath(r), number),
		map[string]string{"state_event": "close"}, nil)
}

func (g *gitlab) ReopenPR(ctx context.Context, r Repo, number int) error {
	return g.do(ctx, http.MethodPut, fmt.Sprintf("%s/merge_requests/%d", g.projectPath(r), number),
		map[string]string{"state_event": "reopen"}, nil)
}

// Comments lists the MR's notes written by users; GitLab's own system notes
// ("closed", "added label") are left out.
func (g *gitlab) Comments(ctx context.Context, r Repo, number int) ([]string, error) {
	var notes []struct {
		Body   string `json:"body"`
		System bool   `json:"system"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests/%d/notes?sort=asc&order_by=created_at&per_page=100",
		g.projectPath(r), number), nil, &notes); err != nil {
		return nil, err
	}
	out := []string{}
	for _, n := range notes {
		if !n.System {
			out = append(out, n.Body)
		}
	}
	return out, nil
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
