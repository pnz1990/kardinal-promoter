// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"fmt"
	"net/http"
)

// Reviewer is a Server that can review a PR as the test runner's user. The
// test runner is not the PR's author (kardinal's bot user opens promotion
// PRs), so its approval counts as a review of someone else's change.
type Reviewer interface {
	// ApprovePR submits an approving review with body.
	ApprovePR(ctx context.Context, r Repo, number int, body string) error
}

// ApprovePR submits an APPROVED review, the state kardinal's Forgejo and Gitea
// providers count in GetPRReviewStatus.
func (f *forgejo) ApprovePR(ctx context.Context, r Repo, number int, body string) error {
	return f.do(ctx, http.MethodPost, fmt.Sprintf("%s/pulls/%d/reviews", f.repoPath(r), number),
		map[string]string{"event": "APPROVED", "body": body}, nil)
}

// BranchProtector is a Server that can protect a branch. Forgejo and Gitea
// implement it.
type BranchProtector interface {
	// ProtectBranch makes merges into r.Branch need approvals approving
	// reviews.
	ProtectBranch(ctx context.Context, r Repo, approvals int) error
}

var _ BranchProtector = (*forgejo)(nil)

func (f *forgejo) ProtectBranch(ctx context.Context, r Repo, approvals int) error {
	return f.do(ctx, http.MethodPost, f.repoPath(r)+"/branch_protections",
		map[string]interface{}{"rule_name": r.Branch, "required_approvals": approvals, "enable_push": true}, nil)
}
