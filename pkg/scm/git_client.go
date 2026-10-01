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
	"net/url"
	"os"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// ErrNothingToCommit is returned by CommitAll when the work tree has no
// changes. Callers treat it as "the environment is already at the target".
var ErrNothingToCommit = errors.New("nothing to commit, working tree clean")

// ErrNonFastForward is returned by Push when the remote branch has moved and
// the push would not be a fast-forward (another writer pushed first).
var ErrNonFastForward = errors.New("non-fast-forward: remote branch has new commits")

// GoGitClient implements GitClient using the go-git library.
// All git operations run in-process — no git binary is required in the controller container.
type GoGitClient struct{}

// NewGoGitClient constructs a new GoGitClient.
func NewGoGitClient() *GoGitClient {
	return &GoGitClient{}
}

// httpAuth returns the HTTP basic-auth credentials for remoteURL, or nil when
// no token is set or the URL is not HTTP(S) (ssh and file remotes use their
// own transports). The username is taken from the URL userinfo when present;
// otherwise it is the provider's token username: "x-token-auth" for
// Bitbucket Cloud, "oauth2" for GitLab, and "x-access-token" elsewhere
// (GitHub, Forgejo/Gitea and Azure DevOps accept any username with a token).
func httpAuth(remoteURL, token string) transport.AuthMethod {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(remoteURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil
	}
	username := "x-access-token"
	host := strings.ToLower(u.Hostname())
	switch {
	case u.User != nil && u.User.Username() != "":
		username = u.User.Username()
	case host == "bitbucket.org":
		username = "x-token-auth"
	case host == "gitlab.com" || strings.HasPrefix(host, "gitlab."):
		username = "oauth2"
	}
	return &gogithttp.BasicAuth{Username: username, Password: token}
}

// Clone performs a shallow (depth=1) clone of branch into dir, authenticating
// with token over HTTP(S) when it is set. dir must not already contain a repo.
// A clone error reads "git clone <url>: <reason>"; errors never contain URL
// credentials.
func (c *GoGitClient) Clone(ctx context.Context, url, branch, dir, token string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create clone dir: %w", err)
	}

	opts := &gogit.CloneOptions{
		URL:          url,
		Depth:        1,
		SingleBranch: true,
		Auth:         httpAuth(url, token),
	}
	if branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(branch)
	}

	if _, err := gogit.PlainCloneContext(ctx, dir, false, opts); err != nil {
		return fmt.Errorf("git clone %s: %s", RedactURL(url), gitErrorText(err))
	}
	return nil
}

// CloneAt clones url into dir and checks out commitSHA (detached). It is used
// to read the content of a specific commit, e.g. the source of a config Bundle.
func (c *GoGitClient) CloneAt(ctx context.Context, url, commitSHA, dir, token string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create clone dir: %w", err)
	}
	repo, err := gogit.PlainCloneContext(ctx, dir, false, &gogit.CloneOptions{
		URL:        url,
		NoCheckout: true,
		Auth:       httpAuth(url, token),
	})
	if err != nil {
		return fmt.Errorf("git clone %s: %s", RedactURL(url), gitErrorText(err))
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(commitSHA))
	if err != nil {
		return fmt.Errorf("resolve commit %s in %s: %w", commitSHA, RedactURL(url), err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{Hash: *hash, Force: true}); err != nil {
		return fmt.Errorf("checkout %s: %w", commitSHA, err)
	}
	return nil
}

// CommitAll stages all changes (including deletions) and creates a commit
// with the given message and author. It returns ErrNothingToCommit when the
// work tree has no changes, instead of creating an empty commit.
func (c *GoGitClient) CommitAll(ctx context.Context, dir, message, authorName, authorEmail string) error {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("open repo at %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	// Stage all changes, including deletions (git add -A).
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		return fmt.Errorf("git add -A: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if status.IsClean() {
		return ErrNothingToCommit
	}

	sig := &object.Signature{
		Name:  authorName,
		Email: authorEmail,
		When:  time.Now(),
	}
	if _, err := wt.Commit(message, &gogit.CommitOptions{
		Author:    sig,
		Committer: sig,
	}); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// Push pushes HEAD to the remote branch using token-based HTTPS authentication.
// With force=false it returns ErrNonFastForward when the remote branch has
// commits that HEAD does not contain. force=true overwrites the remote branch;
// callers use it only for branches kardinal owns (kardinal/<bundle>/<env>).
func (c *GoGitClient) Push(ctx context.Context, dir, remote, branch, token string, force bool) error {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("open repo at %s: %w", dir, err)
	}

	// go-git does not resolve a symbolic "HEAD" source in a refspec: it matches
	// no local hash reference, the push sends nothing and reports
	// NoErrAlreadyUpToDate. Push the resolved commit hash instead.
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("resolve HEAD in %s: %w", dir, err)
	}

	rem, err := repo.Remote(remote)
	if err != nil {
		return fmt.Errorf("get remote %s: %w", remote, err)
	}
	remoteURL := ""
	if urls := rem.Config().URLs; len(urls) > 0 {
		remoteURL = urls[0]
	}
	auth := httpAuth(remoteURL, token)
	target := plumbing.NewBranchReferenceName(branch)

	if !force {
		// go-git's own fast-forward check walks history and fails with
		// "object not found" in a shallow clone, so check the remote tip here.
		if remoteHash, found, lerr := remoteBranchHash(ctx, rem, auth, target); lerr == nil && found &&
			remoteHash != head.Hash() && !isAncestor(repo, remoteHash, head.Hash()) {
			return fmt.Errorf("git push %s %s: %w", remote, branch, ErrNonFastForward)
		}
	}

	refSpec := head.Hash().String() + ":" + target.String()
	if force {
		refSpec = "+" + refSpec
	}
	pushOpts := &gogit.PushOptions{
		RemoteName: remote,
		RefSpecs:   []config.RefSpec{config.RefSpec(refSpec)},
		Force:      force,
		Auth:       auth,
	}

	if err := repo.PushContext(ctx, pushOpts); err != nil {
		if errors.Is(err, gogit.NoErrAlreadyUpToDate) {
			return nil
		}
		if errors.Is(err, gogit.ErrNonFastForwardUpdate) || strings.Contains(err.Error(), "non-fast-forward") {
			return fmt.Errorf("git push %s %s: %w", remote, branch, ErrNonFastForward)
		}
		return fmt.Errorf("git push %s %s: %s", remote, branch, gitErrorText(err))
	}
	return nil
}

// maxErrorBody is how many characters of an HTTP response body a git error
// keeps.
const maxErrorBody = 200

// httpBodyErrors are the go-git errors that end with the HTTP response body.
var httpBodyErrors = []error{
	transport.ErrAuthenticationRequired, transport.ErrAuthorizationFailed, transport.ErrRepositoryNotFound,
}

// gitErrorText returns the text of a go-git error on one line, without URL
// credentials and without what go-git leaves at its end when it appends an
// HTTP response body: the body's trailing newline ("authentication required:
// Unauthorized\n"), or ": " when the body is empty. Runs of whitespace become
// one space, and a body longer than maxErrorBody characters, such as a proxy's
// HTML error page, is cut with "…".
func gitErrorText(err error) string {
	text := strings.Join(strings.Fields(RedactURL(err.Error())), " ")
	text = strings.TrimSpace(strings.TrimSuffix(text, ":"))
	for _, bodyErr := range httpBodyErrors {
		prefix := bodyErr.Error() + ": "
		i := strings.Index(text, prefix)
		if i < 0 || !errors.Is(err, bodyErr) {
			continue
		}
		head, body := text[:i+len(prefix)], []rune(text[i+len(prefix):])
		if len(body) <= maxErrorBody {
			return text
		}
		return head + strings.TrimSpace(string(body[:maxErrorBody])) + "…"
	}
	return text
}

// remoteBranchHash returns the hash the remote advertises for ref.
func remoteBranchHash(ctx context.Context, rem *gogit.Remote, auth transport.AuthMethod, ref plumbing.ReferenceName) (plumbing.Hash, bool, error) {
	refs, err := rem.ListContext(ctx, &gogit.ListOptions{Auth: auth})
	if err != nil {
		return plumbing.ZeroHash, false, err
	}
	for _, r := range refs {
		if r.Name() == ref {
			return r.Hash(), true, nil
		}
	}
	return plumbing.ZeroHash, false, nil
}

// isAncestor reports whether anc is reachable from head in the local object
// store. Missing objects (shallow history) count as "not an ancestor".
func isAncestor(repo *gogit.Repository, anc, head plumbing.Hash) bool {
	if _, err := repo.CommitObject(anc); err != nil {
		return false
	}
	c, err := repo.CommitObject(head)
	if err != nil {
		return false
	}
	found := false
	_ = object.NewCommitPreorderIter(c, nil, nil).ForEach(func(cm *object.Commit) error {
		if cm.Hash == anc {
			found = true
			return storer.ErrStop
		}
		return nil
	})
	return found
}
