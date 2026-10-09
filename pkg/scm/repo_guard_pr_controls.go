// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import "context"

// The Guard forwards the pr controls (PRController) of the provider it
// wraps, each checked against the allowlist like every other call, so an
// install with scm.allowedRepositories keeps reviewers, assignees and
// auto-merge (#1483 with #1453).

// PRSupport implements PRController: what the wrapped provider applies.
func (g *guardedProvider) PRSupport() PRSupport { return SupportOf(g.inner) }

// RequestReviewers implements PRController.
func (g *guardedProvider) RequestReviewers(ctx context.Context, repo string, prNumber int, users, teams []string) error {
	if err := g.check("request reviewers in", repo); err != nil {
		return err
	}
	c, ok := g.inner.(PRController)
	if !ok {
		return ErrPRControlUnsupported
	}
	return c.RequestReviewers(ctx, repo, prNumber, users, teams)
}

// AddAssignees implements PRController.
func (g *guardedProvider) AddAssignees(ctx context.Context, repo string, prNumber int, users []string) error {
	if err := g.check("assign PR in", repo); err != nil {
		return err
	}
	c, ok := g.inner.(PRController)
	if !ok {
		return ErrPRControlUnsupported
	}
	return c.AddAssignees(ctx, repo, prNumber, users)
}

// EnableAutoMerge implements PRController.
func (g *guardedProvider) EnableAutoMerge(ctx context.Context, repo string, prNumber int, opts MergeOptions) error {
	if err := g.check("enable auto-merge in", repo); err != nil {
		return err
	}
	c, ok := g.inner.(PRController)
	if !ok {
		return ErrPRControlUnsupported
	}
	return c.EnableAutoMerge(ctx, repo, prNumber, opts)
}

// DisableAutoMerge implements PRController.
func (g *guardedProvider) DisableAutoMerge(ctx context.Context, repo string, prNumber int) error {
	if err := g.check("disable auto-merge in", repo); err != nil {
		return err
	}
	c, ok := g.inner.(PRController)
	if !ok {
		return ErrPRControlUnsupported
	}
	return c.DisableAutoMerge(ctx, repo, prNumber)
}

var _ PRController = (*guardedProvider)(nil)
