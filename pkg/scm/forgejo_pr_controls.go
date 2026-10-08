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

// PRSupport implements PRController: Forgejo and Gitea apply every control.
// Team reviewers need a repository owned by an organisation.
func (f *ForgejoProvider) PRSupport() PRSupport {
	return PRSupport{
		Provider: "forgejo/gitea", Labels: true, Reviewers: true, TeamReviewers: true, Assignees: true,
		AutoMerge: true, MergeMethods: []string{MergeMethodMerge, MergeMethodSquash, MergeMethodRebase}, CommitMessage: true,
	}
}

// RequestReviewers requests reviews from users and organisation teams.
func (f *ForgejoProvider) RequestReviewers(ctx context.Context, repo string, prNumber int, users, teams []string) error {
	if len(users) == 0 && len(teams) == 0 {
		return nil
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	payload := map[string][]string{}
	if len(users) > 0 {
		payload["reviewers"] = users
	}
	if len(teams) > 0 {
		payload["team_reviewers"] = teams
	}
	if err := f.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/requested_reviewers", owner, name, prNumber), payload, nil); err != nil {
		return fmt.Errorf("request reviewers on PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// AddAssignees adds users to the PR's assignees. Forgejo and Gitea replace
// the assignee list on an issue edit, so the current assignees are read and
// kept.
func (f *ForgejoProvider) AddAssignees(ctx context.Context, repo string, prNumber int, users []string) error {
	if len(users) == 0 {
		return nil
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d", owner, name, prNumber)
	var issue struct {
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
	}
	if err := f.do(ctx, http.MethodGet, path, nil, &issue); err != nil {
		return fmt.Errorf("add assignees to PR %s#%d: %w", repo, prNumber, err)
	}
	logins := make([]string, 0, len(issue.Assignees)+len(users))
	seen := map[string]bool{}
	for _, a := range issue.Assignees {
		seen[a.Login] = true
		logins = append(logins, a.Login)
	}
	for _, u := range users {
		if !seen[u] {
			seen[u] = true
			logins = append(logins, u)
		}
	}
	if err := f.do(ctx, http.MethodPatch, path, map[string][]string{"assignees": logins}, nil); err != nil {
		return fmt.Errorf("add assignees to PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// EnableAutoMerge schedules the PR to merge once its commit checks succeed
// (merge_when_checks_succeed). A PR whose checks have passed already, or
// that has none, is merged at once. The head branch is kept: kardinal
// deletes it when the step ends.
func (f *ForgejoProvider) EnableAutoMerge(ctx context.Context, repo string, prNumber int, opts MergeOptions) error {
	switch opts.Method {
	case MergeMethodMerge, MergeMethodSquash, MergeMethodRebase:
	default:
		return fmt.Errorf("enable auto-merge on PR %s#%d: merge method %q: %w", repo, prNumber, opts.Method, ErrPRControlUnsupported)
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	payload := map[string]interface{}{
		"Do":                        opts.Method,
		"merge_when_checks_succeed": true,
		"delete_branch_after_merge": false,
	}
	if opts.CommitTitle != "" {
		payload["MergeTitleField"] = opts.CommitTitle
		payload["MergeMessageField"] = opts.CommitBody
	}
	if err := f.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", owner, name, prNumber), payload, nil); err != nil {
		return fmt.Errorf("enable auto-merge on PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}
