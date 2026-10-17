// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"errors"
	"fmt"
	"slices"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// HeadContentComparer is implemented by git clients that can tell whether
// a remote branch's head holds exactly what the local HEAD commit holds.
// git-push adopts such a head of kardinal's PR branch as its own (#2095):
// pushing over it changes nothing anyone would deploy.
type HeadContentComparer interface {
	// SameContentAsHead reports whether commit sha, the head of branch on
	// dir's remote, has the tree and the parents of HEAD in dir. It is
	// false when the branch's head is no longer sha.
	SameContentAsHead(ctx context.Context, dir, remote, branch string, auth GitAuth, sha string) (bool, error)
}

// SameContentAsHead implements HeadContentComparer. The head commit is
// fetched alone (depth 1) into memory, so dir's repository is unchanged.
func (c *GoGitClient) SameContentAsHead(ctx context.Context, dir, remote, branch string, auth GitAuth, sha string) (bool, error) {
	repo, closeRepo, err := openRepo(dir)
	if err != nil {
		return false, err
	}
	defer func() { _ = closeRepo() }()
	head, err := repo.Head()
	if err != nil {
		return false, fmt.Errorf("resolve HEAD in %s: %w", dir, err)
	}
	local, err := repo.CommitObject(head.Hash())
	if err != nil {
		return false, fmt.Errorf("read HEAD commit in %s: %w", dir, err)
	}
	rem, err := repo.Remote(remote)
	if err != nil {
		return false, fmt.Errorf("get remote %s: %w", remote, err)
	}
	urls := rem.Config().URLs
	if len(urls) == 0 {
		return false, fmt.Errorf("remote %s has no URL", remote)
	}
	am, err := authMethod(urls[0], auth)
	if err != nil {
		return false, fmt.Errorf("git fetch %s %s: %w", remote, branch, err)
	}
	proxyOpts, release := sshScope(ctx, am)
	defer release()
	mem := memory.NewStorage()
	peek := gogit.NewRemote(mem, &config.RemoteConfig{Name: remote, URLs: urls})
	ref := plumbing.NewBranchReferenceName(branch)
	dst := plumbing.ReferenceName("refs/kardinal/peek")
	err = peek.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName: remote, Auth: am, ProxyOptions: proxyOpts, Depth: 1,
		RefSpecs: []config.RefSpec{config.RefSpec("+" + ref.String() + ":" + dst.String())},
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return false, fmt.Errorf("git fetch %s %s: %s", remote, branch, gitErrorText(err))
	}
	got, err := mem.Reference(dst)
	if err != nil {
		return false, fmt.Errorf("git fetch %s %s: %w", remote, branch, err)
	}
	if !SameCommit(got.Hash().String(), sha) {
		return false, nil
	}
	theirs, err := object.GetCommit(mem, got.Hash())
	if err != nil {
		return false, fmt.Errorf("read the head of %s: %w", branch, err)
	}
	return theirs.TreeHash == local.TreeHash && slices.Equal(theirs.ParentHashes, local.ParentHashes), nil
}
