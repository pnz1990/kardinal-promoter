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
	"net/url"
)

// PRSupport implements PRController. Azure DevOps reviewers, users and
// groups alike, are identity IDs; its PRs have no assignees.
func (a *AzureDevOpsProvider) PRSupport() PRSupport {
	return PRSupport{
		Provider: "azuredevops", Labels: true, Reviewers: true, TeamReviewers: true,
		AutoMerge: true, MergeMethods: []string{MergeMethodMerge, MergeMethodSquash, MergeMethodRebase}, CommitMessage: true,
	}
}

// RequestReviewers adds users and teams, both identity IDs, as optional
// reviewers. Adding a reviewer the PR has already is not an error.
func (a *AzureDevOpsProvider) RequestReviewers(ctx context.Context, repo string, prNumber int, users, teams []string) error {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return err
	}
	for _, id := range append(append([]string{}, users...), teams...) {
		path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullRequests/%d/reviewers/%s?api-version=%s",
			org, project, repoName, prNumber, url.PathEscape(id), azureDevOpsAPIVersion)
		if err := a.do(ctx, http.MethodPut, path, map[string]interface{}{"vote": 0, "isRequired": false}, nil); err != nil {
			return fmt.Errorf("add reviewer %s to ADO PR %s#%d: %w", id, repo, prNumber, err)
		}
	}
	return nil
}

// AddAssignees is not supported: Azure DevOps pull requests have no
// assignees.
func (a *AzureDevOpsProvider) AddAssignees(_ context.Context, repo string, prNumber int, _ []string) error {
	return fmt.Errorf("add assignees to ADO PR %s#%d: %w", repo, prNumber, ErrPRControlUnsupported)
}

// adoMergeStrategies maps pr.merge.method to the GitPullRequestMergeStrategy.
var adoMergeStrategies = map[string]string{
	MergeMethodMerge: "noFastForward", MergeMethodSquash: "squash", MergeMethodRebase: "rebase",
}

// EnableAutoMerge sets auto-complete on the PR: Azure DevOps completes it
// once its branch policies pass. Auto-complete is set by the identity that
// created the PR, the token's. The source branch is kept: kardinal deletes
// it when the step ends.
func (a *AzureDevOpsProvider) EnableAutoMerge(ctx context.Context, repo string, prNumber int, opts MergeOptions) error {
	strategy, ok := adoMergeStrategies[opts.Method]
	if !ok {
		return fmt.Errorf("enable auto-complete on ADO PR %s#%d: merge method %q: %w", repo, prNumber, opts.Method, ErrPRControlUnsupported)
	}
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d?api-version=%s",
		org, project, repoName, prNumber, azureDevOpsAPIVersion)
	var pr struct {
		CreatedBy struct {
			ID string `json:"id"`
		} `json:"createdBy"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &pr); err != nil {
		return fmt.Errorf("enable auto-complete on ADO PR %s#%d: %w", repo, prNumber, err)
	}
	completion := map[string]interface{}{"mergeStrategy": strategy, "deleteSourceBranch": false}
	if opts.CommitTitle != "" {
		completion["mergeCommitMessage"] = commitMessage(opts)
	}
	payload := map[string]interface{}{
		"autoCompleteSetBy": map[string]string{"id": pr.CreatedBy.ID},
		"completionOptions": completion,
	}
	if err := a.do(ctx, http.MethodPatch, path, payload, nil); err != nil {
		return fmt.Errorf("enable auto-complete on ADO PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}
