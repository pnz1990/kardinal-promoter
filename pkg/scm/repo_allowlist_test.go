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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestRepositoryAllowlist_Allows covers #1332: which spec.git.url values a
// --scm-allowed-repositories list allows. Covers SCM-ALLOWREPO-02.
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
		{"scheme and .git in the pattern", []string{"https://github.com/acme/gitops.git"}, "https://github.com/acme/gitops", true},
		{"exact repo, other repo", []string{"github.com/acme/gitops"}, "https://github.com/acme/other", false},
		{"userinfo and port are ignored", []string{"git.example.com/team/*"}, "http://u:p@git.example.com:3000/team/app.git", true},
		{"scp-like ssh URL", []string{"github.com/acme/*"}, "git@github.com:acme/gitops.git", true},
		{"* does not cross a slash", []string{"gitlab.com/acme/*"}, "https://gitlab.com/acme/sub/proj", false},
		{"/** matches subgroups", []string{"gitlab.com/acme/**"}, "https://gitlab.com/acme/sub/proj", true},
		{"/** with a glob prefix", []string{"*.example.com/**"}, "https://git.example.com/a/b/c", true},
		{"/** does not match a sibling prefix", []string{"gitlab.com/acme/**"}, "https://gitlab.com/acme-evil/proj", false},
		{"one of several", []string{"github.com/a/*", "github.com/b/*"}, "https://github.com/b/x", true},
		{"unparsable URL", []string{"*"}, "not a url", false},
		{"file URL has no host", []string{"*/**"}, "file:///tmp/repo", false},
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
	for _, p := range []string{"github.com/[acme/*", "github.com/**/repo"} {
		_, err := scm.ParseRepositoryAllowlist([]string{p})
		assert.Error(t, err, p)
	}
}

// TestRepositoryAllowlist_CheckPipeline covers #1332: a Pipeline without
// git.secretRef must point at an allowed repository; one with its own
// secretRef is always allowed, since it pushes with its namespace's token.
func TestRepositoryAllowlist_CheckPipeline(t *testing.T) {
	a, err := scm.ParseRepositoryAllowlist([]string{"github.com/acme/*"})
	require.NoError(t, err)
	pipeline := func(url string, secret string) *v1alpha1.Pipeline {
		p := &v1alpha1.Pipeline{}
		p.Spec.Git.URL = url
		if secret != "" {
			p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: secret}
		}
		return p
	}
	assert.NoError(t, a.CheckPipeline(pipeline("https://github.com/acme/gitops", "")))
	assert.NoError(t, a.CheckPipeline(pipeline("https://github.com/evil/repo", "team-token")), "own secretRef")

	err = a.CheckPipeline(pipeline("https://x:secret@github.com/evil/repo", ""))
	require.Error(t, err)
	assert.True(t, errors.Is(err, scm.ErrRepositoryNotAllowed))
	assert.Contains(t, err.Error(), "github.com/acme/*")
	assert.Contains(t, err.Error(), "set git.secretRef")
	assert.NotContains(t, err.Error(), "secret@", "the URL is redacted")

	var unset *scm.RepositoryAllowlist
	assert.NoError(t, unset.CheckPipeline(pipeline("https://github.com/evil/repo", "")), "unset allows all")
}
