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
	"time"
)

// forgejo drives Forgejo and Gitea, which share the /api/v1 API.
type forgejo struct {
	client
	kind string
	// retry is the wait between create tries (2s when zero; tests set it).
	retry time.Duration
}

func (f *forgejo) Kind() string { return f.kind }

// forgejoCreateTries is how many times CreateRepo tries a repo whose
// creation failed on a timeout or a server error.
const forgejoCreateTries = 3

// CreateRepo creates the repo and seeds it. Forgejo creates repositories one
// at a time; when many tests ask at once, a request can outlast the client's
// timeout while Forgejo still creates the repo (#1557). A create or seed that
// failed that way is retried from scratch: the half-made repo is deleted
// first, so the retry neither finds it "already exists" nor commits the seed
// twice.
func (f *forgejo) CreateRepo(ctx context.Context, name string, files map[string][]byte) (Repo, error) {
	r := Repo{Owner: f.owner, Name: name, Branch: "main",
		CloneURL: fmt.Sprintf("%s/%s/%s.git", f.cloneBase, f.owner, name)}
	var err error
	for try := 1; try <= forgejoCreateTries; try++ {
		if try > 1 {
			if derr := f.DeleteRepo(ctx, r); derr != nil && !retryableCreate(derr) {
				return Repo{}, fmt.Errorf("delete half-created repo %s before retrying: %w (after: %w)", name, derr, err)
			}
			select {
			case <-ctx.Done():
				return Repo{}, fmt.Errorf("%w (after: %w)", ctx.Err(), err)
			case <-time.After(f.createRetryEvery()):
			}
		}
		if err = f.createOnce(ctx, r, files); err == nil || !retryableCreate(err) || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		return Repo{}, err
	}
	return r, nil
}

func (f *forgejo) createRetryEvery() time.Duration {
	if f.retry > 0 {
		return f.retry
	}
	return 2 * time.Second
}

// retryableCreate reports whether a create request failed in a way a retry
// can fix: a timeout or another transport error, or a 5xx.
func retryableCreate(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code >= 500
	}
	var ue *url.Error
	return errors.As(err, &ue)
}

func (f *forgejo) createOnce(ctx context.Context, r Repo, files map[string][]byte) error {
	// auto_init gives the repo a first commit on main, so the multi-file
	// contents API below has a branch to commit to.
	err := f.do(ctx, http.MethodPost, "/api/v1/orgs/"+f.owner+"/repos", map[string]interface{}{
		"name": r.Name, "default_branch": "main", "auto_init": true, "private": false,
	}, nil)
	if err != nil {
		return err
	}
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
	return f.do(ctx, http.MethodPost, f.repoPath(r)+"/contents", map[string]interface{}{
		"branch": "main", "message": "seed e2e fixture", "files": changes,
	}, nil)
}

func (f *forgejo) DeleteRepo(ctx context.Context, r Repo) error {
	err := f.do(ctx, http.MethodDelete, f.repoPath(r), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (f *forgejo) ReadFile(ctx context.Context, r Repo, ref, path string) ([]byte, error) {
	var raw []byte
	err := f.do(ctx, http.MethodGet, f.repoPath(r)+"/raw/"+path+"?ref="+url.QueryEscape(ref), nil, &raw)
	return raw, err
}

type forgejoPR struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	HTMLURL        string `json:"html_url"`
	User           struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (f *forgejo) PullRequests(ctx context.Context, r Repo) ([]PR, error) {
	var out []PR
	for page := 1; ; page++ {
		var prs []forgejoPR
		if err := f.do(ctx, http.MethodGet,
			fmt.Sprintf("%s/pulls?state=all&limit=50&page=%d", f.repoPath(r), page), nil, &prs); err != nil {
			return nil, err
		}
		for _, p := range prs {
			if p.Base.Ref == r.Branch {
				out = append(out, p.pr())
			}
		}
		if len(prs) < 50 {
			return out, nil
		}
	}
}

func (p forgejoPR) pr() PR {
	pr := PR{Number: p.Number, Title: p.Title, Body: p.Body, Head: p.Head.Ref, Base: p.Base.Ref,
		State: p.State, URL: p.HTMLURL, Author: p.User.Login, HeadSHA: p.Head.SHA}
	if p.Merged {
		pr.State = "merged"
		pr.MergeCommit = p.MergeCommitSHA
	}
	for _, l := range p.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}
	return pr
}

// How long, and how often, MergePR retries while the server is still checking
// whether the PR can be merged.
var mergeRetry, mergeRetryEvery = 30 * time.Second, time.Second

// MergePR merges the PR. Gitea and Forgejo answer 405 "Please try again
// later" while they check a new PR's mergeability, so that answer is retried.
func (f *forgejo) MergePR(ctx context.Context, r Repo, number int) error {
	deadline := time.Now().Add(mergeRetry)
	for {
		err := f.do(ctx, http.MethodPost, fmt.Sprintf("%s/pulls/%d/merge", f.repoPath(r), number),
			map[string]string{"Do": "merge"}, nil)
		var se *StatusError
		if !errors.As(err, &se) || se.Code != http.StatusMethodNotAllowed ||
			!strings.Contains(se.Body, "try again later") || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(mergeRetryEvery):
		}
	}
}

func (f *forgejo) ClosePR(ctx context.Context, r Repo, number int) error {
	return f.setState(ctx, r, number, "closed")
}

func (f *forgejo) ReopenPR(ctx context.Context, r Repo, number int) error {
	return f.setState(ctx, r, number, "open")
}

func (f *forgejo) setState(ctx context.Context, r Repo, number int, state string) error {
	return f.do(ctx, http.MethodPatch, fmt.Sprintf("%s/pulls/%d", f.repoPath(r), number),
		map[string]string{"state": state}, nil)
}

// Comments reads the PR's issue comments; Forgejo keeps PR conversation
// comments on the issue with the same number.
func (f *forgejo) Comments(ctx context.Context, r Repo, number int) ([]string, error) {
	var comments []struct {
		Body string `json:"body"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("%s/issues/%d/comments", f.repoPath(r), number), nil, &comments); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(comments))
	for _, c := range comments {
		out = append(out, c.Body)
	}
	return out, nil
}

func (f *forgejo) AddWebhook(ctx context.Context, r Repo, hookURL, secret string) error {
	return f.do(ctx, http.MethodPost, f.repoPath(r)+"/hooks", map[string]interface{}{
		"type":   f.kind,
		"active": true,
		"events": []string{"pull_request"},
		"config": map[string]string{"url": hookURL, "content_type": "json", "secret": secret},
	}, nil)
}

func (f *forgejo) repoPath(r Repo) string {
	return "/api/v1/repos/" + r.Owner + "/" + r.Name
}
