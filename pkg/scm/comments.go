// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// ErrCommentSearchUnsupported is returned by a provider (or a wrapper of
// one) that cannot search a pull request's comments.
var ErrCommentSearchUnsupported = errors.New("the SCM provider cannot search pull request comments")

// maxCommentPages bounds a comment search, oldest first: a pull request
// with more comments is searched in its first maxCommentPages pages.
const maxCommentPages = 20

// PRCommentFinder is implemented by SCM providers that can tell whether a
// pull request already has a comment containing marker that kardinal's own
// SCM identity wrote: a caller that must comment once searches before it
// posts, so a post whose response was lost, or whose status write did not
// land, is not posted again (search before create,
// docs/design/23-change-requests.md section 7.3). A comment by anyone else
// does not count, whatever it says: who may comment on the pull request
// cannot keep kardinal from posting.
type PRCommentFinder interface {
	HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error)
}

// comment is one pull request comment: its text and who wrote it.
type comment struct {
	body, author string
}

// selfIDs caches each provider's own identity (the token's user, or the
// GitHub App's ID), by provider: it does not change while the provider
// lives (a rotated token builds a new provider).
var selfIDs sync.Map

// self is the identity of p, from lookup the first time.
func self(ctx context.Context, p interface{}, lookup func(context.Context) (string, error)) (string, error) {
	if v, ok := selfIDs.Load(p); ok {
		return v.(string), nil
	}
	id, err := lookup(ctx)
	if err != nil {
		return "", fmt.Errorf("read the SCM identity: %w", err)
	}
	if id == "" {
		return "", errors.New("read the SCM identity: empty")
	}
	selfIDs.Store(p, id)
	return id, nil
}

// mine reports whether a comment in cs is by me and contains marker.
func mine(cs []comment, me, marker string) bool {
	for _, c := range cs {
		if c.author == me && strings.Contains(c.body, marker) {
			return true
		}
	}
	return false
}

// searchPages reads up to maxCommentPages pages of size items with get,
// which returns the page's comments and how many items it had, and reports
// whether a comment by me contains marker. A short page is the last.
func searchPages(ctx context.Context, me, marker string, size int,
	get func(ctx context.Context, page int) ([]comment, int, error)) (bool, error) {
	for page := 1; page <= maxCommentPages; page++ {
		got, n, err := get(ctx, page)
		if err != nil {
			return false, err
		}
		if mine(got, me, marker) {
			return true, nil
		}
		if n < size {
			return false, nil
		}
	}
	return false, nil
}

// HasPRComment searches a GitHub pull request's issue comments. A GitHub
// App's comments are matched by the App (performed_via_github_app.id), a
// token's by its user's login.
func (g *GitHubProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	me, err := self(ctx, g, func(ctx context.Context) (string, error) {
		if a, ok := g.tokens.(interface{ AppID() int64 }); ok {
			return "app:" + strconv.FormatInt(a.AppID(), 10), nil
		}
		var u struct {
			Login string `json:"login"`
		}
		if err := g.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
			return "", err
		}
		return "user:" + u.Login, nil
	})
	if err != nil {
		return false, err
	}
	found, err := searchPages(ctx, me, marker, 100, func(ctx context.Context, page int) ([]comment, int, error) {
		var got []struct {
			Body string `json:"body"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			App *struct {
				ID int64 `json:"id"`
			} `json:"performed_via_github_app"`
		}
		err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&page=%d", repo, prNumber, page), nil, &got)
		out := make([]comment, 0, len(got))
		for _, c := range got {
			author := "user:" + c.User.Login
			if c.App != nil {
				author = "app:" + strconv.FormatInt(c.App.ID, 10)
			}
			out = append(out, comment{body: c.Body, author: author})
		}
		return out, len(got), err
	})
	if err != nil {
		return false, fmt.Errorf("search comments of PR %s#%d: %w", repo, prNumber, err)
	}
	return found, nil
}

// HasPRComment searches a GitLab merge request's notes, by the token's user.
func (g *GitLabProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	me, err := self(ctx, g, func(ctx context.Context) (string, error) {
		var u struct {
			ID int64 `json:"id"`
		}
		if err := g.do(ctx, http.MethodGet, "/api/v4/user", nil, &u); err != nil {
			return "", err
		}
		return strconv.FormatInt(u.ID, 10), nil
	})
	if err != nil {
		return false, err
	}
	projectID := encodeProjectID(repo)
	found, err := searchPages(ctx, me, marker, 100, func(ctx context.Context, page int) ([]comment, int, error) {
		var got []struct {
			Body   string `json:"body"`
			System bool   `json:"system"`
			Author struct {
				ID int64 `json:"id"`
			} `json:"author"`
		}
		err := g.do(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/notes?per_page=100&page=%d&sort=asc",
			projectID, prNumber, page), nil, &got)
		out := make([]comment, 0, len(got))
		for _, c := range got {
			if !c.System {
				out = append(out, comment{body: c.Body, author: strconv.FormatInt(c.Author.ID, 10)})
			}
		}
		return out, len(got), err
	})
	if err != nil {
		return false, fmt.Errorf("search notes of MR %s!%d: %w", repo, prNumber, err)
	}
	return found, nil
}

// HasPRComment searches a Forgejo/Gitea pull request's issue comments, by
// the token's user.
func (f *ForgejoProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return false, err
	}
	me, err := self(ctx, f, func(ctx context.Context) (string, error) {
		var u struct {
			ID int64 `json:"id"`
		}
		if err := f.do(ctx, http.MethodGet, "/api/v1/user", nil, &u); err != nil {
			return "", err
		}
		return strconv.FormatInt(u.ID, 10), nil
	})
	if err != nil {
		return false, err
	}
	// Forgejo and Gitea answer at most MAX_RESPONSE_ITEMS (50 by default).
	found, err := searchPages(ctx, me, marker, 50, func(ctx context.Context, page int) ([]comment, int, error) {
		var got []struct {
			Body string `json:"body"`
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		err := f.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments?limit=50&page=%d",
			owner, name, prNumber, page), nil, &got)
		out := make([]comment, 0, len(got))
		for _, c := range got {
			out = append(out, comment{body: c.Body, author: strconv.FormatInt(c.User.ID, 10)})
		}
		return out, len(got), err
	})
	if err != nil {
		return false, fmt.Errorf("search comments of PR %s#%d: %w", repo, prNumber, err)
	}
	return found, nil
}

// HasPRComment searches a Bitbucket Cloud pull request's comments, following
// the API's next links, by the token's user.
func (b *BitbucketProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return false, err
	}
	me, err := self(ctx, b, func(ctx context.Context) (string, error) {
		var u struct {
			UUID string `json:"uuid"`
		}
		if err := b.do(ctx, http.MethodGet, "/2.0/user", nil, &u); err != nil {
			return "", err
		}
		return u.UUID, nil
	})
	if err != nil {
		return false, err
	}
	for page := 1; page <= maxCommentPages; page++ {
		var got struct {
			Values []struct {
				Content struct {
					Raw string `json:"raw"`
				} `json:"content"`
				User struct {
					UUID string `json:"uuid"`
				} `json:"user"`
			} `json:"values"`
			Next string `json:"next"`
		}
		path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d/comments?pagelen=100&page=%d", workspace, repoSlug, prNumber, page)
		if err := b.do(ctx, http.MethodGet, path, nil, &got); err != nil {
			return false, fmt.Errorf("search comments of Bitbucket PR %s#%d: %w", repo, prNumber, err)
		}
		cs := make([]comment, 0, len(got.Values))
		for _, c := range got.Values {
			cs = append(cs, comment{body: c.Content.Raw, author: c.User.UUID})
		}
		if mine(cs, me, marker) {
			return true, nil
		}
		if got.Next == "" {
			return false, nil
		}
	}
	return false, nil
}

// HasPRComment searches a Bitbucket Data Center pull request's activities
// for a comment, page by page, by the token's user (applinks whoami).
func (b *BitbucketDCProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	path, err := b.prPath(repo)
	if err != nil {
		return false, err
	}
	me, err := self(ctx, b, b.whoami)
	if err != nil {
		return false, err
	}
	start := 0
	for page := 1; page <= maxCommentPages; page++ {
		var got struct {
			Values []struct {
				Action  string `json:"action"`
				Comment *struct {
					Text   string `json:"text"`
					Author struct {
						Name string `json:"name"`
					} `json:"author"`
				} `json:"comment"`
			} `json:"values"`
			IsLastPage    bool `json:"isLastPage"`
			NextPageStart int  `json:"nextPageStart"`
		}
		if err := b.do(ctx, http.MethodGet, fmt.Sprintf("%s/%d/activities?start=%d&limit=100", path, prNumber, start), nil, &got); err != nil {
			return false, fmt.Errorf("search comments of Bitbucket Data Center PR %s#%d: %w", repo, prNumber, err)
		}
		var cs []comment
		for _, a := range got.Values {
			if a.Action == "COMMENTED" && a.Comment != nil {
				cs = append(cs, comment{body: a.Comment.Text, author: a.Comment.Author.Name})
			}
		}
		if mine(cs, me, marker) {
			return true, nil
		}
		if got.IsLastPage || got.NextPageStart <= start {
			return false, nil
		}
		start = got.NextPageStart
	}
	return false, nil
}

// whoami is the token's user name: Bitbucket Data Center's applinks whoami,
// which answers it as plain text.
func (b *BitbucketDCProvider) whoami(ctx context.Context) (string, error) {
	if b.APIURL == "" {
		return "", errors.New("bitbucket-datacenter scm: --scm-api-url (the server's base URL) is not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.APIURL+"/plugins/servlet/applinks/whoami", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	resp, err := b.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("whoami: %w", RedactErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return "", fmt.Errorf("whoami: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whoami answered %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(raw)), nil
}

// HasPRComment searches an Azure DevOps pull request's comment threads (the
// API returns them all at once), by the token's user (connectionData).
func (a *AzureDevOpsProvider) HasPRComment(ctx context.Context, repo string, prNumber int, marker string) (bool, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return false, err
	}
	me, err := self(ctx, a, func(ctx context.Context) (string, error) {
		var d struct {
			AuthenticatedUser struct {
				ID string `json:"id"`
			} `json:"authenticatedUser"`
		}
		if err := a.do(ctx, http.MethodGet, fmt.Sprintf("/%s/_apis/connectionData", org), nil, &d); err != nil {
			return "", err
		}
		return strings.ToLower(d.AuthenticatedUser.ID), nil
	})
	if err != nil {
		return false, err
	}
	var got struct {
		Value []struct {
			Comments []struct {
				Content string `json:"content"`
				Author  struct {
					ID string `json:"id"`
				} `json:"author"`
			} `json:"comments"`
		} `json:"value"`
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d/threads?api-version=%s",
		org, project, repoName, prNumber, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodGet, path, nil, &got); err != nil {
		return false, fmt.Errorf("search comments of ADO PR %s#%d: %w", repo, prNumber, err)
	}
	var cs []comment
	for _, t := range got.Value {
		for _, c := range t.Comments {
			cs = append(cs, comment{body: c.Content, author: strings.ToLower(c.Author.ID)})
		}
	}
	return mine(cs, me, marker), nil
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
