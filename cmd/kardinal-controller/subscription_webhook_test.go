// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

const testWebhookToken = "0123456789abcdef-hook"

func webhookTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Subscription{}).Build()
}

func hookedSub(name string) *v1alpha1.Subscription {
	return &v1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team"},
		Spec: v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeImage, Pipeline: "app",
			Image:   &v1alpha1.ImageSubscriptionSpec{Registry: "ghcr.io/org/app"},
			Webhook: &v1alpha1.SubscriptionWebhook{SecretRef: v1alpha1.SubscriptionSecretRef{Name: "hook"}}},
	}
}

func hookSecret(token string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "hook", Namespace: "team"},
		Data: map[string][]byte{"token": []byte(token)}}
}

func sign(body string) string {
	mac := hmac.New(sha256.New, []byte(testWebhookToken))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func postHook(t *testing.T, h http.Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func refreshOf(t *testing.T, c client.Client, name string) string {
	t.Helper()
	var sub v1alpha1.Subscription
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team", Name: name}, &sub))
	return sub.Annotations[v1alpha1.RefreshAnnotation]
}

// TestSubscriptionWebhook_Providers covers each sender's authentication and
// payload: Docker Hub and Quay with the token in the path, Harbor with its
// Authorization header, Artifactory with X-JFrog-Event-Auth (plain or HMAC),
// GHCR with GitHub's signature, generic with a path token or a signature. An
// authenticated push sets kardinal.io/refresh (202); events that are not
// pushes are answered 200 and change nothing; a bad credential is 401.
func TestSubscriptionWebhook_Providers(t *testing.T) {
	base := "/webhook/subscriptions/team/app/"
	dockerhub := `{"push_data":{"tag":"v1.2.3"},"repository":{"repo_name":"org/app"}}`
	quay := `{"repository":"org/app","updated_tags":["v1.2.3"]}`
	harbor := `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"tag":"v1.2.3"}]}}`
	jfrog := `{"domain":"docker","event_type":"pushed","data":{"tag":"v1.2.3"}}`
	ghcr := `{"action":"published","package":{"name":"app","package_type":"container"}}`
	tests := []struct {
		name     string
		path     string
		body     string
		headers  map[string]string
		wantCode int
		refresh  bool
	}{
		{name: "dockerhub path token", path: base + "dockerhub/" + testWebhookToken, body: dockerhub, wantCode: 202, refresh: true},
		{name: "dockerhub no token", path: base + "dockerhub", body: dockerhub, wantCode: 401},
		{name: "dockerhub wrong token", path: base + "dockerhub/" + strings.Repeat("x", 21), body: dockerhub, wantCode: 401},
		{name: "dockerhub bad payload", path: base + "dockerhub/" + testWebhookToken, body: `{"x":1}`, wantCode: 400},
		{name: "quay", path: base + "quay/" + testWebhookToken, body: quay, wantCode: 202, refresh: true},
		{name: "harbor auth header", path: base + "harbor", body: harbor,
			headers: map[string]string{"Authorization": testWebhookToken}, wantCode: 202, refresh: true},
		{name: "harbor bearer header", path: base + "harbor", body: harbor,
			headers: map[string]string{"Authorization": "Bearer " + testWebhookToken}, wantCode: 202, refresh: true},
		{name: "harbor delete is ignored", path: base + "harbor/" + testWebhookToken, body: `{"type":"DELETE_ARTIFACT"}`, wantCode: 200},
		{name: "artifactory secret header", path: base + "artifactory", body: jfrog,
			headers: map[string]string{"X-JFrog-Event-Auth": testWebhookToken}, wantCode: 202, refresh: true},
		{name: "artifactory signed", path: base + "artifactory", body: jfrog,
			headers: map[string]string{"X-JFrog-Event-Auth": strings.TrimPrefix(sign(jfrog), "sha256=")}, wantCode: 202, refresh: true},
		{name: "artifactory deleted is ignored", path: base + "artifactory/" + testWebhookToken,
			body: `{"domain":"docker","event_type":"deleted"}`, wantCode: 200},
		{name: "ghcr signed package event", path: base + "ghcr", body: ghcr,
			headers: map[string]string{"X-Hub-Signature-256": sign(ghcr), "X-GitHub-Event": "package"}, wantCode: 202, refresh: true},
		{name: "github push", path: base + "github", body: `{"ref":"refs/heads/main"}`,
			headers: map[string]string{"X-Hub-Signature-256": sign(`{"ref":"refs/heads/main"}`), "X-GitHub-Event": "push"}, wantCode: 202, refresh: true},
		{name: "github ping is ignored", path: base + "github", body: `{"zen":"x"}`,
			headers: map[string]string{"X-Hub-Signature-256": sign(`{"zen":"x"}`), "X-GitHub-Event": "ping"}, wantCode: 200},
		{name: "ghcr path token is not enough", path: base + "ghcr/" + testWebhookToken, body: ghcr,
			headers: map[string]string{"X-GitHub-Event": "package"}, wantCode: 401},
		{name: "ghcr bad signature", path: base + "ghcr", body: ghcr,
			headers: map[string]string{"X-Hub-Signature-256": sign(ghcr + " "), "X-GitHub-Event": "package"}, wantCode: 401},
		{name: "generic path token", path: base + "generic/" + testWebhookToken, body: "anything", wantCode: 202, refresh: true},
		{name: "generic signature", path: base + "generic", body: `{"a":1}`,
			headers: map[string]string{"X-Kardinal-Signature-256": sign(`{"a":1}`)}, wantCode: 202, refresh: true},
		{name: "unknown provider", path: base + "gitlab/" + testWebhookToken, body: "{}", wantCode: 404},
		{name: "unknown subscription", path: "/webhook/subscriptions/team/nope/generic/" + testWebhookToken, body: "{}", wantCode: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := webhookTestClient(t, hookedSub("app"), hookSecret(testWebhookToken))
			s := newSubscriptionWebhook(c, zerolog.Nop())
			rec := postHook(t, s.Handler(), tt.path, tt.body, tt.headers)
			assert.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), testWebhookToken)
			assert.Equal(t, tt.refresh, refreshOf(t, c, "app") != "", "refresh annotation")
		})
	}
}

// TestSubscriptionWebhook_Refusals: no spec.webhook, a missing Secret, a token
// shorter than 16 characters, GET, a payload over 1 MB and a burst over the
// rate limit are refused, all without writing the Subscription.
func TestSubscriptionWebhook_Refusals(t *testing.T) {
	noHook := hookedSub("nohook")
	noHook.Spec.Webhook = nil
	c := webhookTestClient(t, hookedSub("app"), noHook, hookSecret("short"))
	s := newSubscriptionWebhook(c, zerolog.Nop())
	h := s.Handler()

	assert.Equal(t, 401, postHook(t, h, "/webhook/subscriptions/team/nohook/generic/"+testWebhookToken, "{}", nil).Code)
	assert.Equal(t, 401, postHook(t, h, "/webhook/subscriptions/team/app/generic/short", "{}", nil).Code, "token under 16 characters")
	req := httptest.NewRequest(http.MethodGet, "/webhook/subscriptions/team/app/generic/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, 405, rec.Code)
	assert.Equal(t, 404, postHook(t, h, "/webhook/subscriptions/team/app", "{}", nil).Code)
	assert.Equal(t, 413, postHook(t, h, "/webhook/subscriptions/team/app/generic/x", strings.Repeat("a", maxWebhookBody+1), nil).Code)

	s.limiter = rate.NewLimiter(rate.Every(time.Hour), 2)
	codes := []int{}
	for range 3 {
		codes = append(codes, postHook(t, h, "/webhook/subscriptions/team/app/generic/x", "{}", nil).Code)
	}
	assert.Equal(t, []int{401, 401, 429}, codes)
	assert.Empty(t, refreshOf(t, c, "app"))
	assert.Empty(t, refreshOf(t, c, "nohook"))
}

// TestSubscriptionWebhook_CoalescesPendingRefresh: while a refresh request is
// pending (not yet in status.lastRefreshRequest), duplicate and concurrent
// deliveries do not write again; once the reconciler answered it, the next
// delivery requests a new one.
func TestSubscriptionWebhook_CoalescesPendingRefresh(t *testing.T) {
	c := webhookTestClient(t, hookedSub("app"), hookSecret(testWebhookToken))
	s := newSubscriptionWebhook(c, zerolog.Nop())
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	h := s.Handler()
	path := "/webhook/subscriptions/team/app/generic/" + testWebhookToken

	var wg sync.WaitGroup
	var mu sync.Mutex
	bodies := map[string]int{}
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := postHook(t, h, path, "{}", nil)
			mu.Lock()
			bodies[strings.TrimSpace(rec.Body.String())]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	first := refreshOf(t, c, "app")
	assert.Equal(t, now.Format(time.RFC3339Nano), first)
	assert.Positive(t, bodies[`{"status":"refresh already pending"}`]+bodies[`{"status":"refresh requested"}`])
	assert.Zero(t, bodies[`{"status":"internal error"}`])

	now = now.Add(time.Minute)
	rec := postHook(t, h, path, "{}", nil)
	assert.Equal(t, `{"status":"refresh already pending"}`, strings.TrimSpace(rec.Body.String()))
	assert.Equal(t, first, refreshOf(t, c, "app"), "a pending request is not rewritten")

	// The reconciler answers it.
	var sub v1alpha1.Subscription
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team", Name: "app"}, &sub))
	sub.Status.LastRefreshRequest = first
	require.NoError(t, c.Status().Update(context.Background(), &sub))

	rec = postHook(t, h, path, "{}", nil)
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, `{"status":"refresh requested"}`, strings.TrimSpace(rec.Body.String()))
	assert.Equal(t, now.Format(time.RFC3339Nano), refreshOf(t, c, "app"))
}

func TestParseSubscriptionWebhookPath(t *testing.T) {
	ns, name, provider, token, ok := parseSubscriptionWebhookPath("/webhook/subscriptions/a/b/quay/tok")
	assert.True(t, ok)
	assert.Equal(t, []string{"a", "b", "quay", "tok"}, []string{ns, name, provider, token})
	_, _, _, token, ok = parseSubscriptionWebhookPath("/webhook/subscriptions/a/b/github/")
	assert.True(t, ok)
	assert.Empty(t, token)
	for _, bad := range []string{"/webhook/subscriptions/a/b", "/webhook/subscriptions/a//quay", "/webhook/subscriptions/a/b/quay/t/x",
		"/webhook/subscriptions/a/b/ftp", "/webhook/scm"} {
		_, _, _, _, ok := parseSubscriptionWebhookPath(bad)
		assert.False(t, ok, bad)
	}
}
