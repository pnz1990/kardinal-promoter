// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// GitServer holds the scenario's git repositories: one bare repository on
// disk per remote URL. kardinal's own git client (scm.GoGitClient) reads and
// writes them through the file transport; only the URL is rewritten
// (RewritingGit), so clone, commit, push, rebase, ls-remote and history
// reads run kardinal's code against a real repository.
type GitServer struct {
	dir string
	mu  sync.Mutex
	// repos maps a remote URL (https://host/owner/repo) to its bare
	// repository directory.
	repos map[string]string
}

// NewGitServer keeps its repositories under dir.
func NewGitServer(dir string) *GitServer {
	return &GitServer{dir: dir, repos: map[string]string{}}
}

// normalizeURL drops a trailing .git and slash.
func normalizeURL(u string) string {
	u = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(u), "/"), ".git")
	return u
}

// CreateRepo creates the repository url with files committed on branch.
func (s *GitServer) CreateRepo(url, branch string, files map[string]string) error {
	url = normalizeURL(url)
	s.mu.Lock()
	if _, ok := s.repos[url]; ok {
		s.mu.Unlock()
		return fmt.Errorf("git server: repository %s exists", url)
	}
	bare := filepath.Join(s.dir, fmt.Sprintf("repo%d.git", len(s.repos)))
	s.repos[url] = bare
	s.mu.Unlock()
	repo, err := gogit.PlainInit(bare, true)
	if err != nil {
		return fmt.Errorf("git server: init %s: %w", url, err)
	}
	// HEAD names the default branch, as on a git host: a clone without a
	// branch checks it out.
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))); err != nil {
		return fmt.Errorf("git server: default branch: %w", err)
	}
	return s.Commit(url, branch, "initial content", files, nil)
}

// Path returns the bare repository directory of url.
func (s *GitServer) Path(url string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.repos[normalizeURL(url)]
	return p, ok
}

// rewrite maps a remote URL to the file URL of its bare repository; other
// URLs are returned unchanged.
func (s *GitServer) rewrite(url string) string {
	if p, ok := s.Path(url); ok {
		return "file://" + p
	}
	return url
}

// Commit writes files (path -> content; "" deletes) on branch as one commit
// on top of the branch head, or as the root commit of a new branch (from
// parent when set). It is how the scenario's "humans" change git.
func (s *GitServer) Commit(url, branch, message string, files map[string]string, parent *plumbing.Hash) error {
	bare, ok := s.Path(url)
	if !ok {
		return fmt.Errorf("git server: no repository %s", url)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	work, err := os.MkdirTemp(s.dir, "work")
	if err != nil {
		return fmt.Errorf("git server: workdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	repo, err := gogit.PlainInit(work, false)
	if err != nil {
		return fmt.Errorf("git server: init work: %w", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{"file://" + bare}}); err != nil {
		return fmt.Errorf("git server: remote: %w", err)
	}
	ref := plumbing.NewBranchReferenceName(branch)
	ferr := repo.Fetch(&gogit.FetchOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"+refs/heads/*:refs/remotes/origin/*"}})
	if ferr != nil && !errors.Is(ferr, gogit.NoErrAlreadyUpToDate) &&
		!strings.Contains(ferr.Error(), "remote repository is empty") {
		return fmt.Errorf("git server: fetch: %w", ferr)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("git server: worktree: %w", err)
	}
	var base *plumbing.Hash
	if r, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true); err == nil {
		h := r.Hash()
		base = &h
	} else if parent != nil {
		base = parent
	}
	if base != nil {
		if err := wt.Checkout(&gogit.CheckoutOptions{Hash: *base, Branch: ref, Create: true, Force: true}); err != nil {
			return fmt.Errorf("git server: checkout: %w", err)
		}
	} else {
		if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, ref)); err != nil {
			return fmt.Errorf("git server: head: %w", err)
		}
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		full := filepath.Join(work, filepath.FromSlash(p))
		if files[p] == "" {
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("git server: remove %s: %w", p, err)
			}
			if _, err := wt.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "entry not found") {
				return fmt.Errorf("git server: rm %s: %w", p, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("git server: mkdir: %w", err)
		}
		if err := os.WriteFile(full, []byte(files[p]), 0o600); err != nil {
			return fmt.Errorf("git server: write %s: %w", p, err)
		}
		if _, err := wt.Add(p); err != nil {
			return fmt.Errorf("git server: add %s: %w", p, err)
		}
	}
	sig := &object.Signature{Name: "scenario", Email: "scenario@example.com", When: time.Unix(1767225600, 0)}
	if _, err := wt.Commit(message, &gogit.CommitOptions{Author: sig, Committer: sig, AllowEmptyCommits: true}); err != nil {
		return fmt.Errorf("git server: commit: %w", err)
	}
	if err := repo.Push(&gogit.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec("+" + ref.String() + ":" + ref.String())}}); err != nil &&
		!errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return fmt.Errorf("git server: push: %w", err)
	}
	return nil
}

// Head returns the commit branch points at, or "" when it does not exist.
func (s *GitServer) Head(url, branch string) (string, error) {
	bare, ok := s.Path(url)
	if !ok {
		return "", fmt.Errorf("git server: no repository %s", url)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := gogit.PlainOpen(bare)
	if err != nil {
		return "", fmt.Errorf("git server: open: %w", err)
	}
	r, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("git server: ref %s: %w", branch, err)
	}
	return r.Hash().String(), nil
}

// ReadFile returns path on branch, and whether it exists.
func (s *GitServer) ReadFile(url, branch, path string) (string, bool, error) {
	bare, ok := s.Path(url)
	if !ok {
		return "", false, fmt.Errorf("git server: no repository %s", url)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := gogit.PlainOpen(bare)
	if err != nil {
		return "", false, fmt.Errorf("git server: open: %w", err)
	}
	r, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("git server: ref: %w", err)
	}
	c, err := repo.CommitObject(r.Hash())
	if err != nil {
		return "", false, fmt.Errorf("git server: commit: %w", err)
	}
	f, err := c.File(path)
	if errors.Is(err, object.ErrFileNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("git server: file %s: %w", path, err)
	}
	content, err := f.Contents()
	if err != nil {
		return "", false, fmt.Errorf("git server: read %s: %w", path, err)
	}
	return content, true, nil
}

// ListFiles returns the paths of every file on branch.
func (s *GitServer) ListFiles(url, branch string) ([]string, error) {
	bare, ok := s.Path(url)
	if !ok {
		return nil, fmt.Errorf("git server: no repository %s", url)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := gogit.PlainOpen(bare)
	if err != nil {
		return nil, fmt.Errorf("git server: open: %w", err)
	}
	r, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("git server: ref: %w", err)
	}
	c, err := repo.CommitObject(r.Hash())
	if err != nil {
		return nil, fmt.Errorf("git server: commit: %w", err)
	}
	files, err := c.Files()
	if err != nil {
		return nil, fmt.Errorf("git server: files: %w", err)
	}
	var out []string
	err = files.ForEach(func(f *object.File) error {
		out = append(out, f.Name)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Branches returns every branch of url with its commit.
func (s *GitServer) Branches(url string) (map[string]string, error) {
	bare, ok := s.Path(url)
	if !ok {
		return nil, fmt.Errorf("git server: no repository %s", url)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := gogit.PlainOpen(bare)
	if err != nil {
		return nil, fmt.Errorf("git server: open: %w", err)
	}
	refs, err := repo.Branches()
	if err != nil {
		return nil, fmt.Errorf("git server: branches: %w", err)
	}
	out := map[string]string{}
	err = refs.ForEach(func(r *plumbing.Reference) error {
		out[r.Name().Short()] = r.Hash().String()
		return nil
	})
	return out, err
}

// ErrMergeConflict is a merge whose branches changed the same file.
var ErrMergeConflict = errors.New("merge conflict")

// Merge merges head into base as a pull request merge does: a fast-forward
// when base is an ancestor of head, else a merge commit made by a
// file-level three-way merge (a file head changed since the merge base is
// taken from head, any other from base; a file both changed differently is
// a conflict, ErrMergeConflict). It returns the commit base points at
// afterwards.
func (s *GitServer) Merge(url, head, base string) (string, error) {
	bare, ok := s.Path(url)
	if !ok {
		return "", fmt.Errorf("git server: no repository %s", url)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := gogit.PlainOpen(bare)
	if err != nil {
		return "", fmt.Errorf("git server: open: %w", err)
	}
	hr, err := repo.Reference(plumbing.NewBranchReferenceName(head), true)
	if err != nil {
		return "", fmt.Errorf("git server: head branch %s: %w", head, err)
	}
	baseName := plumbing.NewBranchReferenceName(base)
	br, err := repo.Reference(baseName, true)
	if err != nil {
		return "", fmt.Errorf("git server: base branch %s: %w", base, err)
	}
	hc, err := repo.CommitObject(hr.Hash())
	if err != nil {
		return "", fmt.Errorf("git server: head commit: %w", err)
	}
	bc, err := repo.CommitObject(br.Hash())
	if err != nil {
		return "", fmt.Errorf("git server: base commit: %w", err)
	}
	if ok, err := bc.IsAncestor(hc); err == nil && ok {
		if err := repo.Storer.SetReference(plumbing.NewHashReference(baseName, hc.Hash)); err != nil {
			return "", fmt.Errorf("git server: update %s: %w", base, err)
		}
		return hc.Hash.String(), nil
	}
	bases, err := bc.MergeBase(hc)
	if err != nil || len(bases) == 0 {
		return "", fmt.Errorf("git server: no merge base of %s and %s: %v", head, base, err)
	}
	mb, headFiles, baseFiles := map[string]plumbing.Hash{}, map[string]plumbing.Hash{}, map[string]plumbing.Hash{}
	for _, x := range []struct {
		c *object.Commit
		m map[string]plumbing.Hash
	}{{bases[0], mb}, {hc, headFiles}, {bc, baseFiles}} {
		files, err := x.c.Files()
		if err != nil {
			return "", fmt.Errorf("git server: files: %w", err)
		}
		if err := files.ForEach(func(f *object.File) error { x.m[f.Name] = f.Hash; return nil }); err != nil {
			return "", fmt.Errorf("git server: files: %w", err)
		}
	}
	merged := map[string]plumbing.Hash{}
	for p, h := range baseFiles {
		merged[p] = h
	}
	paths := map[string]bool{}
	for p := range headFiles {
		paths[p] = true
	}
	for p := range mb {
		paths[p] = true
	}
	for p := range paths {
		hh, inHead := headFiles[p]
		mh, inBase := mb[p]
		if inHead == inBase && hh == mh {
			continue // head did not change p
		}
		bh, inTip := baseFiles[p]
		if (inTip != inBase || bh != mh) && (inTip != inHead || bh != hh) {
			return "", fmt.Errorf("git server: %s: %w", p, ErrMergeConflict)
		}
		if inHead {
			merged[p] = hh
		} else {
			delete(merged, p)
		}
	}
	tree, err := writeTree(repo, merged)
	if err != nil {
		return "", err
	}
	sig := object.Signature{Name: "scenario", Email: "scenario@example.com", When: time.Unix(1767225600, 0)}
	mc := &object.Commit{Author: sig, Committer: sig, Message: "Merge " + head, TreeHash: tree,
		ParentHashes: []plumbing.Hash{bc.Hash, hc.Hash}}
	obj := repo.Storer.NewEncodedObject()
	if err := mc.Encode(obj); err != nil {
		return "", fmt.Errorf("git server: encode merge: %w", err)
	}
	target, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return "", fmt.Errorf("git server: store merge: %w", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(baseName, target)); err != nil {
		return "", fmt.Errorf("git server: update %s: %w", base, err)
	}
	return target.String(), nil
}

// writeTree stores the tree of files (path -> blob) and returns its hash.
func writeTree(repo *gogit.Repository, files map[string]plumbing.Hash) (plumbing.Hash, error) {
	type dir struct {
		files map[string]plumbing.Hash
		dirs  map[string]map[string]plumbing.Hash
	}
	d := dir{files: map[string]plumbing.Hash{}, dirs: map[string]map[string]plumbing.Hash{}}
	for p, h := range files {
		if i := strings.Index(p, "/"); i >= 0 {
			sub := d.dirs[p[:i]]
			if sub == nil {
				sub = map[string]plumbing.Hash{}
				d.dirs[p[:i]] = sub
			}
			sub[p[i+1:]] = h
			continue
		}
		d.files[p] = h
	}
	var entries []object.TreeEntry
	for name, h := range d.files {
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: h})
	}
	for name, sub := range d.dirs {
		h, err := writeTree(repo, sub)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
	}
	// git orders tree entries by name, a directory as if it ended in "/".
	key := func(e object.TreeEntry) string {
		if e.Mode == filemode.Dir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })
	t := &object.Tree{Entries: entries}
	obj := repo.Storer.NewEncodedObject()
	if err := t.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("git server: encode tree: %w", err)
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("git server: store tree: %w", err)
	}
	return h, nil
}

// DeleteBranch removes branch.
func (s *GitServer) DeleteBranch(url, branch string) error {
	bare, ok := s.Path(url)
	if !ok {
		return fmt.Errorf("git server: no repository %s", url)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := gogit.PlainOpen(bare)
	if err != nil {
		return fmt.Errorf("git server: open: %w", err)
	}
	return repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(branch))
}

// RewritingGit is kardinal's git client with every remote URL of a scenario
// repository rewritten to its bare repository on disk. Every optional
// interface of scm.GoGitClient (rebase, ls-remote, history, rendered
// branches) is kept: the embedded client provides it, and the methods that
// take a URL are wrapped.
type RewritingGit struct {
	*scm.GoGitClient
	Server *GitServer
	// effect, when set, is told of every push (World.CrashAfter); dying
	// drops a push of a dead controller.
	effect func(what string)
	dying  func() bool
	// readFail, when set, is the error the remote reads (RemoteHeads,
	// BranchHistory, BranchGraph) return (World.FailGitReads).
	readFail func() error
}

func (g *RewritingGit) readErr() error {
	if g.readFail == nil {
		return nil
	}
	return g.readFail()
}

// Push is GoGitClient.Push, a side effect of the controller.
func (g *RewritingGit) Push(ctx context.Context, dir, remote, branch string, auth scm.GitAuth, force bool) error {
	if g.dying != nil && g.dying() {
		return fmt.Errorf("git push %s: connection reset", branch)
	}
	if err := g.GoGitClient.Push(ctx, dir, remote, branch, auth, force); err != nil {
		return err
	}
	if g.effect != nil {
		g.effect("git push " + branch)
	}
	return nil
}

// file:// remotes need no credentials: auth is dropped so the token of the
// https URL is not offered to the file transport.
func (g *RewritingGit) Clone(ctx context.Context, url, branch, dir string, auth scm.GitAuth) error {
	return g.GoGitClient.Clone(ctx, g.Server.rewrite(url), branch, dir, g.auth(url, auth))
}

func (g *RewritingGit) CloneAt(ctx context.Context, url, sha, dir string, auth scm.GitAuth) error {
	return g.GoGitClient.CloneAt(ctx, g.Server.rewrite(url), sha, dir, g.auth(url, auth))
}

func (g *RewritingGit) RemoteHeads(ctx context.Context, url string, auth scm.GitAuth) (map[string]string, error) {
	if err := g.readErr(); err != nil {
		return nil, err
	}
	return g.GoGitClient.RemoteHeads(ctx, g.Server.rewrite(url), g.auth(url, auth))
}

func (g *RewritingGit) BranchHistory(ctx context.Context, url, branch string, auth scm.GitAuth, max int) ([]scm.CommitPaths, error) {
	if err := g.readErr(); err != nil {
		return nil, err
	}
	return g.GoGitClient.BranchHistory(ctx, g.Server.rewrite(url), branch, g.auth(url, auth), max)
}

func (g *RewritingGit) BranchGraph(ctx context.Context, url, branch string, auth scm.GitAuth, max int) (string, map[string]scm.GraphCommit, error) {
	if err := g.readErr(); err != nil {
		return "", nil, err
	}
	return g.GoGitClient.BranchGraph(ctx, g.Server.rewrite(url), branch, g.auth(url, auth), max)
}

func (g *RewritingGit) CloneOrInit(ctx context.Context, url, branch, dir string, auth scm.GitAuth, depth int) (bool, error) {
	return g.GoGitClient.CloneOrInit(ctx, g.Server.rewrite(url), branch, dir, g.auth(url, auth), depth)
}

func (g *RewritingGit) RemoteBranchHead(ctx context.Context, url, branch string, auth scm.GitAuth) (string, error) {
	return g.GoGitClient.RemoteBranchHead(ctx, g.Server.rewrite(url), branch, g.auth(url, auth))
}

// auth drops the credentials for a scenario repository: the file
// transport takes none.
func (g *RewritingGit) auth(url string, auth scm.GitAuth) scm.GitAuth {
	if _, ok := g.Server.Path(url); ok {
		return scm.GitAuth{}
	}
	return auth
}
