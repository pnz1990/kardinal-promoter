// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const commentMarker = "<!-- kardinal-receipt:payments/checkout-prod -->"

// commentAPI is a fake SCM API: self answers the token's identity, and
// page(n, mine) the n-th page of comments, where mine says whether the
// marker comment on the second page is kardinal's or someone else's.
type commentAPI struct {
	selfPath, commentsPath string
	self                   interface{}
	page                   func(n int, mine bool) interface{}
}

// TestHasPRComment (QA-3, marker spoofing): every provider searches a pull
// request's comments page by page for the marker, and counts it only in a
// comment its own SCM identity wrote. A marker posted first by anyone who may
// comment does not stop kardinal from posting.
func TestHasPRComment(t *testing.T) {
	full := func(size int, f func(i int) map[string]interface{}) []map[string]interface{} {
		out := make([]map[string]interface{}, size)
		for i := range out {
			out[i] = f(i)
		}
		return out
	}
	other := func(i int) string { return fmt.Sprintf("comment %d", i) }
	tests := []struct {
		kind, repo string
		api        commentAPI
	}{
		{"github", "acme/app", commentAPI{"/user", "/repos/acme/app/issues/5/comments", map[string]string{"login": "kardinal-bot"},
			func(n int, mine bool) interface{} {
				who := map[string]string{"login": "mallory"}
				if mine {
					who = map[string]string{"login": "kardinal-bot"}
				}
				if n == 1 {
					return full(100, func(i int) map[string]interface{} { return map[string]interface{}{"body": other(i), "user": who} })
				}
				return []map[string]interface{}{{"body": "Promotion receipt\n\n" + commentMarker, "user": who}}
			}}},
		{"gitlab", "acme/app", commentAPI{"/api/v4/user", "/api/v4/projects/acme%2Fapp/merge_requests/5/notes", map[string]int{"id": 7},
			func(n int, mine bool) interface{} {
				who := map[string]int{"id": 9}
				if mine {
					who = map[string]int{"id": 7}
				}
				if n == 1 {
					// A system note on a full page: the page is still full.
					return full(100, func(i int) map[string]interface{} {
						return map[string]interface{}{"body": other(i), "author": who, "system": i == 0}
					})
				}
				return []map[string]interface{}{{"body": commentMarker, "author": who}}
			}}},
		{"forgejo", "acme/app", commentAPI{"/api/v1/user", "/api/v1/repos/acme/app/issues/5/comments", map[string]int{"id": 3},
			func(n int, mine bool) interface{} {
				who := map[string]int{"id": 4}
				if mine {
					who = map[string]int{"id": 3}
				}
				if n == 1 {
					return full(50, func(i int) map[string]interface{} { return map[string]interface{}{"body": other(i), "user": who} })
				}
				return []map[string]interface{}{{"body": commentMarker, "user": who}}
			}}},
		{"bitbucket", "acme/app", commentAPI{"/2.0/user", "/2.0/repositories/acme/app/pullrequests/5/comments", map[string]string{"uuid": "{me}"},
			func(n int, mine bool) interface{} {
				who := map[string]string{"uuid": "{mallory}"}
				if mine {
					who = map[string]string{"uuid": "{me}"}
				}
				if n == 1 {
					return map[string]interface{}{"values": []map[string]interface{}{{"content": map[string]string{"raw": "lgtm"}, "user": who}},
						"next": "https://api.bitbucket.org/2.0/...page=2"}
				}
				return map[string]interface{}{"values": []map[string]interface{}{{"content": map[string]string{"raw": commentMarker}, "user": who}}}
			}}},
		{"azuredevops", "acme/Web/app", commentAPI{"/acme/_apis/connectionData", "/acme/Web/_apis/git/repositories/app/pullrequests/5/threads",
			map[string]interface{}{"authenticatedUser": map[string]string{"id": "AAAA-me"}},
			func(_ int, mine bool) interface{} {
				who := map[string]string{"id": "bbbb-mallory"}
				if mine {
					who = map[string]string{"id": "aaaa-me"}
				}
				return map[string]interface{}{"value": []map[string]interface{}{
					{"comments": []map[string]interface{}{{"content": "lgtm", "author": who}}},
					{"comments": []map[string]interface{}{{"content": commentMarker, "author": who}}}}}
			}}},
	}
	for _, tc := range tests {
		for _, mine := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s mine=%v", tc.kind, mine), func(t *testing.T) {
				selfCalls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.EscapedPath() {
					case tc.api.selfPath:
						selfCalls++
						_ = json.NewEncoder(w).Encode(tc.api.self)
					case tc.api.commentsPath:
						page, _ := strconv.Atoi(r.URL.Query().Get("page"))
						if page == 0 {
							page = 1
						}
						_ = json.NewEncoder(w).Encode(tc.api.page(page, mine))
					default:
						t.Errorf("unexpected request %s", r.URL)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer srv.Close()
				p, err := scm.NewProvider(tc.kind, "token", srv.URL, "")
				require.NoError(t, err)
				f, ok := p.(scm.PRCommentFinder)
				require.True(t, ok, "%s searches comments", tc.kind)
				for range 2 {
					found, err := f.HasPRComment(context.Background(), tc.repo, 5, commentMarker)
					require.NoError(t, err)
					assert.Equal(t, mine, found, "only kardinal's own comment counts")
				}
				assert.Equal(t, 1, selfCalls, "the identity is read once")
			})
		}
	}
}

// TestHasPRComment_BitbucketDC: Bitbucket Data Center follows nextPageStart
// through the PR's activities (COMMENTED only), and counts the marker only in
// a comment of the token's user (applinks whoami).
func TestHasPRComment_BitbucketDC(t *testing.T) {
	for _, mine := range []bool{false, true} {
		author := "mallory"
		if mine {
			author = "kardinal-bot"
		}
		f := newFakeAPI(t, map[string]apiReply{"GET /bitbucket/plugins/servlet/applinks/whoami": {200, "kardinal-bot\n"}})
		f.queued["GET "+dcPRs+"/12/activities"] = []apiReply{
			{200, `{"values":[{"action":"APPROVED"},{"action":"COMMENTED","comment":{"text":"lgtm","author":{"name":"kardinal-bot"}}}],"isLastPage":false,"nextPageStart":100}`},
			{200, `{"values":[{"action":"RESCOPED","comment":{"text":"` + commentMarker + `","author":{"name":"kardinal-bot"}}},` +
				`{"action":"COMMENTED","comment":{"text":"` + commentMarker + `","author":{"name":"` + author + `"}}}],"isLastPage":true}`},
		}
		found, err := dcProvider(t, f, "").HasPRComment(context.Background(), "PLAT/web-app", 12, commentMarker)
		require.NoError(t, err)
		assert.Equal(t, mine, found, "author %s", author)
	}
}

type appTokens struct{}

func (appTokens) Token(context.Context) (string, error) { return "installation-token", nil }
func (appTokens) AppID() int64                          { return 42 }

// TestHasPRComment_GitHubApp: a GitHub App's own comments are matched by the
// App (performed_via_github_app.id), not by a login anyone could imitate.
func TestHasPRComment_GitHubApp(t *testing.T) {
	comments := []map[string]interface{}{
		{"body": commentMarker, "user": map[string]string{"login": "kardinal[bot]"}},
		{"body": commentMarker, "user": map[string]string{"login": "other[bot]"}, "performed_via_github_app": map[string]int{"id": 7}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/acme/app/issues/5/comments", r.URL.Path, "an installation token does not read /user")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comments)
	}))
	defer srv.Close()
	p := scm.NewGitHubAppProvider(appTokens{}, srv.URL, "")
	found, err := p.HasPRComment(context.Background(), "acme/app", 5, commentMarker)
	require.NoError(t, err)
	assert.False(t, found, "neither comment is the App's")
	comments = append(comments, map[string]interface{}{"body": commentMarker,
		"user": map[string]string{"login": "kardinal[bot]"}, "performed_via_github_app": map[string]int{"id": 42}})
	found, err = p.HasPRComment(context.Background(), "acme/app", 5, commentMarker)
	require.NoError(t, err)
	assert.True(t, found, "the App's own comment")
}

// TestHasPRComment_IdentityForbidden: a token that may not read its own user
// (Forgejo without read:user, the scopes docs/scm-providers.md lists) cannot
// search by author, so the search is unsupported and the caller posts; any
// other failure to read the identity (a rejected token, a rate limit, an
// outage) stays an error, and no comment is ever counted by marker alone.
func TestHasPRComment_IdentityForbidden(t *testing.T) {
	tests := []struct {
		kind, repo, selfPath string
		status               int
		rateLimited          bool
		unsupported          bool
	}{
		{"forgejo", "acme/app", "/api/v1/user", http.StatusForbidden, false, true},
		{"bitbucket", "acme/app", "/2.0/user", http.StatusForbidden, false, true},
		{"gitlab", "acme/app", "/api/v4/user", http.StatusForbidden, false, true},
		{"forgejo", "acme/app", "/api/v1/user", http.StatusForbidden, true, false},
		{"forgejo", "acme/app", "/api/v1/user", http.StatusUnauthorized, false, false},
		{"forgejo", "acme/app", "/api/v1/user", http.StatusInternalServerError, false, false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s %d rateLimited=%v", tc.kind, tc.status, tc.rateLimited), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() != tc.selfPath {
					t.Errorf("unexpected request %s: the comments are not read without the identity", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.rateLimited {
					w.Header().Set("X-RateLimit-Remaining", "0")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"token does not have at least one of required scope(s): [read:user]"}`))
			}))
			defer srv.Close()
			p, err := scm.NewProvider(tc.kind, "token", srv.URL, "")
			require.NoError(t, err)
			found, err := p.(scm.PRCommentFinder).HasPRComment(context.Background(), tc.repo, 5, commentMarker)
			require.Error(t, err)
			assert.False(t, found)
			assert.Equal(t, tc.unsupported, errors.Is(err, scm.ErrCommentSearchUnsupported), "%v", err)
		})
	}
}
