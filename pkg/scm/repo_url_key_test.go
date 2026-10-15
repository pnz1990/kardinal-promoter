// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestRepoURLKey: two URLs have the same RepoURLKey exactly when they
// matched under SameRepoURL's earlier definition (same origin, same path
// ignoring case), so the Pipeline repository index (#1684) finds every
// Pipeline SameRepoURL matched, and SameRepoURL, now defined through the
// key, behaves as before. A URL SameRepoURL never matched has no key. (For
// ASCII; strings.EqualFold also folds a few Unicode letters ToLower does not.)
func TestRepoURLKey(t *testing.T) {
	urls := []string{
		"https://github.com/org/gitops",
		"https://github.com/Org/GitOps.git",
		"https://GITHUB.com:443/org/gitops/",
		"http://github.com/org/gitops",
		"https://github.com/org/other",
		"https://gitlab.example.com/group/sub/repo",
		"https://gitlab.example.com/group/sub/repo/-/tree/main",
		"https://dev.azure.com/acme/web/_git/site",
		"https://dev.azure.com/acme/web/_git/site.git",
		"https://github.example.com:8443/org/gitops",
		"git@github.com:org/gitops.git",
		"ssh://git@github.com/org/gitops",
		"",
	}
	// SameRepoURL's definition before #1684: the same origin and the same
	// repository path, case-insensitively. The key must agree with it.
	before := func(a, b string) bool {
		if !scm.SameOrigin(a, b) {
			return false
		}
		ra, errA := scm.RepoFromURL(a)
		rb, errB := scm.RepoFromURL(b)
		return errA == nil && errB == nil && strings.EqualFold(ra, rb)
	}
	for _, a := range urls {
		for _, b := range urls {
			ka, okA := scm.RepoURLKey(a)
			kb, okB := scm.RepoURLKey(b)
			assert.Equal(t, before(a, b), okA && okB && ka == kb, "key: %q vs %q", a, b)
			assert.Equal(t, before(a, b), scm.SameRepoURL(a, b), "SameRepoURL: %q vs %q", a, b)
		}
	}
	tests := []struct {
		url, want string
		ok        bool
	}{
		{"https://GitHub.com/Org/GitOps.git", "https://github.com:443/org/gitops", true},
		{"http://gitea.local:3000/org/app", "http://gitea.local:3000/org/app", true},
		{"https://gitlab.example.com/group/sub/repo/-/tree/main", "https://gitlab.example.com:443/group/sub/repo", true},
		{"git@github.com:org/gitops.git", "", false},
		{"https://github.com/", "", false},
	}
	for _, tc := range tests {
		got, ok := scm.RepoURLKey(tc.url)
		assert.Equal(t, tc.ok, ok, tc.url)
		assert.Equal(t, tc.want, got, tc.url)
	}
}
