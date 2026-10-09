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

	"github.com/rs/zerolog"
)

// BranchDeleter is implemented by SCM providers that can delete a branch. The
// PromotionStep reconciler deletes the head branch of a PR it closed, so the
// closed PR cannot be merged later: GitHub's merge API merges a closed,
// unmerged PR without reopening it, but not one whose head branch is gone.
// It also deletes the branch of a step that ended before it opened a PR. It
// keeps the branch of a step deleted on its own when kro applies the step
// again and the new step pushes the branch again at once: Forgejo and Gitea
// close the open PRs of a deleted branch from a queue, after DeleteBranch
// returns, and closed the new step's PR too.
//
// DeleteBranch returns nil when the branch does not exist, so a retry after a
// delete whose response was lost succeeds.
type BranchDeleter interface {
	DeleteBranch(ctx context.Context, repo, branch string) error
}

// branchPath escapes each segment of a branch name and keeps the slashes,
// for the APIs that route the branch name as the rest of the path.
func branchPath(branch string) string {
	parts := strings.Split(branch, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// statusIs reports whether err is an APIError with one of the status codes.
func statusIs(err error, codes ...int) (*APIError, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return nil, false
	}
	for _, c := range codes {
		if apiErr.StatusCode == c {
			return apiErr, true
		}
	}
	return apiErr, false
}

// DeleteBranch deletes refs/heads/<branch>. GitHub answers 422 "Reference
// does not exist" for a ref that is gone. A 404 is taken as gone too, since
// the caller has just closed a PR in the same repository with the same token,
// but GitHub also answers 404 when the token cannot see the ref or the
// repository (a classic token without `repo`, say), so the 404 is logged as a
// warning with the repo and branch, through the context's logger: if the
// branch is still there, the closed PR can still be merged.
func (g *GitHubProvider) DeleteBranch(ctx context.Context, repo, branch string) error {
	err := g.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/git/refs/heads/%s", repo, branchPath(branch)), nil, nil)
	if apiErr, ok := statusIs(err, http.StatusNotFound); ok {
		zerolog.Ctx(ctx).Warn().Err(apiErr).Str("repo", repo).Str("branch", branch).
			Msg("GitHub answered 404 to the branch delete; taking the branch as gone, but a token that " +
				"cannot see the repository gets the same answer: check that the branch is deleted")
		return nil
	}
	if apiErr, ok := statusIs(err, http.StatusUnprocessableEntity); ok && strings.Contains(apiErr.Body, "Reference does not exist") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete GitHub branch %s in %s: %w", branch, repo, err)
	}
	return nil
}

// DeleteBranch deletes a GitLab branch. GitLab takes the branch name as one
// escaped path segment.
func (g *GitLabProvider) DeleteBranch(ctx context.Context, repo, branch string) error {
	path := fmt.Sprintf("/api/v4/projects/%s/repository/branches/%s", encodeProjectID(repo), url.PathEscape(branch))
	err := g.do(ctx, http.MethodDelete, path, nil, nil)
	if _, gone := statusIs(err, http.StatusNotFound); gone {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete GitLab branch %s in %s: %w", branch, repo, err)
	}
	return nil
}

// DeleteBranch deletes a Forgejo or Gitea branch. Both route /branches/* by
// path, so the slashes of the name stay. Forgejo answers 500 "object does
// not exist" to the delete of a branch that is not there (B90). That 500
// would count against the SCM circuit, so the branch is read first and a
// branch that reads 404 is gone: deleting a deleted branch is a no-op, as a
// cleanup retried after an outage needs (#1476). After a failed delete the
// branch is read again.
func (f *ForgejoProvider) DeleteBranch(ctx context.Context, repo, branch string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/api/v1/repos/%s/%s/branches/%s", owner, name, branchPath(branch))
	if _, gone := statusIs(f.do(ctx, http.MethodGet, path, nil, nil), http.StatusNotFound); gone {
		return nil
	}
	err = f.do(ctx, http.MethodDelete, path, nil, nil)
	if _, gone := statusIs(err, http.StatusNotFound); gone || err == nil {
		return nil
	}
	if _, gone := statusIs(f.do(ctx, http.MethodGet, path, nil, nil), http.StatusNotFound); gone {
		return nil
	}
	return fmt.Errorf("delete Forgejo branch %s in %s: %w", branch, repo, err)
}

// DeleteBranch deletes a Bitbucket Cloud branch.
func (b *BitbucketProvider) DeleteBranch(ctx context.Context, repo, branch string) error {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/2.0/repositories/%s/%s/refs/branches/%s", workspace, repoSlug, branchPath(branch))
	err = b.do(ctx, http.MethodDelete, path, nil, nil)
	if _, gone := statusIs(err, http.StatusNotFound); gone {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete Bitbucket branch %s in %s: %w", branch, repo, err)
	}
	return nil
}

// adoZeroObjectID is the new object ID of a ref update that deletes the ref.
const adoZeroObjectID = "0000000000000000000000000000000000000000"

// DeleteBranch deletes an Azure DevOps branch. The Refs API deletes a ref by
// updating it from its current commit to the zero object ID, so the branch is
// read first; a branch that is not there is already deleted.
func (a *AzureDevOpsProvider) DeleteBranch(ctx context.Context, repo, branch string) error {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return err
	}
	ref := "refs/heads/" + branch
	base := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/refs", org, project, repoName)
	var refs struct {
		Value []struct {
			Name     string `json:"name"`
			ObjectID string `json:"objectId"`
		} `json:"value"`
	}
	// filter matches a prefix of the name without "refs/".
	q := url.Values{"filter": {"heads/" + branch}, "api-version": {azureDevOpsAPIVersion}}
	if err := a.do(ctx, http.MethodGet, base+"?"+q.Encode(), nil, &refs); err != nil {
		return fmt.Errorf("read ADO branch %s in %s: %w", branch, repo, err)
	}
	oldID := ""
	for _, r := range refs.Value {
		if r.Name == ref {
			oldID = r.ObjectID
		}
	}
	if oldID == "" {
		return nil
	}
	var result struct {
		Value []struct {
			Success      bool   `json:"success"`
			UpdateStatus string `json:"updateStatus"`
		} `json:"value"`
	}
	update := []map[string]string{{"name": ref, "oldObjectId": oldID, "newObjectId": adoZeroObjectID}}
	if err := a.do(ctx, http.MethodPost, base+"?api-version="+azureDevOpsAPIVersion, update, &result); err != nil {
		return fmt.Errorf("delete ADO branch %s in %s: %w", branch, repo, err)
	}
	// The Refs API answers 200 and reports each update on its own.
	if len(result.Value) != 1 || !result.Value[0].Success {
		status := "no result"
		if len(result.Value) > 0 {
			status = result.Value[0].UpdateStatus
		}
		return fmt.Errorf("delete ADO branch %s in %s: update status %s", branch, repo, status)
	}
	return nil
}

// DeleteBranch forwards to the active provider. A provider that cannot delete
// branches leaves them.
func (d *DynamicProvider) DeleteBranch(ctx context.Context, repo, branch string) error {
	bd, ok := d.current().(BranchDeleter)
	if !ok {
		return nil
	}
	return bd.DeleteBranch(ctx, repo, branch)
}

var (
	_ BranchDeleter = (*GitHubProvider)(nil)
	_ BranchDeleter = (*GitLabProvider)(nil)
	_ BranchDeleter = (*ForgejoProvider)(nil)
	_ BranchDeleter = (*BitbucketProvider)(nil)
	_ BranchDeleter = (*AzureDevOpsProvider)(nil)
	_ BranchDeleter = (*DynamicProvider)(nil)
)
