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
