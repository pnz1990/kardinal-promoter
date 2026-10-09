// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// RemoteHeadReader is implemented by git clients that can read the commits
// a remote's branches are at without cloning (git ls-remote), and the paths
// a branch's recent commits changed. The PromotionStep reconciler uses it
// while a PR waits for its merge, to rebuild the PR branch only when the
// base branch moved in a way that matters to the PR.
type RemoteHeadReader interface {
	// RemoteHeads returns every branch of the remote with its commit, from
	// one ls-remote.
	RemoteHeads(ctx context.Context, url, token string) (map[string]string, error)
	// BranchHistory returns the last maxCommits commits of branch, newest
	// first, each with the paths it changed against its first parent. The
	// last entry's paths are nil when its parent was not fetched.
	BranchHistory(ctx context.Context, url, branch, token string, maxCommits int) ([]CommitPaths, error)
}

// CommitPaths is one commit of a BranchHistory and the paths it changed.
type CommitPaths struct {
	SHA   string
	Paths []string
	// Partial is true when the parent was not fetched, so Paths is unknown.
	Partial bool
}

// PathsChangedSince returns the paths the commits of history after since
// changed (since excluded). found is false when since is not among them (the
// branch was force-pushed, or moved by more commits than history holds), or a
// commit's paths are unknown.
func PathsChangedSince(history []CommitPaths, since string) (paths []string, found bool) {
	seen := map[string]bool{}
	for _, c := range history {
		if c.SHA == since {
			return paths, true
		}
		if c.Partial {
			return nil, false
		}
		for _, p := range c.Paths {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	return nil, false
}

// RemoteHeads lists the branches of the remote url.
func (c *GoGitClient) RemoteHeads(ctx context.Context, url, token string) (map[string]string, error) {
	rem := gogit.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{url}})
	refs, err := rem.ListContext(ctx, &gogit.ListOptions{Auth: httpAuth(url, token)})
	if err != nil {
		return nil, fmt.Errorf("git ls-remote %s: %s", RedactURL(url), gitErrorText(err))
	}
	out := map[string]string{}
	for _, r := range refs {
		if r.Name().IsBranch() {
			out[r.Name().Short()] = r.Hash().String()
		}
	}
	return out, nil
}

// BranchHistory fetches the last maxCommits commits of branch into memory
// (no working tree) and diffs each against its parent.
func (c *GoGitClient) BranchHistory(ctx context.Context, url, branch, token string, maxCommits int) ([]CommitPaths, error) {
	repo, err := gogit.CloneContext(ctx, memory.NewStorage(), nil, &gogit.CloneOptions{
		URL: url, Auth: httpAuth(url, token), ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch: true, Depth: maxCommits, NoCheckout: true, Tags: gogit.NoTags,
	})
	if err != nil {
		return nil, fmt.Errorf("git fetch %s %s: %s", RedactURL(url), branch, gitErrorText(err))
	}
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("read %s head: %w", branch, err)
	}
	iter, err := repo.Log(&gogit.LogOptions{From: head.Hash()})
	if err != nil {
		return nil, fmt.Errorf("log %s: %w", branch, err)
	}
	var out []CommitPaths
	err = iter.ForEach(func(cm *object.Commit) error {
		if len(out) >= maxCommits {
			return io.EOF
		}
		entry := CommitPaths{SHA: cm.Hash.String()}
		tree, err := cm.Tree()
		if err != nil {
			return err
		}
		var parentTree *object.Tree
		if cm.NumParents() > 0 {
			p, perr := cm.Parent(0)
			if perr == nil {
				parentTree, perr = p.Tree()
			}
			if perr != nil {
				entry.Partial = true
				out = append(out, entry)
				return io.EOF // shallow: the parent was not fetched
			}
		}
		changes, err := object.DiffTreeWithOptions(ctx, parentTree, tree, nil)
		if err != nil {
			return err
		}
		for _, ch := range changes {
			for _, name := range []string{ch.From.Name, ch.To.Name} {
				if name != "" && !slices.Contains(entry.Paths, name) {
					entry.Paths = append(entry.Paths, name)
				}
			}
		}
		out = append(out, entry)
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, plumbing.ErrObjectNotFound) {
		return nil, fmt.Errorf("walk %s: %w", branch, err)
	}
	return out, nil
}

var _ RemoteHeadReader = (*GoGitClient)(nil)

// BranchGraphReader is implemented by git clients that can read the commit
// graph near a branch head. The health adapters use it to tell whether a
// commit Argo CD or Flux synced contains the promoted one (#1575).
type BranchGraphReader interface {
	// BranchGraph fetches branch at depth maxCommits and returns the head
	// it fetched and every fetched commit with its parents. A parent that
	// is not a key was not fetched (the shallow boundary).
	BranchGraph(ctx context.Context, url, branch, token string, maxCommits int) (head string, parents map[string][]string, err error)
}

// BranchGraph is BranchGraphReader.BranchGraph: a shallow clone of branch
// in memory, and every commit object it fetched.
func (c *GoGitClient) BranchGraph(ctx context.Context, url, branch, token string, maxCommits int) (string, map[string][]string, error) {
	repo, err := gogit.CloneContext(ctx, memory.NewStorage(), nil, &gogit.CloneOptions{
		URL: url, Auth: httpAuth(url, token), ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch: true, Depth: maxCommits, NoCheckout: true, Tags: gogit.NoTags,
	})
	if err != nil {
		return "", nil, fmt.Errorf("git fetch %s %s: %s", RedactURL(url), branch, gitErrorText(err))
	}
	head, err := repo.Head()
	if err != nil {
		return "", nil, fmt.Errorf("read %s head: %w", branch, err)
	}
	iter, err := repo.CommitObjects()
	if err != nil {
		return "", nil, fmt.Errorf("list commits of %s: %w", branch, err)
	}
	parents := map[string][]string{}
	err = iter.ForEach(func(cm *object.Commit) error {
		ps := make([]string, 0, len(cm.ParentHashes))
		for _, p := range cm.ParentHashes {
			ps = append(ps, p.String())
		}
		parents[cm.Hash.String()] = ps
		return nil
	})
	if err != nil {
		return "", nil, fmt.Errorf("read commits of %s: %w", branch, err)
	}
	return head.Hash().String(), parents, nil
}

// Ancestry is what a BranchGraph says about whether one commit contains
// another.
type Ancestry int

const (
	// AncestryUnknown: the graph does not reach far enough (rev is not in
	// it, or the walk from rev hit the shallow boundary before want).
	AncestryUnknown Ancestry = iota
	// AncestryContains: want is rev or one of its ancestors.
	AncestryContains
	// AncestryNotContains: the whole history of rev is in the graph and
	// want is not in it.
	AncestryNotContains
)

// Contains reports whether rev contains want in parents (a BranchGraph):
// whether want is rev or an ancestor of it, through every parent (a merge
// contains both sides). rev and want may be abbreviated (a prefix of at
// least 7 characters, as Argo CD and Flux print them).
func Contains(parents map[string][]string, rev, want string) Ancestry {
	full := func(sha string) string {
		if _, ok := parents[sha]; ok || len(sha) < 7 {
			return sha
		}
		for k := range parents {
			if len(k) > len(sha) && k[:len(sha)] == sha {
				return k
			}
		}
		return sha
	}
	rev, want = full(rev), full(want)
	if _, ok := parents[rev]; !ok {
		return AncestryUnknown
	}
	seen := map[string]bool{rev: true}
	queue := []string{rev}
	complete := true
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == want {
			return AncestryContains
		}
		ps, ok := parents[c]
		if !ok {
			complete = false // the shallow boundary: its history was not fetched
			continue
		}
		for _, p := range ps {
			if !seen[p] {
				seen[p] = true
				queue = append(queue, p)
			}
		}
	}
	if complete {
		return AncestryNotContains
	}
	return AncestryUnknown
}

var _ BranchGraphReader = (*GoGitClient)(nil)
