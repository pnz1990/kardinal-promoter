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

// PRSupport implements PRController. GitLab has no team reviewers, and a
// rebase merge is the project's merge method setting, not a merge option.
func (g *GitLabProvider) PRSupport() PRSupport {
	return PRSupport{
		Provider: "gitlab", Labels: true, Reviewers: true, Assignees: true,
		AutoMerge: true, MergeMethods: []string{MergeMethodMerge, MergeMethodSquash}, CommitMessage: true,
	}
}

// RequestReviewers sets the merge request's reviewers to users, with the
// reviewers it has already. GitLab takes user IDs, so each username is
// looked up.
func (g *GitLabProvider) RequestReviewers(ctx context.Context, repo string, prNumber int, users, teams []string) error {
	if len(teams) > 0 {
		return fmt.Errorf("request team reviewers on MR %s!%d: %w", repo, prNumber, ErrPRControlUnsupported)
	}
	return g.addMRUsers(ctx, repo, prNumber, "reviewer", users)
}

// AddAssignees sets the merge request's assignees to users, with the
// assignees it has already.
func (g *GitLabProvider) AddAssignees(ctx context.Context, repo string, prNumber int, users []string) error {
	return g.addMRUsers(ctx, repo, prNumber, "assignee", users)
}

// addMRUsers adds users to the reviewers or assignees (role) of the MR.
// GitLab's reviewer_ids and assignee_ids replace the whole set, so the MR's
// current set is read first and kept.
func (g *GitLabProvider) addMRUsers(ctx context.Context, repo string, prNumber int, role string, users []string) error {
	if len(users) == 0 {
		return nil
	}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", encodeProjectID(repo), prNumber)
	type user struct {
		ID int `json:"id"`
	}
	var mr struct {
		Reviewers []user `json:"reviewers"`
		Assignees []user `json:"assignees"`
	}
	if err := g.do(ctx, http.MethodGet, path, nil, &mr); err != nil {
		return fmt.Errorf("add %ss to MR %s!%d: %w", role, repo, prNumber, err)
	}
	current := mr.Reviewers
	if role == "assignee" {
		current = mr.Assignees
	}
	ids := make([]int, 0, len(current)+len(users))
	seen := map[int]bool{}
	for _, u := range current {
		seen[u.ID] = true
		ids = append(ids, u.ID)
	}
	for _, name := range users {
		id, err := g.userID(ctx, name)
		if err != nil {
			return fmt.Errorf("add %ss to MR %s!%d: %w", role, repo, prNumber, err)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if err := g.do(ctx, http.MethodPut, path, map[string][]int{role + "_ids": ids}, nil); err != nil {
		return fmt.Errorf("add %ss to MR %s!%d: %w", role, repo, prNumber, err)
	}
	return nil
}

// userID returns the ID of the GitLab user named username.
func (g *GitLabProvider) userID(ctx context.Context, username string) (int, error) {
	var users []struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
	}
	if err := g.do(ctx, http.MethodGet, "/api/v4/users?username="+url.QueryEscape(username), nil, &users); err != nil {
		return 0, fmt.Errorf("look up GitLab user %s: %w", username, err)
	}
	for _, u := range users {
		if u.ID != 0 {
			return u.ID, nil
		}
	}
	return 0, fmt.Errorf("no GitLab user %s", username)
}

// gitlabUnknown are the detailed_merge_status values of an MR whose
// mergeability GitLab is still computing.
var gitlabUnknown = map[string]bool{"unchecked": true, "checking": true, "preparing": true, "approvals_syncing": true}

// EnableAutoMerge sets the merge request to merge once its checks pass
// (GitLab auto-merge: the pipeline, and on GitLab 17 and later approvals and
// the other merge checks too). An MR whose detailed_merge_status is
// mergeable has nothing to wait for, and GitLab would merge it at once: that
// needs opts.AllowImmediate, otherwise ErrNothingPending. While GitLab is
// still checking the MR, ErrMergeabilityUnknown is returned. auto_merge is
// the parameter since GitLab 17.11 and merge_when_pipeline_succeeds the one
// before; GitLab ignores the one it does not know.
func (g *GitLabProvider) EnableAutoMerge(ctx context.Context, repo string, prNumber int, opts MergeOptions) error {
	if opts.Method != MergeMethodMerge && opts.Method != MergeMethodSquash {
		return fmt.Errorf("enable auto-merge on MR %s!%d: merge method %q: %w", repo, prNumber, opts.Method, ErrPRControlUnsupported)
	}
	mrPath := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", encodeProjectID(repo), prNumber)
	var mr struct {
		DetailedMergeStatus string `json:"detailed_merge_status"`
	}
	if err := g.do(ctx, http.MethodGet, mrPath, nil, &mr); err != nil {
		return fmt.Errorf("enable auto-merge on MR %s!%d: %w", repo, prNumber, err)
	}
	immediate := mr.DetailedMergeStatus == "mergeable"
	switch {
	case gitlabUnknown[mr.DetailedMergeStatus]:
		return fmt.Errorf("enable auto-merge on MR %s!%d: %w", repo, prNumber, ErrMergeabilityUnknown)
	case immediate && !opts.AllowImmediate:
		return fmt.Errorf("enable auto-merge on MR %s!%d: %w", repo, prNumber, ErrNothingPending)
	}
	payload := map[string]interface{}{"squash": opts.Method == MergeMethodSquash}
	if !immediate {
		payload["auto_merge"] = true
		payload["merge_when_pipeline_succeeds"] = true
	}
	if opts.CommitTitle != "" {
		// A squash on a project whose merge method makes merge commits
		// leaves both commits, so both get the message.
		payload["merge_commit_message"] = commitMessage(opts)
		if opts.Method == MergeMethodSquash {
			payload["squash_commit_message"] = commitMessage(opts)
		}
	}
	if err := g.do(ctx, http.MethodPut, mrPath+"/merge", payload, nil); err != nil {
		return fmt.Errorf("enable auto-merge on MR %s!%d: %w", repo, prNumber, err)
	}
	return nil
}

// DisableAutoMerge cancels the MR's auto-merge
// (cancel_merge_when_pipeline_succeeds). GitLab answers 406 when the MR
// has none, which is not an error.
func (g *GitLabProvider) DisableAutoMerge(ctx context.Context, repo string, prNumber int) error {
	err := g.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/cancel_merge_when_pipeline_succeeds", encodeProjectID(repo), prNumber), nil, nil)
	if _, none := statusIs(err, http.StatusNotAcceptable); err != nil && !none {
		return fmt.Errorf("disable auto-merge on MR %s!%d: %w", repo, prNumber, err)
	}
	return nil
}
