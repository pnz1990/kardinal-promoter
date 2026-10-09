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

// EnableAutoMerge merges the PR at once when its merge checks pass (the
// merge endpoint applies them: approvals, builds, tasks). When a check still
// fails (409), it turns on Bitbucket's auto-merge, which merges the PR once
// the checks pass (Bitbucket Data Center 8.15 and later; an older server
// answers 404 and the PR waits for a merge by hand).
func (b *BitbucketDCProvider) EnableAutoMerge(ctx context.Context, repo string, prNumber int, opts MergeOptions) error {
	strategy, ok := bbdcMergeStrategies[opts.Method]
	if !ok {
		return fmt.Errorf("merge Bitbucket Data Center PR %s#%d: merge method %q: %w", repo, prNumber, opts.Method, ErrPRControlUnsupported)
	}
	path, err := b.prPath(repo)
	if err != nil {
		return err
	}
	body := map[string]interface{}{"strategyId": strategy}
	if opts.CommitTitle != "" {
		body["message"] = commitMessage(opts)
	}
	err = b.withVersion(ctx, repo, prNumber, func(version int) error {
		return b.do(ctx, http.MethodPost, fmt.Sprintf("%s/%d/merge?version=%d", path, prNumber, version), body, nil)
	})
	if err == nil {
		return nil
	}
	if _, vetoed := statusIs(err, http.StatusConflict); !vetoed {
		return fmt.Errorf("merge Bitbucket Data Center PR %s#%d: %w", repo, prNumber, err)
	}
	autoBody := map[string]interface{}{"strategyId": strategy}
	if opts.CommitTitle != "" {
		autoBody["commitMessage"] = commitMessage(opts)
	}
	autoPath := fmt.Sprintf("/rest/api/latest%s/%d/auto-merge", path[len("/rest/api/1.0"):], prNumber)
	if aerr := b.do(ctx, http.MethodPost, autoPath, autoBody, nil); aerr != nil {
		if _, old := statusIs(aerr, http.StatusNotFound); old {
			return fmt.Errorf("merge Bitbucket Data Center PR %s#%d: its merge checks do not pass yet (%v), and this server has no auto-merge (Bitbucket Data Center 8.15 or later)", repo, prNumber, err)
		}
		return fmt.Errorf("enable auto-merge on Bitbucket Data Center PR %s#%d (merge checks: %v): %w", repo, prNumber, err, aerr)
	}
	return nil
}

var _ PRController = (*BitbucketDCProvider)(nil)
