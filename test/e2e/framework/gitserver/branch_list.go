// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"fmt"
	"net/http"
)

// BranchLister is a Server that can list a repo's branches, for the scale
// suite's orphan-branch invariant. Forgejo and Gitea implement it.
type BranchLister interface {
	// Branches returns the name of every branch of r.
	Branches(ctx context.Context, r Repo) ([]string, error)
}

var _ BranchLister = (*forgejo)(nil)

// Branches pages through /branches, 50 at a time.
func (f *forgejo) Branches(ctx context.Context, r Repo) ([]string, error) {
	var out []string
	for page := 1; ; page++ {
		var bs []struct {
			Name string `json:"name"`
		}
		if err := f.do(ctx, http.MethodGet, fmt.Sprintf("%s/branches?limit=50&page=%d", f.repoPath(r), page), nil, &bs); err != nil {
			return nil, err
		}
		for _, b := range bs {
			out = append(out, b.Name)
		}
		if len(bs) < 50 {
			return out, nil
		}
	}
}
