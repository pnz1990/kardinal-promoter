// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ErrNoWebhookDelivery means the server can't reach the cluster, so webhook
// tests must post signed payloads themselves.
var ErrNoWebhookDelivery = errors.New("github.com can't deliver webhooks to a kind cluster")

// BranchPrefix prefixes the branch each GitHub test gets in the shared repo.
// DeleteRepo only deletes branches under it, plus the PR head branches that
// target them.
const BranchPrefix = "e2e/"

// github gives each test an orphan branch of one shared repo, so the suite
// needs one repo and one token rather than permission to create repos.
type github struct {
	client
	repo string // owner/name
}

func (g *github) Kind() string { return "github" }

func (g *github) CreateRepo(ctx context.Context, name string, files map[string][]byte) (Repo, error) {
	owner, repoName, _ := strings.Cut(g.repo, "/")
	r := Repo{Owner: owner, Name: repoName, Branch: BranchPrefix + name,
		CloneURL: fmt.Sprintf("%s/%s.git", g.cloneBase, g.repo)}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	tree := make([]map[string]string, 0, len(paths))
	for _, p := range paths {
		tree = append(tree, map[string]string{"path": p, "mode": "100644", "type": "blob", "content": string(files[p])})
	}
	var sha struct {
		SHA string `json:"sha"`
	}
	if err := g.do(ctx, http.MethodPost, g.repoPath()+"/git/trees", map[string]interface{}{"tree": tree}, &sha); err != nil {
		return Repo{}, err
	}
	if err := g.do(ctx, http.MethodPost, g.repoPath()+"/git/commits", map[string]interface{}{
		"message": "seed e2e fixture", "tree": sha.SHA, "parents": []string{},
	}, &sha); err != nil {
		return Repo{}, err
	}
	err := g.do(ctx, http.MethodPost, g.repoPath()+"/git/refs", map[string]string{
		"ref": "refs/heads/" + r.Branch, "sha": sha.SHA,
	}, nil)
	return r, err
}

// DeleteRepo closes the PRs that target the test branch, deletes their head
// branches, then deletes the test branch.
func (g *github) DeleteRepo(ctx context.Context, r Repo) error {
	if !strings.HasPrefix(r.Branch, BranchPrefix) {
		return fmt.Errorf("refusing to delete %s: not under %s", r.Branch, BranchPrefix)
	}
	prs, err := g.PullRequests(ctx, r)
	if err != nil {
		return err
	}
	var errs []error
	for _, pr := range prs {
		if pr.State == "open" {
			if err := g.ClosePR(ctx, r, pr.Number); err != nil {
				errs = append(errs, err)
			}
		}
		errs = append(errs, g.deleteBranch(ctx, pr.Head))
	}
	errs = append(errs, g.deleteBranch(ctx, r.Branch))
	return errors.Join(errs...)
}

func (g *github) deleteBranch(ctx context.Context, branch string) error {
	err := g.do(ctx, http.MethodDelete, g.repoPath()+"/git/refs/heads/"+branch, nil, nil)
	// 422 is "Reference does not exist" (already deleted, e.g. on merge).
	if se, ok := err.(*StatusError); ok && (se.Code == http.StatusNotFound || se.Code == http.StatusUnprocessableEntity) {
		return nil
	}
	return err
}

func (g *github) ReadFile(ctx context.Context, r Repo, ref, path string) ([]byte, error) {
	var file struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := g.do(ctx, http.MethodGet, g.repoPath()+"/contents/"+path+"?ref="+url.QueryEscape(ref), nil, &file); err != nil {
		return nil, err
	}
	if file.Encoding != "base64" {
		return nil, fmt.Errorf("%s@%s: unexpected encoding %q", path, ref, file.Encoding)
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
}

type githubPR struct {
	Number   int     `json:"number"`
	Title    string  `json:"title"`
	Body     string  `json:"body"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
	HTMLURL  string  `json:"html_url"`
	Head     struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (g *github) PullRequests(ctx context.Context, r Repo) ([]PR, error) {
	var out []PR
	for page := 1; ; page++ {
		var prs []githubPR
		if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s/pulls?state=all&base=%s&per_page=100&page=%d",
			g.repoPath(), url.QueryEscape(r.Branch), page), nil, &prs); err != nil {
			return nil, err
		}
		for _, p := range prs {
			pr := PR{Number: p.Number, Title: p.Title, Body: p.Body, Head: p.Head.Ref,
				Base: p.Base.Ref, State: p.State, URL: p.HTMLURL}
			if p.MergedAt != nil {
				pr.State = "merged"
			}
			for _, l := range p.Labels {
				pr.Labels = append(pr.Labels, l.Name)
			}
			out = append(out, pr)
		}
		if len(prs) < 100 {
			return out, nil
		}
	}
}

func (g *github) MergePR(ctx context.Context, _ Repo, number int) error {
	return g.do(ctx, http.MethodPut, fmt.Sprintf("%s/pulls/%d/merge", g.repoPath(), number),
		map[string]string{"merge_method": "merge"}, nil)
}

func (g *github) ClosePR(ctx context.Context, _ Repo, number int) error {
	return g.setState(ctx, number, "closed")
}

func (g *github) ReopenPR(ctx context.Context, _ Repo, number int) error {
	return g.setState(ctx, number, "open")
}

func (g *github) setState(ctx context.Context, number int, state string) error {
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("%s/pulls/%d", g.repoPath(), number),
		map[string]string{"state": state}, nil)
}

func (g *github) Comments(ctx context.Context, _ Repo, number int) ([]string, error) {
	var comments []struct {
		Body string `json:"body"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s/issues/%d/comments?per_page=100", g.repoPath(), number), nil, &comments); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(comments))
	for _, c := range comments {
		out = append(out, c.Body)
	}
	return out, nil
}

func (g *github) AddWebhook(context.Context, Repo, string, string) error {
	return ErrNoWebhookDelivery
}

func (g *github) repoPath() string { return "/repos/" + g.repo }
