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
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecGitClient implements GitClient using os/exec to call the system git binary.
// Shallow cloning with --depth=1 keeps clone times under 2 seconds for typical repos.
type ExecGitClient struct{}

// NewExecGitClient constructs a new ExecGitClient.
func NewExecGitClient() *ExecGitClient {
	return &ExecGitClient{}
}

// Clone performs a shallow (--depth=1) clone of the repo into dir.
func (c *ExecGitClient) Clone(ctx context.Context, url, branch, dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return fmt.Errorf("create parent dir for clone: %w", err)
	}
	args := []string{"clone", "--depth=1", "--single-branch"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, url, dir)
	if err := runGit(ctx, "", args...); err != nil {
		return fmt.Errorf("git clone %s: %w", url, err)
	}
	return nil
}

// Checkout creates or switches to branch in dir. If the branch does not exist,
// it creates it from the current HEAD.
func (c *ExecGitClient) Checkout(ctx context.Context, dir, branch string) error {
	// Try to switch to an existing branch first.
	err := runGit(ctx, dir, "checkout", branch)
	if err == nil {
		return nil
	}
	// Branch does not exist — create it.
	if err2 := runGit(ctx, dir, "checkout", "-b", branch); err2 != nil {
		return fmt.Errorf("git checkout -b %s: %w", branch, err2)
	}
	return nil
}

// CommitAll stages all changes and creates a signed commit with the given message.
func (c *ExecGitClient) CommitAll(ctx context.Context, dir, message, authorName, authorEmail string) error {
	if err := runGit(ctx, dir, "add", "--all"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + authorName,
		"GIT_AUTHOR_EMAIL=" + authorEmail,
		"GIT_COMMITTER_NAME=" + authorName,
		"GIT_COMMITTER_EMAIL=" + authorEmail,
	}
	if err := runGitWithEnv(ctx, dir, env, "commit", "--allow-empty", "-m", message); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// Push pushes the branch to the remote, embedding the token in the remote URL
// for HTTPS authentication.
func (c *ExecGitClient) Push(ctx context.Context, dir, remote, branch, token string) error {
	// Embed token in remote URL for HTTPS auth.
	// Only set the URL temporarily using git config.
	if token != "" {
		// Get the current remote URL and inject the token.
		out, err := runGitOutput(ctx, dir, "remote", "get-url", remote)
		if err != nil {
			return fmt.Errorf("get remote url: %w", err)
		}
		remoteURL := strings.TrimSpace(out)
		authedURL, err := injectToken(remoteURL, token)
		if err != nil {
			return fmt.Errorf("inject token: %w", err)
		}
		// Temporarily override remote URL.
		if err := runGit(ctx, dir, "remote", "set-url", remote, authedURL); err != nil {
			return fmt.Errorf("set remote url: %w", err)
		}
		// Restore original URL after push regardless of success.
		defer func() { _ = runGit(ctx, dir, "remote", "set-url", remote, remoteURL) }()
	}

	// Push HEAD to the specified remote branch name.
	// Using "HEAD:<branch>" allows pushing from the current branch to any
	// remote branch name regardless of local branch state.
	if err := runGit(ctx, dir, "push", remote, "HEAD:"+branch); err != nil {
		return fmt.Errorf("git push %s %s: %w", remote, branch, err)
	}
	return nil
}

// injectToken inserts a token into an HTTPS git URL as the password component.
func injectToken(rawURL, token string) (string, error) {
	// Support https://github.com/... → https://x-access-token:TOKEN@github.com/...
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(rawURL, prefix) {
			return prefix + "x-access-token:" + token + "@" + strings.TrimPrefix(rawURL, prefix), nil
		}
	}
	return "", fmt.Errorf("unsupported remote URL scheme (expected https://): %s", rawURL)
}

// runGit runs a git subcommand in the given directory.
func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := runGitOutputErr(ctx, dir, nil, args...)
	return err
}

// runGitWithEnv runs a git subcommand with additional environment variables.
func runGitWithEnv(ctx context.Context, dir string, env []string, args ...string) error {
	_, err := runGitOutputErr(ctx, dir, env, args...)
	return err
}

// runGitOutput runs a git subcommand and returns stdout.
func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	return runGitOutputErr(ctx, dir, nil, args...)
}

func runGitOutputErr(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := isExitError(err, &exitErr); ok {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, string(exitErr.Stderr))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// isExitError is a helper to avoid direct cast in error checking.
func isExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}
