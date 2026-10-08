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
	"net/http"
	"net/url"
	"strings"
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

// EnableAutoMerge asks Forgejo or Gitea to merge the PR once its commit
// checks succeed (merge_when_checks_succeed). Forgejo merges a scheduled PR
// only on a later commit status or review event, so a PR whose head has no
// commit status would wait forever: such a PR is merged at once instead,
// which applies branch protection too. When branch protection refuses that
// merge (approvals missing), the merge is scheduled. "Please try again
// later" (the server is still checking the new PR) is returned for the
// caller to retry. The head branch is kept: kardinal deletes it when the
// step ends.
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
	base := fmt.Sprintf("/api/v1/repos/%s/%s", owner, name)
	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", base, prNumber), nil, &pr); err != nil {
		return fmt.Errorf("enable auto-merge on PR %s#%d: %w", repo, prNumber, err)
	}
	var status struct {
		TotalCount int `json:"total_count"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("%s/commits/%s/status", base, url.PathEscape(pr.Head.SHA)), nil, &status); err != nil {
		return fmt.Errorf("enable auto-merge on PR %s#%d: read commit status: %w", repo, prNumber, err)
	}
	payload := map[string]interface{}{
		"Do":                        opts.Method,
		"delete_branch_after_merge": false,
	}
	if opts.CommitTitle != "" {
		payload["MergeTitleField"] = opts.CommitTitle
		payload["MergeMessageField"] = opts.CommitBody
	}
	mergePath := fmt.Sprintf("%s/pulls/%d/merge", base, prNumber)
	if status.TotalCount == 0 {
		err := f.do(ctx, http.MethodPost, mergePath, payload, nil)
		if err == nil {
			return nil
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusMethodNotAllowed ||
			strings.Contains(strings.ToLower(apiErr.Body), "try again later") {
			return fmt.Errorf("merge PR %s#%d, which has no commit checks: %w", repo, prNumber, err)
		}
		// Branch protection refused the merge: schedule it.
	}
	payload["merge_when_checks_succeed"] = true
	if err := f.do(ctx, http.MethodPost, mergePath, payload, nil); err != nil {
		return fmt.Errorf("enable auto-merge on PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}
