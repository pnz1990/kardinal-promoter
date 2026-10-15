// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestParseWebhookEvent_MergedBy (#1939): a merged-PR webhook names who
// merged it, from each SCM's payload: merged_by on GitHub, Forgejo and
// Gitea, the event's user on GitLab, closedBy on Azure DevOps.
func TestParseWebhookEvent_MergedBy(t *testing.T) {
	newP := func(kind string) scm.SCMProvider {
		p, err := scm.NewProvider(kind, "token", "https://scm.example.com", testWebhookSecret)
		require.NoError(t, err)
		return p
	}
	tests := []struct {
		name, kind, header, payload string
	}{
		{"github", "github", "pull_request",
			`{"action":"closed","pull_request":{"number":5,"merged":true,"merge_commit_sha":"abc","merged_by":{"login":"user1"}},"repository":{"full_name":"acme/app"}}`},
		{"forgejo", "forgejo", "pull_request",
			`{"action":"closed","number":5,"pull_request":{"number":5,"merged":true,"merge_commit_sha":"abc","merged_by":{"login":"user1"}},"repository":{"full_name":"acme/app"}}`},
		{"gitlab", "gitlab", "Merge Request Hook",
			`{"object_kind":"merge_request","user":{"username":"user1"},"object_attributes":{"iid":5,"state":"merged","action":"merge","merge_commit_sha":"abc"},"project":{"path_with_namespace":"acme/app"}}`},
		{"azuredevops", "azuredevops", "",
			`{"eventType":"git.pullrequest.updated","resource":{"pullRequestId":5,"status":"completed","closedBy":{"uniqueName":"user1"},"repository":{"name":"app","project":{"name":"Web"},"remoteUrl":"https://dev.azure.com/acme/Web/_git/app"}}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newP(tc.kind)
			ev, err := p.ParseWebhookEvent([]byte(tc.payload), signedFor(p, []byte(tc.payload)))
			require.NoError(t, err)
			assert.True(t, ev.Merged)
			assert.Equal(t, "user1", ev.MergedBy)
		})
	}
}

// TestGetPRMerger (#1939): each SCM's API names who merged a pull request,
// for a merge found by polling.
func TestGetPRMerger(t *testing.T) {
	tests := []struct {
		kind, repo, path, body string
	}{
		{"github", "acme/app", "/repos/acme/app/pulls/5", `{"merged_by":{"login":"user1"}}`},
		{"gitlab", "acme/app", "/api/v4/projects/acme%2Fapp/merge_requests/5", `{"merge_user":{"username":"user1"},"merged_by":{"username":"older"}}`},
		{"forgejo", "acme/app", "/api/v1/repos/acme/app/pulls/5", `{"merged_by":{"login":"user1"}}`},
		{"bitbucket", "acme/app", "/2.0/repositories/acme/app/pullrequests/5", `{"closed_by":{"nickname":"user1","display_name":"User One"}}`},
		{"azuredevops", "acme/Web/app", "/acme/Web/_apis/git/repositories/app/pullrequests/5", `{"closedBy":{"uniqueName":"user1"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() != tc.path && r.URL.Path != tc.path {
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			p, err := scm.NewProvider(tc.kind, "token", srv.URL, "")
			require.NoError(t, err)
			g, ok := p.(scm.PRMergerGetter)
			require.True(t, ok, "%s reports the merger", tc.kind)
			by, err := g.GetPRMerger(context.Background(), tc.repo, 5)
			require.NoError(t, err)
			assert.Equal(t, "user1", by)
		})
	}
}

// TestGetPRMerger_BitbucketDC (#1939): Bitbucket Data Center names the
// merger in the pull request's MERGED activity.
func TestGetPRMerger_BitbucketDC(t *testing.T) {
	f := newFakeAPI(t, map[string]apiReply{"GET " + dcPRs + "/12/activities": {200,
		`{"values":[{"action":"COMMENTED","user":{"name":"bob"}},{"action":"MERGED","user":{"name":"alice"}},{"action":"APPROVED","user":{"name":"carol"}}]}`}})
	by, err := dcProvider(t, f, "").GetPRMerger(context.Background(), "PLAT/web-app", 12)
	require.NoError(t, err)
	assert.Equal(t, "alice", by)
}
