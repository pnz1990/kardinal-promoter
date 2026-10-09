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
