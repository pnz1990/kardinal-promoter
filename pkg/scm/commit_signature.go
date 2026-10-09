// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"errors"
	"fmt"
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
	// Identities are what an allowed-signers list matches: the signer's
	// login, email and key ID or fingerprint, as the provider reports them.
	// A commit the platform signed itself has only PlatformSignerGitHub or
	// PlatformSignerGitLab.
	Identities []string
}

// Identities of commits an SCM platform signed with its own key: GitHub
// signs web UI edits and merges as web-flow, GitLab reports verified_system
// for commits it signed (web UI, API).
const (
	PlatformSignerGitHub = "web-flow"
	PlatformSignerGitLab = "gitlab-system"
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
		sig.Identities = []string{PlatformSignerGitHub}
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
	if v.Signer != nil {
		sig.Signer = v.Signer.Username
		if sig.Signer == "" {
			sig.Signer = v.Signer.Email
		}
		sig.Identities = nonEmpty(v.Signer.Username, v.Signer.Email)
	}
	return sig, nil
}

// VerifyCommit implements CommitVerifier with
// GET /api/v4/projects/{id}/repository/commits/{sha}/signature. GitLab
// answers 404 for an unsigned commit; the commit itself is read first, so a
// commit that does not exist is still an error.
func (g *GitLabProvider) VerifyCommit(ctx context.Context, repo, sha string) (CommitSignature, error) {
	project := encodeProjectID(repo)
	var commit struct {
		ID string `json:"id"`
	}
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/repository/commits/%s", project, url.PathEscape(sha)), nil, &commit); err != nil {
		return CommitSignature{}, fmt.Errorf("get commit %s@%s: %w", repo, sha, err)
	}
	var result struct {
		SignatureType      string `json:"signature_type"`
		VerificationStatus string `json:"verification_status"`
		GPGKeyUserEmail    string `json:"gpg_key_user_email"`
		GPGKeyPrimaryKeyID string `json:"gpg_key_primary_keyid"`
		Key                *struct {
			Title       string `json:"title"`
			Fingerprint string `json:"fingerprint_sha256"`
		} `json:"key"`
		X509Certificate *struct {
			Email                string `json:"email"`
			SubjectKeyIdentifier string `json:"subject_key_identifier"`
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
	switch {
	case system:
		sig.Signer, sig.Identities = PlatformSignerGitLab, []string{PlatformSignerGitLab}
	case result.GPGKeyUserEmail != "":
		sig.Signer = result.GPGKeyUserEmail
		sig.Identities = nonEmpty(result.GPGKeyUserEmail, result.GPGKeyPrimaryKeyID)
	case result.X509Certificate != nil:
		sig.Signer = result.X509Certificate.Email
		sig.Identities = nonEmpty(result.X509Certificate.Email, result.X509Certificate.SubjectKeyIdentifier)
	case result.Key != nil:
		sig.Signer = result.Key.Title
		sig.Identities = nonEmpty(result.Key.Title, result.Key.Fingerprint)
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
