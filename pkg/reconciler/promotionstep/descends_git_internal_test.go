// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// gitShapes builds commits with explicit parents in a real repository and
// serves it over file://, for descends against real clones.
type gitShapes struct {
	t    *testing.T
	dir  string
	repo *gogit.Repository
	n    int
}

func newGitShapes(t *testing.T) *gitShapes {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	require.NoError(t, err)
	return &gitShapes{t: t, dir: dir, repo: repo}
}

// commit writes a file of its own and commits it with parents (none: a
// root).
func (g *gitShapes) commit(msg string, parents ...plumbing.Hash) plumbing.Hash {
	g.t.Helper()
	return g.commitFile(filepath.Join("notes", msg+".txt"), msg, parents...)
}

// commitFile writes file (relative to the repository) and commits it.
func (g *gitShapes) commitFile(file, msg string, parents ...plumbing.Hash) plumbing.Hash {
	g.t.Helper()
	g.n++
	wt, err := g.repo.Worktree()
	require.NoError(g.t, err)
	require.NoError(g.t, os.MkdirAll(filepath.Dir(filepath.Join(g.dir, file)), 0o755))
	require.NoError(g.t, os.WriteFile(filepath.Join(g.dir, file), []byte(msg), 0o600))
	_, err = wt.Add(file)
	require.NoError(g.t, err)
	when := time.Date(2026, 10, 9, 12, 0, g.n, 0, time.UTC)
	h, err := wt.Commit(msg, &gogit.CommitOptions{Parents: parents, AllowEmptyCommits: true,
		Author: &object.Signature{Name: "t", Email: "t@example.com", When: when}})
	require.NoError(g.t, err)
	return h
}

// setMain points main at h.
func (g *gitShapes) setMain(h plumbing.Hash) {
	g.t.Helper()
	require.NoError(g.t, g.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), h)))
}

func (g *gitShapes) url() string { return "file://" + g.dir }

// TestDescends_GitShapes (#1575 QA): "contains" is ancestry in the real commit
// graph, not an order in a log walk. QA's false Verified was a merge-shaped
// history R←B←{L1, P}←M: a depth-first log from M lists M, L1, B, R, P, so
// comparing positions said L1 contains P. Squash and rebase merges leave the
// PR's original commits off the branch: only the commits on it count.
func TestDescends_GitShapes(t *testing.T) {
	ctx := context.Background()
	git := scm.NewGoGitClient()
	check := func(t *testing.T, g *gitShapes, rev, want plumbing.Hash, contains bool) {
		t.Helper()
		r := &Reconciler{NowFn: func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }}
		ok, err := r.descends(ctx, git, git, g.url(), "main", "", rev.String(), want.String(), []string{"environments/test"})
		require.NoError(t, err)
		assert.Equal(t, contains, ok, "%s contains %s", rev.String()[:7], want.String()[:7])
	}

	t.Run("merge", func(t *testing.T) {
		g := newGitShapes(t)
		r := g.commit("R")
		b := g.commit("B", r)
		l1 := g.commit("L1", b) // main
		p := g.commit("P", b)   // the PR branch
		m := g.commit("M", l1, p)
		g.setMain(m)
		check(t, g, l1, p, false) // the false Verified QA reproduced
		check(t, g, p, l1, false)
		check(t, g, m, p, true) // the merge contains both sides
		check(t, g, m, l1, true)
		check(t, g, l1, b, true)
		check(t, g, b, l1, false)
		check(t, g, m, m, true)
	})

	t.Run("squash", func(t *testing.T) {
		g := newGitShapes(t)
		b := g.commit("B")
		p1 := g.commit("P1", b) // the PR's commits, never on main
		p2 := g.commit("P2", p1)
		s := g.commit("S", b) // the squash merge
		later := g.commit("later", s)
		g.setMain(later)
		check(t, g, later, s, true)
		check(t, g, later, p2, false) // the PR head is not on the branch
		check(t, g, p2, b, false)     // nor is it a revision on the branch
		check(t, g, s, later, false)
	})

	t.Run("rebase", func(t *testing.T) {
		g := newGitShapes(t)
		b := g.commit("B")
		other := g.commit("other", b) // main moved while the PR waited
		p1 := g.commit("P1", b)       // the PR's original commits
		p2 := g.commit("P2", p1)
		p1r := g.commit("P1'", other) // rebased onto main
		p2r := g.commit("P2'", p1r)
		g.setMain(p2r)
		check(t, g, p2r, p1r, true)
		check(t, g, p2r, other, true)
		check(t, g, p2r, p2, false) // the original, not rebased, commit
		check(t, g, p1r, p2r, false)
	})

	t.Run("a later commit changed the environment's files", func(t *testing.T) {
		const env = "environments/test/kustomization.yaml"
		g := newGitShapes(t)
		b := g.commit("B")
		older := g.commitFile(env, "newTag: 1", b) // an older Bundle's push
		ours := g.commitFile(env, "newTag: 2", older)
		other := g.commitFile("environments/prod/kustomization.yaml", "prod", ours)
		note := g.commit("note", other)
		late := g.commitFile(env, "newTag: 1", note) // the older Bundle's retry lands after ours
		side := g.commitFile(env, "newTag: 3", ours) // a merged branch that rewrote it
		merged := g.commit("merge", note, side)
		g.setMain(late)
		check(t, g, note, ours, true)  // others' paths: still ours
		check(t, g, late, ours, false) // rewritten since
		g.setMain(merged)
		check(t, g, note, ours, true)
		check(t, g, merged, ours, false) // the merge brings in a rewrite of our file
	})
}
