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

import "context"

// WebhookEvent carries parsed SCM webhook payload data.
type WebhookEvent struct {
	// EventType is the SCM event type (e.g., "pull_request").
	EventType string

	// PRNumber is the pull request number, if applicable.
	PRNumber int

	// RepoFullName is the repository full name (owner/repo).
	RepoFullName string

	// Merged indicates whether the PR was merged.
	Merged bool

	// Action is the event action (e.g., "closed", "opened").
	Action string
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

	// ParseWebhookEvent parses a raw webhook payload and validates the HMAC signature.
	ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error)

	// AddLabelsToPR applies labels to a pull request.
	// Labels that do not exist in the repository are created with a default color.
	AddLabelsToPR(ctx context.Context, repo string, prNumber int, labels []string) error
}

// GitClient abstracts Git operations needed by the promotion steps engine.
// All implementations must be safe for sequential use within a single step sequence.
type GitClient interface {
	// Clone performs a shallow (depth=1) clone of the repository into dir.
	Clone(ctx context.Context, url, branch, dir string) error

	// Checkout creates or switches to the given branch in dir.
	Checkout(ctx context.Context, dir, branch string) error

	// CommitAll stages all changes in dir and creates a commit with the given message.
	CommitAll(ctx context.Context, dir, message, authorName, authorEmail string) error

	// Push pushes the given branch to the remote using the provided token for auth.
	Push(ctx context.Context, dir, remote, branch, token string) error
}
