// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"fmt"
	"net/http"
)

// PRCommitLister is a Server that can list the commits a PR adds to its base.
// Forgejo and Gitea implement it.
type PRCommitLister interface {
	// PRCommits returns the SHAs of the PR's commits, oldest first.
	PRCommits(ctx context.Context, r Repo, number int) ([]string, error)
}

func (f *forgejo) PRCommits(ctx context.Context, r Repo, number int) ([]string, error) {
	var out []string
	for page := 1; ; page++ {
		var commits []struct {
			SHA string `json:"sha"`
		}
		if err := f.do(ctx, http.MethodGet,
			fmt.Sprintf("%s/pulls/%d/commits?limit=50&page=%d&verification=false&files=false", f.repoPath(r), number, page), nil, &commits); err != nil {
			return nil, err
		}
		for _, c := range commits {
			out = append(out, c.SHA)
		}
		if len(commits) < 50 {
			return out, nil
		}
	}
}
