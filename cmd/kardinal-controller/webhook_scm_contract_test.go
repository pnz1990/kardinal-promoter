// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// webhookDelivery is one request to /webhook/scm and what it must do to the
// PRStatus of the PR it names.
type webhookDelivery struct {
	name       string
	secret     string // controller --webhook-secret
	body       string
	headers    map[string]string
	wantCode   int
	wantMerged bool
}

// deliver sends each delivery to a webhook server backed by the real
// providerType provider, with PRStatus "prs" for repo#pr and "other" for the
// same PR number in another repository.
func deliver(t *testing.T, providerType, repo, otherRepo string, pr int, deliveries []webhookDelivery) {
	t.Helper()
	for _, d := range deliveries {
		t.Run(d.name, func(t *testing.T) {
			p, err := scm.NewProvider(providerType, "token", "", d.secret)
			require.NoError(t, err)
			c := fake.NewClientBuilder().WithScheme(webhookScheme()).
				WithObjects(webhookPRS("prs", "default", repo, pr), webhookPRS("other", "default", otherRepo, pr)).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			srv := newWebhookServerWithConfig(p, c, zerolog.Nop(), d.secret != "")

			req := httptest.NewRequest(http.MethodPost, "/webhook/scm", strings.NewReader(d.body))
			for k, v := range d.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			srv.Handler()(w, req)

			assert.Equal(t, d.wantCode, w.Code, w.Body.String())
			assert.Equal(t, d.wantMerged, webhookMerged(t, c, "prs", "default"), "PRStatus of %s#%d merged", repo, pr)
			assert.False(t, webhookMerged(t, c, "other", "default"), "PRStatus of %s#%d merged", otherRepo, pr)
		})
	}
}

// TestWebhook_BitbucketCloud sends Bitbucket Cloud webhook deliveries to the
// controller's webhook endpoint: a "Pull request: Merged" delivery signed in
// X-Hub-Signature with the webhook secret marks the PRStatus merged, a
// declined PR does not, and a delivery with a wrong, missing or stale
// signature, or to a controller without a webhook secret, is refused with 401.
// Covers SCM-BB-04.
func TestWebhook_BitbucketCloud(t *testing.T) {
	const secret = "bb-webhook-secret"
	event := func(state string) string {
		return `{"actor":{"type":"user","display_name":"Ana Reviewer"},` +
			`"pullrequest":{"type":"pullrequest","id":7,"title":"[kardinal] Promote web-app-v2 to prod","state":"` + state + `",` +
			`"source":{"branch":{"name":"kardinal/web-app-v2/prod"},"repository":{"full_name":"acme/web-app"}},` +
			`"destination":{"branch":{"name":"main"},"repository":{"full_name":"acme/web-app"}},` +
			`"merge_commit":{"hash":"9f8e7d6c5b4a"},` +
			`"links":{"html":{"href":"https://bitbucket.org/acme/web-app/pull-requests/7"}}},` +
			`"repository":{"type":"repository","full_name":"acme/web-app","name":"web-app"}}`
	}
	merged, declined := event("MERGED"), event("DECLINED")
	headers := func(key, sig string) map[string]string {
		h := map[string]string{"Content-Type": "application/json", "User-Agent": "Bitbucket-Webhooks/2.0",
			"X-Event-Key": key, "X-Request-UUID": "4a7c3d9e-1b2f-4c5d-8e6f-7a8b9c0d1e2f"}
		if sig != "" {
			h["X-Hub-Signature"] = sig
		}
		return h
	}
	sign := func(key, body string) string {
		m := hmac.New(sha256.New, []byte(key))
		m.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}

	deliver(t, "bitbucket", "acme/web-app", "acme/api", 7, []webhookDelivery{
		{name: "merged", secret: secret, body: merged, headers: headers("pullrequest:fulfilled", sign(secret, merged)),
			wantCode: http.StatusNoContent, wantMerged: true},
		{name: "declined", secret: secret, body: declined, headers: headers("pullrequest:rejected", sign(secret, declined)),
			wantCode: http.StatusNoContent},
		{name: "wrong secret", secret: secret, body: merged, headers: headers("pullrequest:fulfilled", sign("guess", merged)),
			wantCode: http.StatusUnauthorized},
		{name: "signature of another body", secret: secret, body: merged, headers: headers("pullrequest:fulfilled", sign(secret, declined)),
			wantCode: http.StatusUnauthorized},
		{name: "unsigned", secret: secret, body: merged, headers: headers("pullrequest:fulfilled", ""),
			wantCode: http.StatusUnauthorized},
		{name: "no webhook secret configured", body: merged, headers: headers("pullrequest:fulfilled", sign("", merged)),
			wantCode: http.StatusUnauthorized},
	})
}

// TestWebhook_AzureDevOps sends Azure DevOps service hook deliveries to the
// controller's webhook endpoint: a "Pull request updated" delivery for a
// completed PR with the webhook secret in X-AzureDevOps-Token marks the
// PRStatus merged, an active or abandoned PR does not, and a delivery with a
// wrong or missing token, or to a controller without a webhook secret, is
// refused with 401. Covers SCM-ADO-04.
func TestWebhook_AzureDevOps(t *testing.T) {
	const secret = "ado-webhook-secret"
	event := func(eventType, status string) string {
		return `{"subscriptionId":"00ca946b-2fe9-4f2a-ae2f-40d5c48001bc","notificationId":3,"eventType":"` + eventType + `",` +
			`"publisherId":"tfs","message":{"text":"kardinal-bot updated pull request 12"},` +
			`"resource":{"repository":{"id":"3411ebc1-d5aa-464f-9615-0b527bc66719","name":"web-app",` +
			`"project":{"id":"a7573007-bbb3-4341-b726-0c4148a07853","name":"Web"},` +
			`"remoteUrl":"https://contoso@dev.azure.com/contoso/Web/_git/web-app"},` +
			`"pullRequestId":12,"status":"` + status + `","mergeStatus":"succeeded",` +
			`"sourceRefName":"refs/heads/kardinal/web-app-v2/prod","targetRefName":"refs/heads/main",` +
			`"lastMergeCommit":{"commitId":"6a1f0c9e8d7b6a5f4e3d2c1b0a9f8e7d6c5b4a39"}},` +
			`"resourceVersion":"1.0","createdDate":"2026-09-30T10:10:00.000Z"}`
	}
	completed := event("git.pullrequest.updated", "completed")
	headers := func(token string) map[string]string {
		h := map[string]string{"Content-Type": "application/json; charset=utf-8"}
		if token != "" {
			h["X-AzureDevOps-Token"] = token
		}
		return h
	}

	deliver(t, "azuredevops", "contoso/Web/web-app", "contoso/Web/api", 12, []webhookDelivery{
		{name: "completed", secret: secret, body: completed, headers: headers(secret),
			wantCode: http.StatusNoContent, wantMerged: true},
		{name: "merge attempted on an active PR", secret: secret, body: event("git.pullrequest.merged", "active"), headers: headers(secret),
			wantCode: http.StatusNoContent},
		{name: "abandoned", secret: secret, body: event("git.pullrequest.updated", "abandoned"), headers: headers(secret),
			wantCode: http.StatusNoContent},
		{name: "wrong token", secret: secret, body: completed, headers: headers("ado-webhook-guess"),
			wantCode: http.StatusUnauthorized},
		{name: "no token", secret: secret, body: completed, headers: headers(""),
			wantCode: http.StatusUnauthorized},
		{name: "no webhook secret configured", body: completed, headers: headers(""),
			wantCode: http.StatusUnauthorized},
	})
}
