// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestFindMergedPR (#2065): each provider finds the merged pull request of
// a head branch into a base branch, asking the SCM for merged (or closed)
// PRs of that branch, and keeps the newest one opened at or after since:
// not an older PR of the same branch (an earlier Bundle with the same
// name), not one that was closed without merging, not another branch's.
//
// Covers SCM-MERGED-BEFORE-01.
func TestFindMergedPR(t *testing.T) {
	const head, base = "kardinal/abc/b1/prod", "main"
	since := time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		provider func(url string) scm.MergedPRFinder
		repo     string
		// wantQuery is a request substring that shows the server-side filter.
		wantQuery []string
		body      string
		wantNum   int
		wantURL   string
	}{
		{name: "github", repo: "o/r",
			provider:  func(u string) scm.MergedPRFinder { return scm.NewGitHubProvider("t", u, "") },
			wantQuery: []string{"state=closed", "head=o%3Akardinal%2Fabc%2Fb1%2Fprod", "base=main"},
			body: `[
				{"number":9,"html_url":"https://github.com/o/r/pull/9","created_at":"2026-10-16T05:10:00Z","merged_at":null,"head":{"ref":"kardinal/abc/b1/prod"},"base":{"ref":"main"}},
				{"number":8,"html_url":"https://github.com/o/r/pull/8","created_at":"2026-10-16T05:05:00Z","merged_at":"2026-10-16T05:06:00Z","head":{"ref":"kardinal/abc/b1/prod"},"base":{"ref":"main"}},
				{"number":3,"html_url":"https://github.com/o/r/pull/3","created_at":"2026-10-15T05:05:00Z","merged_at":"2026-10-15T05:06:00Z","head":{"ref":"kardinal/abc/b1/prod"},"base":{"ref":"main"}},
				{"number":7,"html_url":"https://github.com/o/r/pull/7","created_at":"2026-10-16T05:07:00Z","merged_at":"2026-10-16T05:08:00Z","head":{"ref":"other"},"base":{"ref":"main"}}]`,
			wantNum: 8, wantURL: "https://github.com/o/r/pull/8"},
		{name: "gitlab", repo: "g/p",
			provider:  func(u string) scm.MergedPRFinder { return scm.NewGitLabProvider("t", u, "") },
			wantQuery: []string{"state=merged", "source_branch=kardinal%2Fabc%2Fb1%2Fprod", "target_branch=main"},
			body: `[
				{"iid":8,"web_url":"https://gitlab.com/g/p/-/merge_requests/8","created_at":"2026-10-16T05:05:00Z","state":"merged","source_branch":"kardinal/abc/b1/prod","target_branch":"main"},
				{"iid":3,"web_url":"https://gitlab.com/g/p/-/merge_requests/3","created_at":"2026-10-15T05:05:00Z","state":"merged","source_branch":"kardinal/abc/b1/prod","target_branch":"main"},
				{"iid":7,"web_url":"https://gitlab.com/g/p/-/merge_requests/7","created_at":"2026-10-16T05:07:00Z","state":"merged","source_branch":"kardinal/abc/b1/prod","target_branch":"release"}]`,
			wantNum: 8, wantURL: "https://gitlab.com/g/p/-/merge_requests/8"},
		{name: "forgejo and gitea", repo: "o/r",
			provider:  func(u string) scm.MergedPRFinder { return scm.NewForgejoProvider("t", u, "") },
			wantQuery: []string{"state=closed"},
			body: `[
				{"number":9,"html_url":"http://forgejo/o/r/pulls/9","merged":false,"created_at":"2026-10-16T05:10:00Z","head":{"ref":"kardinal/abc/b1/prod","label":"kardinal/abc/b1/prod"},"base":{"ref":"main"}},
				{"number":8,"html_url":"http://forgejo/o/r/pulls/8","merged":true,"created_at":"2026-10-16T05:05:00Z","head":{"ref":"kardinal/abc/b1/prod","label":"kardinal/abc/b1/prod"},"base":{"ref":"main"}},
				{"number":3,"html_url":"http://forgejo/o/r/pulls/3","merged":true,"created_at":"2026-10-15T05:05:00Z","head":{"ref":"kardinal/abc/b1/prod","label":"kardinal/abc/b1/prod"},"base":{"ref":"main"}}]`,
			wantNum: 8, wantURL: "http://forgejo/o/r/pulls/8"},
		{name: "bitbucket cloud", repo: "ws/r",
			provider:  func(u string) scm.MergedPRFinder { return scm.NewBitbucketProvider("t", u, "") },
			wantQuery: []string{"state=MERGED", "source.branch.name%3D%22kardinal%2Fabc%2Fb1%2Fprod%22"},
			body: `{"values":[
				{"id":8,"state":"MERGED","created_on":"2026-10-16T05:05:00.123456+00:00","source":{"branch":{"name":"kardinal/abc/b1/prod"}},"destination":{"branch":{"name":"main"}},"links":{"html":{"href":"https://bitbucket.org/ws/r/pull-requests/8"}}},
				{"id":3,"state":"MERGED","created_on":"2026-10-15T05:05:00.123456+00:00","source":{"branch":{"name":"kardinal/abc/b1/prod"}},"destination":{"branch":{"name":"main"}},"links":{"html":{"href":"https://bitbucket.org/ws/r/pull-requests/3"}}}]}`,
			wantNum: 8, wantURL: "https://bitbucket.org/ws/r/pull-requests/8"},
		{name: "bitbucket data center", repo: "PROJ/repo",
			provider:  func(u string) scm.MergedPRFinder { return scm.NewBitbucketDCProvider("t", u, "") },
			wantQuery: []string{"state=MERGED", "at=refs%2Fheads%2Fkardinal%2Fabc%2Fb1%2Fprod", "direction=OUTGOING"},
			body: `{"isLastPage":true,"values":[
				{"id":8,"state":"MERGED","createdDate":1792127100000,"fromRef":{"id":"refs/heads/kardinal/abc/b1/prod"},"toRef":{"id":"refs/heads/main"},"links":{"self":[{"href":"https://bb.example/projects/PROJ/repos/repo/pull-requests/8"}]}},
				{"id":3,"state":"MERGED","createdDate":1792040700000,"fromRef":{"id":"refs/heads/kardinal/abc/b1/prod"},"toRef":{"id":"refs/heads/main"},"links":{"self":[{"href":"https://bb.example/projects/PROJ/repos/repo/pull-requests/3"}]}}]}`,
			wantNum: 8, wantURL: "https://bb.example/projects/PROJ/repos/repo/pull-requests/8"},
		{name: "azure devops", repo: "org/proj/repo",
			provider:  func(u string) scm.MergedPRFinder { return scm.NewAzureDevOpsProvider("t", u, "") },
			wantQuery: []string{"searchCriteria.status=completed", "searchCriteria.sourceRefName=refs%2Fheads%2Fkardinal%2Fabc%2Fb1%2Fprod"},
			body: `{"value":[
				{"pullRequestId":8,"status":"completed","creationDate":"2026-10-16T05:05:00.1234567Z","sourceRefName":"refs/heads/kardinal/abc/b1/prod","targetRefName":"refs/heads/main","repository":{"webUrl":"https://dev.azure.com/org/proj/_git/repo"}},
				{"pullRequestId":3,"status":"completed","creationDate":"2026-10-15T05:05:00Z","sourceRefName":"refs/heads/kardinal/abc/b1/prod","targetRefName":"refs/heads/main","repository":{"webUrl":"https://dev.azure.com/org/proj/_git/repo"}}]}`,
			wantNum: 8, wantURL: "https://dev.azure.com/org/proj/_git/repo/pullrequest/8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var queries []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method, "a lookup only reads")
				queries = append(queries, r.URL.RawQuery)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			f := tt.provider(srv.URL)

			pr, err := f.FindMergedPR(context.Background(), tt.repo, head, base, since)
			require.NoError(t, err)
			require.NotNil(t, pr, "the merged PR opened after since")
			assert.Equal(t, tt.wantNum, pr.Number)
			assert.Equal(t, tt.wantURL, pr.URL)
			require.NotEmpty(t, queries)
			for _, q := range tt.wantQuery {
				assert.Contains(t, queries[0], q, "filtered on the server")
			}

			// Only PRs older than since: none is this promotion's.
			pr, err = f.FindMergedPR(context.Background(), tt.repo, head, base, since.Add(24*time.Hour))
			require.NoError(t, err)
			assert.Nil(t, pr)
		})
	}
}

// TestFindMergedPR_ErrorsAreErrors: a failing lookup is an error, never "no
// merged PR", so the step retries instead of recording no changes.
func TestFindMergedPR_ErrorsAreErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	pr, err := scm.NewGitHubProvider("t", srv.URL, "").FindMergedPR(context.Background(), "o/r", "h", "main", time.Time{})
	require.Error(t, err)
	assert.Nil(t, pr)
	assert.True(t, strings.Contains(err.Error(), "502"), err.Error())
}
