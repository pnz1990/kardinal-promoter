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
	"fmt"
	"slices"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ErrPRControlUnsupported is returned by a PRController method the provider
// does not implement.
var ErrPRControlUnsupported = errors.New("not supported by this SCM provider")

// PRSupport says which pr controls (v1alpha1.PRConfig) a provider applies.
// docs/scm-providers.md#pr-controls lists the same matrix.
type PRSupport struct {
	// Provider names the provider in error messages.
	Provider string

	Labels        bool
	Reviewers     bool
	TeamReviewers bool
	Assignees     bool
	AutoMerge     bool
	// MergeMethods are the pr.merge.method values the provider's auto-merge
	// takes.
	MergeMethods []string
	// CommitMessage is true when the provider's auto-merge takes a commit
	// message.
	CommitMessage bool
}

// PRController is implemented by the providers that apply pr controls after
// OpenPR. Every method adds to what the PR has, so calling it again on the
// same PR (a re-run of open-pr after a crash) is harmless.
type PRController interface {
	// PRSupport reports which controls the provider applies.
	PRSupport() PRSupport

	// RequestReviewers asks users and teams to review the PR.
	RequestReviewers(ctx context.Context, repo string, prNumber int, users, teams []string) error

	// AddAssignees assigns the PR to users.
	AddAssignees(ctx context.Context, repo string, prNumber int, users []string) error

	// EnableAutoMerge asks the SCM to merge the PR with opts once its
	// required checks and reviews pass. A PR that can be merged already may
	// be merged at once.
	EnableAutoMerge(ctx context.Context, repo string, prNumber int, opts MergeOptions) error
}

// SupportOf returns the pr controls p applies: none when p is not a
// PRController.
func SupportOf(p SCMProvider) PRSupport {
	if c, ok := p.(PRController); ok {
		return c.PRSupport()
	}
	return PRSupport{Provider: fmt.Sprintf("%T", p)}
}

// CheckPRSupport returns an error naming the first control cfg asks for that
// sup does not apply. The open-pr step fails with it before it opens the PR.
func CheckPRSupport(cfg *v1alpha1.PRConfig, sup PRSupport) error {
	if cfg == nil {
		return nil
	}
	unsupported := func(field string) error {
		return fmt.Errorf("pr.%s is not supported by the %s SCM provider (docs/scm-providers.md#pr-controls)", field, sup.Provider)
	}
	switch {
	case len(cfg.Labels) > 0 && !sup.Labels:
		return unsupported("labels")
	case len(cfg.Reviewers) > 0 && !sup.Reviewers:
		return unsupported("reviewers")
	case len(cfg.TeamReviewers) > 0 && !sup.TeamReviewers:
		return unsupported("teamReviewers")
	case len(cfg.Assignees) > 0 && !sup.Assignees:
		return unsupported("assignees")
	}
	m := cfg.Merge
	if m == nil || !m.Auto {
		return nil
	}
	if !sup.AutoMerge {
		return unsupported("merge.auto")
	}
	method := m.Method
	if method == "" {
		method = MergeMethodMerge
	}
	if !slices.Contains(sup.MergeMethods, method) {
		return fmt.Errorf("pr.merge.method %s is not supported by the %s SCM provider (docs/scm-providers.md#pr-controls)", method, sup.Provider)
	}
	if m.CommitMessageTemplate != "" && !sup.CommitMessage {
		return unsupported("merge.commitMessageTemplate")
	}
	return nil
}

// PRSupport implements PRController.
func (d *DynamicProvider) PRSupport() PRSupport { return SupportOf(d.current()) }

// RequestReviewers implements PRController.
func (d *DynamicProvider) RequestReviewers(ctx context.Context, repo string, prNumber int, users, teams []string) error {
	c, ok := d.current().(PRController)
	if !ok {
		return ErrPRControlUnsupported
	}
	return c.RequestReviewers(ctx, repo, prNumber, users, teams)
}

// AddAssignees implements PRController.
func (d *DynamicProvider) AddAssignees(ctx context.Context, repo string, prNumber int, users []string) error {
	c, ok := d.current().(PRController)
	if !ok {
		return ErrPRControlUnsupported
	}
	return c.AddAssignees(ctx, repo, prNumber, users)
}

// EnableAutoMerge implements PRController.
func (d *DynamicProvider) EnableAutoMerge(ctx context.Context, repo string, prNumber int, opts MergeOptions) error {
	c, ok := d.current().(PRController)
	if !ok {
		return ErrPRControlUnsupported
	}
	return c.EnableAutoMerge(ctx, repo, prNumber, opts)
}

// commitMessage joins a merge commit title and body into one message, for
// the providers that take one string.
func commitMessage(opts MergeOptions) string {
	if opts.CommitBody == "" {
		return opts.CommitTitle
	}
	return opts.CommitTitle + "\n\n" + opts.CommitBody
}

var (
	_ PRController = (*GitHubProvider)(nil)
	_ PRController = (*GitLabProvider)(nil)
	_ PRController = (*ForgejoProvider)(nil)
	_ PRController = (*BitbucketProvider)(nil)
	_ PRController = (*AzureDevOpsProvider)(nil)
	_ PRController = (*DynamicProvider)(nil)
)
