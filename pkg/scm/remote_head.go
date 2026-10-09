// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"fmt"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
)

// RemoteHeadReader is implemented by git clients that can read the commit a
// remote branch is at without cloning (git ls-remote). The PromotionStep
// reconciler uses it while a PR waits for its merge, to rebuild the PR
// branch when the base branch moved.
type RemoteHeadReader interface {
	RemoteBranchHead(ctx context.Context, url, branch, token string) (string, error)
}

// RemoteBranchHead returns the commit branch is at on the remote url, or ""
// when the remote has no such branch.
func (c *GoGitClient) RemoteBranchHead(ctx context.Context, url, branch, token string) (string, error) {
	rem := gogit.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{url}})
	h, ok, err := remoteBranchHash(ctx, rem, httpAuth(url, token), plumbing.NewBranchReferenceName(branch))
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s: %s", RedactURL(url), gitErrorText(err))
	}
	if !ok {
		return "", nil
	}
	return h.String(), nil
}

var _ RemoteHeadReader = (*GoGitClient)(nil)
