// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scm

import (
	"context"
	"errors"
	"time"
)

// providerHTTPTimeout bounds every SCM API call, so a hung SCM endpoint cannot
// pin a reconcile worker (the reconcile context has no deadline).
const providerHTTPTimeout = 30 * time.Second

const (
	// maxListPages bounds how many pages a paginated list call reads.
	maxListPages = 20
	// githubPageSize is the per_page used for GitHub list calls (the maximum).
	githubPageSize = 100
	// forgejoPageSize is the limit used for Forgejo/Gitea list calls (the
	// default server maximum).
	forgejoPageSize = 50
)

// ErrNoWebhookSecret is returned by every provider's ParseWebhookEvent when
// the provider has no webhook secret. A signature is only proof with a secret
// (an HMAC with an empty key is one anyone can compute), so a provider without
// one refuses every event instead of skipping the check.
var ErrNoWebhookSecret = errors.New("webhook secret not configured; event refused")

// WebhookEvent carries parsed SCM webhook payload data.
//
// Every provider reports a merged pull request (GitLab merge request, Azure
// DevOps completed PR) the same way: EventType "pull_request", Action
// "closed" and Merged true, so the webhook handler needs a single check.
// Other events keep the provider's own event type and action.
type WebhookEvent struct {
	// EventType is the SCM's event type, from the event header or the
	// payload: "pull_request", "push", "issue_comment", GitLab's
	// "merge_request" or "note", and so on.
	EventType string

	// PRNumber is the pull request number, if applicable.
	PRNumber int

	// RepoFullName is the repository identifier in the format the provider
	// APIs use (see RepoFromURL): "owner/repo", the full GitLab project path,
	// or "org/project/repo" for Azure DevOps. For a pull request it is the
	// base (target) repository, not a fork.
	RepoFullName string

	// Merged indicates whether the PR was merged.
	Merged bool

	// MergeCommitSHA is the commit a merged PR produced on its base branch,
	// when the payload reports it (GitHub, GitLab, Forgejo and Gitea). Empty
	// otherwise; the PRStatus reconciler then asks the SCM API for it.
	MergeCommitSHA string

	// Action is the event action (e.g., "closed", "opened").
	Action string
}

// mergedPREvent returns the normalised event for a merged pull request.
func mergedPREvent(repo string, number int) WebhookEvent {
	return WebhookEvent{EventType: "pull_request", Action: "closed", Merged: true, RepoFullName: repo, PRNumber: number}
}

// SCMProvider abstracts pull request lifecycle operations for a given SCM platform.
// All implementations must be safe for concurrent use.
type SCMProvider interface {
	// OpenPR creates a pull request and returns the PR URL and PR number.
	OpenPR(ctx context.Context, repo, title, body, head, base string) (prURL string, prNumber int, err error)

	// ClosePR closes (without merging) the given pull request.
	ClosePR(ctx context.Context, repo string, prNumber int) error

	// CommentOnPR posts a comment on the given pull request.
	CommentOnPR(ctx context.Context, repo string, prNumber int, body string) error

	// GetPRStatus returns whether the PR has been merged and whether it is still open.
	GetPRStatus(ctx context.Context, repo string, prNumber int) (merged bool, open bool, err error)

	// GetPRReviewStatus returns the review approval state of the PR.
	// approved is true when at least one approving review exists and no
	// change-request review is outstanding.
	// approvalCount is the number of distinct approving reviews.
	GetPRReviewStatus(ctx context.Context, repo string, prNumber int) (approved bool, approvalCount int, err error)

	// ParseWebhookEvent parses a raw webhook payload and validates the HMAC signature.
	// A provider without a webhook secret returns ErrNoWebhookSecret for every payload.
	// The webhook handler calls ParseWebhookRequest instead, which also passes
	// the event header to the providers that need it.
	ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error)

	// AddLabelsToPR applies labels to a pull request.
	// Labels that do not exist in the repository are created with a default color.
	AddLabelsToPR(ctx context.Context, repo string, prNumber int, labels []string) error
}

// GitClient abstracts Git operations needed by the promotion steps engine.
// All implementations must be safe for sequential use within a single step sequence.
type GitClient interface {
	// Clone performs a shallow (depth=1) clone of branch into dir, using token
	// (when non-empty) for HTTP(S) authentication. Its error names the URL
	// ("git clone <url>: <reason>"), so callers do not add it again.
	Clone(ctx context.Context, url, branch, dir, token string) error

	// CloneAt clones the repository into dir and checks out commitSHA. Its
	// error names the URL or the commit, as Clone's does.
	CloneAt(ctx context.Context, url, commitSHA, dir, token string) error

	// CommitAll stages all changes in dir and creates a commit with the given
	// message. It returns ErrNothingToCommit when there is nothing to commit.
	CommitAll(ctx context.Context, dir, message, authorName, authorEmail string) error

	// Push pushes HEAD to branch on the remote using token for auth. With
	// force=false it returns ErrNonFastForward when the remote branch moved.
	Push(ctx context.Context, dir, remote, branch, token string, force bool) error
}
