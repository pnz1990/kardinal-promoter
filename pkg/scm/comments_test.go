// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestHasPRComment: GitHub, GitLab and Forgejo search a pull request's
// comments page by page, oldest first, for a marker: found on a later page,
// and not found once a short page ends the list.
func TestHasPRComment(t *testing.T) {
	tests := []struct {
		kind, path string
		size       int
	}{
		{"github", "/repos/acme/app/issues/5/comments", 100},
		{"gitlab", "/api/v4/projects/acme%2Fapp/merge_requests/5/notes", 100},
		{"forgejo", "/api/v1/repos/acme/app/issues/5/comments", 50},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			marker := "<!-- kardinal-receipt:payments/checkout-prod -->"
			pages := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() != tc.path && r.URL.Path != tc.path {
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				pages++
				var out []map[string]string
				switch page {
				case 1: // a full page without it
					for i := 0; i < tc.size; i++ {
						out = append(out, map[string]string{"body": fmt.Sprintf("comment %d", i)})
					}
				case 2: // a short page with it
					out = append(out, map[string]string{"body": "Promotion receipt ...\n\n" + marker})
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer srv.Close()
			p, err := scm.NewProvider(tc.kind, "token", srv.URL, "")
			require.NoError(t, err)
			f, ok := p.(scm.PRCommentFinder)
			require.True(t, ok, "%s searches comments", tc.kind)
			found, err := f.HasPRComment(context.Background(), "acme/app", 5, marker)
			require.NoError(t, err)
			assert.True(t, found, "on the second page")
			assert.Equal(t, 2, pages)

			pages = 0
			found, err = f.HasPRComment(context.Background(), "acme/app", 5, "<!-- kardinal-receipt:other -->")
			require.NoError(t, err)
			assert.False(t, found)
			assert.Equal(t, 2, pages, "the short page ends the search")
		})
	}
}

// TestHasPRComment_BitbucketAndAzure: Bitbucket Cloud follows next links,
// Bitbucket Data Center follows nextPageStart through the PR's activities
// (COMMENTED only), and Azure DevOps reads every thread at once.
func TestHasPRComment_BitbucketAndAzure(t *testing.T) {
	marker := "<!-- kardinal-receipt:payments/checkout-prod -->"
	ctx := context.Background()

	t.Run("bitbucket", func(t *testing.T) {
		path := "GET /2.0/repositories/acme/app/pullrequests/5/comments"
		f := newFakeAPI(t, nil)
		f.queued[path] = []apiReply{
			{200, `{"values":[{"content":{"raw":"lgtm"}}],"next":"https://api.bitbucket.org/2.0/...page=2"}`},
			{200, `{"values":[{"content":{"raw":"Promotion receipt\n\n` + marker + `"}}]}`},
			{200, `{"values":[{"content":{"raw":"lgtm"}}]}`},
		}
		p, err := scm.NewProvider("bitbucket", "token", f.URL, "")
		require.NoError(t, err)
		finder := p.(scm.PRCommentFinder)
		found, err := finder.HasPRComment(ctx, "acme/app", 5, marker)
		require.NoError(t, err)
		assert.True(t, found, "on the second page")
		found, err = finder.HasPRComment(ctx, "acme/app", 5, marker)
		require.NoError(t, err)
		assert.False(t, found, "a last page without it")
	})

	t.Run("bitbucket-datacenter", func(t *testing.T) {
		path := "GET " + dcPRs + "/12/activities"
		f := newFakeAPI(t, nil)
		f.queued[path] = []apiReply{
			{200, `{"values":[{"action":"APPROVED"},{"action":"COMMENTED","comment":{"text":"lgtm"}}],"isLastPage":false,"nextPageStart":100}`},
			// A reply quoting the marker in another action does not count.
			{200, `{"values":[{"action":"RESCOPED","comment":{"text":"` + marker + `"}},{"action":"COMMENTED","comment":{"text":"` + marker + `"}}],"isLastPage":true}`},
		}
		found, err := dcProvider(t, f, "").HasPRComment(ctx, "PLAT/web-app", 12, marker)
		require.NoError(t, err)
		assert.True(t, found)
		require.Len(t, f.calls, 2)
		assert.Equal(t, "100", f.calls[1].Query.Get("start"), "the next page")
	})

	t.Run("azuredevops", func(t *testing.T) {
		path := "GET /acme/Web/_apis/git/repositories/app/pullrequests/5/threads"
		f := newFakeAPI(t, nil)
		f.queued[path] = []apiReply{
			{200, `{"value":[{"comments":[{"content":"lgtm"}]},{"comments":[{"content":"x"},{"content":"` + marker + `"}]}]}`},
			{200, `{"value":[{"comments":[{"content":"lgtm"}]}]}`},
		}
		p, err := scm.NewProvider("azuredevops", "token", f.URL, "")
		require.NoError(t, err)
		finder := p.(scm.PRCommentFinder)
		found, err := finder.HasPRComment(ctx, "acme/Web/app", 5, marker)
		require.NoError(t, err)
		assert.True(t, found)
		found, err = finder.HasPRComment(ctx, "acme/Web/app", 5, marker)
		require.NoError(t, err)
		assert.False(t, found)
	})
}
