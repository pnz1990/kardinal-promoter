// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestGetPRMergeCommit proves the merge-commit half of C03-promotionstep-11 /
// E2E-01: every provider reports the commit a merged PR produced, so health can
// require the GitOps tool to have synced it.
func TestGetPRMergeCommit(t *testing.T) {
	tests := []struct {
		name     string
		provider func(url string) scm.MergeCommitGetter
		repo     string
		path     string
		body     map[string]interface{}
		want     string
	}{
		{name: "github", repo: "owner/repo", path: "/repos/owner/repo/pulls/7",
			provider: func(u string) scm.MergeCommitGetter { return scm.NewGitHubProvider("t", u, "") },
			body:     map[string]interface{}{"merged": true, "merge_commit_sha": "aaa1111"}, want: "aaa1111"},
		{name: "gitlab merge commit", repo: "group/sub/repo", path: "/api/v4/projects/group%2Fsub%2Frepo/merge_requests/7",
			provider: func(u string) scm.MergeCommitGetter { return scm.NewGitLabProvider("t", u, "") },
			body:     map[string]interface{}{"merge_commit_sha": "bbb2222", "sha": "head"}, want: "bbb2222"},
		{name: "gitlab squash", repo: "group/repo", path: "/api/v4/projects/group%2Frepo/merge_requests/7",
			provider: func(u string) scm.MergeCommitGetter { return scm.NewGitLabProvider("t", u, "") },
			body:     map[string]interface{}{"squash_commit_sha": "ccc3333", "sha": "head"}, want: "ccc3333"},
		{name: "gitlab fast-forward", repo: "group/repo", path: "/api/v4/projects/group%2Frepo/merge_requests/7",
			provider: func(u string) scm.MergeCommitGetter { return scm.NewGitLabProvider("t", u, "") },
			body:     map[string]interface{}{"sha": "ddd4444"}, want: "ddd4444"},
		{name: "forgejo", repo: "owner/repo", path: "/api/v1/repos/owner/repo/pulls/7",
			provider: func(u string) scm.MergeCommitGetter { return scm.NewForgejoProvider("t", u, "") },
			body:     map[string]interface{}{"merge_commit_sha": "eee5555"}, want: "eee5555"},
		{name: "bitbucket", repo: "ws/repo", path: "/2.0/repositories/ws/repo/pullrequests/7",
			provider: func(u string) scm.MergeCommitGetter { return scm.NewBitbucketProvider("t", u, "") },
			body:     map[string]interface{}{"merge_commit": map[string]interface{}{"hash": "fff6666"}}, want: "fff6666"},
		{name: "bitbucket not merged", repo: "ws/repo", path: "/2.0/repositories/ws/repo/pullrequests/7",
			provider: func(u string) scm.MergeCommitGetter { return scm.NewBitbucketProvider("t", u, "") },
			body:     map[string]interface{}{"state": "OPEN"}, want: ""},
		{name: "azure devops", repo: "org/proj/repo", path: "/org/proj/_apis/git/repositories/repo/pullrequests/7",
			provider: func(u string) scm.MergeCommitGetter { return scm.NewAzureDevOpsProvider("t", u, "") },
			body:     map[string]interface{}{"lastMergeCommit": map[string]interface{}{"commitId": "abc7777"}}, want: "abc7777"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, tt.path, r.URL.EscapedPath())
				assert.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tt.body)
			}))
			defer srv.Close()
			got, err := tt.provider(srv.URL).GetPRMergeCommit(context.Background(), tt.repo, 7)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDynamicProvider_GetPRMergeCommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"merge_commit_sha":"123abcd"}`))
	}))
	defer srv.Close()
	dp, err := scm.NewDynamicProvider("github", "t", srv.URL, "")
	require.NoError(t, err)
	got, err := dp.GetPRMergeCommit(context.Background(), "owner/repo", 1)
	require.NoError(t, err)
	assert.Equal(t, "123abcd", got)
}

func TestGoGitClient_HeadCommit(t *testing.T) {
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("f.txt")
	require.NoError(t, err)
	hash, err := wt.Commit("c", &gogit.CommitOptions{Author: &object.Signature{Name: "a", Email: "a@b", When: time.Now()}})
	require.NoError(t, err)

	got, err := scm.NewGoGitClient().HeadCommit(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, hash.String(), got)

	_, err = scm.NewGoGitClient().HeadCommit(context.Background(), t.TempDir())
	assert.Error(t, err, "a directory that is not a repository has no HEAD")
}
