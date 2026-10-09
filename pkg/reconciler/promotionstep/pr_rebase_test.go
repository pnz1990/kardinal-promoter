// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const prodKustomization = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
- name: ghcr.io/test/app
  newTag: 1.0.0
`

// gitRemote is a bare repository served over file://.
type gitRemote struct {
	t   *testing.T
	dir string
	c   *scm.GoGitClient
}

func (g *gitRemote) url() string { return "file://" + g.dir }

// commit writes files in a new commit on main. With rewrite the commit has
// no parent and replaces main by force (a force-pushed base branch).
func (g *gitRemote) commit(files map[string]string, rewrite bool) string {
	g.t.Helper()
	return g.commitOn("main", files, rewrite)
}

// commitOn is commit on branch.
func (g *gitRemote) commitOn(branch string, files map[string]string, rewrite bool) string {
	g.t.Helper()
	ctx := context.Background()
	dir := filepath.Join(g.t.TempDir(), "w")
	if rewrite {
		repo, err := gogit.PlainInitWithOptions(dir, &gogit.PlainInitOptions{
			InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
		require.NoError(g.t, err)
		_, err = repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{g.url()}})
		require.NoError(g.t, err)
	} else {
		require.NoError(g.t, g.c.Clone(ctx, g.url(), branch, dir, ""))
	}
	for p, content := range files {
		require.NoError(g.t, os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755))
		require.NoError(g.t, os.WriteFile(filepath.Join(dir, p), []byte(content), 0o600))
	}
	require.NoError(g.t, g.c.CommitAll(ctx, dir, "other writer", "o", "o@example.com"))
	require.NoError(g.t, g.c.Push(ctx, dir, "origin", branch, "", rewrite))
	return g.head(branch).Hash.String()
}

type commitInfo struct {
	Hash    plumbing.Hash
	Parents []plumbing.Hash
	tree    *object.Tree
}

func (g *gitRemote) head(branch string) commitInfo {
	g.t.Helper()
	repo, err := gogit.PlainOpen(g.dir)
	require.NoError(g.t, err)
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	require.NoError(g.t, err)
	c, err := repo.CommitObject(ref.Hash())
	require.NoError(g.t, err)
	tree, err := c.Tree()
	require.NoError(g.t, err)
	return commitInfo{Hash: c.Hash, Parents: c.ParentHashes, tree: tree}
}

func (c commitInfo) file(t *testing.T, path string) string {
	t.Helper()
	f, err := c.tree.File(path)
	if err != nil {
		return ""
	}
	s, err := f.Contents()
	require.NoError(t, err)
	return s
}

func newGitRemote(t *testing.T) *gitRemote {
	t.Helper()
	g := &gitRemote{t: t, dir: t.TempDir(), c: scm.NewGoGitClient()}
	_, err := gogit.PlainInitWithOptions(g.dir, &gogit.PlainInitOptions{Bare: true,
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
	require.NoError(t, err)
	g.commit(map[string]string{"environments/prod/kustomization.yaml": prodKustomization}, true)
	return g
}

// TestWaitingForMerge_RebuildsPRBranchOnMovedBase (#1504 QA): while a
// pr-review PR waits for its merge, the base branch moves.
//   - Commits that change none of the PR's paths: the PR still merges, so
//     only status.outputs.baseSHA follows the base; nothing is pushed (no
//     livelock of PRs rebuilding on each other's merges).
//   - A commit that changes a file under the environment's path: the PR
//     branch is rebuilt on the new head (one commit whose parent is the head,
//     with the promotion's change and the other writer's file).
//   - A force-pushed base (baseSHA not in its history): re-created from the
//     new base.
//   - Someone commits to the PR branch: it is not rebuilt, and the message
//     says so.
//
// The PR keeps its number and branch; status.outputs.prBranchRebuilds counts
// rebuilds. Remote heads are read once per 30 s, so the clock moves past
// that between checks. Paused: no git write.
func TestWaitingForMerge_RebuildsPRBranchOnMovedBase(t *testing.T) {
	remote := newGitRemote(t)
	pl, b := makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")
	pl.Spec.Git.URL = remote.url()
	pl.Spec.Environments[1].Path = "environments/prod"
	b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/test/app", Tag: "1.2.3"}}
	step := builtStep(t, pl, b, "prod")
	step.Status.State = "Promoting"
	c := newClient(t, step, pl, b, openPRStatus(step.Spec.PRStatusRef, "", 0))
	m := &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/5", prNumber: 5}
	now := time.Now()
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: scm.NewGoGitClient(),
		NowFn:     func() time.Time { return now },
		WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}
	check := func() promotionstepState {
		t.Helper()
		now = now.Add(31 * time.Second)
		reconcileStep(t, r, step.Name)
		got := getStep(t, c, step.Name)
		require.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
		return promotionstepState{got.Status.Outputs, got.Status.Message}
	}

	reconcileStep(t, r, step.Name) // records the step list
	reconcileStep(t, r, step.Name) // runs it, opens the PR
	got := getStep(t, c, step.Name)
	require.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
	branch := got.Status.Outputs["branch"]
	require.Equal(t, "kardinal/37a8eec1/bundle-1/prod", branch)
	first := remote.head(branch)
	assert.Equal(t, remote.head("main").Hash.String(), got.Status.Outputs["baseSHA"])
	assert.Equal(t, first.Hash.String(), got.Status.Outputs["pushedSHA"], "the lease")
	assert.Contains(t, first.file(t, "environments/prod/kustomization.yaml"), "newTag: 1.2.3")

	// No move: nothing is pushed.
	st := check()
	assert.Equal(t, first.Hash, remote.head(branch).Hash)
	assert.Empty(t, st.outputs["prBranchRebuilds"])

	// Other writers move main at other paths: baseSHA follows, no push.
	remote.commit(map[string]string{"notes/ci.txt": "note\n"}, false)
	elsewhere := remote.commit(map[string]string{"environments/test/kustomization.yaml": "other\n"}, false)
	st = check()
	assert.Equal(t, elsewhere, st.outputs["baseSHA"])
	assert.Equal(t, first.Hash, remote.head(branch).Hash, "the PR's paths did not change: no rebuild")
	assert.Empty(t, st.outputs["prBranchRebuilds"])

	// A busy branch: more commits than the first history read (20) at other
	// paths. The deeper read finds the PR's base: still no rebuild.
	var busy string
	for i := range 25 {
		busy = remote.commit(map[string]string{fmt.Sprintf("notes/busy-%02d.txt", i): "x\n"}, false)
	}
	st = check()
	assert.Equal(t, busy, st.outputs["baseSHA"])
	assert.Equal(t, first.Hash, remote.head(branch).Hash, "25 commits at other paths: no rebuild")
	assert.Empty(t, st.outputs["prBranchRebuilds"])

	// A commit under environments/prod: rebuilt on the new head.
	moved := remote.commit(map[string]string{"environments/prod/extra.yaml": "x: 1\n"}, false)
	st = check()
	pr := remote.head(branch)
	assert.Equal(t, []plumbing.Hash{plumbing.NewHash(moved)}, pr.Parents, "one commit on the new head")
	assert.Equal(t, "x: 1\n", pr.file(t, "environments/prod/extra.yaml"), "the other writer's file")
	assert.Contains(t, pr.file(t, "environments/prod/kustomization.yaml"), "newTag: 1.2.3")
	assert.Equal(t, moved, st.outputs["baseSHA"])
	assert.Equal(t, pr.Hash.String(), st.outputs["pushedSHA"])
	assert.Equal(t, "1", st.outputs["prBranchRebuilds"])
	assert.Equal(t, branch, st.outputs["branch"])
	assert.Equal(t, "5", st.outputs["prNumber"])
	assert.Contains(t, st.message, "rebuilt the PR branch")
	assert.Equal(t, 1, m.openCalled, "the same PR, not a new one")

	// main is force-pushed: re-created from the new base.
	forced := remote.commit(map[string]string{
		"environments/prod/kustomization.yaml": prodKustomization,
		"README.md":                            "rewritten\n",
	}, true)
	st = check()
	pr = remote.head(branch)
	assert.Equal(t, []plumbing.Hash{plumbing.NewHash(forced)}, pr.Parents, "re-created from the new base")
	assert.Empty(t, pr.file(t, "notes/ci.txt"), "nothing of the old history")
	assert.Equal(t, "rewritten\n", pr.file(t, "README.md"))
	assert.Equal(t, forced, st.outputs["baseSHA"])
	assert.Equal(t, "2", st.outputs["prBranchRebuilds"])
	assert.Contains(t, st.message, "the base branch history since the PR was built could not be read (force-pushed, "+
		"or the read timed out), so the PR branch was rebuilt to be safe", "the rebuild names why")

	// Someone pushes to the PR branch; then main moves under the PR's path.
	human := remote.commitOn(branch, map[string]string{"environments/prod/fix.yaml": "by hand\n"}, false)
	remote.commit(map[string]string{"environments/prod/extra.yaml": "x: 2\n"}, false)
	st = check()
	assert.Equal(t, human, remote.head(branch).Hash.String(), "a human commit is never overwritten")
	assert.Contains(t, st.message, "its branch has commits kardinal did not push")
	assert.Equal(t, "2", st.outputs["prBranchRebuilds"])
	assert.Equal(t, forced, st.outputs["baseSHA"])

	// Paused: no git write.
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pl), &p))
	p.Spec.Paused = true
	require.NoError(t, c.Update(context.Background(), &p))
	before := remote.head(branch).Hash
	now = now.Add(31 * time.Second)
	reconcileStep(t, r, step.Name)
	assert.Equal(t, before, remote.head(branch).Hash, "paused: the PR branch is left alone")
}

type promotionstepState struct {
	outputs map[string]string
	message string
}

// slowHistory is a git client whose branch history reads never answer before
// their context ends.
type slowHistory struct {
	*scm.GoGitClient
	calls int
}

func (s *slowHistory) BranchHistory(ctx context.Context, _, _, _ string, _ int) ([]scm.CommitPaths, error) {
	s.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestWaitingForMerge_HistoryTimeoutRebuilds (#1504 QA): a base branch
// history read that takes longer than its timeout counts as history not
// found. The reconcile does not wait for it: the PR branch is rebuilt on the
// moved base, which is always safe, and the message says why.
func TestWaitingForMerge_HistoryTimeoutRebuilds(t *testing.T) {
	defer promotionstep.SetHistoryTimeout(50 * time.Millisecond)()
	remote := newGitRemote(t)
	pl, b := makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")
	pl.Spec.Git.URL = remote.url()
	pl.Spec.Environments[1].Path = "environments/prod"
	b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/test/app", Tag: "1.2.3"}}
	step := builtStep(t, pl, b, "prod")
	step.Status.State = "Promoting"
	c := newClient(t, step, pl, b, openPRStatus(step.Spec.PRStatusRef, "", 0))
	m := &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/5", prNumber: 5}
	git := &slowHistory{GoGitClient: scm.NewGoGitClient()}
	now := time.Now()
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: git,
		NowFn:     func() time.Time { return now },
		WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}
	reconcileStep(t, r, step.Name)
	reconcileStep(t, r, step.Name)
	got := getStep(t, c, step.Name)
	require.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
	branch := got.Status.Outputs["branch"]

	// A commit at another path: with the history readable this would not
	// rebuild; with a read that times out it does.
	moved := remote.commit(map[string]string{"notes/ci.txt": "note\n"}, false)
	now = now.Add(31 * time.Second)
	start := time.Now()
	reconcileStep(t, r, step.Name)
	assert.Less(t, time.Since(start), 20*time.Second, "the reconcile does not wait for the slow read")
	got = getStep(t, c, step.Name)
	require.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
	assert.Positive(t, git.calls, "the history was asked for")
	assert.Equal(t, []plumbing.Hash{plumbing.NewHash(moved)}, remote.head(branch).Parents, "rebuilt on the new head")
	assert.Equal(t, "1", got.Status.Outputs["prBranchRebuilds"])
	assert.Contains(t, got.Status.Message, "could not be read (force-pushed, or the read timed out), so the PR branch was rebuilt to be safe")
}

// staleHeads is a git client whose next ls-remote answers stale (once), as
// the shared heads cache does when another step read it before this step's
// PR was built.
type staleHeads struct {
	*scm.GoGitClient
	stale map[string]string
	// staleHistory, when set, answers the next history read (once): the
	// history the cache holds for the stale head.
	staleHistory []scm.CommitPaths
}

func (s *staleHeads) BranchHistory(ctx context.Context, url, branch, token string, max int) ([]scm.CommitPaths, error) {
	if h := s.staleHistory; h != nil {
		s.staleHistory = nil
		return h, nil
	}
	return s.GoGitClient.BranchHistory(ctx, url, branch, token, max)
}

func (s *staleHeads) RemoteHeads(ctx context.Context, url, token string) (map[string]string, error) {
	if h := s.stale; h != nil {
		s.stale = nil
		return h, nil
	}
	return s.GoGitClient.RemoteHeads(ctx, url, token)
}

// TestWaitingForMerge_StaleHeadsDoNotRebuild (#1575): the heads a waiting
// step reads can be older than the commit its PR was built on. That head is
// not a move of the base, and built is not in its history: the step reads
// the heads again instead of rebuilding the PR branch "to be safe", and then
// follows the real head as usual (a commit at another path: no rebuild).
func TestWaitingForMerge_StaleHeadsDoNotRebuild(t *testing.T) {
	remote := newGitRemote(t)
	older := remote.head("main").Hash.String()
	remote.commit(map[string]string{"notes/before.txt": "x\n"}, false)
	pl, b := makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")
	pl.Spec.Git.URL = remote.url()
	pl.Spec.Environments[1].Path = "environments/prod"
	b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/test/app", Tag: "1.2.3"}}
	step := builtStep(t, pl, b, "prod")
	step.Status.State = "Promoting"
	c := newClient(t, step, pl, b, openPRStatus(step.Spec.PRStatusRef, "", 0))
	m := &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/5", prNumber: 5}
	git := &staleHeads{GoGitClient: scm.NewGoGitClient()}
	now := time.Now()
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: git,
		NowFn:     func() time.Time { return now },
		WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}
	reconcileStep(t, r, step.Name)
	reconcileStep(t, r, step.Name)
	got := getStep(t, c, step.Name)
	require.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
	branch := got.Status.Outputs["branch"]
	pushed := remote.head(branch).Hash

	git.stale = map[string]string{"main": older, branch: pushed.String()}
	elsewhere := remote.commit(map[string]string{"notes/ci.txt": "note\n"}, false)
	now = now.Add(31 * time.Second)
	reconcileStep(t, r, step.Name)
	got = getStep(t, c, step.Name)
	require.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
	assert.Equal(t, pushed, remote.head(branch).Hash, "not rebuilt for a head older than its base")
	assert.Empty(t, got.Status.Outputs["prBranchRebuilds"], got.Status.Message)
	assert.Equal(t, elsewhere, got.Status.Outputs["baseSHA"], "follows the head read again")
	assert.NotContains(t, got.Status.Message, "rebuilt")

	// The cache also holds the stale head's history, which ends before the
	// PR's base: the heads are read again, and the real move at another
	// path does not rebuild either.
	git.stale = map[string]string{"main": older, branch: pushed.String()}
	git.staleHistory = []scm.CommitPaths{{SHA: older}}
	again := remote.commit(map[string]string{"notes/ci2.txt": "note\n"}, false)
	now = now.Add(31 * time.Second)
	reconcileStep(t, r, step.Name)
	got = getStep(t, c, step.Name)
	assert.Equal(t, pushed, remote.head(branch).Hash, "not rebuilt for a stale history")
	assert.Empty(t, got.Status.Outputs["prBranchRebuilds"], got.Status.Message)
	assert.Equal(t, again, got.Status.Outputs["baseSHA"])
}
