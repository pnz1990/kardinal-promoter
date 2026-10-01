// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// ApprovePR approves the MR as the test runner (root). GitLab keeps no review
// body with an approval, so body is posted as a note.
func (g *gitlab) ApprovePR(ctx context.Context, r Repo, number int, body string) error {
	if err := g.do(ctx, http.MethodPost, fmt.Sprintf("%s/merge_requests/%d/approve", g.projectPath(r), number), nil, nil); err != nil {
		return err
	}
	if body == "" {
		return nil
	}
	return g.do(ctx, http.MethodPost, fmt.Sprintf("%s/merge_requests/%d/notes", g.projectPath(r), number),
		map[string]string{"body": body}, nil)
}

// FastForwarder is a Server whose repos can merge by fast-forward, which
// leaves no merge commit.
type FastForwarder interface {
	// SetFastForwardMerge makes the repo merge PRs by fast-forward only.
	SetFastForwardMerge(ctx context.Context, r Repo) error
}

// SetFastForwardMerge sets the project's merge_method to ff.
func (g *gitlab) SetFastForwardMerge(ctx context.Context, r Repo) error {
	return g.do(ctx, http.MethodPut, g.projectPath(r), map[string]string{"merge_method": "ff"}, nil)
}

// Subgrouper is a Server with nested namespaces.
type Subgrouper interface {
	// CreateSubgroupRepo creates a repo like CreateRepo, in subgroup sub of
	// the owner (owner/sub/name). The subgroup is created when missing and
	// kept: DeleteRepo removes only the repo.
	CreateSubgroupRepo(ctx context.Context, sub, name string, files map[string][]byte) (Repo, error)
}

// CreateSubgroupRepo creates subgroup sub under the owner group if needed,
// then the project in it.
func (g *gitlab) CreateSubgroupRepo(ctx context.Context, sub, name string, files map[string][]byte) (Repo, error) {
	full := g.owner + "/" + sub
	err := g.do(ctx, http.MethodGet, "/api/v4/groups/"+url.PathEscape(full), nil, nil)
	if IsNotFound(err) {
		var parent struct {
			ID int `json:"id"`
		}
		if err := g.do(ctx, http.MethodGet, "/api/v4/groups/"+url.PathEscape(g.owner), nil, &parent); err != nil {
			return Repo{}, err
		}
		err = g.do(ctx, http.MethodPost, "/api/v4/groups", map[string]interface{}{
			"name": sub, "path": sub, "parent_id": parent.ID, "visibility": "public",
		}, nil)
		// Two tests creating it at once: the loser sees "has already been taken".
		if se, ok := err.(*StatusError); ok && se.Code == http.StatusBadRequest {
			err = g.do(ctx, http.MethodGet, "/api/v4/groups/"+url.PathEscape(full), nil, nil)
		}
	}
	if err != nil {
		return Repo{}, err
	}
	return g.createIn(ctx, full, name, files)
}
