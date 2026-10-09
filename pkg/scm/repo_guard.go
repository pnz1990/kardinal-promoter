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
	"fmt"
)

// Guard returns p wrapped so that every call for a repository the allowlist
// does not allow on host (WebHost) fails with an error wrapping
// ErrRepositoryNotAllowed, before any request is sent: opening, closing,
// commenting on, labelling and polling PRs, reading reviews and merge
// commits, and deleting branches. Whatever code path makes the call (a
// superseded step deleting its branch, a PRStatus poll, a webhook
// confirmation), the shared token cannot reach another repository (#1332).
// Webhook parsing, which names no repository, passes through. A nil
// allowlist returns p.
func (a *RepositoryAllowlist) Guard(p SCMProvider, host string) SCMProvider {
	if a == nil {
		return p
	}
	return &guardedProvider{inner: p, allow: a, host: host}
}

// guardedProvider is the SCMProvider Guard returns. It implements the
// optional BranchDeleter and MergeCommitGetter too; when the inner provider
// does not, they do nothing, as callers do without them.
type guardedProvider struct {
	inner SCMProvider
	allow *RepositoryAllowlist
	host  string
}

func (g *guardedProvider) check(op, repo string) error {
	if g.allow.AllowsRepo(g.host, repo) {
		return nil
	}
	return fmt.Errorf("%s %s/%s refused: %w: it is not in the controller's allowed repositories (%v), "+
		"so the controller's SCM token may not act on it", op, g.host, repo, ErrRepositoryNotAllowed, g.allow.Patterns())
}

// OpenPR implements SCMProvider.
func (g *guardedProvider) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	if err := g.check("open PR in", repo); err != nil {
		return "", 0, err
	}
	return g.inner.OpenPR(ctx, repo, title, body, head, base)
}

// ClosePR implements SCMProvider.
func (g *guardedProvider) ClosePR(ctx context.Context, repo string, prNumber int) error {
	if err := g.check("close PR in", repo); err != nil {
		return err
	}
	return g.inner.ClosePR(ctx, repo, prNumber)
}

// CommentOnPR implements SCMProvider.
func (g *guardedProvider) CommentOnPR(ctx context.Context, repo string, prNumber int, body string) error {
	if err := g.check("comment on PR in", repo); err != nil {
		return err
	}
	return g.inner.CommentOnPR(ctx, repo, prNumber, body)
}

// GetPRStatus implements SCMProvider.
func (g *guardedProvider) GetPRStatus(ctx context.Context, repo string, prNumber int) (bool, bool, error) {
	if err := g.check("get PR status in", repo); err != nil {
		return false, false, err
	}
	return g.inner.GetPRStatus(ctx, repo, prNumber)
}

// GetPRReviewStatus implements SCMProvider.
func (g *guardedProvider) GetPRReviewStatus(ctx context.Context, repo string, prNumber int) (bool, int, error) {
	if err := g.check("get PR reviews in", repo); err != nil {
		return false, 0, err
	}
	return g.inner.GetPRReviewStatus(ctx, repo, prNumber)
}

// AddLabelsToPR implements SCMProvider.
func (g *guardedProvider) AddLabelsToPR(ctx context.Context, repo string, prNumber int, labels []string) error {
	if err := g.check("label PR in", repo); err != nil {
		return err
	}
	return g.inner.AddLabelsToPR(ctx, repo, prNumber, labels)
}

// ParseWebhookEvent implements SCMProvider; it names no repository.
func (g *guardedProvider) ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error) {
	return g.inner.ParseWebhookEvent(payload, signature)
}

// parseWebhookEvent passes the event type on, as DynamicProvider does.
func (g *guardedProvider) parseWebhookEvent(payload []byte, signature, eventType string) (WebhookEvent, error) {
	if tp, ok := g.inner.(eventTypeParser); ok {
		return tp.parseWebhookEvent(payload, signature, eventType)
	}
	return g.inner.ParseWebhookEvent(payload, signature)
}

// DeleteBranch implements BranchDeleter.
func (g *guardedProvider) DeleteBranch(ctx context.Context, repo, branch string) error {
	if err := g.check("delete branch "+branch+" in", repo); err != nil {
		return err
	}
	bd, ok := g.inner.(BranchDeleter)
	if !ok {
		return nil
	}
	return bd.DeleteBranch(ctx, repo, branch)
}

// GetPRMergeCommit implements MergeCommitGetter.
func (g *guardedProvider) GetPRMergeCommit(ctx context.Context, repo string, prNumber int) (string, error) {
	if err := g.check("get PR merge commit in", repo); err != nil {
		return "", err
	}
	mg, ok := g.inner.(MergeCommitGetter)
	if !ok {
		return "", nil
	}
	return mg.GetPRMergeCommit(ctx, repo, prNumber)
}

var (
	_ SCMProvider       = (*guardedProvider)(nil)
	_ BranchDeleter     = (*guardedProvider)(nil)
	_ MergeCommitGetter = (*guardedProvider)(nil)
	_ eventTypeParser   = (*guardedProvider)(nil)
)
