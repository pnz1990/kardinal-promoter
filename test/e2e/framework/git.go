// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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
	return e.newRepo(t, ns, true, func(ctx context.Context) (gitserver.Repo, error) {
		return e.Git.CreateRepo(ctx, ns, files)
	})
}

// RepoWithoutWebhook is Repo without the webhook: the controller learns of
// merges only by polling the PR.
func (e *Env) RepoWithoutWebhook(t *testing.T, ns string, files map[string][]byte) gitserver.Repo {
	t.Helper()
	return e.newRepo(t, ns, false, func(ctx context.Context) (gitserver.Repo, error) {
		return e.Git.CreateRepo(ctx, ns, files)
	})
}

// SubgroupRepo is Repo in subgroup sub of the owner (owner/sub/ns), on a git
// server with nested namespaces.
func (e *Env) SubgroupRepo(t *testing.T, sub, ns string, files map[string][]byte) gitserver.Repo {
	t.Helper()
	s, ok := e.Git.(gitserver.Subgrouper)
	if !ok {
		t.Fatalf("%s git server has no subgroups", e.Git.Kind())
	}
	return e.newRepo(t, ns, true, func(ctx context.Context) (gitserver.Repo, error) {
		return s.CreateSubgroupRepo(ctx, sub, ns, files)
	})
}

// EnvGitCreateSlots bounds how many tests create a repo at once (default
// 4). A core shard's parallel tests start in the same second once its serial
// tests end; about 100 repo creates at once queue in the git server past the
// client's timeout (#1557).
const EnvGitCreateSlots = "KARDINAL_E2E_GIT_CREATE_SLOTS"

var (
	repoCreateOnce  sync.Once
	repoCreateSlots chan struct{}
)

// acquireRepoCreate waits for a repo create slot and returns its release.
func acquireRepoCreate(t *testing.T) func() {
	t.Helper()
	repoCreateOnce.Do(func() {
		n := 4
		if v, err := strconv.Atoi(os.Getenv(EnvGitCreateSlots)); err == nil && v > 0 {
			n = v
		}
		repoCreateSlots = make(chan struct{}, n)
	})
	select {
	case repoCreateSlots <- struct{}{}:
		return func() { <-repoCreateSlots }
	case <-time.After(10 * time.Minute):
		t.Fatalf("no repo create slot in 10m (%s=%d)", EnvGitCreateSlots, cap(repoCreateSlots))
		return func() {}
	}
}

// newRepo creates a repo with create, at most EnvGitCreateSlots at once,
// deletes it when the test ends unless KARDINAL_E2E_KEEP=1, and registers
// the suite's webhook on it when hook is set. Before deleting the repo it drains the test's namespaces (see
// beforeRepoDelete).
func (e *Env) newRepo(t *testing.T, ns string, hook bool, create func(context.Context) (gitserver.Repo, error)) gitserver.Repo {
	t.Helper()
	release := acquireRepoCreate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	repo, err := create(ctx)
	release()
	if err != nil {
		t.Fatalf("create %s repo %s: %v", e.Git.Kind(), ns, err)
	}
	t.Logf("%s repo %s/%s branch %s (clone %s)", e.Git.Kind(), repo.Owner, repo.Name, repo.Branch, repo.CloneURL)
	t.Cleanup(func() {
		if os.Getenv(EnvKeep) == "1" {
			return
		}
		e.beforeRepoDelete(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		// DeleteRepo retries the git server's transient refusals; one that
		// lasts leaves a repo behind in a throwaway cluster, which harms no
		// later test (repo names are per namespace), so it does not fail a
		// test whose assertions passed (#1558).
		if err := e.Git.DeleteRepo(ctx, repo); err != nil {
			t.Logf("leaving repo %s: delete failed: %v", repo.Name, err)
		}
	})

	if url := os.Getenv(EnvWebhookURL); url != "" && hook {
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

// StallPushes makes the suite's Forgejo or Gitea hold every push to repo in
// a pre-receive hook, so a test can stop the controller while its push is in
// flight. stalled reports whether a push is being held. release removes the
// hook: a held push is then refused, and later pushes go through. It also
// runs when the test ends.
func (e *Env) StallPushes(t *testing.T, repo gitserver.Repo) (stalled func() bool, release func()) {
	t.Helper()
	kind := e.Git.Kind()
	if kind != "forgejo" && kind != "gitea" {
		t.Fatalf("StallPushes needs a forgejo or gitea git server, not %s", kind)
	}
	// The server's global pre-receive hook runs each repo's
	// hooks/pre-receive.d/* (giteafamily.sh deploys it as deploy/<kind> in
	// namespace <kind>, with its data at /var/lib/gitea).
	dir := fmt.Sprintf("/var/lib/gitea/git/repositories/%s/%s.git", strings.ToLower(repo.Owner), strings.ToLower(repo.Name))
	hook := dir + "/hooks/pre-receive.d/kardinal-e2e-stall"
	marker := dir + "/kardinal-e2e-stalled"
	script := fmt.Sprintf("#!/bin/sh\n# kardinal e2e: hold the push until the test removes this hook, then refuse it.\n"+
		"touch %[1]s\nwhile [ -e %[2]s ]; do sleep 1; done\nrm -f %[1]s\necho 'refused by the kardinal e2e stall hook'\nexit 1\n", marker, hook)
	e.Kubectl(t, kind, script, "exec", "-i", "deploy/"+kind, "--", "sh", "-c",
		fmt.Sprintf("test -d %[1]s && mkdir -p %[1]s/hooks/pre-receive.d && cat > %[2]s && chmod +x %[2]s", dir, hook))
	run := func(cmd string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "kubectl", "--context", e.Context, "-n", kind,
			"exec", "deploy/"+kind, "--", "sh", "-c", cmd).Run()
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			if err := run("rm -f " + hook); err != nil {
				t.Errorf("remove the stall hook of %s: %v", repo.Name, err)
			}
		})
	}
	t.Cleanup(release)
	return func() bool { return run("test -e "+marker) == nil }, release
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

// PRPeople returns the requested reviewers and the assignees of PR number.
// Forgejo, Gitea and GitLab only.
func (e *Env) PRPeople(t *testing.T, repo gitserver.Repo, number int) (reviewers, assignees []string) {
	t.Helper()
	p, ok := e.Git.(gitserver.PRPeople)
	if !ok {
		t.Fatalf("%s git server can't read PR reviewers", e.Git.Kind())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reviewers, assignees, err := p.PRReviewersAndAssignees(ctx, repo, number)
	if err != nil {
		t.Fatalf("reviewers of PR #%d: %v", number, err)
	}
	return reviewers, assignees
}

// Commit returns the message and the number of parents of commit sha.
// Forgejo, Gitea and GitLab only.
func (e *Env) Commit(t *testing.T, repo gitserver.Repo, sha string) (message string, parents int) {
	t.Helper()
	p, ok := e.Git.(gitserver.PRPeople)
	if !ok {
		t.Fatalf("%s git server can't read commits", e.Git.Kind())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	message, parents, err := p.Commit(ctx, repo, sha)
	if err != nil {
		t.Fatalf("commit %s: %v", sha, err)
	}
	return message, parents
}
