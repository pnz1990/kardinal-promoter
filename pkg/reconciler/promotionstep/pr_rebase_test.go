// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
	ctx := context.Background()
	dir := filepath.Join(g.t.TempDir(), "w")
	if rewrite {
		repo, err := gogit.PlainInitWithOptions(dir, &gogit.PlainInitOptions{
			InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
		require.NoError(g.t, err)
		_, err = repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{g.url()}})
		require.NoError(g.t, err)
	} else {
		require.NoError(g.t, g.c.Clone(ctx, g.url(), "main", dir, ""))
	}
	for p, content := range files {
		require.NoError(g.t, os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755))
		require.NoError(g.t, os.WriteFile(filepath.Join(dir, p), []byte(content), 0o600))
	}
	require.NoError(g.t, g.c.CommitAll(ctx, dir, "other writer", "o", "o@example.com"))
	require.NoError(g.t, g.c.Push(ctx, dir, "origin", "main", "", rewrite))
	return g.head("main").Hash.String()
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
// pr-review PR waits for its merge, the base branch moves. When other
// writers fast-forward it, the next reconcile rebuilds the PR branch on the
// new head: one commit whose parent is the new head, with the promotion's
// change and the other writers' files. When the base is force-pushed (its
// history rewritten), the PR branch is re-created from the new base the same
// way. The PR keeps its number and branch; status.outputs.baseSHA follows
// the base and status.outputs.prBranchRebuilds counts the rebuilds. When the
// base did not move, nothing is pushed.
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
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: scm.NewGoGitClient(),
		WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}

	reconcileStep(t, r, step.Name) // records the step list
	reconcileStep(t, r, step.Name) // runs it, opens the PR
	got := getStep(t, c, step.Name)
	require.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
	branch := got.Status.Outputs["branch"]
	require.Equal(t, "kardinal/37a8eec1/bundle-1/prod", branch)
	first := remote.head(branch)
	built := remote.head("main").Hash.String()
	assert.Equal(t, built, got.Status.Outputs["baseSHA"])
	assert.Contains(t, first.file(t, "environments/prod/kustomization.yaml"), "newTag: 1.2.3")

	// No move: nothing is pushed.
	reconcileStep(t, r, step.Name)
	assert.Equal(t, first.Hash, remote.head(branch).Hash)
	assert.Empty(t, getStep(t, c, step.Name).Status.Outputs["prBranchRebuilds"])

	// Another writer fast-forwards main.
	moved := remote.commit(map[string]string{"notes/ci.txt": "note\n"}, false)
	reconcileStep(t, r, step.Name)
	got = getStep(t, c, step.Name)
	require.Equal(t, "WaitingForMerge", got.Status.State)
	pr := remote.head(branch)
	assert.Equal(t, []plumbing.Hash{plumbing.NewHash(moved)}, pr.Parents, "one commit on the new head")
	assert.Equal(t, "note\n", pr.file(t, "notes/ci.txt"), "the other writer's file")
	assert.Contains(t, pr.file(t, "environments/prod/kustomization.yaml"), "newTag: 1.2.3")
	assert.Equal(t, moved, got.Status.Outputs["baseSHA"])
	assert.Equal(t, "1", got.Status.Outputs["prBranchRebuilds"])
	assert.Equal(t, branch, got.Status.Outputs["branch"])
	assert.Equal(t, "5", got.Status.Outputs["prNumber"])
	assert.Contains(t, got.Status.Message, "rebuilt the PR branch")
	assert.Equal(t, 1, m.openCalled, "the same PR, not a new one")

	// main is force-pushed: its history is replaced.
	forced := remote.commit(map[string]string{
		"environments/prod/kustomization.yaml": prodKustomization,
		"README.md":                            "rewritten\n",
	}, true)
	reconcileStep(t, r, step.Name)
	got = getStep(t, c, step.Name)
	pr = remote.head(branch)
	assert.Equal(t, []plumbing.Hash{plumbing.NewHash(forced)}, pr.Parents, "re-created from the new base")
	assert.Empty(t, pr.file(t, "notes/ci.txt"), "nothing of the old history")
	assert.Equal(t, "rewritten\n", pr.file(t, "README.md"))
	assert.Contains(t, pr.file(t, "environments/prod/kustomization.yaml"), "newTag: 1.2.3")
	assert.Equal(t, forced, got.Status.Outputs["baseSHA"])
	assert.Equal(t, "2", got.Status.Outputs["prBranchRebuilds"])

	// Paused: no git write.
	remote.commit(map[string]string{"notes/ci2.txt": "note\n"}, false)
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pl), &p))
	p.Spec.Paused = true
	require.NoError(t, c.Update(context.Background(), &p))
	reconcileStep(t, r, step.Name)
	assert.Equal(t, pr.Hash, remote.head(branch).Hash, "paused: the PR branch is left alone")
}
