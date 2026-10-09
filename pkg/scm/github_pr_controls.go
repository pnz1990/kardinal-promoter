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
	"strings"
)

// PRSupport implements PRController: GitHub applies every control.
func (g *GitHubProvider) PRSupport() PRSupport {
	return PRSupport{
		Provider: "github", Labels: true, Reviewers: true, TeamReviewers: true, Assignees: true,
		AutoMerge: true, MergeMethods: []string{MergeMethodMerge, MergeMethodSquash, MergeMethodRebase}, CommitMessage: true,
	}
}

// RequestReviewers requests reviews from users and from organisation teams
// (team slugs).
func (g *GitHubProvider) RequestReviewers(ctx context.Context, repo string, prNumber int, users, teams []string) error {
	if len(users) == 0 && len(teams) == 0 {
		return nil
	}
	payload := map[string][]string{}
	if len(users) > 0 {
		payload["reviewers"] = users
	}
	if len(teams) > 0 {
		payload["team_reviewers"] = teams
	}
	if err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", repo, prNumber), payload, nil); err != nil {
		return fmt.Errorf("request reviewers on PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// AddAssignees adds assignees to the PR.
func (g *GitHubProvider) AddAssignees(ctx context.Context, repo string, prNumber int, users []string) error {
	if len(users) == 0 {
		return nil
	}
	if err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/assignees", repo, prNumber),
		map[string][]string{"assignees": users}, nil); err != nil {
		return fmt.Errorf("add assignees to PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// githubMergeMethods maps pr.merge.method to the GraphQL
// PullRequestMergeMethod.
var githubMergeMethods = map[string]string{
	MergeMethodMerge: "MERGE", MergeMethodSquash: "SQUASH", MergeMethodRebase: "REBASE",
}

// enableAutoMergeMutation is GitHub's enablePullRequestAutoMerge mutation.
// Auto-merge has no REST endpoint.
const enableAutoMergeMutation = `mutation($id: ID!, $method: PullRequestMergeMethod!, $title: String, $body: String) {
  enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: $method, commitHeadline: $title, commitBody: $body}) {
    pullRequest { number }
  }
}`

// EnableAutoMerge enables auto-merge on the PR with the GraphQL
// enablePullRequestAutoMerge mutation. GitHub refuses it for a PR that can
// be merged already ("clean status": no required check or review is
// pending). Such a PR is merged with the REST merge endpoint, which applies
// branch protection too, only with opts.AllowImmediate; otherwise
// ErrNothingPending is returned and the PR waits for a merge by hand. The
// repository must allow auto-merge (Settings > General > Allow auto-merge).
func (g *GitHubProvider) EnableAutoMerge(ctx context.Context, repo string, prNumber int, opts MergeOptions) error {
	method, ok := githubMergeMethods[opts.Method]
	if !ok {
		return fmt.Errorf("enable auto-merge on PR %s#%d: merge method %q: %w", repo, prNumber, opts.Method, ErrPRControlUnsupported)
	}
	nodeID, err := g.prNodeID(ctx, repo, prNumber)
	if err != nil {
		return fmt.Errorf("enable auto-merge on PR %s#%d: %w", repo, prNumber, err)
	}
	vars := map[string]interface{}{"id": nodeID, "method": method}
	if opts.CommitTitle != "" {
		vars["title"] = opts.CommitTitle
		vars["body"] = opts.CommitBody
	}
	err = g.graphql(ctx, enableAutoMergeMutation, vars)
	if err == nil {
		return nil
	}
	if !strings.Contains(strings.ToLower(err.Error()), "clean status") {
		return fmt.Errorf("enable auto-merge on PR %s#%d: %w", repo, prNumber, err)
	}
	if !opts.AllowImmediate {
		return fmt.Errorf("enable auto-merge on PR %s#%d: %w", repo, prNumber, ErrNothingPending)
	}
	merge := map[string]string{"merge_method": opts.Method}
	if opts.CommitTitle != "" {
		merge["commit_title"] = opts.CommitTitle
		merge["commit_message"] = opts.CommitBody
	}
	if err := g.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, prNumber), merge, nil); err != nil {
		return fmt.Errorf("merge PR %s#%d, which needs no more checks or reviews: %w", repo, prNumber, err)
	}
	return nil
}

// disableAutoMergeMutation is GitHub's disablePullRequestAutoMerge mutation.
const disableAutoMergeMutation = `mutation($id: ID!) {
  disablePullRequestAutoMerge(input: {pullRequestId: $id}) {
    pullRequest { number }
  }
}`

// DisableAutoMerge turns auto-merge off with the GraphQL
// disablePullRequestAutoMerge mutation.
func (g *GitHubProvider) DisableAutoMerge(ctx context.Context, repo string, prNumber int) error {
	nodeID, err := g.prNodeID(ctx, repo, prNumber)
	if err == nil {
		err = g.graphql(ctx, disableAutoMergeMutation, map[string]interface{}{"id": nodeID})
	}
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "not enabled") {
		return fmt.Errorf("disable auto-merge on PR %s#%d: %w", repo, prNumber, err)
	}
	return nil
}

// prNodeID returns the GraphQL node ID of the PR.
func (g *GitHubProvider) prNodeID(ctx context.Context, repo string, prNumber int) (string, error) {
	var pr struct {
		NodeID string `json:"node_id"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, prNumber), nil, &pr); err != nil {
		return "", err
	}
	return pr.NodeID, nil
}

// graphqlURL is the GraphQL endpoint next to the REST API: api.github.com
// serves both at its root, and GitHub Enterprise Server serves REST at
// /api/v3 and GraphQL at /api/graphql.
func (g *GitHubProvider) graphqlURL() string {
	if base, ok := strings.CutSuffix(g.APIURL, "/api/v3"); ok {
		return base + "/api/graphql"
	}
	return g.APIURL + "/graphql"
}

// graphql runs a GraphQL request. GitHub answers a failed operation with
// HTTP 200 and an errors list, which becomes the error.
func (g *GitHubProvider) graphql(ctx context.Context, query string, vars map[string]interface{}) error {
	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := g.doURL(ctx, http.MethodPost, g.graphqlURL(), "/graphql",
		map[string]interface{}{"query": query, "variables": vars}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(resp.Errors))
	for _, e := range resp.Errors {
		msgs = append(msgs, e.Message)
	}
	return errors.New("GitHub GraphQL: " + strings.Join(msgs, "; "))
}
