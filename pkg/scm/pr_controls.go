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
	"net"
	"net/http"
	"slices"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ErrPRControlUnsupported is returned by a PRController method the provider
// does not implement.
var ErrPRControlUnsupported = errors.New("not supported by this SCM provider")

// ErrNothingPending is returned by EnableAutoMerge when the PR has no check,
// review or pipeline to wait for, so the SCM would merge it at once: kardinal
// does not merge a PR itself unless pr.merge.allowImmediate is set.
var ErrNothingPending = errors.New("nothing is pending on the PR (no required check, review or pipeline), " +
	"so the SCM would merge it at once; kardinal leaves it for a merge by hand unless pr.merge.allowImmediate is true")

// ErrMergeabilityUnknown is returned by EnableAutoMerge while the SCM is
// still computing whether the PR can be merged; try again later.
var ErrMergeabilityUnknown = errors.New("the SCM is still checking whether the PR can be merged")

// IsRetryableMergeError reports whether EnableAutoMerge or DisableAutoMerge
// may succeed when tried again: the SCM still computing mergeability, a
// transient API error (5xx, 429, a rate limit), a network error, or a refusal
// of a PR whose mergeability is still being computed (405, 406, 409, 422).
// A GraphQL error, an unsupported method, ErrNothingPending and a permanent
// API error are not retried.
func IsRetryableMergeError(err error) bool {
	if errors.Is(err, ErrMergeabilityUnknown) {
		return true
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		var netErr net.Error
		return errors.As(err, &netErr)
	}
	switch apiErr.StatusCode {
	case http.StatusMethodNotAllowed, http.StatusNotAcceptable, http.StatusConflict, http.StatusUnprocessableEntity:
		return true
	}
	return apiErr.Transient
}

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
	// required checks and reviews pass. A PR with nothing pending is merged
	// at once only with opts.AllowImmediate; otherwise EnableAutoMerge
	// returns ErrNothingPending and changes nothing.
	EnableAutoMerge(ctx context.Context, repo string, prNumber int, opts MergeOptions) error

	// DisableAutoMerge turns the SCM's auto-merge of the PR off again
	// (the pipeline was paused or a gate closed). A PR without auto-merge is
	// not an error.
	DisableAutoMerge(ctx context.Context, repo string, prNumber int) error
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

// DisableAutoMerge implements PRController.
func (d *DynamicProvider) DisableAutoMerge(ctx context.Context, repo string, prNumber int) error {
	c, ok := d.current().(PRController)
	if !ok {
		return ErrPRControlUnsupported
	}
	return c.DisableAutoMerge(ctx, repo, prNumber)
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
