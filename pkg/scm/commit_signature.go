// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// CommitSignature is what an SCM provider reports about a commit's
// signature (GPG, SSH or S/MIME), as it verified it against the keys its
// users registered.
type CommitSignature struct {
	// Verified is true when the provider verified the signature.
	Verified bool
	// Signer names who signed: a user or email, as the provider reports it.
	Signer string
	// Reason is the provider's reason when Verified is false ("unsigned",
	// "unknown_key", ...).
	Reason string
	// SHA is the commit the provider answered for; the caller checks it is
	// the one it asked about.
	SHA string
	// Identities are what an allowed-signers list matches: the verified
	// signer's login and email, as the provider reports them (never a key
	// title, which the key's owner chooses). A commit the platform signed
	// itself has only its platform identity.
	Identities []string
	// Platform is true when the SCM signed the commit with its own key (a
	// web UI edit or merge), not a user: GitHub web-flow, GitLab
	// verified_system, the Forgejo/Gitea instance key. Image verification
	// refuses such a commit unless commits.allowedSigners lists the
	// platform identity.
	Platform bool
}

// Identities of commits an SCM platform signed with its own key: GitHub
// signs web UI edits and merges as web-flow, GitLab reports verified_system
// for commits it signed (web UI, API), Forgejo and Gitea sign with the
// instance key (repository.signing).
const (
	PlatformSignerGitHub  = "web-flow"
	PlatformSignerGitLab  = "gitlab-system"
	PlatformSignerForgejo = "forgejo-instance"
)

func nonEmpty(v ...string) []string {
	var out []string
	for _, s := range v {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// CommitVerifier is implemented by the providers that report commit
// signatures: GitHub, GitLab, Forgejo and Gitea. Image verification
// (pkg/reconciler/imageverification) fails a config Bundle whose commit must
// be signed when the provider does not implement it.
type CommitVerifier interface {
	// VerifyCommit returns the signature state of commit sha in repo
	// ("owner/name", a GitLab path with groups). A commit that does not
	// exist is an error.
	VerifyCommit(ctx context.Context, repo, sha string) (CommitSignature, error)
}

// ErrCommitVerificationUnsupported is returned by DynamicProvider when the
// configured provider cannot report commit signatures.
var ErrCommitVerificationUnsupported = errors.New("the SCM provider does not report commit signatures")

// VerifyCommit implements CommitVerifier with GET /repos/{repo}/commits/{sha}
// (commit.verification).
func (g *GitHubProvider) VerifyCommit(ctx context.Context, repo, sha string) (CommitSignature, error) {
	var result struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Email string `json:"email"`
			} `json:"committer"`
			Verification struct {
				Verified bool   `json:"verified"`
				Reason   string `json:"reason"`
			} `json:"verification"`
		} `json:"commit"`
		Committer *struct {
			Login string `json:"login"`
		} `json:"committer"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s", repo, url.PathEscape(sha)), nil, &result); err != nil {
		return CommitSignature{}, fmt.Errorf("get commit %s@%s: %w", repo, sha, err)
	}
	// GitHub verifies a signature against the committer's keys, so the
	// signer is the committer.
	login := ""
	if result.Committer != nil {
		login = result.Committer.Login
	}
	sig := CommitSignature{Verified: result.Commit.Verification.Verified, Reason: result.Commit.Verification.Reason,
		SHA: result.SHA, Signer: login, Identities: nonEmpty(login, result.Commit.Committer.Email)}
	if sig.Signer == "" {
		sig.Signer = result.Commit.Committer.Email
	}
	if login == PlatformSignerGitHub {
		sig.Identities, sig.Platform = []string{PlatformSignerGitHub}, true
	}
	return sig, nil
}

// VerifyCommit implements CommitVerifier with
// GET /api/v1/repos/{owner}/{name}/git/commits/{sha} (commit.verification).
func (f *ForgejoProvider) VerifyCommit(ctx context.Context, repo, sha string) (CommitSignature, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return CommitSignature{}, err
	}
	var result struct {
		SHA    string `json:"sha"`
		Commit struct {
			Verification struct {
				Verified bool   `json:"verified"`
				Reason   string `json:"reason"`
				Signer   *struct {
					Name     string `json:"name"`
					Email    string `json:"email"`
					Username string `json:"username"`
				} `json:"signer"`
			} `json:"verification"`
		} `json:"commit"`
	}
	if err := f.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/repos/%s/%s/git/commits/%s", owner, name, url.PathEscape(sha)), nil, &result); err != nil {
		return CommitSignature{}, fmt.Errorf("get commit %s@%s: %w", repo, sha, err)
	}
	v := result.Commit.Verification
	sig := CommitSignature{Verified: v.Verified, Reason: v.Reason, SHA: result.SHA}
	if v.Signer == nil {
		return sig, nil
	}
	// Forgejo sets signer.name to the signing user's login and never sets
	// username; Gitea sets username to the login and name to the display
	// name (services/convert ToVerification in both). For the instance key
	// both report the instance's SIGNING_NAME, which is not a user.
	login := v.Signer.Username
	if login == "" {
		login = v.Signer.Name
	}
	sig.Signer = login
	if sig.Signer == "" {
		sig.Signer = v.Signer.Email
	}
	sig.Identities = nonEmpty(login, v.Signer.Email)
	if !v.Verified || login == "" {
		return sig, nil
	}
	user, err := f.userExists(ctx, login)
	if err != nil {
		return CommitSignature{}, fmt.Errorf("look up commit %s@%s signer %q: %w", repo, sha, login, err)
	}
	if !user {
		// No such user: the instance key (repository.signing).
		sig.Signer, sig.Identities, sig.Platform = PlatformSignerForgejo, []string{PlatformSignerForgejo}, true
	}
	return sig, nil
}

// userExists reports whether login is a user of the instance
// (GET /api/v1/users/{login}; 404 means no such user). A token without the
// read:user scope gets 403, so the lookup is then made anonymously, which
// sees public and limited users. A private user then reads as no user, and
// its signatures as the instance key: refused, never accepted as a person.
// Grant the token read:user to resolve private users.
func (f *ForgejoProvider) userExists(ctx context.Context, login string) (bool, error) {
	path := "/api/v1/users/" + url.PathEscape(login)
	err := f.do(ctx, http.MethodGet, path, nil, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden {
		err = f.getAnonymous(ctx, path)
	}
	switch {
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound:
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// getAnonymous is a GET without the token.
func (f *ForgejoProvider) getAnonymous(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.APIURL+path, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("execute request GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return newAPIError("forgejo", http.MethodGet, path, resp, raw)
	}
	return nil
}

// VerifyCommit implements CommitVerifier with
// GET /api/v4/projects/{id}/repository/commits/{sha}/signature. GitLab
// answers 404 for an unsigned commit; the commit itself is read first, so a
// commit that does not exist is still an error.
func (g *GitLabProvider) VerifyCommit(ctx context.Context, repo, sha string) (CommitSignature, error) {
	project := encodeProjectID(repo)
	var commit struct {
		ID             string `json:"id"`
		CommitterEmail string `json:"committer_email"`
	}
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/repository/commits/%s", project, url.PathEscape(sha)), nil, &commit); err != nil {
		return CommitSignature{}, fmt.Errorf("get commit %s@%s: %w", repo, sha, err)
	}
	var result struct {
		SignatureType      string `json:"signature_type"`
		VerificationStatus string `json:"verification_status"`
		GPGKeyUserEmail    string `json:"gpg_key_user_email"`
		X509Certificate    *struct {
			Email string `json:"email"`
		} `json:"x509_certificate"`
		CommitSource string `json:"commit_source"`
	}
	err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/repository/commits/%s/signature", project, url.PathEscape(sha)), nil, &result)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return CommitSignature{Reason: "unsigned", SHA: commit.ID}, nil
	}
	if err != nil {
		return CommitSignature{}, fmt.Errorf("get commit signature %s@%s: %w", repo, sha, err)
	}
	// verified_system: GitLab signed the commit itself (web UI, API).
	system := result.VerificationStatus == "verified_system"
	sig := CommitSignature{Verified: result.VerificationStatus == "verified" || system,
		Reason: result.VerificationStatus, SHA: commit.ID}
	// GitLab verifies a signature only when the key's owner has the
	// committer's email verified, so the identity is that email: the GPG
	// key's user email, the X.509 certificate's email, or for SSH (whose
	// response names only the key, by a title its owner chose) the commit's
	// committer email. GitLab reports no full GPG fingerprint (only the
	// 16-hex key ID, which is not unique), so keys are not identities.
	switch {
	case system:
		sig.Signer, sig.Identities, sig.Platform = PlatformSignerGitLab, []string{PlatformSignerGitLab}, true
	case result.GPGKeyUserEmail != "":
		sig.Signer, sig.Identities = result.GPGKeyUserEmail, nonEmpty(result.GPGKeyUserEmail)
	case result.X509Certificate != nil:
		sig.Signer, sig.Identities = result.X509Certificate.Email, nonEmpty(result.X509Certificate.Email)
	case result.SignatureType == "SSH" && sig.Verified:
		sig.Signer, sig.Identities = commit.CommitterEmail, nonEmpty(commit.CommitterEmail)
	}
	return sig, nil
}

// VerifyCommit forwards to the current provider when it reports commit
// signatures, and returns ErrCommitVerificationUnsupported otherwise.
func (d *DynamicProvider) VerifyCommit(ctx context.Context, repo, sha string) (CommitSignature, error) {
	v, ok := d.current().(CommitVerifier)
	if !ok {
		return CommitSignature{}, ErrCommitVerificationUnsupported
	}
	return v.VerifyCommit(ctx, repo, sha)
}
