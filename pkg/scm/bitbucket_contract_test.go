// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// Bitbucket Cloud and Azure DevOps cannot be self-hosted in the e2e suite, so
// their documented behaviour is proven against fake APIs that answer with the
// real REST request and response shapes (Bitbucket Cloud REST 2.0, Azure
// DevOps REST 7.1).

// apiCall is one request a fake SCM API received.
type apiCall struct {
	Method string
	Path   string // escaped
	Query  url.Values
	Header http.Header
	Body   string
}

// apiReply is the answer of a fake SCM API route.
type apiReply struct {
	Status int
	Body   string
}

// fakeAPI is an httptest server that records every request and answers it
// from routes, keyed by "METHOD /escaped/path". A request without a route
// fails the test.
type fakeAPI struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]apiReply
	queued map[string][]apiReply
	calls  []apiCall
}

func newFakeAPI(t *testing.T, routes map[string]apiReply) *fakeAPI {
	t.Helper()
	f := &fakeAPI{routes: routes, queued: map[string][]apiReply{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.EscapedPath()
		f.mu.Lock()
		f.calls = append(f.calls, apiCall{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query(),
			Header: r.Header.Clone(), Body: string(body)})
		reply, ok := f.routes[key]
		if q := f.queued[key]; len(q) > 0 {
			reply, ok, f.queued[key] = q[0], true, q[1:]
		}
		f.mu.Unlock()
		if !ok {
			t.Errorf("unexpected request %s", r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.Status)
		_, _ = io.WriteString(w, reply.Body)
	}))
	t.Cleanup(f.Close)
	return f
}

// queue makes the next requests to a route get replies, in order, before the
// route's own reply applies again.
func (f *fakeAPI) queue(key string, replies ...apiReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued[key] = append(f.queued[key], replies...)
}

func (f *fakeAPI) Calls() []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apiCall(nil), f.calls...)
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

const (
	bbToken    = "bb-access-token"
	bbPRPath   = "/2.0/repositories/acme/web-app/pullrequests"
	bbPR7Path  = bbPRPath + "/7"
	bbBranch   = "kardinal/web-app-v2/prod"
	bbPR7HTML  = "https://bitbucket.org/acme/web-app/pull-requests/7"
	bbMergeSHA = "9f8e7d6c5b4a"
)

// bbRepository is a Bitbucket Cloud repository object, as it appears inside a
// pullrequest and at the top of a webhook payload.
func bbRepository() map[string]interface{} {
	return map[string]interface{}{
		"type":      "repository",
		"full_name": "acme/web-app",
		"name":      "web-app",
		"uuid":      "{0c6e7f3a-5d2b-4c1e-9a8f-2b7d6e5c4a31}",
		"links": map[string]interface{}{
			"html": map[string]string{"href": "https://bitbucket.org/acme/web-app"},
		},
	}
}

// bbParticipant is an entry of pullrequest.participants. state is
// "approved", "changes_requested" or "" (null).
func bbParticipant(role string, approved bool, state string) map[string]interface{} {
	p := map[string]interface{}{
		"type":            "participant",
		"user":            map[string]string{"type": "user", "display_name": "Ana Reviewer", "uuid": "{5f1c2d3e-4b5a-4c6d-8e7f-9a0b1c2d3e4f}"},
		"role":            role,
		"approved":        approved,
		"state":           nil,
		"participated_on": "2026-09-30T10:04:00.000000+00:00",
	}
	if state != "" {
		p["state"] = state
	}
	return p
}

// bbPullRequest is a Bitbucket Cloud REST 2.0 pullrequest object, as
// POST .../pullrequests and GET .../pullrequests/{id} return it. mergeCommit
// "" is a null merge_commit.
func bbPullRequest(id int, state, source, mergeCommit string, participants ...map[string]interface{}) map[string]interface{} {
	api := fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/acme/web-app/pullrequests/%d", id)
	pr := map[string]interface{}{
		"type":        "pullrequest",
		"id":          id,
		"title":       "[kardinal] Promote web-app-v2 to prod",
		"description": "Promotion evidence",
		"state":       state,
		"author":      map[string]string{"type": "user", "display_name": "kardinal-bot", "uuid": "{8b0e1c1e-2f3a-4b5c-8d6e-7f8091a2b3c4}"},
		"source": map[string]interface{}{
			"branch":     map[string]string{"name": source},
			"commit":     map[string]string{"type": "commit", "hash": "1a2b3c4d5e6f"},
			"repository": bbRepository(),
		},
		"destination": map[string]interface{}{
			"branch":     map[string]string{"name": "main"},
			"commit":     map[string]string{"type": "commit", "hash": "0f9e8d7c6b5a"},
			"repository": bbRepository(),
		},
		"merge_commit":        nil,
		"close_source_branch": false,
		"closed_by":           nil,
		"reason":              "",
		"comment_count":       0,
		"task_count":          0,
		"created_on":          "2026-09-30T10:00:00.000000+00:00",
		"updated_on":          "2026-09-30T10:05:00.000000+00:00",
		"reviewers":           []interface{}{},
		"participants":        append([]map[string]interface{}{}, participants...),
		"links": map[string]interface{}{
			"self":    map[string]string{"href": api},
			"html":    map[string]string{"href": fmt.Sprintf("https://bitbucket.org/acme/web-app/pull-requests/%d", id)},
			"decline": map[string]string{"href": api + "/decline"},
			"merge":   map[string]string{"href": api + "/merge"},
		},
	}
	if mergeCommit != "" {
		pr["merge_commit"] = map[string]interface{}{
			"type": "commit",
			"hash": mergeCommit,
			"links": map[string]interface{}{
				"html": map[string]string{"href": "https://bitbucket.org/acme/web-app/commits/" + mergeCommit},
			},
		}
	}
	return pr
}

// bbError is the Bitbucket Cloud error envelope.
func bbError(message string) string {
	return fmt.Sprintf(`{"type":"error","error":{"message":%q}}`, message)
}

// assertBitbucketAuth checks that a request carries the access token as a
// Bearer token.
func assertBitbucketAuth(t *testing.T, c apiCall) {
	t.Helper()
	assert.Equal(t, "Bearer "+bbToken, c.Header.Get("Authorization"), "%s %s", c.Method, c.Path)
}

// newBitbucket builds the provider the way the controller does: through the
// factory, with the token as read from a Secret (trailing newline) and the
// API URL with a trailing slash.
func newBitbucket(t *testing.T, apiURL, secret string) scm.SCMProvider {
	t.Helper()
	p, err := scm.NewProvider("bitbucket", bbToken+"\n", apiURL+"/", secret)
	require.NoError(t, err)
	return p
}

// TestBitbucketCloudContract_OpenPR checks the request OpenPR sends to the
// Bitbucket Cloud REST 2.0 API: POST .../pullrequests on the workspace and
// repository slug taken from spec.git.url, with the access token as a Bearer
// token, and the PR link and id read from the created pullrequest. A rejected
// token is a permanent error. Covers SCM-BB-01.
func TestBitbucketCloudContract_OpenPR(t *testing.T) {
	for _, gitURL := range []string{
		"https://bitbucket.org/acme/web-app.git",
		"https://kardinal-bot@bitbucket.org/acme/web-app.git",
		"git@bitbucket.org:acme/web-app.git",
	} {
		repo, err := scm.RepoFromURL(gitURL)
		require.NoError(t, err)
		assert.Equal(t, "acme/web-app", repo, "workspace/repo of %s", gitURL)
	}

	t.Run("created", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"POST " + bbPRPath: {http.StatusCreated, mustJSON(t, bbPullRequest(7, "OPEN", bbBranch, ""))},
		})
		prURL, n, err := newBitbucket(t, api.URL, "").OpenPR(context.Background(), "acme/web-app",
			"[kardinal] Promote web-app-v2 to prod", "Promotion evidence", bbBranch, "main")
		require.NoError(t, err)
		assert.Equal(t, bbPR7HTML, prURL)
		assert.Equal(t, 7, n)

		calls := api.Calls()
		require.Len(t, calls, 1)
		c := calls[0]
		assert.Equal(t, http.MethodPost, c.Method)
		assert.Equal(t, bbPRPath, c.Path)
		assert.Empty(t, c.Query)
		assertBitbucketAuth(t, c)
		assert.Equal(t, "application/json", c.Header.Get("Content-Type"))
		assert.JSONEq(t, `{
			"title": "[kardinal] Promote web-app-v2 to prod",
			"description": "Promotion evidence",
			"source": {"branch": {"name": "kardinal/web-app-v2/prod"}},
			"destination": {"branch": {"name": "main"}},
			"close_source_branch": false
		}`, c.Body)

		// The PRStatus is keyed by the repository and number parsed back
		// from this URL; they must match what webhooks and polling use.
		gotRepo, gotN, err := scm.ParsePRURL(prURL)
		require.NoError(t, err)
		assert.Equal(t, "acme/web-app", gotRepo)
		assert.Equal(t, 7, gotN)
	})

	t.Run("token rejected", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"POST " + bbPRPath: {http.StatusUnauthorized, bbError("Token is invalid, expired, or not supported for this endpoint.")},
		})
		_, _, err := newBitbucket(t, api.URL, "").OpenPR(context.Background(), "acme/web-app", "t", "b", bbBranch, "main")
		require.Error(t, err)
		assert.True(t, scm.IsPermanentError(err), "a rejected token is not retried: %v", err)
		var apiErr *scm.APIError
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, "bitbucket", apiErr.Provider)
		assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
		assert.Contains(t, err.Error(), "open Bitbucket PR acme/web-app")
		assert.Len(t, api.Calls(), 1, "a rejected token is not taken for an existing PR")
	})

	t.Run("repository is not workspace/repo", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{})
		_, _, err := newBitbucket(t, api.URL, "").OpenPR(context.Background(), "web-app", "t", "b", bbBranch, "main")
		require.ErrorContains(t, err, "workspace/repo_slug")
		assert.Empty(t, api.Calls())
	})
}

// TestBitbucketCloudContract_PRStateAndMergeCommit checks what polling reads
// from GET .../pullrequests/{id}: MERGED is merged, OPEN is open, DECLINED and
// SUPERSEDED are closed, and the merge commit is merge_commit.hash, which is
// null until the PR is merged. Covers SCM-BB-03.
func TestBitbucketCloudContract_PRStateAndMergeCommit(t *testing.T) {
	tests := []struct {
		state       string
		mergeCommit string
		wantMerged  bool
		wantOpen    bool
	}{
		{state: "OPEN", wantOpen: true},
		{state: "MERGED", mergeCommit: bbMergeSHA, wantMerged: true},
		{state: "DECLINED"},
		{state: "SUPERSEDED"},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			api := newFakeAPI(t, map[string]apiReply{
				"GET " + bbPR7Path: {http.StatusOK, mustJSON(t, bbPullRequest(7, tt.state, bbBranch, tt.mergeCommit))},
			})
			p := newBitbucket(t, api.URL, "")

			merged, open, err := p.GetPRStatus(context.Background(), "acme/web-app", 7)
			require.NoError(t, err)
			assert.Equal(t, tt.wantMerged, merged, "merged")
			assert.Equal(t, tt.wantOpen, open, "open")

			// The PRStatus reconciler asks for the merge commit through this
			// interface.
			getter, ok := p.(scm.MergeCommitGetter)
			require.True(t, ok, "the Bitbucket provider reports merge commits")
			sha, err := getter.GetPRMergeCommit(context.Background(), "acme/web-app", 7)
			require.NoError(t, err)
			assert.Equal(t, tt.mergeCommit, sha)

			calls := api.Calls()
			require.Len(t, calls, 2)
			for _, c := range calls {
				assert.Equal(t, http.MethodGet, c.Method)
				assert.Equal(t, bbPR7Path, c.Path)
				assert.Empty(t, c.Query)
				assert.Empty(t, c.Body)
				assert.Empty(t, c.Header.Get("Content-Type"))
				assertBitbucketAuth(t, c)
			}
		})
	}

	t.Run("PR not found", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"GET " + bbPR7Path: {http.StatusNotFound, bbError("Pull request 7 not found")},
		})
		p := newBitbucket(t, api.URL, "")
		_, _, err := p.GetPRStatus(context.Background(), "acme/web-app", 7)
		require.Error(t, err)
		assert.True(t, scm.IsPermanentError(err), "%v", err)
		_, err = p.(scm.MergeCommitGetter).GetPRMergeCommit(context.Background(), "acme/web-app", 7)
		require.ErrorContains(t, err, "get Bitbucket PR merge commit acme/web-app#7")
	})
}

// bbWebhook is a Bitbucket Cloud webhook delivery: the X-Event-Key event and
// its body, {"actor", "pullrequest", "repository"}.
func bbWebhook(t *testing.T, state string, withRepository bool) []byte {
	t.Helper()
	mergeCommit := ""
	if state == "MERGED" {
		mergeCommit = bbMergeSHA
	}
	body := map[string]interface{}{
		"actor":       map[string]string{"type": "user", "display_name": "Ana Reviewer"},
		"pullrequest": bbPullRequest(7, state, bbBranch, mergeCommit),
	}
	if withRepository {
		body["repository"] = bbRepository()
	}
	return []byte(mustJSON(t, body))
}

// bbWebhookHeaders are the headers Bitbucket Cloud sends with a delivery.
// signature "" sends no X-Hub-Signature.
func bbWebhookHeaders(event, signature string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "Bitbucket-Webhooks/2.0")
	h.Set("X-Event-Key", event)
	h.Set("X-Request-UUID", "4a7c3d9e-1b2f-4c5d-8e6f-7a8b9c0d1e2f")
	h.Set("X-Hook-UUID", "{2d1f0e9c-8b7a-4c6d-9e5f-4a3b2c1d0e9f}")
	if signature != "" {
		h.Set("X-Hub-Signature", signature)
	}
	return h
}

// TestBitbucketCloudContract_ParseWebhookEvent checks webhook parsing as the
// handler calls it, with the signature read from the request headers: the
// X-Hub-Signature "sha256=<hex>" HMAC-SHA256 of the body with the webhook
// secret is verified, a pullrequest with state MERGED is a merged event for
// the destination repository, and any other state is not merged. A wrong,
// missing or unprefixed signature, or a changed body, is an error. The
// webhook test in cmd/kardinal-controller proves the handler answers 401.
// Covers SCM-BB-04.
func TestBitbucketCloudContract_ParseWebhookEvent(t *testing.T) {
	const secret = "bb-webhook-secret"
	merged := bbWebhook(t, "MERGED", true)
	declined := bbWebhook(t, "DECLINED", true)
	noRepository := bbWebhook(t, "MERGED", false)
	sign := func(key string, body []byte) string { return "sha256=" + hmacHex(key, body) }

	tests := []struct {
		name    string
		event   string
		body    []byte
		sig     string
		want    scm.WebhookEvent
		wantErr bool
	}{
		{name: "pullrequest:fulfilled is merged", event: "pullrequest:fulfilled", body: merged, sig: sign(secret, merged),
			want: scm.WebhookEvent{EventType: "pull_request", Action: "closed", Merged: true, PRNumber: 7, RepoFullName: "acme/web-app"}},
		{name: "pullrequest:rejected is not merged", event: "pullrequest:rejected", body: declined, sig: sign(secret, declined),
			want: scm.WebhookEvent{EventType: "pullrequest", Action: "declined", PRNumber: 7, RepoFullName: "acme/web-app"}},
		{name: "repository from the PR destination", event: "pullrequest:fulfilled", body: noRepository, sig: sign(secret, noRepository),
			want: scm.WebhookEvent{EventType: "pull_request", Action: "closed", Merged: true, PRNumber: 7, RepoFullName: "acme/web-app"}},
		{name: "wrong secret", event: "pullrequest:fulfilled", body: merged, sig: sign("guess", merged), wantErr: true},
		{name: "signature of another body", event: "pullrequest:fulfilled", body: merged, sig: sign(secret, declined), wantErr: true},
		{name: "no signature", event: "pullrequest:fulfilled", body: merged, wantErr: true},
		{name: "no sha256= prefix", event: "pullrequest:fulfilled", body: merged, sig: hmacHex(secret, merged), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newBitbucket(t, "https://api.bitbucket.org", secret)
			h := bbWebhookHeaders(tt.event, tt.sig)
			require.Equal(t, tt.sig, scm.WebhookSignature(h), "the handler reads X-Hub-Signature")

			ev, err := p.ParseWebhookEvent(tt.body, scm.WebhookSignature(h))
			if tt.wantErr {
				require.ErrorContains(t, err, "invalid HMAC-SHA256 signature")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, ev)
		})
	}
}

// TestBitbucketCloudContract_ReviewStatus checks how approvals are read from
// the participants of GET .../pullrequests/{id}: every participant with
// approved true counts, and one with state changes_requested blocks the
// approval. Covers SCM-BB-05.
func TestBitbucketCloudContract_ReviewStatus(t *testing.T) {
	tests := []struct {
		name         string
		participants []map[string]interface{}
		wantApproved bool
		wantCount    int
	}{
		{name: "no participants"},
		{name: "reviewer not voted", participants: []map[string]interface{}{bbParticipant("REVIEWER", false, "")}},
		{name: "reviewer approved", participants: []map[string]interface{}{bbParticipant("REVIEWER", true, "approved")},
			wantApproved: true, wantCount: 1},
		{name: "participant who is not a reviewer approved",
			participants: []map[string]interface{}{bbParticipant("PARTICIPANT", true, "approved")},
			wantApproved: true, wantCount: 1},
		{name: "two approvals", participants: []map[string]interface{}{
			bbParticipant("REVIEWER", true, "approved"), bbParticipant("PARTICIPANT", true, "approved")},
			wantApproved: true, wantCount: 2},
		{name: "changes requested blocks", participants: []map[string]interface{}{
			bbParticipant("REVIEWER", true, "approved"), bbParticipant("REVIEWER", false, "changes_requested")},
			wantCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t, map[string]apiReply{
				"GET " + bbPR7Path: {http.StatusOK, mustJSON(t, bbPullRequest(7, "OPEN", bbBranch, "", tt.participants...))},
			})
			approved, count, err := newBitbucket(t, api.URL, "").GetPRReviewStatus(context.Background(), "acme/web-app", 7)
			require.NoError(t, err)
			assert.Equal(t, tt.wantApproved, approved, "approved")
			assert.Equal(t, tt.wantCount, count, "approval count")

			calls := api.Calls()
			require.Len(t, calls, 1)
			assert.Equal(t, http.MethodGet, calls[0].Method)
			assert.Equal(t, bbPR7Path, calls[0].Path)
			assertBitbucketAuth(t, calls[0])
		})
	}

	t.Run("PR not found", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"GET " + bbPR7Path: {http.StatusNotFound, bbError("Pull request 7 not found")},
		})
		_, _, err := newBitbucket(t, api.URL, "").GetPRReviewStatus(context.Background(), "acme/web-app", 7)
		require.ErrorContains(t, err, "get Bitbucket PR reviewers acme/web-app#7")
	})
}

// TestBitbucketCloudContract_ClosePR checks that ClosePR declines the PR with
// POST .../pullrequests/{id}/decline, which takes no body, and reports a
// refused decline. Covers SCM-BB-05.
func TestBitbucketCloudContract_ClosePR(t *testing.T) {
	t.Run("declined", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"POST " + bbPR7Path + "/decline": {http.StatusOK, mustJSON(t, bbPullRequest(7, "DECLINED", bbBranch, ""))},
		})
		require.NoError(t, newBitbucket(t, api.URL, "").ClosePR(context.Background(), "acme/web-app", 7))

		calls := api.Calls()
		require.Len(t, calls, 1)
		assert.Equal(t, http.MethodPost, calls[0].Method)
		assert.Equal(t, bbPR7Path+"/decline", calls[0].Path)
		assert.Empty(t, calls[0].Body)
		assert.Empty(t, calls[0].Header.Get("Content-Type"))
		assertBitbucketAuth(t, calls[0])
	})

	t.Run("refused", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"POST " + bbPR7Path + "/decline": {http.StatusBadRequest, bbError("You can't decline a merged pull request.")},
		})
		err := newBitbucket(t, api.URL, "").ClosePR(context.Background(), "acme/web-app", 7)
		require.ErrorContains(t, err, "close Bitbucket PR acme/web-app#7")
		assert.ErrorContains(t, err, "status 400")
	})
}

// TestBitbucketCloudContract_OpenPRReusesExisting checks that when Bitbucket
// refuses a PR because one is already open from the branch into the same
// destination, OpenPR returns that PR, found with GET
// .../pullrequests?q=source.branch.name="<branch>" AND
// destination.branch.name="<base>" AND state="OPEN". A PR from the branch into
// another destination is not reused, and any other refusal is returned as an
// error without a lookup. Covers SCM-BB-05.
func TestBitbucketCloudContract_OpenPRReusesExisting(t *testing.T) {
	duplicate := apiReply{http.StatusBadRequest, bbError("There are already open pull requests for this branch.")}
	into := func(pr map[string]interface{}, dest string) map[string]interface{} {
		pr["destination"].(map[string]interface{})["branch"] = map[string]string{"name": dest}
		return pr
	}
	page := func(prs ...map[string]interface{}) apiReply {
		return apiReply{http.StatusOK, mustJSON(t, map[string]interface{}{
			"pagelen": 50, "size": len(prs), "page": 1, "values": prs,
		})}
	}

	tests := []struct {
		name      string
		create    apiReply
		list      apiReply
		wantURL   string
		wantN     int
		wantErr   string
		wantCalls int
	}{
		{
			name:   "open PR for the branch is reused",
			create: duplicate,
			// A PR from a similarly named branch, or from the branch into
			// another destination, is never taken.
			list: page(bbPullRequest(5, "OPEN", bbBranch+"-canary", ""),
				into(bbPullRequest(4, "OPEN", bbBranch, ""), "release"), bbPullRequest(6, "OPEN", bbBranch, "")),
			wantURL: "https://bitbucket.org/acme/web-app/pull-requests/6", wantN: 6, wantCalls: 2,
		},
		{
			name:    "no open PR for the branch",
			create:  duplicate,
			list:    page(),
			wantErr: "bitbucket PR exists for branch kardinal/web-app-v2/prod into main but could not find it in open PRs", wantCalls: 2,
		},
		{
			name:    "open PR from the branch into another destination is not reused",
			create:  duplicate,
			list:    page(into(bbPullRequest(4, "OPEN", bbBranch, ""), "release")),
			wantErr: "bitbucket PR exists for branch kardinal/web-app-v2/prod into main but could not find it in open PRs", wantCalls: 2,
		},
		{
			name:    "other refusal is not a duplicate",
			create:  apiReply{http.StatusBadRequest, bbError("source: Branch not found: kardinal/web-app-v2/prod")},
			wantErr: "status 400", wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t, map[string]apiReply{"POST " + bbPRPath: tt.create, "GET " + bbPRPath: tt.list})
			prURL, n, err := newBitbucket(t, api.URL, "").OpenPR(context.Background(), "acme/web-app", "t", "b", bbBranch, "main")
			calls := api.Calls()
			require.Len(t, calls, tt.wantCalls)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantURL, prURL)
				assert.Equal(t, tt.wantN, n)
			}
			if tt.wantCalls < 2 {
				return
			}
			list := calls[1]
			assert.Equal(t, http.MethodGet, list.Method)
			assert.Equal(t, bbPRPath, list.Path)
			assert.Equal(t, url.Values{
				"pagelen": {"50"},
				"q":       {`source.branch.name="kardinal/web-app-v2/prod" AND destination.branch.name="main" AND state="OPEN"`},
			}, list.Query)
			assertBitbucketAuth(t, list)
		})
	}
}
