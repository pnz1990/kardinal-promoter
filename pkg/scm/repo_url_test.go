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
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestRepoFromURL covers every supported provider's remote URL forms
// (C06-scm-health-02, C06-scm-health-03).
func TestRepoFromURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{name: "github https", url: "https://github.com/owner/repo", want: "owner/repo"},
		{name: "github .git", url: "https://github.com/owner/repo.git", want: "owner/repo"},
		{name: "github trailing slash", url: "https://github.com/owner/repo/", want: "owner/repo"},
		{name: "github ssh scp", url: "git@github.com:owner/repo.git", want: "owner/repo"},
		{name: "github ssh url", url: "ssh://git@github.com/owner/repo.git", want: "owner/repo"},
		{name: "github with token userinfo", url: "https://x-access-token:secret@github.com/owner/repo.git", want: "owner/repo"},
		{name: "github enterprise", url: "https://ghe.example.com/corp/gitops.git", want: "corp/gitops"},
		{name: "gitlab", url: "https://gitlab.com/group/proj.git", want: "group/proj"},
		{name: "gitlab subgroups", url: "https://gitlab.com/group/sub/proj.git", want: "group/sub/proj"},
		{name: "gitlab web suffix", url: "https://gitlab.example.com/group/sub/proj/-/tree/main", want: "group/sub/proj"},
		{name: "bitbucket", url: "https://bitbucket.org/ws/repo.git", want: "ws/repo"},
		{name: "bitbucket user", url: "https://alice@bitbucket.org/ws/repo.git", want: "ws/repo"},
		{name: "forgejo", url: "https://codeberg.org/owner/repo.git", want: "owner/repo"},
		{name: "ado https", url: "https://dev.azure.com/org/proj/_git/repo", want: "org/proj/repo"},
		{name: "ado https user", url: "https://org@dev.azure.com/org/proj/_git/repo", want: "org/proj/repo"},
		{name: "ado visualstudio", url: "https://org.visualstudio.com/proj/_git/repo", want: "org/proj/repo"},
		{name: "ado visualstudio DefaultCollection", url: "https://org.visualstudio.com/DefaultCollection/proj/_git/repo", want: "org/proj/repo"},
		{name: "ado server", url: "https://tfs.corp.example/tfs/Coll/proj/_git/repo", want: "Coll/proj/repo"},
		{name: "ado ssh", url: "git@ssh.dev.azure.com:v3/org/proj/repo", want: "org/proj/repo"},
		{name: "empty", url: "", wantErr: true},
		{name: "host only", url: "https://github.com", wantErr: true},
		{name: "one segment", url: "https://github.com/owner", wantErr: true},
		{name: "not a url", url: "just-text", wantErr: true},
		{name: "ado missing repo", url: "https://dev.azure.com/org/proj/_git/", wantErr: true},
		{name: "dot-dot segment", url: "https://github.com/owner/../repo", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scm.RepoFromURL(tc.url)
			if tc.wantErr {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "secret")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestSameOrigin guards the config-source clone: the pipeline token is only
// sent to the pipeline's own origin (scheme, host and port), so it is never
// sent over plain http or to another port (C05-steps-03, C05-steps-08).
func TestSameOrigin(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"same https host", "https://github.com/o/r", "https://github.com/o/other.git", true},
		{"host is case-insensitive", "https://GitHub.com/o/r", "https://github.com/o/r", true},
		{"default port is explicit", "https://github.com/o/r", "https://github.com:443/o/r", true},
		{"userinfo is ignored", "https://github.com/o/r", "https://x-access-token@github.com/o/r", true},
		{"same http origin", "http://gitea.local:3000/o/r", "http://gitea.local:3000/o/c", true},
		{"https to http downgrade", "https://github.com/o/r", "http://github.com/o/r", false},
		{"http to https", "http://github.com/o/r", "https://github.com/o/r", false},
		{"other port", "https://github.com/o/r", "https://github.com:8443/o/r", false},
		{"ssh remote", "https://github.com/o/r", "git@github.com:o/r.git", false},
		{"ssh scheme", "https://github.com/o/r", "ssh://git@github.com/o/r.git", false},
		{"other host", "https://github.com/o/r", "https://evil.example/o/r", false},
		{"suffix host", "https://github.com/o/r", "https://github.com.evil.example/o/r", false},
		{"empty", "https://github.com/o/r", "", false},
		{"both empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, scm.SameOrigin(tc.a, tc.b), "%q vs %q", tc.a, tc.b)
		})
	}
}

// TestParsePRURL covers the PR URL shape of every provider (C06-scm-health-03).
func TestParsePRURL(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		wantRepo string
		wantNum  int
		wantErr  bool
	}{
		{name: "github", url: "https://github.com/o/r/pull/42", wantRepo: "o/r", wantNum: 42},
		{name: "github files tab", url: "https://github.com/o/r/pull/42/files", wantRepo: "o/r", wantNum: 42},
		{name: "github enterprise", url: "https://ghe.example.com/corp/gitops/pull/7", wantRepo: "corp/gitops", wantNum: 7},
		{name: "gitlab subgroup", url: "https://gitlab.com/group/sub/proj/-/merge_requests/3", wantRepo: "group/sub/proj", wantNum: 3},
		{name: "bitbucket", url: "https://bitbucket.org/ws/repo/pull-requests/5", wantRepo: "ws/repo", wantNum: 5},
		{name: "forgejo", url: "https://codeberg.org/o/r/pulls/9", wantRepo: "o/r", wantNum: 9},
		{name: "ado", url: "https://dev.azure.com/org/proj/_git/repo/pullrequest/7", wantRepo: "org/proj/repo", wantNum: 7},
		{name: "query string", url: "https://github.com/o/r/pull/42?w=1", wantRepo: "o/r", wantNum: 42},
		{name: "no marker", url: "https://github.com/o/r", wantErr: true},
		{name: "bad number", url: "https://github.com/o/r/pull/abc", wantErr: true},
		{name: "empty", url: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, n, err := scm.ParsePRURL(tc.url)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantRepo, repo)
			assert.Equal(t, tc.wantNum, n)
		})
	}
}

// TestRedactURL verifies credentials never survive redaction (C05-steps-26).
func TestRedactURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://x-access-token:ghp_SECRET@github.com/o/r.git", "https://github.com/o/r.git"},
		{"https://ghp_SECRET@github.com/o/r.git", "https://github.com/o/r.git"},
		{"https://github.com/o/r.git", "https://github.com/o/r.git"},
		{"git@github.com:o/r.git", "git@github.com:o/r.git"},
		{"clone https://u:ghp_SECRET@host/o/r failed", "clone https://host/o/r failed"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := scm.RedactURL(tc.in)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "SECRET")
		})
	}
}

// TestWebhookSignature verifies each provider's signature header is read
// (C07-controller-04, C06-scm-health-12).
func TestWebhookSignature(t *testing.T) {
	cases := []struct {
		name   string
		header string
		value  string
	}{
		{"github", "X-Hub-Signature-256", "sha256=abc"},
		{"gitea", "X-Gitea-Signature", "abc"},
		{"forgejo", "X-Forgejo-Signature", "abc"},
		{"gitlab", "X-Gitlab-Token", "tok"},
		{"azure devops", "X-AzureDevOps-Token", "tok"},
		{"bitbucket", "X-Hub-Signature", "sha256=abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			h.Set(tc.header, tc.value)
			assert.Equal(t, tc.value, scm.WebhookSignature(h))
			assert.Equal(t, tc.header, scm.WebhookSignatureHeader(h))
		})
	}
	t.Run("github prefers sha256 over sha1", func(t *testing.T) {
		h := http.Header{}
		h.Set("X-Hub-Signature", "sha1=old")
		h.Set("X-Hub-Signature-256", "sha256=new")
		assert.Equal(t, "sha256=new", scm.WebhookSignature(h))
		assert.Equal(t, "X-Hub-Signature-256", scm.WebhookSignatureHeader(h))
	})
	t.Run("gitea signature and X-Hub-Signature-256", func(t *testing.T) {
		// WebhookSignatureHeader names the header WebhookSignature reads (B76).
		h := http.Header{}
		h.Set("X-Gitea-Signature", "abc")
		h.Set("X-Hub-Signature-256", "sha256=abc")
		assert.Equal(t, "sha256=abc", scm.WebhookSignature(h))
		assert.Equal(t, "X-Hub-Signature-256", scm.WebhookSignatureHeader(h))
	})
	t.Run("none", func(t *testing.T) {
		assert.Empty(t, scm.WebhookSignature(http.Header{}))
		assert.Empty(t, scm.WebhookSignatureHeader(http.Header{}))
	})
}
