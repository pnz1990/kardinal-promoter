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

package scm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestRepositoryAllowlist_Allows covers #1332: which spec.git.url values a
// --scm-allowed-repositories list allows. The repository is matched as the
// SCM API names it (RepoFromURL), so an Azure DevOps URL is matched on
// dev.azure.com/{org}/{project}/{repo}, not on its raw path.
//
// Covers SCM-ALLOWREPO-02.
func TestRepositoryAllowlist_Allows(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		url      string
		want     bool
	}{
		{"unset allows everything", nil, "https://github.com/evil/repo", true},
		{"only empty patterns is unset", []string{"", " "}, "https://github.com/evil/repo", true},
		{"owner glob", []string{"github.com/acme/*"}, "https://github.com/acme/gitops.git", true},
		{"owner glob, other owner", []string{"github.com/acme/*"}, "https://github.com/evil/gitops", false},
		{"owner glob, other host", []string{"github.com/acme/*"}, "https://github.example.com/acme/gitops", false},
		{"case-insensitive", []string{"GitHub.com/Acme/*"}, "https://github.com/ACME/GitOps", true},
		{"scheme, port and .git in the pattern", []string{"https://github.com:443/acme/gitops.git"}, "https://github.com/acme/gitops", true},
		{"exact repo, other repo", []string{"github.com/acme/gitops"}, "https://github.com/acme/other", false},
		{"userinfo and port are ignored", []string{"git.example.com/team/*"}, "http://u:p@git.example.com:3000/team/app.git", true},
		{"scp-like ssh URL", []string{"github.com/acme/*"}, "git@github.com:acme/gitops.git", true},
		{"* does not cross a slash", []string{"gitlab.com/acme/*"}, "https://gitlab.com/acme/sub/proj", false},
		{"/** matches subgroups", []string{"gitlab.com/acme/**"}, "https://gitlab.com/acme/sub/proj", true},
		{"GitLab web URL suffix is not part of the project", []string{"gitlab.com/acme/proj"}, "https://gitlab.com/acme/proj/-/tree/main", true},
		{"/** with a glob host", []string{"*.example.com/**"}, "https://git.example.com/a/b/c", true},
		{"/** does not match a sibling prefix", []string{"gitlab.com/acme/**"}, "https://gitlab.com/acme-evil/proj", false},
		{"/** needs a repository below it", []string{"gitlab.com/acme/**"}, "https://gitlab.com/acme", false},
		{"one of several", []string{"github.com/a/*", "github.com/b/*"}, "https://github.com/b/x", true},
		{"unparsable URL", []string{"*/**"}, "not a url", false},
		{"file URL has no host", []string{"*/**"}, "file:///tmp/repo", false},
		{"dot segments are refused", []string{"github.com/acme/**"}, "https://github.com/acme/../evil/repo", false},
		// QA #1483: the raw path put the organization two segments before
		// _git, so a path under acme named another organization.
		{"Azure DevOps: organization from the path, not the raw prefix", []string{"dev.azure.com/acme/**"},
			"https://dev.azure.com/acme/victimorg/victimproj/_git/repo", false},
		{"Azure DevOps: the victim's real identity", []string{"dev.azure.com/victimorg/**"},
			"https://dev.azure.com/acme/victimorg/victimproj/_git/repo", true},
		{"Azure DevOps: allowed project", []string{"dev.azure.com/acme/platform/*"}, "https://dev.azure.com/acme/platform/_git/gitops", true},
		{"Azure DevOps: visualstudio.com is dev.azure.com/<org>", []string{"dev.azure.com/acme/**"},
			"https://acme.visualstudio.com/platform/_git/gitops", true},
		{"Azure DevOps: visualstudio.com of another org", []string{"dev.azure.com/acme/**"},
			"https://victim.visualstudio.com/acme/_git/gitops", false},
		{"Azure DevOps: a raw visualstudio.com pattern matches nothing", []string{"acme.visualstudio.com/**"},
			"https://acme.visualstudio.com/platform/_git/gitops", false},
		{"Azure DevOps: ssh", []string{"dev.azure.com/acme/platform/gitops"}, "git@ssh.dev.azure.com:v3/acme/platform/gitops", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := scm.ParseRepositoryAllowlist(tt.patterns)
			require.NoError(t, err)
			assert.Equal(t, tt.want, a.Allows(tt.url))
		})
	}
}

func TestParseRepositoryAllowlist_BadPattern(t *testing.T) {
	for _, p := range []string{"github.com/[acme/*", "github.com/**/repo", "github.com", "*", "github.com/"} {
		_, err := scm.ParseRepositoryAllowlist([]string{p})
		assert.Error(t, err, p)
	}
}

func TestWebHost(t *testing.T) {
	tests := []struct{ typ, apiURL, want string }{
		{"", "", "github.com"},
		{"github", "https://api.github.com", "github.com"},
		{"github", "https://github.example.com/api/v3", "github.example.com"},
		{"gitlab", "", "gitlab.com"},
		{"gitlab", "https://gitlab.example.com", "gitlab.example.com"},
		{"forgejo", "http://forgejo.forgejo.svc.cluster.local:3000", "forgejo.forgejo.svc.cluster.local"},
		{"bitbucket", "https://api.bitbucket.org", "bitbucket.org"},
		{"azuredevops", "", "dev.azure.com"},
	}
	for _, tt := range tests {
		got, err := scm.WebHost(tt.typ, tt.apiURL)
		require.NoError(t, err)
		assert.Equal(t, tt.want, got, "%s %s", tt.typ, tt.apiURL)
	}
	_, err := scm.WebHost("svn", "")
	assert.Error(t, err)
}

// TestRepositoryAllowlist_CheckPipeline covers #1332 and QA #1483: a Pipeline
// whose spec.git.url is not allowed is refused unless it never needs the
// shared token: its own git.secretRef Secret exists and no environment uses
// approval: pr-review, whose PRs the shared token opens and polls, or it
// names its own SCM provider (spec.git.providerRef).
func TestRepositoryAllowlist_CheckPipeline(t *testing.T) {
	a, err := scm.ParseRepositoryAllowlist([]string{"github.com/acme/*"})
	require.NoError(t, err)
	pipeline := func(url, approval string) *v1alpha1.Pipeline {
		p := &v1alpha1.Pipeline{}
		p.Spec.Git.URL = url
		p.Spec.Environments = []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod", Approval: approval}}
		return p
	}
	assert.NoError(t, a.CheckPipeline(pipeline("https://github.com/acme/gitops", "pr-review"), false))
	assert.NoError(t, a.CheckPipeline(pipeline("https://github.com/evil/repo", "auto"), true),
		"own Secret, no PR: the shared token is never used")

	err = a.CheckPipeline(pipeline("https://github.com/evil/repo", "pr-review"), true)
	require.Error(t, err, "own Secret, but the shared token opens the PR")
	assert.Contains(t, err.Error(), `environment "prod" uses approval: pr-review`)

	err = a.CheckPipeline(pipeline("https://x:secret@github.com/evil/repo", "auto"), false)
	require.Error(t, err, "no Secret")
	assert.True(t, errors.Is(err, scm.ErrRepositoryNotAllowed))
	assert.True(t, scm.IsPermanentError(err))
	assert.Contains(t, err.Error(), "github.com/acme/*")
	assert.NotContains(t, err.Error(), "secret@", "the URL is redacted")
	assert.NotContains(t, scm.NotAllowedMessage(err), scm.ErrRepositoryNotAllowed.Error())

	// spec.git.providerRef (#1459): the provider's token opens and tracks
	// the PRs, checked against the provider's own allowedRepositories, so
	// the controller's list does not apply to them; git still needs the
	// Pipeline's own Secret.
	own := pipeline("https://github.com/evil/repo", "pr-review")
	own.Spec.Git.ProviderRef = &v1alpha1.ScmProviderRef{Name: "team"}
	assert.NoError(t, a.CheckPipeline(own, true), "own Secret and own provider: the shared token is never used")
	assert.ErrorIs(t, a.CheckPipeline(own, false), scm.ErrRepositoryNotAllowed, "own provider, but no Secret for git")

	var unset *scm.RepositoryAllowlist
	assert.NoError(t, unset.CheckPipeline(pipeline("https://github.com/evil/repo", "pr-review"), false), "unset allows all")
}

// guardSCM records the calls that reach it.
type guardSCM struct{ calls []string }

func (g *guardSCM) OpenPR(_ context.Context, repo, _, _, _, _ string) (string, int, error) {
	g.calls = append(g.calls, "open "+repo)
	return "u", 1, nil
}
func (g *guardSCM) ClosePR(_ context.Context, repo string, _ int) error {
	g.calls = append(g.calls, "close "+repo)
	return nil
}
func (g *guardSCM) CommentOnPR(_ context.Context, repo string, _ int, _ string) error {
	g.calls = append(g.calls, "comment "+repo)
	return nil
}
func (g *guardSCM) GetPRStatus(_ context.Context, repo string, _ int) (bool, bool, error) {
	g.calls = append(g.calls, "status "+repo)
	return false, true, nil
}
func (g *guardSCM) GetPRReviewStatus(_ context.Context, repo string, _ int) (bool, int, error) {
	g.calls = append(g.calls, "reviews "+repo)
	return false, 0, nil
}
func (g *guardSCM) ParseWebhookEvent(_ []byte, _ string) (scm.WebhookEvent, error) {
	g.calls = append(g.calls, "webhook")
	return scm.WebhookEvent{}, nil
}
func (g *guardSCM) AddLabelsToPR(_ context.Context, repo string, _ int, _ []string) error {
	g.calls = append(g.calls, "labels "+repo)
	return nil
}
func (g *guardSCM) SetPRCommitStatus(_ context.Context, repo string, _ int, _ string, _ scm.CommitStatus) error {
	g.calls = append(g.calls, "commit status "+repo)
	return nil
}
func (g *guardSCM) DeleteBranch(_ context.Context, repo, branch string) error {
	g.calls = append(g.calls, "delete "+repo+":"+branch)
	return nil
}
func (g *guardSCM) GetPRMergeCommit(_ context.Context, repo string, _ int) (string, error) {
	g.calls = append(g.calls, "merge "+repo)
	return "sha", nil
}

// TestGuard covers QA #1483: every SCM call the shared token makes is
// checked against the allowlist, whichever code path makes it, so a call for
// a repository that is not allowed never reaches the provider and fails with
// a permanent ErrRepositoryNotAllowed. Calls for an allowed repository and
// webhook parsing pass through; an unset allowlist returns the provider.
func TestGuard(t *testing.T) {
	a, err := scm.ParseRepositoryAllowlist([]string{"github.com/acme/*"})
	require.NoError(t, err)
	ctx := context.Background()
	calls := func(p scm.SCMProvider, repo string) []error {
		_, _, e1 := p.OpenPR(ctx, repo, "t", "b", "h", "main")
		e2 := p.ClosePR(ctx, repo, 1)
		e3 := p.CommentOnPR(ctx, repo, 1, "c")
		_, _, e4 := p.GetPRStatus(ctx, repo, 1)
		_, _, e5 := p.GetPRReviewStatus(ctx, repo, 1)
		e6 := p.AddLabelsToPR(ctx, repo, 1, []string{"l"})
		e7 := p.(scm.BranchDeleter).DeleteBranch(ctx, repo, "kardinal/b/prod")
		_, e8 := p.(scm.MergeCommitGetter).GetPRMergeCommit(ctx, repo, 1)
		e9 := p.(scm.CommitStatusSetter).SetPRCommitStatus(ctx, repo, 1, "abc", scm.CommitStatus{State: "success"})
		return []error{e1, e2, e3, e4, e5, e6, e7, e8, e9}
	}

	inner := &guardSCM{}
	g := a.Guard(inner, "github.com")
	for i, err := range calls(g, "evil/victim") {
		require.Error(t, err, "call %d", i)
		assert.True(t, errors.Is(err, scm.ErrRepositoryNotAllowed), "call %d: %v", i, err)
		assert.True(t, scm.IsPermanentError(err), "call %d", i)
	}
	assert.Empty(t, inner.calls, "nothing reaches the provider")

	for i, err := range calls(g, "acme/gitops") {
		assert.NoError(t, err, "call %d", i)
	}
	assert.Len(t, inner.calls, 9)
	_, err = g.ParseWebhookEvent(nil, "")
	assert.NoError(t, err)

	// QA #1483: a repository that only looks allowed, through escapes,
	// separators or characters no SCM allows in a name, is refused too.
	for _, repo := range []string{
		"acme/%2e%2e%2fvictim%2frepo", "acme/..%2fvictim", "acme/a%20b", `acme\victim`, "acme/x?y", "acme/x#y",
		"acme/a b", "acme/a\tb", "acme/a\nb", "acme/a\x00b", "acme/../victim", "acme//victim", "acme/./x",
	} {
		_, _, err := g.GetPRStatus(ctx, repo, 1)
		assert.True(t, errors.Is(err, scm.ErrRepositoryNotAllowed), "%q: %v", repo, err)
		assert.False(t, a.AllowsRepo("github.com", repo), "%q", repo)
	}
	assert.Len(t, inner.calls, 10, "none of them reached the provider")

	// Azure DevOps: single spaces in the project and repository names only.
	ado, err := scm.ParseRepositoryAllowlist([]string{"dev.azure.com/acme/**"})
	require.NoError(t, err)
	adoInner := &guardSCM{}
	adoGuard := ado.Guard(adoInner, "dev.azure.com")
	for repo, want := range map[string]bool{
		"acme/My Project/gitops":    true,
		"acme/Project/my gitops":    true,
		"acme/My Project/my gitops": true,
		"acme/My  Project/gitops":   false,
		"acme/Project/my  gitops":   false,
		"acme/ Project/gitops":      false,
		"acme/Project /gitops":      false,
		"acme/Project/ gitops":      false,
		"acme/Project/gitops ":      false,
		"ac me/Project/gitops":      false,
		"acme/Pro\tject/gitops":     false,
		"acme/Project/git%20ops":    false,
	} {
		_, _, err := adoGuard.GetPRStatus(ctx, repo, 1)
		assert.Equal(t, want, err == nil, "%q: %v", repo, err)
		if !want {
			assert.True(t, errors.Is(err, scm.ErrRepositoryNotAllowed), "%q", repo)
		}
	}
	assert.Len(t, adoInner.calls, 3, "only the three allowed repositories reached the provider")
	// A space outside Azure DevOps is refused, also where Azure allows one.
	for _, repo := range []string{"acme/my repo", "acme/My Project/gitops", "acme/sub group/proj"} {
		_, _, err := g.GetPRStatus(ctx, repo, 1)
		assert.True(t, errors.Is(err, scm.ErrRepositoryNotAllowed), "github.com %q: %v", repo, err)
	}

	other := &guardSCM{}
	for _, err := range calls(a.Guard(other, "github.example.com"), "acme/gitops") {
		assert.Error(t, err, "the same repository on another SCM host is not allowed")
	}
	assert.Empty(t, other.calls)

	var unset *scm.RepositoryAllowlist
	assert.Same(t, inner, unset.Guard(inner, "github.com").(*guardSCM))
}

// TestRepositoryAllowlist_Segments: a repository segment is
// [A-Za-z0-9._-]+, except Azure DevOps project and repository names, which
// may hold single spaces; a percent sign, backslash, ?, # or a control character is never
// allowed (QA #1483). Covers SCM-ALLOWREPO-02.
func TestRepositoryAllowlist_Segments(t *testing.T) {
	a, err := scm.ParseRepositoryAllowlist([]string{"github.com/acme/**", "dev.azure.com/acme/**"})
	require.NoError(t, err)
	tests := []struct {
		host, repo string
		want       bool
	}{
		{"github.com", "acme/my_repo-1.0", true},
		{"github.com", "acme/sub/deep.repo", true},
		{"github.com", "acme/my repo", false},
		{"github.com", "acme/my%20repo", false},
		{"dev.azure.com", "acme/My Project/gitops", true},
		{"dev.azure.com", "acme/My Project/git ops", true},
		{"dev.azure.com", "acme/Project/git  ops", false},
		{"dev.azure.com", "acme/Project/gitops ", false},
		{"dev.azure.com", "ac me/Project/gitops", false},
		{"dev.azure.com", "acme/ Project/gitops", false},
		{"dev.azure.com", "acme/Pro\tject/gitops", false},
		{"dev.azure.com", "acme/Pro%20ject/gitops", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, a.AllowsRepo(tt.host, tt.repo), "%s %q", tt.host, tt.repo)
	}
	assert.True(t, a.Allows("https://dev.azure.com/acme/My%20Project/_git/gitops"), "an escaped space in the URL is the project's space")
}

// TestRepositoryAllowlist_IPv6Host: a pattern for an IPv6 SCM host, written
// with brackets and a port as in a URL, matches remotes and the API host on
// that address (QA #1483).
func TestRepositoryAllowlist_IPv6Host(t *testing.T) {
	a, err := scm.ParseRepositoryAllowlist([]string{"[fd00::1]:3000/acme/*"})
	require.NoError(t, err)
	assert.Equal(t, []string{"fd00::1/acme/*"}, a.Patterns())
	assert.True(t, a.Allows("http://[fd00::1]:3000/acme/gitops.git"))
	assert.False(t, a.Allows("http://[fd00::2]:3000/acme/gitops.git"))
	host, err := scm.WebHost("forgejo", "http://[fd00::1]:3000")
	require.NoError(t, err)
	assert.True(t, a.AllowsRepo(host, "acme/gitops"))
}
