// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package gitserver drives the git server a live e2e suite promotes through:
// an in-cluster Forgejo, Gitea or GitLab, or real GitHub. Tests use it to seed
// a GitOps repo, read what the controller committed, and act as the reviewer
// (merge or close the PRs the controller opens).
package gitserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Environment variables hack/e2e/up.sh sets for the suite's git server.
const (
	EnvKind      = "KARDINAL_E2E_GIT_KIND"       // forgejo, gitea, gitlab or github
	EnvAPI       = "KARDINAL_E2E_GIT_API"        // API base URL reachable from the test runner
	EnvCloneBase = "KARDINAL_E2E_GIT_CLONE_BASE" // base URL the controller clones from
	EnvOwner     = "KARDINAL_E2E_GIT_OWNER"      // user or org that owns test repos
	EnvToken     = "KARDINAL_E2E_GIT_TOKEN"      // API token (never logged)
	EnvRepo      = "KARDINAL_E2E_GIT_REPO"       // github only: the shared repo test branches live in
)

// Repo is a GitOps repo (or, on GitHub, a branch of the shared repo) created
// for one test.
type Repo struct {
	Owner string
	Name  string
	// Branch is the Pipeline's spec.git.branch. PRs the controller opens target it.
	Branch string
	// CloneURL is the Pipeline's spec.git.url.
	CloneURL string
	// id is the provider's own handle (GitLab project ID).
	id string
}

// PR is a pull or merge request as the provider reports it.
type PR struct {
	Number int
	Title  string
	Body   string
	Head   string
	Base   string
	// State is open, closed or merged.
	State  string
	Labels []string
	URL    string
}

// Server is one git server.
type Server interface {
	Kind() string
	// CreateRepo creates a repo (a branch on GitHub) named after name, holding
	// files on its default branch.
	CreateRepo(ctx context.Context, name string, files map[string][]byte) (Repo, error)
	// DeleteRepo removes the repo, or on GitHub the branch and the PR head
	// branches that target it.
	DeleteRepo(ctx context.Context, r Repo) error
	ReadFile(ctx context.Context, r Repo, ref, path string) ([]byte, error)
	// PullRequests lists every PR (any state) whose base is r.Branch.
	PullRequests(ctx context.Context, r Repo) ([]PR, error)
	MergePR(ctx context.Context, r Repo, number int) error
	ClosePR(ctx context.Context, r Repo, number int) error
	// AddWebhook registers a pull request webhook signed with secret.
	AddWebhook(ctx context.Context, r Repo, url, secret string) error
}

// FromEnv builds the Server hack/e2e/up.sh configured.
func FromEnv() (Server, error) {
	kind := os.Getenv(EnvKind)
	c := client{
		api:       strings.TrimRight(os.Getenv(EnvAPI), "/"),
		cloneBase: strings.TrimRight(os.Getenv(EnvCloneBase), "/"),
		owner:     os.Getenv(EnvOwner),
		token:     os.Getenv(EnvToken),
		http:      &http.Client{Timeout: 30 * time.Second},
	}
	repo := os.Getenv(EnvRepo)
	if kind == "github" {
		// The shared repo names the owner, and repos clone from github.com.
		if i := strings.Index(repo, "/"); i > 0 {
			c.owner = repo[:i]
		}
		if c.api == "" {
			c.api = "https://api.github.com"
		}
		if c.cloneBase == "" {
			c.cloneBase = "https://github.com"
		}
	}
	for name, v := range map[string]string{EnvKind: kind, EnvAPI: c.api, EnvOwner: c.owner, EnvToken: c.token} {
		if v == "" {
			return nil, fmt.Errorf("%s is not set", name)
		}
	}
	if c.cloneBase == "" {
		c.cloneBase = c.api
	}
	return newServer(kind, c, repo)
}

func newServer(kind string, c client, repo string) (Server, error) {
	switch kind {
	case "forgejo", "gitea":
		c.headers = map[string]string{"Authorization": "token " + c.token}
		return &forgejo{client: c, kind: kind}, nil
	case "gitlab":
		c.headers = map[string]string{"PRIVATE-TOKEN": c.token}
		return &gitlab{client: c}, nil
	case "github":
		if !strings.Contains(repo, "/") {
			return nil, fmt.Errorf("%s=%q: want owner/name", EnvRepo, repo)
		}
		c.headers = map[string]string{"Authorization": "Bearer " + c.token, "X-GitHub-Api-Version": "2022-11-28"}
		return &github{client: c, repo: repo}, nil
	}
	return nil, fmt.Errorf("%s=%q: want forgejo, gitea, gitlab or github", EnvKind, kind)
}

// client is the shared JSON-over-HTTP plumbing.
type client struct {
	api, cloneBase, owner, token string
	http                         *http.Client
	// headers are the provider's auth (and version) headers.
	headers map[string]string
}

// do sends a JSON request and decodes a JSON response into out (if non-nil).
// A status outside 2xx is an error that includes the response body.
func (c *client) do(ctx context.Context, method, path string, in, out interface{}) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &StatusError{Method: method, Path: path, Code: resp.StatusCode, Body: string(raw)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if b, ok := out.(*[]byte); ok {
		*b = raw
		return nil
	}
	return json.Unmarshal(raw, out)
}

// StatusError is a non-2xx API response.
type StatusError struct {
	Method, Path string
	Code         int
	Body         string
}

func (e *StatusError) Error() string {
	body := e.Body
	if len(body) > 500 {
		body = body[:500] + "..."
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Code, body)
}

// IsNotFound reports whether err is an HTTP 404.
func IsNotFound(err error) bool {
	se, ok := err.(*StatusError)
	return ok && se.Code == http.StatusNotFound
}
