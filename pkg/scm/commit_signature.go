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
		Commit struct {
			Author struct {
				Email string `json:"email"`
			} `json:"author"`
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
	signer := result.Commit.Author.Email
	if result.Committer != nil && result.Committer.Login != "" {
		signer = result.Committer.Login
	}
	return CommitSignature{Verified: result.Commit.Verification.Verified, Signer: signer,
		Reason: result.Commit.Verification.Reason}, nil
}

// VerifyCommit implements CommitVerifier with
// GET /api/v1/repos/{owner}/{name}/git/commits/{sha} (commit.verification).
func (f *ForgejoProvider) VerifyCommit(ctx context.Context, repo, sha string) (CommitSignature, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return CommitSignature{}, err
	}
	var result struct {
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
	sig := CommitSignature{Verified: v.Verified, Reason: v.Reason}
	if v.Signer != nil {
		sig.Signer = v.Signer.Username
		if sig.Signer == "" {
			sig.Signer = v.Signer.Email
		}
	}
	return sig, nil
}

// VerifyCommit implements CommitVerifier with
// GET /api/v4/projects/{id}/repository/commits/{sha}/signature. GitLab
// answers 404 for an unsigned commit; the commit itself is read first, so a
// commit that does not exist is still an error.
func (g *GitLabProvider) VerifyCommit(ctx context.Context, repo, sha string) (CommitSignature, error) {
	project := encodeProjectID(repo)
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/repository/commits/%s", project, url.PathEscape(sha)), nil, nil); err != nil {
		return CommitSignature{}, fmt.Errorf("get commit %s@%s: %w", repo, sha, err)
	}
	var result struct {
		SignatureType      string `json:"signature_type"`
		VerificationStatus string `json:"verification_status"`
		GPGKeyUserEmail    string `json:"gpg_key_user_email"`
		Key                *struct {
			Title string `json:"title"`
		} `json:"key"`
		X509Certificate *struct {
			Email string `json:"email"`
		} `json:"x509_certificate"`
		CommitSource string `json:"commit_source"`
	}
	err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/repository/commits/%s/signature", project, url.PathEscape(sha)), nil, &result)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return CommitSignature{Reason: "unsigned"}, nil
	}
	if err != nil {
		return CommitSignature{}, fmt.Errorf("get commit signature %s@%s: %w", repo, sha, err)
	}
	sig := CommitSignature{Verified: result.VerificationStatus == "verified", Reason: result.VerificationStatus}
	switch {
	case result.GPGKeyUserEmail != "":
		sig.Signer = result.GPGKeyUserEmail
	case result.X509Certificate != nil:
		sig.Signer = result.X509Certificate.Email
	case result.Key != nil:
		sig.Signer = result.Key.Title
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
