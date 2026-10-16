// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrCommentSearchUnsupported is returned by a provider (or a wrapper of
// one) that cannot search a pull request's comments.
var ErrCommentSearchUnsupported = errors.New("the SCM provider cannot search pull request comments")

// maxCommentPages bounds a comment search, oldest first: a pull request
// with more comments is searched in its first maxCommentPages pages.
const maxCommentPages = 20

// PRCommentFinder is implemented by SCM providers that can tell whether a
// pull request already has a comment containing marker: a caller that must
// comment once searches before it posts, so a post whose response was lost,
// or whose status write did not land, is not posted again (search before
// create, docs/design/23-change-requests.md section 7.3).
type PRCommentFinder interface {
	HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error)
}

type commentBody struct {
	Body string `json:"body"`
}

func containsMarker(page []commentBody, marker string) bool {
	for _, c := range page {
		if strings.Contains(c.Body, marker) {
			return true
		}
	}
	return false
}

// searchPages reads up to maxCommentPages pages of size comments with get
// and reports whether one contains marker. A short page is the last.
func searchPages(ctx context.Context, marker string, size int,
	get func(ctx context.Context, page int, out *[]commentBody) error) (bool, error) {
	for page := 1; page <= maxCommentPages; page++ {
		var got []commentBody
		if err := get(ctx, page, &got); err != nil {
			return false, err
		}
		if containsMarker(got, marker) {
			return true, nil
		}
		if len(got) < size {
			return false, nil
		}
	}
	return false, nil
}

// HasPRComment searches a GitHub pull request's issue comments.
func (g *GitHubProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	found, err := searchPages(ctx, marker, 100, func(ctx context.Context, page int, out *[]commentBody) error {
		return g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&page=%d", repo, prNumber, page), nil, out)
	})
	if err != nil {
		return false, fmt.Errorf("search comments of PR %s#%d: %w", repo, prNumber, err)
	}
	return found, nil
}

// HasPRComment searches a GitLab merge request's notes.
func (g *GitLabProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	projectID := encodeProjectID(repo)
	found, err := searchPages(ctx, marker, 100, func(ctx context.Context, page int, out *[]commentBody) error {
		return g.do(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/notes?per_page=100&page=%d&sort=asc",
			projectID, prNumber, page), nil, out)
	})
	if err != nil {
		return false, fmt.Errorf("search notes of MR %s!%d: %w", repo, prNumber, err)
	}
	return found, nil
}

// HasPRComment searches a Forgejo/Gitea pull request's issue comments.
func (f *ForgejoProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return false, err
	}
	// Forgejo and Gitea answer at most MAX_RESPONSE_ITEMS (50 by default).
	found, err := searchPages(ctx, marker, 50, func(ctx context.Context, page int, out *[]commentBody) error {
		return f.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments?limit=50&page=%d",
			owner, name, prNumber, page), nil, out)
	})
	if err != nil {
		return false, fmt.Errorf("search comments of PR %s#%d: %w", repo, prNumber, err)
	}
	return found, nil
}

// HasPRComment forwards to the active provider.
func (d *DynamicProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	f, ok := d.current().(PRCommentFinder)
	if !ok {
		return false, ErrCommentSearchUnsupported
	}
	return f.HasPRComment(ctx, repo, prNumber, marker)
}

// HasPRComment implements PRCommentFinder.
func (g *guardedProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	if err := g.check("search PR comments in", repo); err != nil {
		return false, err
	}
	f, ok := g.inner.(PRCommentFinder)
	if !ok {
		return false, ErrCommentSearchUnsupported
	}
	return f.HasPRComment(ctx, repo, prNumber, marker)
}
