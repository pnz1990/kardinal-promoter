// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// Reviewer merges every open kardinal PR on its repos as a team of
// reviewers would, every two seconds, until Stop. Merged counts what it
// merged; errors are logged, not fatal: a PR kardinal closes between the
// list and the merge cannot be merged.
type Reviewer struct {
	t      *testing.T
	git    gitserver.Server
	repos  []gitserver.Repo
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	merged map[string]bool
	// skip, when set, decides a PR is left alone (a test closes it itself).
	skip func(gitserver.PR) bool
}

// Review starts a Reviewer on repos; skip (may be nil) leaves PRs open.
func Review(t *testing.T, git gitserver.Server, skip func(gitserver.PR) bool, repos ...gitserver.Repo) *Reviewer {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Reviewer{t: t, git: git, repos: repos, cancel: cancel, done: make(chan struct{}), merged: map[string]bool{}, skip: skip}
	go r.loop(ctx)
	t.Cleanup(r.Stop)
	return r
}

func (r *Reviewer) loop(ctx context.Context) {
	defer close(r.done)
	for {
		for _, repo := range r.repos {
			prs, err := r.git.PullRequests(ctx, repo)
			if err != nil {
				continue
			}
			for _, pr := range prs {
				if pr.State != "open" || !strings.HasPrefix(pr.Head, "kardinal/") || (r.skip != nil && r.skip(pr)) {
					continue
				}
				key := repo.Name + "#" + pr.Head
				if err := r.git.MergePR(ctx, repo, pr.Number); err != nil {
					if ctx.Err() == nil {
						r.t.Logf("reviewer: merge %s PR #%d: %v", repo.Name, pr.Number, err)
					}
					continue
				}
				r.mu.Lock()
				r.merged[key] = true
				r.mu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// Merged is how many PRs the Reviewer merged.
func (r *Reviewer) Merged() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.merged)
}

// Stop ends the loop and waits for it.
func (r *Reviewer) Stop() {
	r.cancel()
	<-r.done
}
