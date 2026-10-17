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

// CommitPayload is a commit's signed payload and signature as the SCM API
// returns them: the commit object without its signature header, and the
// header's value. The caller recomputes the commit SHA from both, so a
// provider cannot answer for another commit.
type CommitPayload struct {
	// SHA is the commit the provider answered for.
	SHA string
	// Payload is the signed commit object, without its signature header.
	Payload string
	// Signature is the signature header's value (empty: unsigned).
	Signature string
}

// CommitPayloadReader is implemented by the providers whose API returns a
// commit's signed payload: GitHub, Forgejo and Gitea. Image verification
// reads it to verify gitsign signatures itself (commits.authorities).
type CommitPayloadReader interface {
	// CommitPayload returns the signed payload and signature of commit sha
	// in repo ("owner/name"). A commit that does not exist is an error.
	CommitPayload(ctx context.Context, repo, sha string) (CommitPayload, error)
}

// ErrCommitPayloadUnsupported is returned when the provider's API does not
// return a commit's signed payload (GitLab, Bitbucket, Azure DevOps).
var ErrCommitPayloadUnsupported = errors.New("the SCM provider's API does not return a commit's signed payload " +
	"(GitHub, Forgejo and Gitea do), so its gitsign signature cannot be verified")

// commitVerificationPayload is the verification object GitHub and
// Forgejo/Gitea return with a git commit.
type commitVerificationPayload struct {
	Signature string `json:"signature"`
	Payload   string `json:"payload"`
}

// CommitPayload implements CommitPayloadReader with
// GET /repos/{repo}/git/commits/{sha} (verification.payload and
// verification.signature).
func (g *GitHubProvider) CommitPayload(ctx context.Context, repo, sha string) (CommitPayload, error) {
	var result struct {
		SHA          string                    `json:"sha"`
		Verification commitVerificationPayload `json:"verification"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/git/commits/%s", repo, url.PathEscape(sha)), nil, &result); err != nil {
		return CommitPayload{}, fmt.Errorf("get commit %s@%s: %w", repo, sha, err)
	}
	return CommitPayload{SHA: result.SHA, Payload: result.Verification.Payload, Signature: result.Verification.Signature}, nil
}

// CommitPayload implements CommitPayloadReader with
// GET /api/v1/repos/{owner}/{name}/git/commits/{sha}
// (commit.verification.payload and commit.verification.signature).
func (f *ForgejoProvider) CommitPayload(ctx context.Context, repo, sha string) (CommitPayload, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return CommitPayload{}, err
	}
	var result struct {
		SHA    string `json:"sha"`
		Commit struct {
			Verification commitVerificationPayload `json:"verification"`
		} `json:"commit"`
	}
	if err := f.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/repos/%s/%s/git/commits/%s", owner, name, url.PathEscape(sha)), nil, &result); err != nil {
		return CommitPayload{}, fmt.Errorf("get commit %s@%s: %w", repo, sha, err)
	}
	v := result.Commit.Verification
	return CommitPayload{SHA: result.SHA, Payload: v.Payload, Signature: v.Signature}, nil
}

// CommitPayload forwards to the current provider when its API returns
// signed payloads, and returns ErrCommitPayloadUnsupported otherwise.
func (d *DynamicProvider) CommitPayload(ctx context.Context, repo, sha string) (CommitPayload, error) {
	r, ok := d.current().(CommitPayloadReader)
	if !ok {
		return CommitPayload{}, ErrCommitPayloadUnsupported
	}
	return r.CommitPayload(ctx, repo, sha)
}

// CommitPayload implements CommitPayloadReader, checking the repository
// like every other call.
func (g *guardedProvider) CommitPayload(ctx context.Context, repo, sha string) (CommitPayload, error) {
	if err := g.check("read a commit in", repo); err != nil {
		return CommitPayload{}, err
	}
	r, ok := g.inner.(CommitPayloadReader)
	if !ok {
		return CommitPayload{}, ErrCommitPayloadUnsupported
	}
	return r.CommitPayload(ctx, repo, sha)
}

var (
	_ CommitPayloadReader = (*GitHubProvider)(nil)
	_ CommitPayloadReader = (*ForgejoProvider)(nil)
	_ CommitPayloadReader = (*DynamicProvider)(nil)
	_ CommitPayloadReader = (*guardedProvider)(nil)
)
