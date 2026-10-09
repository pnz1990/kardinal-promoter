// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scm

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// ErrRebaseConflict is returned by RebaseOnRemote when the commits that
// arrived on the remote branch changed a file the local commit changes too.
// The caller must redo its change on the new branch head (a fresh clone)
// instead of replaying the old one, which would lose the other writer's
// update.
var ErrRebaseConflict = errors.New("the remote branch changed the same files")

// ErrBranchNotMoved is returned by RebaseOnRemote when the remote branch is
// still at the commit HEAD was made on: a push refused as non-fast-forward
// was not refused because another writer moved the branch (a stale lock
// file, a read-only repository or a full disk on the server), so retrying as
// contention would never end.
var ErrBranchNotMoved = errors.New("the remote branch did not move")

// Rebaser is implemented by git clients that can move a local commit onto
// the remote branch's new head. git-push uses it when another writer (another
// Pipeline, or another environment) pushed first.
type Rebaser interface {
	// RebaseOnRemote fetches branch and replaces HEAD, one commit on top of
	// the commit it was cloned at, by a commit on top of the remote head
	// with the same message and author and the same file changes. It
	// returns the paths the local commit changes. With ErrRebaseConflict it
	// changes nothing.
	RebaseOnRemote(ctx context.Context, dir, remote, branch string, auth GitAuth) ([]string, error)
}

var _ Rebaser = (*GoGitClient)(nil)

// RebaseOnRemote implements Rebaser. The file changes are replayed path by
// path: every file the local commit added, changed or deleted takes its
// local version, every other file the remote head's version. That is a
// rebase without a merge: it refuses (ErrRebaseConflict) when the remote
// commits since the clone touched any of those paths. Nothing is lost:
// neither the other writer's files nor this commit's.
func (c *GoGitClient) RebaseOnRemote(ctx context.Context, dir, remote, branch string, auth GitAuth) ([]string, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("open repo at %s: %w", dir, err)
	}
	headRef, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolve HEAD in %s: %w", dir, err)
	}
	local, err := repo.CommitObject(headRef.Hash())
	if err != nil {
		return nil, fmt.Errorf("read HEAD commit: %w", err)
	}
	if local.NumParents() != 1 {
		return nil, fmt.Errorf("rebase: HEAD %s has %d parents, want 1", local.Hash, local.NumParents())
	}
	base, err := local.Parent(0)
	if err != nil {
		return nil, fmt.Errorf("rebase: read the commit HEAD was made on: %w", err)
	}

	rem, err := repo.Remote(remote)
	if err != nil {
		return nil, fmt.Errorf("get remote %s: %w", remote, err)
	}
	remoteURL := ""
	if urls := rem.Config().URLs; len(urls) > 0 {
		remoteURL = urls[0]
	}
	am, err := authMethod(remoteURL, auth)
	if err != nil {
		return nil, fmt.Errorf("git fetch %s %s: %w", remote, branch, err)
	}
	proxyOpts, release := sshScope(ctx, am)
	defer release()
	tracking := plumbing.NewRemoteReferenceName(remote, branch)
	spec := config.RefSpec("+" + plumbing.NewBranchReferenceName(branch).String() + ":" + tracking.String())
	err = repo.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName:   remote,
		RefSpecs:     []config.RefSpec{spec},
		Depth:        1,
		Auth:         am,
		Force:        true,
		Tags:         gogit.NoTags,
		ProxyOptions: proxyOpts,
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return nil, fmt.Errorf("git fetch %s %s: %s", remote, branch, gitErrorText(err))
	}
	trackRef, err := repo.Reference(tracking, true)
	if err != nil {
		return nil, fmt.Errorf("rebase: read %s: %w", tracking, err)
	}
	upstream, err := repo.CommitObject(trackRef.Hash())
	if err != nil {
		return nil, fmt.Errorf("rebase: read the remote head: %w", err)
	}
	if upstream.Hash == base.Hash {
		return nil, fmt.Errorf("rebase onto %s at %s: %w", branch, upstream.Hash, ErrBranchNotMoved)
	}

	baseTree, err := base.Tree()
	if err != nil {
		return nil, fmt.Errorf("rebase: %w", err)
	}
	localTree, err := local.Tree()
	if err != nil {
		return nil, fmt.Errorf("rebase: %w", err)
	}
	upTree, err := upstream.Tree()
	if err != nil {
		return nil, fmt.Errorf("rebase: %w", err)
	}
	ours, err := object.DiffTree(baseTree, localTree)
	if err != nil {
		return nil, fmt.Errorf("rebase: diff the local commit: %w", err)
	}
	theirs, err := object.DiffTree(baseTree, upTree)
	if err != nil {
		return nil, fmt.Errorf("rebase: diff the remote commits: %w", err)
	}
	theirPaths := map[string]bool{}
	for _, ch := range theirs {
		for _, p := range []string{ch.From.Name, ch.To.Name} {
			if p != "" {
				theirPaths[p] = true
			}
		}
	}
	edits := map[string]*object.TreeEntry{}
	var changed []string
	for _, ch := range ours {
		for _, p := range []string{ch.From.Name, ch.To.Name} {
			if p == "" {
				continue
			}
			if theirPaths[p] {
				return nil, fmt.Errorf("%w: %s", ErrRebaseConflict, p)
			}
			if _, done := edits[p]; done {
				continue
			}
			changed = append(changed, p)
			edits[p] = nil // deleted unless the local tree has it
			if f, err := localTree.FindEntry(p); err == nil {
				e := *f
				e.Name = path.Base(p)
				edits[p] = &e
			}
		}
	}
	sort.Strings(changed)

	newTree, err := editTree(repo.Storer, upTree, edits)
	if err != nil {
		return nil, fmt.Errorf("rebase: build the tree: %w", err)
	}
	now := time.Now()
	committer := local.Committer
	committer.When = now
	commit := &object.Commit{
		Author:       local.Author,
		Committer:    committer,
		Message:      local.Message,
		TreeHash:     newTree,
		ParentHashes: []plumbing.Hash{upstream.Hash},
	}
	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return nil, fmt.Errorf("rebase: encode the commit: %w", err)
	}
	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return nil, fmt.Errorf("rebase: store the commit: %w", err)
	}
	// The shallow clone knows the remote head now; record it as shallow so
	// history walks stop there.
	if err := addShallow(repo.Storer, upstream.Hash); err != nil {
		return nil, err
	}
	name := headRef.Name()
	if !name.IsBranch() {
		name = plumbing.NewBranchReferenceName(branch)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(name, hash)); err != nil {
		return nil, fmt.Errorf("rebase: move %s: %w", name, err)
	}
	if wt, err := repo.Worktree(); err == nil {
		if err := wt.Reset(&gogit.ResetOptions{Commit: hash, Mode: gogit.HardReset}); err != nil {
			return nil, fmt.Errorf("rebase: check out the rebased commit: %w", err)
		}
	}
	return changed, nil
}

// addShallow adds h to the repository's shallow list.
func addShallow(s storer.Storer, h plumbing.Hash) error {
	ss, ok := s.(storer.ShallowStorer)
	if !ok {
		return nil
	}
	cur, err := ss.Shallow()
	if err != nil {
		return fmt.Errorf("rebase: read the shallow list: %w", err)
	}
	for _, c := range cur {
		if c == h {
			return nil
		}
	}
	if err := ss.SetShallow(append(cur, h)); err != nil {
		return fmt.Errorf("rebase: write the shallow list: %w", err)
	}
	return nil
}

// editTree writes a copy of base with edits applied (path → entry, nil to
// delete) and returns its hash. Unchanged subtrees keep their hashes.
func editTree(s storer.EncodedObjectStorer, base *object.Tree, edits map[string]*object.TreeEntry) (plumbing.Hash, error) {
	here := map[string]*object.TreeEntry{}
	sub := map[string]map[string]*object.TreeEntry{}
	for p, e := range edits {
		dir, rest, nested := strings.Cut(p, "/")
		if !nested {
			here[dir] = e
			continue
		}
		if sub[dir] == nil {
			sub[dir] = map[string]*object.TreeEntry{}
		}
		sub[dir][rest] = e
	}
	var entries []object.TreeEntry
	seen := map[string]bool{}
	if base != nil {
		for _, e := range base.Entries {
			seen[e.Name] = true
			switch {
			case sub[e.Name] != nil:
				var child *object.Tree
				if e.Mode == filemode.Dir {
					t, err := object.GetTree(s, e.Hash)
					if err != nil {
						return plumbing.ZeroHash, fmt.Errorf("read tree %s: %w", e.Name, err)
					}
					child = t
				}
				h, err := editTree(s, child, sub[e.Name])
				if err != nil {
					return plumbing.ZeroHash, err
				}
				if h != plumbing.ZeroHash {
					entries = append(entries, object.TreeEntry{Name: e.Name, Mode: filemode.Dir, Hash: h})
				}
			case here[e.Name] != nil || hasKey(here, e.Name):
				if ne := here[e.Name]; ne != nil {
					entries = append(entries, *ne)
				}
			default:
				entries = append(entries, e)
			}
		}
	}
	for name, e := range here {
		if !seen[name] && e != nil {
			entries = append(entries, *e)
		}
	}
	for name, edits := range sub {
		if seen[name] {
			continue
		}
		h, err := editTree(s, nil, edits)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if h != plumbing.ZeroHash {
			entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
		}
	}
	if len(entries) == 0 {
		return plumbing.ZeroHash, nil // an empty directory is dropped
	}
	// Git orders tree entries by name, a directory as if it ended in "/".
	sortKey := func(e object.TreeEntry) string {
		if e.Mode == filemode.Dir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(entries, func(i, j int) bool { return sortKey(entries[i]) < sortKey(entries[j]) })
	tree := &object.Tree{Entries: entries}
	obj := s.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode tree: %w", err)
	}
	return s.SetEncodedObject(obj)
}

func hasKey(m map[string]*object.TreeEntry, k string) bool {
	_, ok := m[k]
	return ok
}
