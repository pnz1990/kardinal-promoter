// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// commits is Commits on GitHub: the shared repo's commits on branch, newest
// first, at most 100 a page.
func (g *github) commits(ctx context.Context, branch string, limit int) ([]Commit, error) {
	const pageSize = 100
	var out []Commit
	for page := 1; len(out) < limit; page++ {
		var raw []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Name  string `json:"name"`
					Email string `json:"email"`
				} `json:"author"`
			} `json:"commit"`
		}
		path := fmt.Sprintf("%s/commits?sha=%s&per_page=%d&page=%d", g.repoPath(), url.QueryEscape(branch), min(pageSize, limit), page)
		if err := g.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		for _, c := range raw {
			out = append(out, Commit{SHA: c.SHA, Message: c.Commit.Message,
				AuthorName: c.Commit.Author.Name, AuthorEmail: c.Commit.Author.Email})
		}
		if len(raw) < min(pageSize, limit) {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// pushRemote is PushRemote on GitHub: the shared repo over HTTPS, with the
// suite's token as the password (any user name; x-access-token by
// convention). A push from the test runner may only write branches under
// BranchPrefix (framework.Env.PushBranch checks it).
func (g *github) pushRemote() (string, string) {
	return fmt.Sprintf("%s/%s.git", g.cloneBase, g.repo), g.token
}
