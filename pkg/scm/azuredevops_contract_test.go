// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const (
	adoPAT           = "ado-pat"
	adoPRsPath       = "/contoso/Web/_apis/git/repositories/web-app/pullrequests"
	adoPR12Path      = adoPRsPath + "/12"
	adoReviewersPath = adoPR12Path + "/reviewers"
	adoLabelsPath    = "/contoso/Web/_apis/git/repositories/web-app/pullRequests/12/labels"
	adoWebURL        = "https://dev.azure.com/contoso/Web/_git/web-app"
	adoPR12WebURL    = adoWebURL + "/pullrequest/12"
	adoBranch        = "kardinal/web-app-v2/prod"
	adoMergeSHA      = "6a1f0c9e8d7b6a5f4e3d2c1b0a9f8e7d6c5b4a39"
)

// adoRepository is a GitRepository reference as it appears in a
// GitPullRequest and in a service hook resource. webURL "" leaves out webUrl,
// which the PR create response does not always carry.
func adoRepository(webURL string) map[string]interface{} {
	r := map[string]interface{}{
		"id":   "3411ebc1-d5aa-464f-9615-0b527bc66719",
		"name": "web-app",
		"url":  "https://dev.azure.com/contoso/_apis/git/repositories/3411ebc1-d5aa-464f-9615-0b527bc66719",
		"project": map[string]interface{}{
			"id": "a7573007-bbb3-4341-b726-0c4148a07853", "name": "Web", "state": "wellFormed", "visibility": "private",
		},
		"remoteUrl": "https://contoso@dev.azure.com/contoso/Web/_git/web-app",
	}
	if webURL != "" {
		r["webUrl"] = webURL
	}
	return r
}

// adoPullRequest is an Azure DevOps REST 7.1 GitPullRequest, as
// POST/GET/PATCH .../pullrequests return it. status is active, completed or
// abandoned. lastMergeCommit is the test merge of an active PR and the merge
// commit of a completed one.
func adoPullRequest(id int, status, source, webURL string) map[string]interface{} {
	commit := func(sha string) map[string]string {
		return map[string]string{
			"commitId": sha,
			"url":      "https://dev.azure.com/contoso/_apis/git/repositories/3411ebc1-d5aa-464f-9615-0b527bc66719/commits/" + sha,
		}
	}
	return map[string]interface{}{
		"repository":            adoRepository(webURL),
		"pullRequestId":         id,
		"codeReviewId":          id,
		"status":                status,
		"createdBy":             map[string]string{"displayName": "kardinal-bot", "uniqueName": "kardinal-bot@contoso.com", "id": "d6245f20-2af8-44f4-9451-8107cb2767db"},
		"creationDate":          "2026-09-30T10:00:00.000Z",
		"title":                 "[kardinal] Promote web-app-v2 to prod",
		"description":           "Promotion evidence",
		"sourceRefName":         "refs/heads/" + source,
		"targetRefName":         "refs/heads/main",
		"mergeStatus":           "succeeded",
		"isDraft":               false,
		"mergeId":               "f5fc8381-3fb2-49fe-8a0d-27dcc2d6ef82",
		"lastMergeSourceCommit": commit("1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"),
		"lastMergeTargetCommit": commit("0f9e8d7c6b5a49382716f5e4d3c2b1a09f8e7d6c"),
		"lastMergeCommit":       commit(adoMergeSHA),
		"reviewers":             []interface{}{},
		"url":                   fmt.Sprintf("https://dev.azure.com/contoso/_apis/git/repositories/3411ebc1-d5aa-464f-9615-0b527bc66719/pullRequests/%d", id),
		"supportsIterations":    true,
	}
}

// adoError is the Azure DevOps error body.
func adoError(typeKey, message string) string {
	return fmt.Sprintf(`{"$id":"1","innerException":null,"message":%q,"typeName":"Microsoft.TeamFoundation.Git.Server.%s, Microsoft.TeamFoundation.Git.Server","typeKey":%q,"errorCode":0,"eventId":3000}`,
		message, typeKey, typeKey)
}

// assertADORequest checks that a request carries the PAT as Basic auth with
// an empty user name, asks for JSON and pins api-version 7.1.
func assertADORequest(t *testing.T, c apiCall) {
	t.Helper()
	req := &http.Request{Header: c.Header}
	user, pass, ok := req.BasicAuth()
	assert.True(t, ok, "%s %s: Basic auth", c.Method, c.Path)
	assert.Equal(t, "", user, "%s %s: user name", c.Method, c.Path)
	assert.Equal(t, adoPAT, pass, "%s %s: PAT", c.Method, c.Path)
	assert.Equal(t, "application/json", c.Header.Get("Accept"), "%s %s", c.Method, c.Path)
	assert.Equal(t, "7.1", c.Query.Get("api-version"), "%s %s", c.Method, c.Path)
}

// newAzureDevOps builds the provider through the factory, with the PAT as
// read from a Secret (trailing newline).
func newAzureDevOps(t *testing.T, apiURL, secret string) scm.SCMProvider {
	t.Helper()
	p, err := scm.NewProvider("azuredevops", adoPAT+"\n", apiURL, secret)
	require.NoError(t, err)
	return p
}

// TestAzureDevOpsContract_OpenPR checks the request OpenPR sends to the Azure
// DevOps REST 7.1 API: POST {org}/{project}/_apis/git/repositories/{repo}/pullrequests
// on the org/project/repo taken from a dev.azure.com spec.git.url, with the
// PAT as Basic auth and refs/heads/ branch refs, and the PR web URL built from
// the created GitPullRequest. A rejected PAT is a permanent error.
// Covers SCM-ADO-01.
func TestAzureDevOpsContract_OpenPR(t *testing.T) {
	for _, gitURL := range []string{
		"https://dev.azure.com/contoso/Web/_git/web-app",
		"https://contoso@dev.azure.com/contoso/Web/_git/web-app",
		"git@ssh.dev.azure.com:v3/contoso/Web/web-app",
	} {
		repo, err := scm.RepoFromURL(gitURL)
		require.NoError(t, err)
		assert.Equal(t, "contoso/Web/web-app", repo, "org/project/repo of %s", gitURL)
	}

	for _, tt := range []struct {
		name, webURL string
		wantURL      func(apiURL string) string
	}{
		{name: "created, repository.webUrl in the response", webURL: adoWebURL,
			wantURL: func(string) string { return adoPR12WebURL }},
		{name: "created, no repository.webUrl", wantURL: func(apiURL string) string {
			return apiURL + "/contoso/Web/_git/web-app/pullrequest/12"
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t, map[string]apiReply{
				"POST " + adoPRsPath: {http.StatusCreated, mustJSON(t, adoPullRequest(12, "active", adoBranch, tt.webURL))},
			})
			prURL, n, err := newAzureDevOps(t, api.URL, "").OpenPR(context.Background(), "contoso/Web/web-app",
				"[kardinal] Promote web-app-v2 to prod", "Promotion evidence", adoBranch, "main")
			require.NoError(t, err)
			assert.Equal(t, tt.wantURL(api.URL), prURL)
			assert.Equal(t, 12, n)

			calls := api.Calls()
			require.Len(t, calls, 1)
			c := calls[0]
			assert.Equal(t, http.MethodPost, c.Method)
			assert.Equal(t, adoPRsPath, c.Path)
			assert.Equal(t, url.Values{"api-version": {"7.1"}}, c.Query)
			assertADORequest(t, c)
			assert.Equal(t, "application/json", c.Header.Get("Content-Type"))
			assert.JSONEq(t, `{
				"title": "[kardinal] Promote web-app-v2 to prod",
				"description": "Promotion evidence",
				"sourceRefName": "refs/heads/kardinal/web-app-v2/prod",
				"targetRefName": "refs/heads/main"
			}`, c.Body)

			// The PRStatus is keyed by the repository and number parsed back
			// from this URL; they must match what webhooks and polling use.
			gotRepo, gotN, err := scm.ParsePRURL(prURL)
			require.NoError(t, err)
			assert.Equal(t, "contoso/Web/web-app", gotRepo)
			assert.Equal(t, 12, gotN)
		})
	}

	t.Run("PAT rejected", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"POST " + adoPRsPath: {http.StatusUnauthorized, `{"$id":"1","innerException":null,"message":"TF400813: The user '' is not authorized to access this resource.","typeName":"Microsoft.TeamFoundation.Framework.Server.UnauthorizedRequestException, Microsoft.TeamFoundation.Framework.Server","typeKey":"UnauthorizedRequestException","errorCode":0,"eventId":3000}`},
		})
		_, _, err := newAzureDevOps(t, api.URL, "").OpenPR(context.Background(), "contoso/Web/web-app", "t", "b", adoBranch, "main")
		require.Error(t, err)
		assert.True(t, scm.IsPermanentError(err), "a rejected PAT is not retried: %v", err)
		var apiErr *scm.APIError
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, "azuredevops", apiErr.Provider)
		assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
		assert.Contains(t, err.Error(), "open Azure DevOps PR contoso/Web/web-app")
		assert.Len(t, api.Calls(), 1, "a rejected PAT is not taken for an existing PR")
	})

	t.Run("repository is not org/project/repo", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{})
		_, _, err := newAzureDevOps(t, api.URL, "").OpenPR(context.Background(), "Web/web-app", "t", "b", adoBranch, "main")
		require.ErrorContains(t, err, "org/project/repo")
		assert.Empty(t, api.Calls())
	})
}

// TestAzureDevOpsContract_AddLabelsToPR checks that labels become PR tags:
// one POST .../pullRequests/{id}/labels with {"name": label} per label, in
// order. A refused label stops the rest and names the label.
// Covers SCM-ADO-02.
func TestAzureDevOpsContract_AddLabelsToPR(t *testing.T) {
	tag := func(name string) apiReply {
		return apiReply{http.StatusOK, mustJSON(t, map[string]interface{}{
			"active": true, "id": "b4f5d1f0-8c1b-4f8e-9a7b-2f3c4d5e6f70", "name": name,
			"url": "https://dev.azure.com/contoso/_apis/git/repositories/3411ebc1-d5aa-464f-9615-0b527bc66719/pullRequests/12/labels/b4f5d1f0-8c1b-4f8e-9a7b-2f3c4d5e6f70",
		})}
	}
	labels := []string{"kardinal", "kardinal/promotion", "kardinal/rollback"}

	t.Run("applied", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{"POST " + adoLabelsPath: tag("kardinal")})
		require.NoError(t, newAzureDevOps(t, api.URL, "").AddLabelsToPR(context.Background(), "contoso/Web/web-app", 12, labels))

		calls := api.Calls()
		require.Len(t, calls, len(labels))
		for i, c := range calls {
			assert.Equal(t, http.MethodPost, c.Method)
			assert.Equal(t, adoLabelsPath, c.Path)
			assertADORequest(t, c)
			assert.Equal(t, "application/json", c.Header.Get("Content-Type"))
			assert.JSONEq(t, mustJSON(t, map[string]string{"name": labels[i]}), c.Body)
		}
	})

	t.Run("no labels sends nothing", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{})
		require.NoError(t, newAzureDevOps(t, api.URL, "").AddLabelsToPR(context.Background(), "contoso/Web/web-app", 12, nil))
		assert.Empty(t, api.Calls())
	})

	t.Run("refused", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{"POST " + adoLabelsPath: tag("kardinal")})
		api.queue("POST "+adoLabelsPath, tag("kardinal"), apiReply{http.StatusForbidden,
			adoError("GitNeedsPermissionException", "TF401027: You need the Git 'PullRequestContribute' permission to perform this action.")})
		err := newAzureDevOps(t, api.URL, "").AddLabelsToPR(context.Background(), "contoso/Web/web-app", 12, labels)
		require.ErrorContains(t, err, `add label "kardinal/promotion" to ADO PR contoso/Web/web-app#12`)
		assert.ErrorContains(t, err, "status 403")
		assert.Len(t, api.Calls(), 2, "labels after a refused one are not sent")
	})
}

// TestAzureDevOpsContract_PRStatusAndMergeCommit checks what polling reads
// from GET .../pullrequests/{id}: status completed is merged, active is open,
// abandoned is closed, and the merge commit is lastMergeCommit.commitId.
// Covers SCM-ADO-03.
func TestAzureDevOpsContract_PRStatusAndMergeCommit(t *testing.T) {
	for _, tt := range []struct {
		status               string
		wantMerged, wantOpen bool
	}{
		{status: "active", wantOpen: true},
		{status: "completed", wantMerged: true},
		{status: "abandoned"},
	} {
		t.Run(tt.status, func(t *testing.T) {
			api := newFakeAPI(t, map[string]apiReply{
				"GET " + adoPR12Path: {http.StatusOK, mustJSON(t, adoPullRequest(12, tt.status, adoBranch, adoWebURL))},
			})
			p := newAzureDevOps(t, api.URL, "")

			merged, open, err := p.GetPRStatus(context.Background(), "contoso/Web/web-app", 12)
			require.NoError(t, err)
			assert.Equal(t, tt.wantMerged, merged, "merged")
			assert.Equal(t, tt.wantOpen, open, "open")

			// The PRStatus reconciler asks for the merge commit through this
			// interface, and only once the PR is merged.
			getter, ok := p.(scm.MergeCommitGetter)
			require.True(t, ok, "the Azure DevOps provider reports merge commits")
			sha, err := getter.GetPRMergeCommit(context.Background(), "contoso/Web/web-app", 12)
			require.NoError(t, err)
			assert.Equal(t, adoMergeSHA, sha)

			calls := api.Calls()
			require.Len(t, calls, 2)
			for _, c := range calls {
				assert.Equal(t, http.MethodGet, c.Method)
				assert.Equal(t, adoPR12Path, c.Path)
				assert.Equal(t, url.Values{"api-version": {"7.1"}}, c.Query)
				assert.Empty(t, c.Body)
				assert.Empty(t, c.Header.Get("Content-Type"))
				assertADORequest(t, c)
			}
		})
	}

	t.Run("PR not found", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"GET " + adoPR12Path: {http.StatusNotFound, adoError("GitPullRequestNotFoundException", "TF401180: The requested pull request was not found.")},
		})
		p := newAzureDevOps(t, api.URL, "")
		_, _, err := p.GetPRStatus(context.Background(), "contoso/Web/web-app", 12)
		require.ErrorContains(t, err, "get ADO PR status contoso/Web/web-app#12")
		assert.True(t, scm.IsPermanentError(err), "%v", err)
		_, err = p.(scm.MergeCommitGetter).GetPRMergeCommit(context.Background(), "contoso/Web/web-app", 12)
		require.ErrorContains(t, err, "get ADO PR merge commit contoso/Web/web-app#12")
	})
}

// adoServiceHook is an Azure DevOps "Web Hooks" service hook delivery for a
// pull request event.
func adoServiceHook(t *testing.T, eventType, status string) []byte {
	t.Helper()
	pr := adoPullRequest(12, status, adoBranch, "")
	return []byte(mustJSON(t, map[string]interface{}{
		"subscriptionId":  "00ca946b-2fe9-4f2a-ae2f-40d5c48001bc",
		"notificationId":  3,
		"id":              "2ab4e3d3-b7a6-425e-92b1-5a9982c1269e",
		"eventType":       eventType,
		"publisherId":     "tfs",
		"message":         map[string]string{"text": "kardinal-bot updated pull request 12 ([kardinal] Promote web-app-v2 to prod)"},
		"resource":        pr,
		"resourceVersion": "1.0",
		"resourceContainers": map[string]interface{}{
			"collection": map[string]string{"id": "c12d0eb8-e382-443b-9f9c-c52cba5014c2"},
			"account":    map[string]string{"id": "f844ec47-a9db-4511-8281-8b63f4eaf94e"},
			"project":    map[string]string{"id": "a7573007-bbb3-4341-b726-0c4148a07853"},
		},
		"createdDate": "2026-09-30T10:10:00.000Z",
	}))
}

// TestAzureDevOpsContract_ParseWebhookEvent checks service hook parsing as
// the handler calls it, with the token read from the X-AzureDevOps-Token
// header: the token must equal the webhook secret, compared in constant time,
// and a PR whose resource.status is completed is a merged event for the
// org/project/repo of its remote URL. Other statuses, including
// git.pullrequest.merged for a PR that is still active, are not merged. The
// webhook test in cmd/kardinal-controller proves the handler answers 401 to a
// wrong token. Covers SCM-ADO-04.
func TestAzureDevOpsContract_ParseWebhookEvent(t *testing.T) {
	const secret = "ado-webhook-secret"
	completed := adoServiceHook(t, "git.pullrequest.updated", "completed")

	tests := []struct {
		name    string
		body    []byte
		token   string
		want    scm.WebhookEvent
		wantErr bool
	}{
		{name: "completed is merged", body: completed, token: secret,
			want: scm.WebhookEvent{EventType: "pull_request", Action: "closed", Merged: true, PRNumber: 12, RepoFullName: "contoso/Web/web-app"}},
		{name: "merge attempted on an active PR is not merged", body: adoServiceHook(t, "git.pullrequest.merged", "active"), token: secret,
			want: scm.WebhookEvent{EventType: "git.pullrequest.merged", Action: "active", PRNumber: 12, RepoFullName: "contoso/Web/web-app"}},
		{name: "abandoned is not merged", body: adoServiceHook(t, "git.pullrequest.updated", "abandoned"), token: secret,
			want: scm.WebhookEvent{EventType: "git.pullrequest.updated", Action: "abandoned", PRNumber: 12, RepoFullName: "contoso/Web/web-app"}},
		{name: "wrong token", body: completed, token: "ado-webhook-guess", wantErr: true},
		{name: "prefix of the token", body: completed, token: secret[:len(secret)-1], wantErr: true},
		{name: "token with a suffix", body: completed, token: secret + "x", wantErr: true},
		{name: "token in another case", body: completed, token: "ADO-WEBHOOK-SECRET", wantErr: true},
		{name: "no token", body: completed, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newAzureDevOps(t, "https://dev.azure.com", secret)
			h := http.Header{}
			h.Set("Content-Type", "application/json; charset=utf-8")
			if tt.token != "" {
				h.Set("X-AzureDevOps-Token", tt.token)
			}
			require.Equal(t, tt.token, scm.WebhookSignature(h), "the handler reads X-AzureDevOps-Token")

			ev, err := p.ParseWebhookEvent(tt.body, scm.WebhookSignature(h))
			if tt.wantErr {
				require.ErrorContains(t, err, "token mismatch")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, ev)
		})
	}

	// Constant time is not observable in a unit test, so check the code:
	// the token is only ever read as an argument of subtle.ConstantTimeCompare.
	t.Run("token compared in constant time", func(t *testing.T) {
		file, err := parser.ParseFile(token.NewFileSet(), "azuredevops.go", nil, 0)
		require.NoError(t, err)
		var fn *ast.FuncDecl
		for _, d := range file.Decls {
			if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "ParseWebhookEvent" && f.Recv != nil {
				fn = f
			}
		}
		require.NotNil(t, fn, "AzureDevOpsProvider.ParseWebhookEvent")
		param := fn.Type.Params.List[1].Names[0]
		require.Equal(t, "signature", param.Name)

		var compares []*ast.CallExpr
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ConstantTimeCompare" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "subtle" {
						compares = append(compares, call)
					}
				}
			}
			return true
		})
		require.Len(t, compares, 1, "one subtle.ConstantTimeCompare")
		withSecret := false
		ast.Inspect(compares[0], func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "WebhookSecret" {
				withSecret = true
			}
			return true
		})
		assert.True(t, withSecret, "the token is compared with the webhook secret")

		uses := 0
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || id.Name != param.Name {
				return true
			}
			uses++
			assert.True(t, id.Pos() >= compares[0].Pos() && id.End() <= compares[0].End(),
				"signature is used outside subtle.ConstantTimeCompare at offset %d", id.Pos())
			return true
		})
		assert.Positive(t, uses, "the token is checked")
	})
}

// TestAzureDevOpsContract_ReviewStatus checks how approvals are read from GET
// .../pullrequests/{id}/reviewers: votes 10 (approved) and 5 (approved with
// suggestions) count, 0 (no vote) does not, and -5 (waiting for author) or
// -10 (rejected) block the approval. Covers SCM-ADO-05.
func TestAzureDevOpsContract_ReviewStatus(t *testing.T) {
	reviewer := func(vote int) map[string]interface{} {
		return map[string]interface{}{
			"reviewerUrl": "https://dev.azure.com/contoso/_apis/git/repositories/3411ebc1-d5aa-464f-9615-0b527bc66719/pullRequests/12/reviewers/0e53bf3a-9f47-4fbf-9e3c-2a5f1b8d7c6e",
			"vote":        vote, "hasDeclined": false, "isRequired": false, "isFlagged": false,
			"displayName": "Ana Reviewer", "uniqueName": "ana@contoso.com", "id": "0e53bf3a-9f47-4fbf-9e3c-2a5f1b8d7c6e",
		}
	}
	tests := []struct {
		name         string
		votes        []int
		wantApproved bool
		wantCount    int
	}{
		{name: "no reviewers"},
		{name: "no vote", votes: []int{0}},
		{name: "approved", votes: []int{10}, wantApproved: true, wantCount: 1},
		{name: "approved with suggestions", votes: []int{5}, wantApproved: true, wantCount: 1},
		{name: "two approvals", votes: []int{10, 5, 0}, wantApproved: true, wantCount: 2},
		{name: "waiting for author blocks", votes: []int{10, -5}, wantCount: 1},
		{name: "rejected blocks", votes: []int{10, -10}, wantCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := []interface{}{}
			for _, v := range tt.votes {
				value = append(value, reviewer(v))
			}
			api := newFakeAPI(t, map[string]apiReply{
				"GET " + adoReviewersPath: {http.StatusOK, mustJSON(t, map[string]interface{}{"count": len(value), "value": value})},
			})
			approved, count, err := newAzureDevOps(t, api.URL, "").GetPRReviewStatus(context.Background(), "contoso/Web/web-app", 12)
			require.NoError(t, err)
			assert.Equal(t, tt.wantApproved, approved, "approved")
			assert.Equal(t, tt.wantCount, count, "approval count")

			calls := api.Calls()
			require.Len(t, calls, 1)
			assert.Equal(t, http.MethodGet, calls[0].Method)
			assert.Equal(t, adoReviewersPath, calls[0].Path)
			assertADORequest(t, calls[0])
		})
	}

	t.Run("PR not found", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"GET " + adoReviewersPath: {http.StatusNotFound, adoError("GitPullRequestNotFoundException", "TF401180: The requested pull request was not found.")},
		})
		_, _, err := newAzureDevOps(t, api.URL, "").GetPRReviewStatus(context.Background(), "contoso/Web/web-app", 12)
		require.ErrorContains(t, err, "get ADO PR reviewers contoso/Web/web-app#12")
	})
}

// TestAzureDevOpsContract_ClosePR checks that ClosePR abandons the PR with
// PATCH .../pullrequests/{id} {"status": "abandoned"} and reports a refusal.
// Covers SCM-ADO-05.
func TestAzureDevOpsContract_ClosePR(t *testing.T) {
	t.Run("abandoned", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"PATCH " + adoPR12Path: {http.StatusOK, mustJSON(t, adoPullRequest(12, "abandoned", adoBranch, ""))},
		})
		require.NoError(t, newAzureDevOps(t, api.URL, "").ClosePR(context.Background(), "contoso/Web/web-app", 12))

		calls := api.Calls()
		require.Len(t, calls, 1)
		assert.Equal(t, http.MethodPatch, calls[0].Method)
		assert.Equal(t, adoPR12Path, calls[0].Path)
		assertADORequest(t, calls[0])
		assert.Equal(t, "application/json", calls[0].Header.Get("Content-Type"))
		assert.JSONEq(t, `{"status": "abandoned"}`, calls[0].Body)
	})

	t.Run("refused", func(t *testing.T) {
		api := newFakeAPI(t, map[string]apiReply{
			"PATCH " + adoPR12Path: {http.StatusForbidden,
				adoError("GitNeedsPermissionException", "TF401027: You need the Git 'PullRequestContribute' permission to perform this action.")},
		})
		err := newAzureDevOps(t, api.URL, "").ClosePR(context.Background(), "contoso/Web/web-app", 12)
		require.ErrorContains(t, err, "close ADO PR contoso/Web/web-app#12")
		assert.True(t, scm.IsPermanentError(err), "%v", err)
	})
}

// TestAzureDevOpsContract_OpenPRReusesExisting checks that when Azure DevOps
// refuses a PR with TF401179 (409, an active PR for the branch exists),
// OpenPR returns that PR, found with GET .../pullrequests?searchCriteria.sourceRefName=refs/heads/<branch>&searchCriteria.status=active.
// Any other refusal is returned as an error without a lookup.
// Covers SCM-ADO-05.
func TestAzureDevOpsContract_OpenPRReusesExisting(t *testing.T) {
	duplicate := apiReply{http.StatusConflict,
		adoError("GitPullRequestExistsException", "TF401179: An active pull request for the source and target branch already exists.")}
	list := func(prs ...map[string]interface{}) apiReply {
		return apiReply{http.StatusOK, mustJSON(t, map[string]interface{}{"count": len(prs), "value": prs})}
	}

	tests := []struct {
		name      string
		create    apiReply
		list      apiReply
		wantURL   string
		wantErr   string
		wantCalls int
	}{
		{name: "active PR for the branch is reused", create: duplicate,
			list: list(adoPullRequest(12, "active", adoBranch, adoWebURL)), wantURL: adoPR12WebURL, wantCalls: 2},
		{name: "no active PR for the branch", create: duplicate, list: list(),
			wantErr: "ADO PR already exists for branch kardinal/web-app-v2/prod but could not find it in active PRs", wantCalls: 2},
		{name: "other refusal is not a duplicate",
			create: apiReply{http.StatusBadRequest, adoError("GitPullRequestCannotBeActivated",
				"TF401398: The pull request cannot be activated because the source and/or the target branch no longer exists, or the requested refs are not branches")},
			wantErr: "status 400", wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t, map[string]apiReply{"POST " + adoPRsPath: tt.create, "GET " + adoPRsPath: tt.list})
			prURL, n, err := newAzureDevOps(t, api.URL, "").OpenPR(context.Background(), "contoso/Web/web-app", "t", "b", adoBranch, "main")
			calls := api.Calls()
			require.Len(t, calls, tt.wantCalls)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantURL, prURL)
				assert.Equal(t, 12, n)
			}
			if tt.wantCalls < 2 {
				return
			}
			get := calls[1]
			assert.Equal(t, http.MethodGet, get.Method)
			assert.Equal(t, adoPRsPath, get.Path)
			assert.Equal(t, url.Values{
				"searchCriteria.sourceRefName": {"refs/heads/kardinal/web-app-v2/prod"},
				"searchCriteria.status":        {"active"},
				"api-version":                  {"7.1"},
			}, get.Query)
			assertADORequest(t, get)
		})
	}
}
