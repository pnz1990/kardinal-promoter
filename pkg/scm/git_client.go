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
	"net/http"
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
	"go.opentelemetry.io/otel/attribute"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// ErrNothingToCommit is returned by CommitAll when the work tree has no
// changes. Callers treat it as "the environment is already at the target".
var ErrNothingToCommit = errors.New("nothing to commit, working tree clean")

// ErrAlreadyCommitted is returned by CommitAll when the work tree has no
// changes because HEAD is already a commit with the same message: an earlier
// attempt of the same promotion committed and pushed it, and its result was
// lost (for example a status write that failed after the push). It wraps
// ErrNothingToCommit.
var ErrAlreadyCommitted = fmt.Errorf("%w: HEAD is this promotion's commit from an earlier attempt", ErrNothingToCommit)

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
// A clone error reads "git clone <url>: <reason>". Every error names the URL
// once and never contains URL credentials.
func (c *GoGitClient) Clone(ctx context.Context, url, branch, dir, token string) (err error) {
	ctx, span := tracing.Start(ctx, "git clone", attribute.String("server.address", tracing.HostOf(url)),
		attribute.String("kardinal.git.branch", branch))
	defer func() { tracing.End(span, err) }()
	start := time.Now()
	defer func() { observeGit("clone", start, err) }()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create clone dir for %s: %w", RedactURL(url), err)
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
func (c *GoGitClient) CloneAt(ctx context.Context, url, commitSHA, dir, token string) (err error) {
	ctx, span := tracing.Start(ctx, "git clone", attribute.String("server.address", tracing.HostOf(url)),
		attribute.String("kardinal.git.commit", commitSHA))
	defer func() { tracing.End(span, err) }()
	start := time.Now()
	defer func() { observeGit("clone", start, err) }()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create clone dir for %s: %w", RedactURL(url), err)
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
		return fmt.Errorf("get worktree of %s: %w", RedactURL(url), err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{Hash: *hash, Force: true}); err != nil {
		return fmt.Errorf("checkout %s in %s: %w", commitSHA, RedactURL(url), err)
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
		return fmt.Errorf("get worktree of %s: %w", repoName(repo, dir), err)
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
		if head, err := repo.Head(); err == nil {
			if commit, err := repo.CommitObject(head.Hash()); err == nil &&
				strings.TrimSpace(commit.Message) == strings.TrimSpace(message) {
				return ErrAlreadyCommitted
			}
		}
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
// callers use it only for branches kardinal owns (kardinal/...).
func (c *GoGitClient) Push(ctx context.Context, dir, remote, branch, token string, force bool) (err error) {
	ctx, span := tracing.Start(ctx, "git push", attribute.String("server.address", tracing.HostOf(remote)),
		attribute.String("kardinal.git.branch", branch), attribute.Bool("kardinal.git.force", force))
	defer func() { tracing.End(span, err) }()
	start := time.Now()
	defer func() { observeGit("push", start, err) }()
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

	var checked plumbing.Hash
	if !force {
		// go-git's own fast-forward check walks history and fails with
		// "object not found" in a shallow clone, so check the remote tip here.
		remoteHash, found, lerr := remoteBranchHash(ctx, rem, auth, target)
		if lerr == nil && found && remoteHash != head.Hash() && !isAncestor(repo, remoteHash, head.Hash()) {
			return fmt.Errorf("git push %s %s: %w", remote, branch, ErrNonFastForward)
		}
		checked = remoteHash
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
		if errors.Is(err, gogit.ErrNonFastForwardUpdate) || isConcurrentUpdate(err.Error()) {
			return fmt.Errorf("git push %s %s: %w", remote, branch, ErrNonFastForward)
		}
		// Another writer moved the branch between the check above and the
		// push: go-git then walks from a remote head it does not have.
		if !force && errors.Is(err, plumbing.ErrObjectNotFound) {
			if now, found, lerr := remoteBranchHash(ctx, rem, auth, target); lerr == nil && found && now != checked {
				return fmt.Errorf("git push %s %s: %w", remote, branch, ErrNonFastForward)
			}
		}
		return fmt.Errorf("git push %s %s: %s", remote, branch, gitErrorText(err))
	}
	return nil
}

// concurrentUpdateReasons are the ref update failures a server reports when
// another writer moved the branch while this push was in flight: the push
// lost a race and can be retried on the new head, like a non-fast-forward.
var concurrentUpdateReasons = []string{
	"non-fast-forward", "fetch first", "failed to update ref", "failed to lock",
	"cannot lock ref", "stale info", "reference already exists", "incorrect old value",
}

// isConcurrentUpdate reports whether a push error is a lost race on the ref.
func isConcurrentUpdate(msg string) bool {
	msg = strings.ToLower(msg)
	for _, r := range concurrentUpdateReasons {
		if strings.Contains(msg, r) {
			return true
		}
	}
	return false
}

// repoName names the repository repo, opened at dir, in an error: its origin
// URL without credentials, or dir when it has no origin.
func repoName(repo *gogit.Repository, dir string) string {
	if rem, err := repo.Remote("origin"); err == nil && len(rem.Config().URLs) > 0 {
		return RedactURL(rem.Config().URLs[0])
	}
	return dir
}

// maxErrorBody is how many characters of an HTTP response body a git error
// keeps.
const maxErrorBody = 200

// httpBodyErrors are the go-git errors that end with the HTTP response body.
var httpBodyErrors = []error{
	transport.ErrAuthenticationRequired, transport.ErrAuthorizationFailed, transport.ErrRepositoryNotFound,
}

// gitErrorText returns the text of a go-git error on one line, without URL
// credentials. An HTTP error ends with the status and the response body, if
// any. go-git appends the body to its 401, 403 and 404 errors
// ("authentication required: Unauthorized\n", or ": " when the body is
// empty). It reports other codes as "unexpected client error: unexpected
// requesting "<url>" status code: 500", without the body; the caller's text
// already names the URL, so that part becomes "HTTP 500 Internal Server
// Error" and the body follows it. Runs of whitespace become one space, and a
// body longer than maxErrorBody characters, such as a proxy's HTML error
// page, is cut with "…". Credentials are removed before the cut, so a URL the
// cut runs through keeps none of them.
func gitErrorText(err error) string {
	head, body := err.Error(), ""
	var unexpected *plumbing.UnexpectedError
	var httpErr *gogithttp.Err
	if errors.As(err, &unexpected) && errors.As(unexpected.Err, &httpErr) && httpErr.Response != nil {
		code := httpErr.Response.StatusCode
		status := strings.TrimSpace(fmt.Sprintf("HTTP %d %s", code, http.StatusText(code)))
		head, body = strings.Replace(head, unexpected.Error(), status, 1), httpErr.Reason
	} else {
		for _, bodyErr := range httpBodyErrors {
			prefix := bodyErr.Error() + ": "
			if i := strings.Index(head, prefix); i >= 0 && errors.Is(err, bodyErr) {
				head, body = head[:i+len(bodyErr.Error())], head[i+len(prefix):]
				break
			}
		}
	}
	head, body = oneLine(head), oneLine(body)
	if body == "" {
		return strings.TrimSpace(strings.TrimSuffix(head, ":"))
	}
	if runes := []rune(body); len(runes) > maxErrorBody {
		body = strings.TrimSpace(string(runes[:maxErrorBody])) + "…"
	}
	return head + ": " + body
}

// oneLine returns s without URL credentials, with each run of whitespace as
// one space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(RedactText(s)), " ")
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
