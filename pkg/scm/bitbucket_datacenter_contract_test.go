// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// Bitbucket Data Center needs a commercial license (no free tier, and the
// evaluation licenses need an Atlassian account), so the e2e suite cannot
// boot it. Its behaviour is proven against a fake REST API 1.0 that answers
// with the request and response shapes of Bitbucket Data Center 8.x.

const (
	dcToken  = "bbdc-http-access-token"
	dcPRs    = "/bitbucket/rest/api/1.0/projects/PLAT/repos/web-app/pull-requests"
	dcPR12   = dcPRs + "/12"
	dcPRHTML = "https://git.example.com/bitbucket/projects/PLAT/repos/web-app/pull-requests/12"
	dcBranch = "kardinal/web-app-v2/prod"
	dcMerge  = "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567"
)

// dcPRJSON is a pull request as Bitbucket Data Center returns it.
func dcPRJSON(t *testing.T, state string, version int, reviewers ...map[string]interface{}) string {
	t.Helper()
	ref := func(id string) map[string]interface{} {
		return map[string]interface{}{"id": id, "displayId": id[len("refs/heads/"):],
			"repository": map[string]interface{}{"slug": "web-app", "project": map[string]interface{}{"key": "PLAT"}}}
	}
	pr := map[string]interface{}{
		"id": 12, "version": version, "title": "[kardinal] Promote web-app-v2 to prod", "state": state, "open": state == "OPEN",
		"fromRef": ref("refs/heads/" + dcBranch), "toRef": ref("refs/heads/main"),
		"reviewers": reviewers,
		"links":     map[string]interface{}{"self": []map[string]string{{"href": dcPRHTML}}},
	}
	if state == "MERGED" {
		pr["properties"] = map[string]interface{}{"mergeCommit": map[string]string{"displayId": dcMerge[:11], "id": dcMerge}}
	}
	return mustJSON(t, pr)
}

func dcProvider(t *testing.T, f *fakeAPI, secret string) *scm.BitbucketDCProvider {
	t.Helper()
	p, err := scm.NewProvider("bitbucket-datacenter", dcToken, f.URL+"/bitbucket", secret)
	require.NoError(t, err)
	return p.(*scm.BitbucketDCProvider)
}

// TestBitbucketDCContract_OpenPR: a PR is opened on the project and slug of
// any URL form RepoFromURL gives (HTTP clone with a context path, browse,
// ssh), with the HTTP access token as a Bearer and the XSRF header; a PR
// that exists already between the branches (409) is found on a later page
// of the open outgoing PRs. Covers SCM-BBDC-01.
func TestBitbucketDCContract_OpenPR(t *testing.T) {
	for _, gitURL := range []string{
		"https://git.example.com/bitbucket/scm/PLAT/web-app.git",
		"https://git.example.com/bitbucket/projects/PLAT/repos/web-app/browse",
		"ssh://git@git.example.com:7999/PLAT/web-app.git",
	} {
		t.Run(gitURL, func(t *testing.T) {
			f := newFakeAPI(t, map[string]apiReply{"POST " + dcPRs: {201, dcPRJSON(t, "OPEN", 0)}})
			repo, err := scm.RepoFromURL(gitURL)
			require.NoError(t, err)
			url, num, err := dcProvider(t, f, "").OpenPR(context.Background(), repo, "title", "body", dcBranch, "main")
			require.NoError(t, err)
			assert.Equal(t, dcPRHTML, url)
			assert.Equal(t, 12, num)
			c := f.Calls()[0]
			assert.Equal(t, "Bearer "+dcToken, c.Header.Get("Authorization"))
			assert.Equal(t, "no-check", c.Header.Get("X-Atlassian-Token"))
			var body map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(c.Body), &body))
			assert.Equal(t, "title", body["title"])
			assert.Equal(t, "body", body["description"])
			repoRef := map[string]interface{}{"slug": "web-app", "project": map[string]interface{}{"key": "PLAT"}}
			assert.Equal(t, map[string]interface{}{"id": "refs/heads/" + dcBranch, "repository": repoRef}, body["fromRef"])
			assert.Equal(t, map[string]interface{}{"id": "refs/heads/main", "repository": repoRef}, body["toRef"])
		})
	}

	t.Run("existing PR", func(t *testing.T) {
		other := mustJSON(t, map[string]interface{}{"id": 3, "fromRef": map[string]string{"id": "refs/heads/" + dcBranch},
			"toRef": map[string]string{"id": "refs/heads/release"}})
		f := newFakeAPI(t, map[string]apiReply{
			"POST " + dcPRs: {409, `{"errors":[{"exceptionName":"com.atlassian.bitbucket.pull.DuplicatePullRequestException","message":"Only one pull request may be open for a given source and target branch"}]}`},
		})
		f.queue("GET "+dcPRs,
			apiReply{200, `{"values":[` + other + `],"isLastPage":false,"nextPageStart":1}`},
			apiReply{200, `{"values":[` + dcPRJSON(t, "OPEN", 2) + `],"isLastPage":true}`})
		url, num, err := dcProvider(t, f, "").OpenPR(context.Background(), "PLAT/web-app", "t", "b", dcBranch, "main")
		require.NoError(t, err)
		assert.Equal(t, 12, num)
		assert.Equal(t, dcPRHTML, url)
		calls := f.Calls()
		require.Len(t, calls, 3)
		assert.Equal(t, "OPEN", calls[1].Query.Get("state"))
		assert.Equal(t, "OUTGOING", calls[1].Query.Get("direction"))
		assert.Equal(t, "refs/heads/"+dcBranch, calls[1].Query.Get("at"))
		assert.Equal(t, "1", calls[2].Query.Get("start"))
	})

	t.Run("no server URL", func(t *testing.T) {
		_, err := scm.NewProvider("bitbucket-datacenter", dcToken, "", "")
		assert.ErrorContains(t, err, "needs --scm-api-url")
	})
}

// TestBitbucketDCContract_PRState reads the PR state (OPEN, MERGED,
// DECLINED), the merge commit from properties.mergeCommit, and the reviews:
// approved with an APPROVED reviewer and none NEEDS_WORK.
// Covers SCM-BBDC-02.
func TestBitbucketDCContract_PRState(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		state        string
		merged, open bool
	}{{"OPEN", false, true}, {"MERGED", true, false}, {"DECLINED", false, false}} {
		f := newFakeAPI(t, map[string]apiReply{"GET " + dcPR12: {200, dcPRJSON(t, tc.state, 1)}})
		merged, open, err := dcProvider(t, f, "").GetPRStatus(ctx, "scm/PLAT/web-app", 12)
		require.NoError(t, err)
		assert.Equal(t, tc.merged, merged, tc.state)
		assert.Equal(t, tc.open, open, tc.state)
	}
	f := newFakeAPI(t, map[string]apiReply{"GET " + dcPR12: {200, dcPRJSON(t, "MERGED", 1)}})
	sha, err := dcProvider(t, f, "").GetPRMergeCommit(ctx, "PLAT/web-app", 12)
	require.NoError(t, err)
	assert.Equal(t, dcMerge, sha)

	reviewer := func(name, status string) map[string]interface{} {
		return map[string]interface{}{"user": map[string]string{"name": name}, "role": "REVIEWER", "approved": status == "APPROVED", "status": status}
	}
	for _, tc := range []struct {
		name      string
		reviewers []map[string]interface{}
		approved  bool
		count     int
	}{
		{"none", nil, false, 0},
		{"two approvals", []map[string]interface{}{reviewer("a", "APPROVED"), reviewer("b", "APPROVED"), reviewer("c", "UNAPPROVED")}, true, 2},
		{"needs work blocks", []map[string]interface{}{reviewer("a", "APPROVED"), reviewer("b", "NEEDS_WORK")}, false, 1},
	} {
		f := newFakeAPI(t, map[string]apiReply{"GET " + dcPR12: {200, dcPRJSON(t, "OPEN", 1, tc.reviewers...)}})
		approved, count, err := dcProvider(t, f, "").GetPRReviewStatus(ctx, "PLAT/web-app", 12)
		require.NoError(t, err)
		assert.Equal(t, tc.approved, approved, tc.name)
		assert.Equal(t, tc.count, count, tc.name)
	}
}

// TestBitbucketDCContract_CloseCommentDelete: ClosePR declines with the PR's
// current version, reading it again when another change made it out of date;
// CommentOnPR posts the text; DeleteBranch uses the branch-utils API and a
// branch that is gone already is not an error; labels are a no-op.
// Covers SCM-BBDC-03.
func TestBitbucketDCContract_CloseCommentDelete(t *testing.T) {
	ctx := context.Background()
	f := newFakeAPI(t, map[string]apiReply{
		"POST " + dcPR12 + "/decline":  {200, dcPRJSON(t, "DECLINED", 4)},
		"POST " + dcPR12 + "/comments": {201, `{"id":5,"text":"x"}`},
	})
	f.queue("GET "+dcPR12, apiReply{200, dcPRJSON(t, "OPEN", 2)}, apiReply{200, dcPRJSON(t, "OPEN", 3)})
	f.queue("POST "+dcPR12+"/decline", apiReply{409, `{"errors":[{"exceptionName":"com.atlassian.bitbucket.pull.PullRequestOutOfDateException","currentVersion":3}]}`})
	p := dcProvider(t, f, "")
	require.NoError(t, p.ClosePR(ctx, "PLAT/web-app", 12))
	require.NoError(t, p.CommentOnPR(ctx, "PLAT/web-app", 12, "closed by kardinal"))
	require.NoError(t, p.AddLabelsToPR(ctx, "PLAT/web-app", 12, []string{"kardinal"}))
	var declines []string
	for _, c := range f.Calls() {
		if c.Path == dcPR12+"/decline" {
			declines = append(declines, c.Query.Get("version"))
		}
	}
	assert.Equal(t, []string{"2", "3"}, declines, "the out-of-date decline is retried with the new version")
	assert.JSONEq(t, `{"text":"closed by kardinal"}`, f.Calls()[len(f.Calls())-1].Body)

	branches := "/bitbucket/rest/branch-utils/1.0/projects/PLAT/repos/web-app/branches"
	f = newFakeAPI(t, map[string]apiReply{"DELETE " + branches: {204, ``}})
	require.NoError(t, dcProvider(t, f, "").DeleteBranch(ctx, "PLAT/web-app", dcBranch))
	assert.JSONEq(t, `{"name":"refs/heads/`+dcBranch+`","dryRun":false}`, f.Calls()[0].Body)
	f = newFakeAPI(t, map[string]apiReply{"DELETE " + branches: {400, `{"errors":[{"exceptionName":"com.atlassian.bitbucket.repository.NoSuchBranchException"}]}`}})
	assert.NoError(t, dcProvider(t, f, "").DeleteBranch(ctx, "PLAT/web-app", dcBranch), "already deleted")
	f = newFakeAPI(t, map[string]apiReply{"DELETE " + branches: {403, `{"errors":[{"message":"no permission"}]}`}})
	assert.Error(t, dcProvider(t, f, "").DeleteBranch(ctx, "PLAT/web-app", dcBranch))
}

func dcSign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestBitbucketDCContract_Webhook: a pr:merged event with a valid
// X-Hub-Signature is a merged PR event with the merge commit and the
// target repository; the repository matches the Pipeline's in any URL form;
// a declined PR is not merged; a wrong signature, no secret and the
// diagnostics:ping test event are handled. Covers SCM-BBDC-04.
func TestBitbucketDCContract_Webhook(t *testing.T) {
	const secret = "dc-webhook-secret"
	p := dcProvider(t, newFakeAPI(t, nil), secret)
	merged := []byte(`{"eventKey":"pr:merged","date":"2026-10-09T10:00:00+0000","actor":{"name":"alice"},
"pullRequest":{"id":12,"version":3,"state":"MERGED","fromRef":{"id":"refs/heads/` + dcBranch + `","repository":{"slug":"web-app","project":{"key":"PLAT"}}},
"toRef":{"id":"refs/heads/main","repository":{"slug":"web-app","project":{"key":"PLAT"}}},
"properties":{"mergeCommit":{"displayId":"0a1b2c3d4e5","id":"` + dcMerge + `"}}}}`)
	h := http.Header{}
	h.Set("X-Event-Key", "pr:merged")
	h.Set("X-Hub-Signature", dcSign(secret, merged))
	ev, err := scm.ParseWebhookRequest(p, merged, h)
	require.NoError(t, err)
	assert.Equal(t, scm.WebhookEvent{EventType: "pull_request", Action: "closed", Merged: true, RepoFullName: "PLAT/web-app", PRNumber: 12, MergeCommitSHA: dcMerge}, ev)
	for _, pipelineURL := range []string{"https://git.example.com/bitbucket/scm/PLAT/web-app.git", "ssh://git@git.example.com:7999/plat/web-app.git"} {
		repo, err := scm.RepoFromURL(pipelineURL)
		require.NoError(t, err)
		assert.True(t, scm.SameRepo(p, repo, ev.RepoFullName), pipelineURL)
	}
	assert.False(t, scm.SameRepo(p, "PLAT/other", ev.RepoFullName))

	declined := []byte(`{"eventKey":"pr:declined","pullRequest":{"id":12,"state":"DECLINED","toRef":{"repository":{"slug":"web-app","project":{"key":"PLAT"}}}}}`)
	ev, err = p.ParseWebhookEvent(declined, dcSign(secret, declined))
	require.NoError(t, err)
	assert.False(t, ev.Merged)
	assert.Equal(t, "pr:declined", ev.EventType)

	ping := []byte(`{"test":true}`)
	h = http.Header{}
	h.Set("X-Event-Key", "diagnostics:ping")
	h.Set("X-Hub-Signature", dcSign(secret, ping))
	ev, err = scm.ParseWebhookRequest(p, ping, h)
	require.NoError(t, err)
	assert.Equal(t, scm.WebhookEvent{EventType: "diagnostics:ping"}, ev)

	_, err = p.ParseWebhookEvent(merged, dcSign("wrong", merged))
	assert.ErrorContains(t, err, "invalid HMAC-SHA256 signature")
	_, err = dcProvider(t, newFakeAPI(t, nil), "").ParseWebhookEvent(merged, dcSign(secret, merged))
	assert.ErrorIs(t, err, scm.ErrNoWebhookSecret)
}

// TestBitbucketDCContract_PRControls: reviewers are added as REVIEWER
// participants; while a merge check vetoes the merge, auto-merge turns on
// Bitbucket's auto-merge with the strategy and message, and says the server
// is too old when it has none (404); a PR whose checks pass (nothing
// pending) is merged only with allowImmediate; DisableAutoMerge deletes it;
// team reviewers, assignees and labels are not supported.
// Covers SCM-BBDC-05.
func TestBitbucketDCContract_PRControls(t *testing.T) {
	ctx := context.Background()
	f := newFakeAPI(t, map[string]apiReply{"POST " + dcPR12 + "/participants": {200, `{}`}})
	p := dcProvider(t, f, "")
	require.NoError(t, p.RequestReviewers(ctx, "PLAT/web-app", 12, []string{"alice", "bob"}, nil))
	require.Len(t, f.Calls(), 2)
	assert.JSONEq(t, `{"user":{"name":"alice"},"role":"REVIEWER"}`, f.Calls()[0].Body)
	assert.ErrorIs(t, p.RequestReviewers(ctx, "PLAT/web-app", 12, nil, []string{"team"}), scm.ErrPRControlUnsupported)
	assert.ErrorIs(t, p.AddAssignees(ctx, "PLAT/web-app", 12, []string{"a"}), scm.ErrPRControlUnsupported)
	sup := scm.SupportOf(p)
	assert.False(t, sup.Labels || sup.TeamReviewers || sup.Assignees)

	opts := scm.MergeOptions{Method: "squash", CommitTitle: "deploy 2.0", CommitBody: "Bundle web-app-v2"}
	auto := "/bitbucket/rest/api/latest/projects/PLAT/repos/web-app/pull-requests/12/auto-merge"
	vetoed := apiReply{200, `{"canMerge":false,"conflicted":false,"outcome":"CLEAN","vetoes":[{"summaryMessage":"Requires 1 approval"}]}`}
	clean := apiReply{200, `{"canMerge":true,"conflicted":false,"outcome":"CLEAN","vetoes":[]}`}
	t.Run("vetoed: auto-merge", func(t *testing.T) {
		f := newFakeAPI(t, map[string]apiReply{"GET " + dcPR12 + "/merge": vetoed, "POST " + auto: {200, `{"autoMergeEnabled":true}`}})
		require.NoError(t, dcProvider(t, f, "").EnableAutoMerge(ctx, "PLAT/web-app", 12, scm.MergeOptions{Method: "rebase", CommitTitle: "deploy"}))
		calls := f.Calls()
		assert.Equal(t, auto, calls[len(calls)-1].Path)
		assert.JSONEq(t, `{"strategyId":"rebase-no-ff","commitMessage":"deploy"}`, calls[len(calls)-1].Body)
	})
	t.Run("nothing pending", func(t *testing.T) {
		f := newFakeAPI(t, map[string]apiReply{"GET " + dcPR12 + "/merge": clean})
		err := dcProvider(t, f, "").EnableAutoMerge(ctx, "PLAT/web-app", 12, opts)
		assert.ErrorIs(t, err, scm.ErrNothingPending)
		assert.Len(t, f.Calls(), 1, "no merge without allowImmediate")
	})
	t.Run("nothing pending, allowImmediate", func(t *testing.T) {
		f := newFakeAPI(t, map[string]apiReply{"GET " + dcPR12 + "/merge": clean, "GET " + dcPR12: {200, dcPRJSON(t, "OPEN", 5)},
			"POST " + dcPR12 + "/merge": {200, dcPRJSON(t, "MERGED", 6)}})
		o := opts
		o.AllowImmediate = true
		require.NoError(t, dcProvider(t, f, "").EnableAutoMerge(ctx, "PLAT/web-app", 12, o))
		c := f.Calls()[2]
		assert.Equal(t, "5", c.Query.Get("version"))
		assert.JSONEq(t, `{"strategyId":"squash","message":"deploy 2.0\n\nBundle web-app-v2"}`, c.Body)
	})
	t.Run("a server without auto-merge", func(t *testing.T) {
		f := newFakeAPI(t, map[string]apiReply{"GET " + dcPR12 + "/merge": vetoed, "POST " + auto: {404, `{}`}})
		err := dcProvider(t, f, "").EnableAutoMerge(ctx, "PLAT/web-app", 12, scm.MergeOptions{Method: "merge"})
		assert.ErrorContains(t, err, "this server has no auto-merge (Bitbucket Data Center 8.15 or later)")
	})
	t.Run("disable", func(t *testing.T) {
		f := newFakeAPI(t, map[string]apiReply{"DELETE " + auto: {404, `{}`}})
		require.NoError(t, dcProvider(t, f, "").DisableAutoMerge(ctx, "PLAT/web-app", 12))
	})
}
