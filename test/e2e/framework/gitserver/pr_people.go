// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// PRPeople is a Server that reads who a PR asks for review and who it is
// assigned to, and the commits a merge left. Forgejo, Gitea and GitLab
// implement it.
type PRPeople interface {
	// PRReviewersAndAssignees returns the logins of the PR's requested
	// reviewers and of its assignees.
	PRReviewersAndAssignees(ctx context.Context, r Repo, number int) (reviewers, assignees []string, err error)
	// Commit returns the message and the number of parents of commit sha.
	Commit(ctx context.Context, r Repo, sha string) (message string, parents int, err error)
}

var (
	_ PRPeople = (*forgejo)(nil)
	_ PRPeople = (*gitlab)(nil)
)

type login struct {
	Login    string `json:"login"`
	Username string `json:"username"`
}

func logins(us []login) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		if u.Login != "" {
			out = append(out, u.Login)
		} else {
			out = append(out, u.Username)
		}
	}
	return out
}

func (f *forgejo) PRReviewersAndAssignees(ctx context.Context, r Repo, number int) ([]string, []string, error) {
	var pr struct {
		RequestedReviewers []login `json:"requested_reviewers"`
		Assignees          []login `json:"assignees"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", f.repoPath(r), number), nil, &pr); err != nil {
		return nil, nil, err
	}
	return logins(pr.RequestedReviewers), logins(pr.Assignees), nil
}

func (f *forgejo) Commit(ctx context.Context, r Repo, sha string) (string, int, error) {
	var c struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("%s/git/commits/%s", f.repoPath(r), url.PathEscape(sha)), nil, &c); err != nil {
		return "", 0, err
	}
	return c.Commit.Message, len(c.Parents), nil
}

func (g *gitlab) PRReviewersAndAssignees(ctx context.Context, r Repo, number int) ([]string, []string, error) {
	var mr struct {
		Reviewers []login `json:"reviewers"`
		Assignees []login `json:"assignees"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests/%d", g.projectPath(r), number), nil, &mr); err != nil {
		return nil, nil, err
	}
	return logins(mr.Reviewers), logins(mr.Assignees), nil
}

func (g *gitlab) Commit(ctx context.Context, r Repo, sha string) (string, int, error) {
	var c struct {
		Message   string   `json:"message"`
		ParentIDs []string `json:"parent_ids"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s/repository/commits/%s", g.projectPath(r), url.PathEscape(sha)), nil, &c); err != nil {
		return "", 0, err
	}
	return c.Message, len(c.ParentIDs), nil
}
