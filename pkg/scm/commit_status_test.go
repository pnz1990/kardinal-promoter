// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// statusCall is one request the fake API received.
type statusCall struct {
	route string
	body  map[string]interface{}
}

// statusAPI answers get with pr and records every request.
func statusAPI(t *testing.T, get, pr string) (string, func() []statusCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []statusCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := statusCall{route: r.Method + " " + r.URL.Path}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			require.NoError(t, json.Unmarshal(raw, &c.body))
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if c.route == get {
			_, _ = io.WriteString(w, pr)
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []statusCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]statusCall(nil), calls...)
	}
}

// TestSetPRCommitStatus: every provider posts the kardinal/gates status on
// the commit it is given (the one kardinal pushed), in its own API's terms,
// without reading the PR's current head: a head someone else pushed is never
// given kardinal's status. Azure DevOps posts on the PR iteration whose
// source commit it is.
//
// Covers SCM-GATESTATUS-03.
func TestSetPRCommitStatus(t *testing.T) {
	failing := scm.CommitStatus{Context: scm.GatesStatusContext, State: scm.CommitStatusFailure,
		Description: "freeze: prod is frozen", TargetURL: "https://kardinal.example/b"}
	cases := []struct {
		name     string
		provider func(url string) scm.CommitStatusSetter
		repo     string
		get, pr  string
		post     string
		wantBody map[string]interface{}
	}{
		{name: "github", provider: func(u string) scm.CommitStatusSetter { return scm.NewGitHubProvider("t", u, "") },
			repo: "org/repo", post: "POST /repos/org/repo/statuses/abc123",
			wantBody: map[string]interface{}{"state": "failure", "context": "kardinal/gates", "description": "freeze: prod is frozen", "target_url": "https://kardinal.example/b"}},
		{name: "forgejo", provider: func(u string) scm.CommitStatusSetter { return scm.NewForgejoProvider("t", u, "") },
			repo: "org/repo", post: "POST /api/v1/repos/org/repo/statuses/abc123",
			wantBody: map[string]interface{}{"state": "failure", "context": "kardinal/gates", "description": "freeze: prod is frozen", "target_url": "https://kardinal.example/b"}},
		{name: "gitlab", provider: func(u string) scm.CommitStatusSetter { return scm.NewGitLabProvider("t", u, "") },
			repo: "org/repo", post: "POST /api/v4/projects/org/repo/statuses/abc123",
			wantBody: map[string]interface{}{"state": "failed", "name": "kardinal/gates", "description": "freeze: prod is frozen", "target_url": "https://kardinal.example/b"}},
		{name: "bitbucket", provider: func(u string) scm.CommitStatusSetter { return scm.NewBitbucketProvider("u:t", u, "") },
			repo: "ws/repo", post: "POST /2.0/repositories/ws/repo/commit/abc123/statuses/build",
			wantBody: map[string]interface{}{"state": "FAILED", "key": "kardinal/gates", "name": "kardinal/gates",
				"url": "https://kardinal.example/b", "description": "freeze: prod is frozen"}},
		{name: "azuredevops", provider: func(u string) scm.CommitStatusSetter { return scm.NewAzureDevOpsProvider("t", u, "") },
			repo: "org/proj/repo", get: "GET /org/proj/_apis/git/repositories/repo/pullrequests/5/iterations",
			pr: `{"value":[{"id":1,"sourceRefCommit":{"commitId":"old000"}},{"id":2,"sourceRefCommit":{"commitId":"abc123"}},` +
				`{"id":3,"sourceRefCommit":{"commitId":"someone-else"}}]}`,
			post: "POST /org/proj/_apis/git/repositories/repo/pullrequests/5/iterations/2/statuses",
			wantBody: map[string]interface{}{"state": "failed", "description": "freeze: prod is frozen", "targetUrl": "https://kardinal.example/b",
				"context": map[string]interface{}{"genre": "kardinal", "name": "gates"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, calls := statusAPI(t, tc.get, tc.pr)
			require.NoError(t, tc.provider(url).SetPRCommitStatus(context.Background(), tc.repo, 5, "abc123", failing))
			got := calls()
			if tc.get != "" {
				require.Len(t, got, 2, "%+v", got)
				assert.Equal(t, tc.get, got[0].route)
				got = got[1:]
			}
			require.Len(t, got, 1, "%+v", got)
			assert.Equal(t, tc.post, got[0].route)
			assert.Equal(t, tc.wantBody, got[0].body)
		})
	}
}

// TestSetPRCommitStatus_Edges: a long description is shortened to GitHub's
// 140 characters; Bitbucket without a target URL links the PR page; an Azure
// DevOps PR with no iteration for the commit (its head moved) gets nothing.
func TestSetPRCommitStatus_Edges(t *testing.T) {
	url, calls := statusAPI(t, "", "")
	require.NoError(t, scm.NewGitHubProvider("t", url, "").SetPRCommitStatus(context.Background(), "org/repo", 5, "s",
		scm.CommitStatus{Context: "kardinal/gates", State: "failure", Description: strings.Repeat("é", 300)}))
	desc := calls()[0].body["description"].(string)
	assert.Equal(t, 140, len([]rune(desc)))
	assert.True(t, strings.HasSuffix(desc, "…"))

	url, calls = statusAPI(t, "GET /2.0/repositories/ws/repo/pullrequests/5",
		`{"links":{"html":{"href":"https://bitbucket.org/ws/repo/pull-requests/5"}}}`)
	require.NoError(t, scm.NewBitbucketProvider("u:t", url, "").SetPRCommitStatus(context.Background(), "ws/repo", 5, "abc",
		scm.CommitStatus{Context: "kardinal/gates", State: "error"}))
	got := calls()
	require.Len(t, got, 2)
	assert.Equal(t, "https://bitbucket.org/ws/repo/pull-requests/5", got[1].body["url"])
	assert.Equal(t, "FAILED", got[1].body["state"])

	url, calls = statusAPI(t, "GET /org/proj/_apis/git/repositories/repo/pullrequests/5/iterations",
		`{"value":[{"id":1,"sourceRefCommit":{"commitId":"someone-else"}}]}`)
	err := scm.NewAzureDevOpsProvider("t", url, "").SetPRCommitStatus(context.Background(), "org/proj/repo", 5, "abc",
		scm.CommitStatus{Context: "kardinal/gates", State: "success"})
	assert.ErrorIs(t, err, scm.ErrCommitNotInPR)
	assert.Len(t, calls(), 1, "no status posted")
}

// TestSetPRCommitStatus_GitLabSameState: GitLab answers 400 "Cannot
// transition status" when the commit already has that state; that is the
// status kardinal wants, so it is not an error. Other 400s are.
func TestSetPRCommitStatus_GitLabSameState(t *testing.T) {
	body := `{"message":"Cannot transition status via :run from :running"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	p := scm.NewGitLabProvider("t", srv.URL, "")
	st := scm.CommitStatus{Context: "kardinal/gates", State: "pending"}
	assert.NoError(t, p.SetPRCommitStatus(context.Background(), "org/repo", 5, "abc", st))
	body = `{"message":"name is too long"}`
	assert.Error(t, p.SetPRCommitStatus(context.Background(), "org/repo", 5, "abc", st))
}

// TestDynamicProviderForwardsCommitStatus: the DynamicProvider the controller
// runs forwards SetPRCommitStatus to its current provider.
func TestDynamicProviderForwardsCommitStatus(t *testing.T) {
	url, calls := statusAPI(t, "", "")
	d, err := scm.NewDynamicProvider("forgejo", "t", url, "")
	require.NoError(t, err)
	require.NoError(t, d.SetPRCommitStatus(context.Background(), "org/repo", 5, "abc", scm.CommitStatus{Context: "kardinal/gates", State: "pending"}))
	got := calls()
	require.Len(t, got, 1)
	assert.Equal(t, "POST /api/v1/repos/org/repo/statuses/abc", got[0].route)
}

// TestDynamicProviderTokenID: the token identity is a short hash that changes
// when the token is rotated and never contains the token; the allowlist
// guard forwards it.
func TestDynamicProviderTokenID(t *testing.T) {
	d, err := scm.NewDynamicProvider("github", "secret-token-one", "", "")
	require.NoError(t, err)
	first := d.TokenID()
	assert.Len(t, first, 12)
	assert.NotContains(t, first, "secret")
	require.NoError(t, d.Reload("secret-token-one"))
	assert.Equal(t, first, d.TokenID(), "the same token, the same identity")
	require.NoError(t, d.Reload("secret-token-two"))
	assert.NotEqual(t, first, d.TokenID())

	a, err := scm.ParseRepositoryAllowlist([]string{"github.com/acme/*"})
	require.NoError(t, err)
	g := a.Guard(d, "github.com")
	ti, ok := g.(scm.TokenIdentifier)
	require.True(t, ok)
	assert.Equal(t, d.TokenID(), ti.TokenID())
}
