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

// PRSupport implements PRController. Bitbucket Cloud has reviewers only: it
// has no PR labels or assignees, no team reviewers, and its REST API has no
// auto-merge.
func (b *BitbucketProvider) PRSupport() PRSupport {
	return PRSupport{Provider: "bitbucket", Reviewers: true}
}

// bitbucketUser is a reviewer of a Bitbucket pull request: a {UUID} or an
// Atlassian account ID.
type bitbucketUser struct {
	UUID      string `json:"uuid,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

// bitbucketUserRef reads a reviewer entry: "{...}" is a UUID, anything else
// an account ID.
func bitbucketUserRef(s string) bitbucketUser {
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		return bitbucketUser{UUID: s}
	}
	return bitbucketUser{AccountID: s}
}

// RequestReviewers adds users (UUIDs or account IDs) to the PR's reviewers.
// Bitbucket replaces the reviewer list on an update, and the update needs
// the title, so the PR is read first and its reviewers kept.
func (b *BitbucketProvider) RequestReviewers(ctx context.Context, repo string, prNumber int, users, teams []string) error {
	if len(teams) > 0 {
		return fmt.Errorf("request team reviewers on Bitbucket PR %s#%d: %w", repo, prNumber, ErrPRControlUnsupported)
	}
	if len(users) == 0 {
		return nil
	}
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests/%d", workspace, repoSlug, prNumber)
	var pr struct {
		Title     string          `json:"title"`
		Reviewers []bitbucketUser `json:"reviewers"`
	}
	if err := b.do(ctx, http.MethodGet, path, nil, &pr); err != nil {
		return fmt.Errorf("request reviewers on Bitbucket PR %s#%d: %w", repo, prNumber, err)
	}
	reviewers := make([]bitbucketUser, 0, len(pr.Reviewers)+len(users))
	seen := map[string]bool{}
	for _, r := range pr.Reviewers {
		seen[r.UUID], seen[r.AccountID] = true, true
		reviewers = append(reviewers, bitbucketUser{UUID: r.UUID})
	}
	for _, u := range users {
		if !seen[u] {
			seen[u] = true
			reviewers = append(reviewers, bitbucketUserRef(u))
		}
	}
	payload := map[string]interface{}{"title": pr.Title, "reviewers": reviewers}
	if err := b.do(ctx, http.MethodPut, path, payload, nil); err != nil {
		return fmt.Errorf("request reviewers on Bitbucket PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// AddAssignees is not supported: Bitbucket pull requests have no assignees.
func (b *BitbucketProvider) AddAssignees(_ context.Context, repo string, prNumber int, _ []string) error {
	return fmt.Errorf("add assignees to Bitbucket PR %s#%d: %w", repo, prNumber, ErrPRControlUnsupported)
}

// EnableAutoMerge is not supported: the Bitbucket Cloud REST API has no
// auto-merge.
func (b *BitbucketProvider) EnableAutoMerge(_ context.Context, repo string, prNumber int, _ MergeOptions) error {
	return fmt.Errorf("enable auto-merge on Bitbucket PR %s#%d: %w", repo, prNumber, ErrPRControlUnsupported)
}

// DisableAutoMerge is not supported: the Bitbucket Cloud REST API has no
// auto-merge.
func (b *BitbucketProvider) DisableAutoMerge(_ context.Context, repo string, prNumber int) error {
	return fmt.Errorf("disable auto-merge on Bitbucket PR %s#%d: %w", repo, prNumber, ErrPRControlUnsupported)
}
