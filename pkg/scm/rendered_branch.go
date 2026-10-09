// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
)

// BranchCloner is implemented by git clients that can check out a branch
// that may not exist yet: the rendered branch of layout: branch.
type BranchCloner interface {
	// CloneOrInit clones branch into dir. When the remote has no such branch
	// it creates an empty repository in dir with url as origin instead, and
	// reports created; the first commit then becomes the branch's root commit
	// when it is pushed. depth 0 clones the whole history.
	CloneOrInit(ctx context.Context, url, branch, dir, token string, depth int) (created bool, err error)
}

// CommitMessage is a commit and its message.
type CommitMessage struct {
	SHA     string
	Message string
}

// HistoryReader is implemented by git clients that can list the commits of
// a checkout.
type HistoryReader interface {
	// CommitMessages returns up to limit commits reachable from HEAD in dir,
	// newest first.
	CommitMessages(ctx context.Context, dir string, limit int) ([]CommitMessage, error)
}

// CloneOrInit implements BranchCloner.
func (c *GoGitClient) CloneOrInit(ctx context.Context, url, branch, dir, token string, depth int) (bool, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return false, fmt.Errorf("create clone dir for %s: %w", RedactURL(url), err)
	}
	_, err := gogit.PlainCloneContext(ctx, dir, false, &gogit.CloneOptions{
		URL:           url,
		Depth:         depth,
		SingleBranch:  true,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		Auth:          httpAuth(url, token),
	})
	if err == nil {
		return false, nil
	}
	if !isMissingBranch(err) {
		return false, fmt.Errorf("git clone %s: %s", RedactURL(url), gitErrorText(err))
	}
	// The branch does not exist: confirm the repository does (a wrong URL or
	// token must not look like a new branch), then start an empty one.
	if err := os.RemoveAll(dir); err != nil {
		return false, fmt.Errorf("clean clone dir: %w", err)
	}
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		return false, fmt.Errorf("init %s: %w", dir, err)
	}
	rem, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}})
	if err != nil {
		return false, fmt.Errorf("add origin %s: %w", RedactURL(url), err)
	}
	if _, err := rem.ListContext(ctx, &gogit.ListOptions{Auth: httpAuth(url, token)}); err != nil &&
		!errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return false, fmt.Errorf("git ls-remote %s: %s", RedactURL(url), gitErrorText(err))
	}
	// HEAD points at the new branch, so the first commit is on it.
	head := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))
	if err := repo.Storer.SetReference(head); err != nil {
		return false, fmt.Errorf("set HEAD to %s: %w", branch, err)
	}
	return true, nil
}

// isMissingBranch reports whether a clone failed because the branch does not
// exist on the remote.
func isMissingBranch(err error) bool {
	if errors.Is(err, plumbing.ErrReferenceNotFound) || errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "couldn't find remote ref") || strings.Contains(msg, "reference not found")
}

// CommitMessages implements HistoryReader.
func (c *GoGitClient) CommitMessages(_ context.Context, dir string, limit int) ([]CommitMessage, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("open repository %s: %w", dir, err)
	}
	head, err := repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read HEAD in %s: %w", dir, err)
	}
	iter, err := repo.Log(&gogit.LogOptions{From: head.Hash()})
	if err != nil {
		return nil, fmt.Errorf("git log in %s: %w", dir, err)
	}
	defer iter.Close()
	var out []CommitMessage
	err = iter.ForEach(func(cm *object.Commit) error {
		if len(out) >= limit {
			return errStopLog
		}
		out = append(out, CommitMessage{SHA: cm.Hash.String(), Message: cm.Message})
		return nil
	})
	if err != nil && !errors.Is(err, errStopLog) && !errors.Is(err, plumbing.ErrObjectNotFound) {
		return nil, fmt.Errorf("git log in %s: %w", dir, err)
	}
	return out, nil
}

var errStopLog = errors.New("stop")

// TreeReader is implemented by git clients that can read the files of a
// commit in a checkout.
type TreeReader interface {
	// CommitFiles returns the regular files of commit sha in dir, by
	// slash-separated path. It fails when they add up to more than maxBytes.
	CommitFiles(ctx context.Context, dir, sha string, maxBytes int64) (map[string][]byte, error)
}

// AncestryChecker is implemented by git clients that can tell whether a
// commit is reachable from a branch of a full clone.
type AncestryChecker interface {
	// ReachableFrom reports whether commit is branch's head or one of its
	// ancestors, in the clone dir (refs/remotes/origin/<branch>).
	ReachableFrom(ctx context.Context, dir, commit, branch string) (bool, error)
}

// CommitFiles implements TreeReader.
func (c *GoGitClient) CommitFiles(_ context.Context, dir, sha string, maxBytes int64) (map[string][]byte, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("open repository %s: %w", dir, err)
	}
	cm, err := repo.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return nil, fmt.Errorf("read commit %s: %w", sha, err)
	}
	tree, err := cm.Tree()
	if err != nil {
		return nil, fmt.Errorf("read the tree of %s: %w", sha, err)
	}
	out := map[string][]byte{}
	var total int64
	err = tree.Files().ForEach(func(f *object.File) error {
		if !f.Mode.IsRegular() {
			return nil
		}
		total += f.Size
		if total > maxBytes {
			return fmt.Errorf("commit %s holds more than %d bytes", sha, maxBytes)
		}
		content, err := f.Contents()
		if err != nil {
			return fmt.Errorf("read %s at %s: %w", f.Name, sha, err)
		}
		out[f.Name] = []byte(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReachableFrom implements AncestryChecker.
func (c *GoGitClient) ReachableFrom(_ context.Context, dir, commit, branch string) (bool, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return false, fmt.Errorf("open repository %s: %w", dir, err)
	}
	ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		return false, fmt.Errorf("resolve origin/%s: %w", branch, err)
	}
	tip, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return false, fmt.Errorf("read origin/%s: %w", branch, err)
	}
	if tip.Hash.String() == commit {
		return true, nil
	}
	target, err := repo.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return false, nil
	}
	ok, err := target.IsAncestor(tip)
	if err != nil {
		return false, fmt.Errorf("walk origin/%s: %w", branch, err)
	}
	return ok, nil
}

// RemoteHeadReader is implemented by git clients that can read the head of
// a branch on the remote without cloning it (git ls-remote).
type RemoteHeadReader interface {
	// RemoteBranchHead returns the commit branch points at on url, or ""
	// when the branch does not exist.
	RemoteBranchHead(ctx context.Context, url, branch, token string) (string, error)
}

// RemoteBranchHead implements RemoteHeadReader.
func (c *GoGitClient) RemoteBranchHead(ctx context.Context, url, branch, token string) (string, error) {
	rem := gogit.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{url}})
	refs, err := rem.ListContext(ctx, &gogit.ListOptions{Auth: httpAuth(url, token)})
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s: %s", RedactURL(url), gitErrorText(err))
	}
	want := plumbing.NewBranchReferenceName(branch)
	for _, r := range refs {
		if r.Name() == want {
			return r.Hash().String(), nil
		}
	}
	return "", nil
}
