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
	"net/http"
	"strings"
)

// PRSupport implements PRController. Bitbucket Data Center has reviewers
// (users) and merges with a strategy; its pull requests have no labels, no
// assignees and no team reviewers (default reviewer groups are repository
// settings).
func (b *BitbucketDCProvider) PRSupport() PRSupport {
	return PRSupport{
		Provider: "bitbucket-datacenter", Reviewers: true, AutoMerge: true,
		MergeMethods: []string{MergeMethodMerge, MergeMethodSquash, MergeMethodRebase}, CommitMessage: true,
	}
}

// RequestReviewers adds users (user slugs) as reviewers. Adding a reviewer
// the PR has already is not an error.
func (b *BitbucketDCProvider) RequestReviewers(ctx context.Context, repo string, prNumber int, users, teams []string) error {
	if len(teams) > 0 {
		return fmt.Errorf("request team reviewers on Bitbucket Data Center PR %s#%d: %w", repo, prNumber, ErrPRControlUnsupported)
	}
	path, err := b.prPath(repo)
	if err != nil {
		return err
	}
	for _, u := range users {
		if err := b.do(ctx, http.MethodPost, fmt.Sprintf("%s/%d/participants", path, prNumber),
			map[string]interface{}{"user": map[string]string{"name": u}, "role": "REVIEWER"}, nil); err != nil {
			return fmt.Errorf("add reviewer %s to Bitbucket Data Center PR %s#%d: %w", u, repo, prNumber, err)
		}
	}
	return nil
}

// AddAssignees is not supported: Bitbucket Data Center pull requests have
// no assignees.
func (b *BitbucketDCProvider) AddAssignees(_ context.Context, repo string, prNumber int, _ []string) error {
	return fmt.Errorf("add assignees to Bitbucket Data Center PR %s#%d: %w", repo, prNumber, ErrPRControlUnsupported)
}

// bbdcMergeStrategies maps pr.merge.method to Bitbucket merge strategy IDs.
// The strategy must be enabled in the repository's merge strategies.
var bbdcMergeStrategies = map[string]string{
	MergeMethodMerge: "no-ff", MergeMethodSquash: "squash", MergeMethodRebase: "rebase-no-ff",
}

// EnableAutoMerge turns on Bitbucket's auto-merge (Bitbucket Data Center
// 8.15 and later), which merges the PR with the strategy and message once
// its merge checks (approvals, builds, tasks) pass. The merge endpoint's
// GET says first whether a check still vetoes the merge: when none does,
// nothing is pending, and the PR is merged at once only with
// opts.AllowImmediate (otherwise ErrNothingPending). A server without
// auto-merge answers 404; the PR then waits for a merge by hand.
func (b *BitbucketDCProvider) EnableAutoMerge(ctx context.Context, repo string, prNumber int, opts MergeOptions) error {
	strategy, ok := bbdcMergeStrategies[opts.Method]
	if !ok {
		return fmt.Errorf("merge Bitbucket Data Center PR %s#%d: merge method %q: %w", repo, prNumber, opts.Method, ErrPRControlUnsupported)
	}
	path, err := b.prPath(repo)
	if err != nil {
		return err
	}
	var check struct {
		CanMerge   bool `json:"canMerge"`
		Conflicted bool `json:"conflicted"`
	}
	if err := b.do(ctx, http.MethodGet, fmt.Sprintf("%s/%d/merge", path, prNumber), nil, &check); err != nil {
		return fmt.Errorf("merge Bitbucket Data Center PR %s#%d: read merge checks: %w", repo, prNumber, err)
	}
	if check.Conflicted {
		return fmt.Errorf("merge Bitbucket Data Center PR %s#%d: the PR has conflicts", repo, prNumber)
	}
	if check.CanMerge {
		if !opts.AllowImmediate {
			return fmt.Errorf("merge Bitbucket Data Center PR %s#%d: %w", repo, prNumber, ErrNothingPending)
		}
		body := map[string]interface{}{"strategyId": strategy}
		if opts.CommitTitle != "" {
			body["message"] = commitMessage(opts)
		}
		err := b.withVersion(ctx, repo, prNumber, func(version int) error {
			return b.do(ctx, http.MethodPost, fmt.Sprintf("%s/%d/merge?version=%d", path, prNumber, version), body, nil)
		})
		if err != nil {
			return fmt.Errorf("merge Bitbucket Data Center PR %s#%d: %w", repo, prNumber, err)
		}
		return nil
	}
	autoBody := map[string]interface{}{"strategyId": strategy}
	if opts.CommitTitle != "" {
		autoBody["commitMessage"] = commitMessage(opts)
	}
	err = b.do(ctx, http.MethodPost, b.autoMergePath(path, prNumber), autoBody, nil)
	if _, old := statusIs(err, http.StatusNotFound); old {
		return fmt.Errorf("enable auto-merge on Bitbucket Data Center PR %s#%d: this server has no auto-merge (Bitbucket Data Center 8.15 or later)", repo, prNumber)
	}
	if err != nil {
		return fmt.Errorf("enable auto-merge on Bitbucket Data Center PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// DisableAutoMerge cancels Bitbucket's auto-merge of the PR. A PR without
// one, or a server without auto-merge (404), is not an error.
func (b *BitbucketDCProvider) DisableAutoMerge(ctx context.Context, repo string, prNumber int) error {
	path, err := b.prPath(repo)
	if err != nil {
		return err
	}
	err = b.do(ctx, http.MethodDelete, b.autoMergePath(path, prNumber), nil, nil)
	if _, none := statusIs(err, http.StatusNotFound); err != nil && !none {
		return fmt.Errorf("disable auto-merge on Bitbucket Data Center PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// autoMergePath is the auto-merge resource of the PR, on the latest REST API.
func (b *BitbucketDCProvider) autoMergePath(prPath string, prNumber int) string {
	return fmt.Sprintf("/rest/api/latest%s/%d/auto-merge", strings.TrimPrefix(prPath, "/rest/api/1.0"), prNumber)
}

var _ PRController = (*BitbucketDCProvider)(nil)
