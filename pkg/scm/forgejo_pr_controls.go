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

// EnableAutoMerge schedules the PR to merge once its commit checks succeed
// (merge_when_checks_succeed), and Forgejo and Gitea also merge a scheduled
// PR on a later approval. Something must be pending for that: a commit
// status that is not success yet, or base branch protection that requires
// approvals or status checks. Otherwise Gitea would merge at once and Forgejo
// never, so the PR is merged directly with opts.AllowImmediate, and left for
// a merge by hand otherwise (ErrNothingPending). The head branch is kept:
// kardinal deletes it when the step ends.
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
	pending, err := f.mergePending(ctx, base, prNumber)
	if err != nil {
		return fmt.Errorf("enable auto-merge on PR %s#%d: %w", repo, prNumber, err)
	}
	if !pending && !opts.AllowImmediate {
		return fmt.Errorf("enable auto-merge on PR %s#%d: %w", repo, prNumber, ErrNothingPending)
	}
	payload := map[string]interface{}{
		"Do":                        opts.Method,
		"delete_branch_after_merge": false,
	}
	if pending {
		payload["merge_when_checks_succeed"] = true
	}
	if opts.CommitTitle != "" {
		payload["MergeTitleField"] = opts.CommitTitle
		payload["MergeMessageField"] = opts.CommitBody
	}
	err = f.do(ctx, http.MethodPost, fmt.Sprintf("%s/pulls/%d/merge", base, prNumber), payload, nil)
	if apiErr, conflict := statusIs(err, http.StatusConflict); conflict && strings.Contains(apiErr.Body, "already scheduled") {
		// Turned on already (a retried call): nothing to do.
		return nil
	}
	if err != nil {
		return fmt.Errorf("enable auto-merge on PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// mergePending reports whether the PR has something a scheduled merge waits
// for: a head commit status that is not success, or base branch protection
// that requires approvals or status checks.
func (f *ForgejoProvider) mergePending(ctx context.Context, base string, prNumber int) (bool, error) {
	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", base, prNumber), nil, &pr); err != nil {
		return false, err
	}
	var status struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("%s/commits/%s/status", base, url.PathEscape(pr.Head.SHA)), nil, &status); err != nil {
		return false, fmt.Errorf("read commit status: %w", err)
	}
	if status.TotalCount > 0 && status.State != "success" {
		return true, nil
	}
	var branch struct {
		Protected         bool `json:"protected"`
		RequiredApprovals int  `json:"required_approvals"`
		EnableStatusCheck bool `json:"enable_status_check"`
	}
	if err := f.do(ctx, http.MethodGet, fmt.Sprintf("%s/branches/%s", base, url.PathEscape(pr.Base.Ref)), nil, &branch); err != nil {
		return false, fmt.Errorf("read base branch protection: %w", err)
	}
	return branch.Protected && (branch.RequiredApprovals > 0 || branch.EnableStatusCheck), nil
}

// DisableAutoMerge cancels the PR's scheduled merge. A PR without one (404)
// is not an error.
func (f *ForgejoProvider) DisableAutoMerge(ctx context.Context, repo string, prNumber int) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	err = f.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", owner, name, prNumber), nil, nil)
	if _, none := statusIs(err, http.StatusNotFound); err != nil && !none {
		return fmt.Errorf("disable auto-merge on PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}
