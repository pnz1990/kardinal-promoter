// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// Repo creates a git repo for the test (a branch of the shared repo on
// GitHub) holding files, named after the test namespace ns. It registers the
// controller's webhook when the suite has one, and deletes the repo when the
// test ends unless KARDINAL_E2E_KEEP=1.
func (e *Env) Repo(t *testing.T, ns string, files map[string][]byte) gitserver.Repo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	repo, err := e.Git.CreateRepo(ctx, ns, files)
	if err != nil {
		t.Fatalf("create %s repo %s: %v", e.Git.Kind(), ns, err)
	}
	t.Logf("%s repo %s/%s branch %s (clone %s)", e.Git.Kind(), repo.Owner, repo.Name, repo.Branch, repo.CloneURL)
	t.Cleanup(func() {
		if os.Getenv(EnvKeep) == "1" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := e.Git.DeleteRepo(ctx, repo); err != nil {
			t.Errorf("delete repo %s: %v", repo.Name, err)
		}
	})

	if url := os.Getenv(EnvWebhookURL); url != "" {
		err := e.Git.AddWebhook(ctx, repo, url, os.Getenv(EnvWebhookSecret))
		switch {
		case errors.Is(err, gitserver.ErrNoWebhookDelivery):
			t.Logf("no webhook: %v; merges are seen by PR polling", err)
		case err != nil:
			t.Fatalf("add webhook to %s: %v", repo.Name, err)
		}
	}
	return repo
}

// WaitPR waits for exactly one PR matching match on repo and returns it.
// It fails when a second matching PR appears: the controller must not open
// duplicates.
func (e *Env) WaitPR(t *testing.T, repo gitserver.Repo, timeout time.Duration, what string, match func(gitserver.PR) bool) gitserver.PR {
	t.Helper()
	var found gitserver.PR
	Eventually(t, timeout, what, func(ctx context.Context) (bool, string) {
		prs, err := e.Git.PullRequests(ctx, repo)
		if err != nil {
			return false, err.Error()
		}
		var hits []gitserver.PR
		for _, pr := range prs {
			if match(pr) {
				hits = append(hits, pr)
			}
		}
		switch len(hits) {
		case 0:
			return false, describePRs(prs)
		case 1:
			found = hits[0]
			return true, ""
		}
		t.Fatalf("%s: %d PRs match, want 1: %s", what, len(hits), describePRs(hits))
		return false, ""
	})
	return found
}

// WaitPRState waits until PR number on repo is in state (open, closed or
// merged) and returns it.
func (e *Env) WaitPRState(t *testing.T, repo gitserver.Repo, number int, state string, timeout time.Duration) gitserver.PR {
	t.Helper()
	var found gitserver.PR
	Eventually(t, timeout, fmt.Sprintf("PR #%d to be %s", number, state), func(ctx context.Context) (bool, string) {
		prs, err := e.Git.PullRequests(ctx, repo)
		if err != nil {
			return false, err.Error()
		}
		for _, pr := range prs {
			if pr.Number == number {
				found = pr
				return pr.State == state, "state=" + pr.State
			}
		}
		return false, describePRs(prs)
	})
	return found
}

// PRComments returns the bodies of PR number's comments that contain substr.
func (e *Env) PRComments(t *testing.T, repo gitserver.Repo, number int, substr string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	all, err := e.Git.Comments(ctx, repo, number)
	if err != nil {
		t.Fatalf("comments of PR #%d: %v", number, err)
	}
	var hits []string
	for _, c := range all {
		if strings.Contains(c, substr) {
			hits = append(hits, c)
		}
	}
	return hits
}

// ReadFile reads path at ref, failing the test on error.
func (e *Env) ReadFile(t *testing.T, repo gitserver.Repo, ref, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := e.Git.ReadFile(ctx, repo, ref, path)
	if err != nil {
		t.Fatalf("read %s@%s: %v", path, ref, err)
	}
	return string(raw)
}

func describePRs(prs []gitserver.PR) string {
	if len(prs) == 0 {
		return "no PRs"
	}
	parts := make([]string, 0, len(prs))
	for _, pr := range prs {
		parts = append(parts, fmt.Sprintf("#%d %s %s→%s [%s]", pr.Number, pr.State, pr.Head, pr.Base, strings.Join(pr.Labels, ",")))
	}
	return strings.Join(parts, "; ")
}
