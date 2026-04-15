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
	"fmt"
	"os"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// GoGitClient implements GitClient using the go-git library.
// All git operations run in-process — no git binary is required in the controller container.
type GoGitClient struct{}

// NewGoGitClient constructs a new GoGitClient.
func NewGoGitClient() *GoGitClient {
	return &GoGitClient{}
}

// Clone performs a shallow (depth=1) clone of the repo into dir.
func (c *GoGitClient) Clone(ctx context.Context, url, branch, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create clone dir: %w", err)
	}

	opts := &gogit.CloneOptions{
		URL:          url,
		Depth:        1,
		SingleBranch: true,
	}
	if branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(branch)
	}

	if _, err := gogit.PlainCloneContext(ctx, dir, false, opts); err != nil {
		return fmt.Errorf("git clone %s: %w", url, err)
	}
	return nil
}

// Checkout creates or switches to branch in the repo at dir.
// If the branch does not exist, it is created from the current HEAD.
func (c *GoGitClient) Checkout(ctx context.Context, dir, branch string) error {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("open repo at %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	// Try to switch to an existing branch first.
	checkoutErr := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Force:  false,
	})
	if checkoutErr == nil {
		return nil
	}

	// Branch does not exist — create it from HEAD.
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Create: true,
	}); err != nil {
		return fmt.Errorf("git checkout -b %s: %w", branch, err)
	}
	return nil
}

// CommitAll stages all changes and creates a commit with the given message and author.
func (c *GoGitClient) CommitAll(ctx context.Context, dir, message, authorName, authorEmail string) error {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("open repo at %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	// Stage all changes.
	if _, err := wt.Add("."); err != nil {
		return fmt.Errorf("git add .: %w", err)
	}

	sig := &object.Signature{
		Name:  authorName,
		Email: authorEmail,
		When:  time.Now(),
	}
	if _, err := wt.Commit(message, &gogit.CommitOptions{
		Author:            sig,
		Committer:         sig,
		AllowEmptyCommits: true,
	}); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// Push pushes HEAD to the remote branch using token-based HTTPS authentication.
func (c *GoGitClient) Push(ctx context.Context, dir, remote, branch, token string) error {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("open repo at %s: %w", dir, err)
	}

	pushOpts := &gogit.PushOptions{
		RemoteName: remote,
		RefSpecs:   []config.RefSpec{config.RefSpec("HEAD:refs/heads/" + branch)},
		Force:      false,
	}
	if token != "" {
		pushOpts.Auth = &gogithttp.BasicAuth{
			Username: "x-access-token",
			Password: token,
		}
	}

	if err := repo.PushContext(ctx, pushOpts); err != nil {
		if err == gogit.NoErrAlreadyUpToDate {
			return nil
		}
		return fmt.Errorf("git push %s %s: %w", remote, branch, err)
	}
	return nil
}
