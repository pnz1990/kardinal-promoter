// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// commits is Commits on GitLab: the project's repository commits on branch,
// newest first, at most 100 a page.
func (g *gitlab) commits(ctx context.Context, r Repo, branch string, limit int) ([]Commit, error) {
	const pageSize = 100
	var out []Commit
	for page := 1; len(out) < limit; page++ {
		var raw []struct {
			ID          string `json:"id"`
			Message     string `json:"message"`
			AuthorName  string `json:"author_name"`
			AuthorEmail string `json:"author_email"`
		}
		path := fmt.Sprintf("%s/repository/commits?ref_name=%s&per_page=%d&page=%d",
			g.projectPath(r), url.QueryEscape(branch), min(pageSize, limit), page)
		if err := g.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		for _, c := range raw {
			out = append(out, Commit{SHA: c.ID, Message: c.Message, AuthorName: c.AuthorName, AuthorEmail: c.AuthorEmail})
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
