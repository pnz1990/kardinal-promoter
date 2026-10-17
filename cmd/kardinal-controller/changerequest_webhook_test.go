// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func crTemplateWithHook(name string) *v1alpha1.ChangeRequest {
	return &v1alpha1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team"},
		Spec: v1alpha1.ChangeRequestSpec{PerPromotion: true, Provider: "servicenow",
			Webhook: &v1alpha1.ChangeRequestWebhook{SecretRef: v1alpha1.SubscriptionSecretRef{Name: "hook"}}}}
}

func crInstanceOf(name, template string, mut ...func(*v1alpha1.ChangeRequest)) *v1alpha1.ChangeRequest {
	cr := &v1alpha1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team",
		Labels: map[string]string{graph.LabelChangeRequestTemplate: template, "kardinal.io/bundle": "app-v1"}},
		Spec: v1alpha1.ChangeRequestSpec{Provider: "servicenow"},
		Status: v1alpha1.ChangeRequestStatus{State: "pending",
			Ticket: &v1alpha1.ChangeRequestTicket{Key: "CHG0000001"}}}
	for _, m := range mut {
		m(cr)
	}
	return cr
}

func crHookClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(&v1alpha1.ChangeRequest{}).Build()
}

func crRefresh(t *testing.T, c client.Client, name string) string {
	t.Helper()
	var cr v1alpha1.ChangeRequest
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team", Name: name}, &cr))
	return cr.Annotations[v1alpha1.RefreshAnnotation]
}

// TestChangeRequestWebhook (spec 23 §7.1): the template's token (constant
// time) refreshes its instances that are not suspended and have no refresh
// pending; the body is never read for state; every failure is the same 401
// and writes nothing; a replay only re-reads.
func TestChangeRequestWebhook(t *testing.T) {
	suspended := func(cr *v1alpha1.ChangeRequest) { cr.Spec.Suspend = true }
	c := crHookClient(t, crTemplateWithHook("prod-change"), hookSecret(testWebhookToken),
		crInstanceOf("i1", "prod-change"), crInstanceOf("i2", "prod-change", suspended), crInstanceOf("other", "uat-change"))
	h := newChangeRequestWebhook(c, zerolog.Nop())
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return t0 }
	path := "/webhook/changerequests/team/prod-change/" + testWebhookToken

	rec := postHook(t, h.Handler(), path, `{"state":"approved","ticket":"CHG0000001"}`, nil)
	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "refresh requested for 1 instances")
	assert.Equal(t, t0.Format(time.RFC3339Nano), crRefresh(t, c, "i1"))
	assert.Empty(t, crRefresh(t, c, "i2"), "a suspended instance reads nothing")
	assert.Empty(t, crRefresh(t, c, "other"), "another template's instance")
	var i1 v1alpha1.ChangeRequest
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team", Name: "i1"}, &i1))
	assert.Equal(t, "pending", i1.Status.State, "the body's claim changes nothing")

	// A replay while the refresh is pending writes nothing new.
	h.now = func() time.Time { return t0.Add(time.Second) }
	rec = postHook(t, h.Handler(), path, "", nil)
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Contains(t, rec.Body.String(), "refresh requested for 0 instances")
	assert.Equal(t, t0.Format(time.RFC3339Nano), crRefresh(t, c, "i1"))
	// Answered (the reconciler read the ticket): the next trigger refreshes again.
	i1.Status.ObservedRefresh = t0.Format(time.RFC3339Nano)
	require.NoError(t, c.Status().Update(context.Background(), &i1))
	rec = postHook(t, h.Handler(), path, "", nil)
	assert.Contains(t, rec.Body.String(), "refresh requested for 1 instances")
	assert.Equal(t, t0.Add(time.Second).Format(time.RFC3339Nano), crRefresh(t, c, "i1"))

	before := crRefresh(t, c, "i1")
	for name, p := range map[string]string{
		"wrong token":       "/webhook/changerequests/team/prod-change/0123456789abcdef-hooX",
		"no such template":  "/webhook/changerequests/team/nope/" + testWebhookToken,
		"an instance":       "/webhook/changerequests/team/i1/" + testWebhookToken,
		"another namespace": "/webhook/changerequests/other/prod-change/" + testWebhookToken,
	} {
		h.now = func() time.Time { return t0.Add(time.Minute) }
		rec := postHook(t, h.Handler(), p, "", nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, name)
		assert.JSONEq(t, `{"status":"unauthorized"}`, rec.Body.String(), name)
	}
	assert.Equal(t, before, crRefresh(t, c, "i1"), "a refused request writes nothing")

	assert.Equal(t, http.StatusUnauthorized, postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change", "", nil).Code,
		"no token in the header or the path")
	for name, p := range map[string]string{"extra": path + "/x", "no name": "/webhook/changerequests/team"} {
		assert.Equal(t, http.StatusNotFound, postHook(t, h.Handler(), p, "", nil).Code, name)
	}
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	h.Handler().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

// TestChangeRequestWebhook_Secrets: a template without spec.webhook, a
// Secret without the referenceable label, and a token shorter than 16 are
// refused.
func TestChangeRequestWebhook_Secrets(t *testing.T) {
	noHook := crTemplateWithHook("plain")
	noHook.Spec.Webhook = nil
	unlabelled := hookSecret(testWebhookToken)
	unlabelled.Labels = nil
	for name, objs := range map[string][]client.Object{
		"no webhook":        {noHook, hookSecret(testWebhookToken), crInstanceOf("i", "plain")},
		"not referenceable": {crTemplateWithHook("plain"), unlabelled, crInstanceOf("i", "plain")},
		"short token":       {crTemplateWithHook("plain"), hookSecret("short"), crInstanceOf("i", "plain")},
	} {
		c := crHookClient(t, objs...)
		h := newChangeRequestWebhook(c, zerolog.Nop())
		token := testWebhookToken
		if name == "short token" {
			token = "short"
		}
		rec := postHook(t, h.Handler(), "/webhook/changerequests/team/plain/"+token, "", nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, name)
		assert.Empty(t, crRefresh(t, c, "i"), name)
	}
}

// TestChangeRequestWebhook_NotATemplateAndLargeBody: a ChangeRequest that
// is not a per-promotion template is refused even with a webhook and the
// right token; a body over the limit gets 413 and triggers nothing.
func TestChangeRequestWebhook_NotATemplateAndLargeBody(t *testing.T) {
	notTemplate := crTemplateWithHook("by-hand")
	notTemplate.Spec.PerPromotion = false
	c := crHookClient(t, notTemplate, crTemplateWithHook("prod-change"), hookSecret(testWebhookToken),
		crInstanceOf("i", "by-hand"), crInstanceOf("j", "prod-change"))
	h := newChangeRequestWebhook(c, zerolog.Nop())
	rec := postHook(t, h.Handler(), "/webhook/changerequests/team/by-hand/"+testWebhookToken, "", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, crRefresh(t, c, "i"))
	rec = postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change/"+testWebhookToken,
		strings.Repeat("x", maxWebhookBody+1), nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Empty(t, crRefresh(t, c, "j"))
}

// TestChangeRequestWebhook_RateLimit (#2139 QA): wrong tokens are refused
// one by one and never use up the template's budget; past its burst, valid
// triggers get 429; the coarse global limit answers 429 before anything is
// read.
func TestChangeRequestWebhook_RateLimit(t *testing.T) {
	c := crHookClient(t, crTemplateWithHook("prod-change"), hookSecret(testWebhookToken))
	h := newChangeRequestWebhook(c, zerolog.Nop())
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return t0 }
	codes := map[int]int{}
	for range webhookPerSubBurst + 3 {
		codes[postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change/wrong-token-xxxxxxxx", "", nil).Code]++
	}
	assert.Equal(t, map[int]int{http.StatusUnauthorized: webhookPerSubBurst + 3}, codes, "a wrong token is never rate limited per template")
	codes = map[int]int{}
	for range webhookPerSubBurst + 3 {
		codes[postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change", "",
			map[string]string{changeRequestTokenHeader: testWebhookToken}).Code]++
	}
	assert.Equal(t, map[int]int{http.StatusAccepted: webhookPerSubBurst, http.StatusTooManyRequests: 3}, codes,
		"wrong tokens left the budget whole")

	lists := 0
	c2 := crHookClient(t, crTemplateWithHook("prod-change"), hookSecret(testWebhookToken))
	h2 := newChangeRequestWebhook(&countingClient{Client: c2, gets: &lists}, zerolog.Nop())
	h2.now = func() time.Time { return t0 }
	codes = map[int]int{}
	for range changeRequestGlobalBurst + 5 {
		codes[postHook(t, h2.Handler(), "/webhook/changerequests/team/other/wrong-token-xxxxxxxx", "", nil).Code]++
	}
	assert.Equal(t, 5, codes[http.StatusTooManyRequests], "the global limit")
	assert.Equal(t, 2*changeRequestGlobalBurst, lists, "a globally limited request reads nothing (a request reads a template and a Secret)")
}

// countingClient counts Get calls.
type countingClient struct {
	client.Client
	gets *int
}

func (c *countingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	*c.gets++
	return c.Client.Get(ctx, key, obj, opts...)
}

// TestChangeRequestWebhook_HeaderToken (#2139 QA): the X-Kardinal-Token
// header carries the token and is preferred to the path segment.
func TestChangeRequestWebhook_HeaderToken(t *testing.T) {
	c := crHookClient(t, crTemplateWithHook("prod-change"), hookSecret(testWebhookToken), crInstanceOf("i1", "prod-change"))
	h := newChangeRequestWebhook(c, zerolog.Nop())
	rec := postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change", "", map[string]string{changeRequestTokenHeader: testWebhookToken})
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.NotEmpty(t, crRefresh(t, c, "i1"))

	c = crHookClient(t, crTemplateWithHook("prod-change"), hookSecret(testWebhookToken), crInstanceOf("i1", "prod-change"))
	h = newChangeRequestWebhook(c, zerolog.Nop())
	rec = postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change/"+testWebhookToken, "",
		map[string]string{changeRequestTokenHeader: "wrong-token-xxxxxxxx"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the header wins over the path")
	assert.Empty(t, crRefresh(t, c, "i1"))
}

// TestChangeRequestWebhook_NamespaceScope (#2139 QA): a template's token
// triggers only its own namespace's instances: the same template name and
// token in another namespace is another template, and instances of this
// template's name elsewhere are not touched.
func TestChangeRequestWebhook_NamespaceScope(t *testing.T) {
	otherTmpl := crTemplateWithHook("prod-change")
	otherTmpl.Namespace = "team-b"
	otherSecret := hookSecret("team-b-token-0123456789")
	otherSecret.Namespace = "team-b"
	otherInst := crInstanceOf("b1", "prod-change")
	otherInst.Namespace = "team-b"
	c := crHookClient(t, crTemplateWithHook("prod-change"), hookSecret(testWebhookToken), crInstanceOf("a1", "prod-change"),
		otherTmpl, otherSecret, otherInst)
	h := newChangeRequestWebhook(c, zerolog.Nop())
	tok := map[string]string{changeRequestTokenHeader: testWebhookToken}

	assert.Equal(t, http.StatusAccepted, postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change", "", tok).Code)
	assert.NotEmpty(t, crRefresh(t, c, "a1"))
	var b1 v1alpha1.ChangeRequest
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-b", Name: "b1"}, &b1))
	assert.Empty(t, b1.Annotations[v1alpha1.RefreshAnnotation], "another namespace's instance of the same template name")

	assert.Equal(t, http.StatusUnauthorized, postHook(t, h.Handler(), "/webhook/changerequests/team-b/prod-change", "", tok).Code,
		"team's token is not team-b's")
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-b", Name: "b1"}, &b1))
	assert.Empty(t, b1.Annotations[v1alpha1.RefreshAnnotation])
}

// TestChangeRequestWebhook_LogsNoToken (#2139 QA): neither a refused nor an
// accepted trigger logs the token, from the header or the path.
func TestChangeRequestWebhook_LogsNoToken(t *testing.T) {
	var logs bytes.Buffer
	c := crHookClient(t, crTemplateWithHook("prod-change"), hookSecret(testWebhookToken), crInstanceOf("i1", "prod-change"))
	h := newChangeRequestWebhook(c, zerolog.New(&logs))
	wrong := "wrong-token-SECRET-1234"
	postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change/"+wrong, "", nil)
	postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change", "", map[string]string{changeRequestTokenHeader: wrong})
	postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change/"+testWebhookToken, "", nil)
	postHook(t, h.Handler(), "/webhook/changerequests/team/prod-change", "", map[string]string{changeRequestTokenHeader: testWebhookToken})
	out := logs.String()
	assert.Contains(t, out, "change request webhook refused")
	assert.Contains(t, out, "change request webhook received")
	assert.NotContains(t, out, wrong)
	assert.NotContains(t, out, testWebhookToken)
}
