// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/accesslog"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

const fixerSA = "system:serviceaccount:ci:fixer"

// actorFixture is a cluster with Pipeline checkout in payments-eu and an
// AgentIndex in which fixer (sponsor alice) may act in payments-* only.
func actorFixture(t *testing.T) (client.Client, *actorWriter) {
	t.Helper()
	reg := actor.Evaluate([]v1alpha1.AgentIdentity{{
		ObjectMeta: metav1.ObjectMeta{Name: "fixer"},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"}},
			Sponsor: v1alpha1.AgentSponsor{User: "alice"}, Scope: v1alpha1.AgentScope{Namespaces: []string{"payments-*"}}},
	}, {
		ObjectMeta: metav1.ObjectMeta{Name: "parked"},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "parked"}},
			Sponsor: v1alpha1.AgentSponsor{User: "bob"}, Scope: v1alpha1.AgentScope{Namespaces: []string{"*"}}, Suspended: true},
	}}, actor.MaxIndexBytes)
	reg.Index.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"}}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "payments-eu"}},
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "billing"}},
		&v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}, Status: reg.Index},
	).Build()
	return c, &actorWriter{source: &actor.Source{Reader: c, Name: "kardinal-promoter"}}
}

// recordingSink captures what the access log hands its audit sink.
type recordingSink struct {
	status []int
	target []*accesslog.AuditTarget
}

func (r *recordingSink) Record(_, _ string, status int, t *accesslog.AuditTarget) {
	r.status, r.target = append(r.status, status), append(r.target, t)
}

func uiPost(t *testing.T, s *uiAPIServer, sink *recordingSink, user, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	handler := accesslog.New(accesslog.Config{Audit: sink}, zerolog.Nop()).Middleware("ui", mux)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if user != "" {
		req = req.WithContext(uiauth.WithUser(req.Context(), authv1.UserInfo{Username: user}))
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// TestUIAPI_ActorStamps: the UI stamps a version with the caller it
// authenticated (via ui), refuses an agent outside its scope with 403
// before writing anything, and hands every write to the APIWrite audit
// with the caller's actor, refusals included.
func TestUIAPI_ActorStamps(t *testing.T) {
	c, actors := actorFixture(t)
	s := newUIAPIServer(c, zerolog.Nop())
	s.actors = actors
	sink := &recordingSink{}

	w := uiPost(t, s, sink, fixerSA, "/api/v1/ui/bundles",
		`{"pipeline":"checkout","image":"ghcr.io/acme/checkout:1.5","namespace":"payments-eu"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles, client.InNamespace("payments-eu")))
	require.Len(t, bundles.Items, 1)
	want := &v1alpha1.Actor{Kind: "agent", Principal: fixerSA, Agent: "fixer", Sponsor: "alice", Via: "ui"}
	assert.Equal(t, want, bundles.Items[0].Spec.Actor)
	require.Len(t, sink.target, 1)
	assert.Equal(t, http.StatusCreated, sink.status[0])
	assert.Equal(t, want, sink.target[0].Actor)
	assert.Equal(t, bundles.Items[0].Name, sink.target[0].Bundle)
	assert.Equal(t, "checkout", sink.target[0].Pipeline)
	assert.Equal(t, "/api/v1/ui/bundles", sink.target[0].Route)

	// Outside payments-*: refused, nothing written, the refusal audited.
	w = uiPost(t, s, sink, fixerSA, "/api/v1/ui/bundles",
		`{"pipeline":"checkout","image":"ghcr.io/acme/checkout:1.5","namespace":"billing"}`)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "agent fixer may not act in namespace billing")
	require.NoError(t, c.List(context.Background(), &bundles, client.InNamespace("billing")))
	assert.Empty(t, bundles.Items)
	require.Len(t, sink.target, 2)
	assert.Equal(t, http.StatusForbidden, sink.status[1])
	assert.Nil(t, sink.target[1].Actor)
	assert.Contains(t, sink.target[1].Message, "by "+fixerSA)

	// A person: human, via ui.
	_ = uiPost(t, s, sink, "bob", "/api/v1/ui/pause", `{"pipeline":"checkout","namespace":"billing"}`)
	require.Len(t, sink.target, 3)
	assert.Equal(t, &v1alpha1.Actor{Kind: "human", Principal: "bob", Via: "ui"}, sink.target[2].Actor)
	assert.Equal(t, "/api/v1/ui/pause", sink.target[2].Route)

	// The static token (no user): the shared credential, a bot.
	w = uiPost(t, s, sink, "", "/api/v1/ui/bundles",
		`{"pipeline":"checkout","image":"ghcr.io/acme/checkout:1.6","namespace":"billing"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.NoError(t, c.List(context.Background(), &bundles, client.InNamespace("billing")))
	require.Len(t, bundles.Items, 1)
	assert.Equal(t, &v1alpha1.Actor{Kind: "bot", Principal: "kardinal-ui", Via: "ui"}, bundles.Items[0].Spec.Actor)
}

// TestBundleAPI_ActorStamps: the Bundle API stamps the static token's
// Bundles as the bot bundle-api, and a reviewed caller's as that caller
// (via api); an agent outside its scope is refused.
func TestBundleAPI_ActorStamps(t *testing.T) {
	c, actors := actorFixture(t)
	s := newBundleAPIServer(c, "tok", "payments-eu")
	s.actors = actors
	sink := &recordingSink{}
	handler := accesslog.New(accesslog.Config{Audit: sink}, zerolog.Nop()).Middleware("bundle-api", s.Handler())
	post := func(ctx context.Context, body string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(body)).WithContext(ctx)
		if auth {
			req.Header.Set("Authorization", "Bearer tok")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	w := post(context.Background(), `{"pipeline":"checkout","images":[{"repository":"r","tag":"1"}]}`, true)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles, client.InNamespace("payments-eu")))
	require.Len(t, bundles.Items, 1)
	assert.Equal(t, &v1alpha1.Actor{Kind: "bot", Principal: "bundle-api", Via: "api"}, bundles.Items[0].Spec.Actor)
	require.Len(t, sink.target, 1)
	assert.Equal(t, bundles.Items[0].Name, sink.target[0].Bundle)

	// A reviewed caller, served as serve does after the TokenReview.
	ctx := uiauth.WithUser(context.Background(), authv1.UserInfo{Username: fixerSA})
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles",
		strings.NewReader(`{"pipeline":"checkout","namespace":"billing","images":[{"repository":"r","tag":"2"}]}`)).WithContext(ctx)
	s.serve(w, req, c, fixerSA)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "namespace billing")
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/bundles",
		strings.NewReader(`{"pipeline":"checkout","images":[{"repository":"r","tag":"3"}]}`)).WithContext(ctx)
	s.serve(w, req, c, fixerSA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.NoError(t, c.List(context.Background(), &bundles, client.InNamespace("payments-eu")))
	require.Len(t, bundles.Items, 2)
	stamped := map[string]bool{}
	for _, b := range bundles.Items {
		stamped[actor.Describe(*b.Spec.Actor)] = true
	}
	assert.True(t, stamped["agent fixer for alice via the API"], "%v", stamped)
}

// TestAPIAudit: a recorded write becomes an APIWrite AuditEvent in the
// target's namespace with the actor, the route and the outcome; a target
// without a namespace or a name is not recorded; past the per-second budget
// records are dropped.
func TestAPIAudit(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	a := newAPIAudit(c, zerolog.Nop())
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	bob := &v1alpha1.Actor{Kind: "human", Principal: "bob", Via: "ui"}
	a.Record("ui", "POST", http.StatusOK, &accesslog.AuditTarget{Namespace: "team-a", Pipeline: "checkout",
		Route: "/api/v1/ui/pause", Message: "paused pipeline checkout", Actor: bob})
	a.Record("ui", "POST", http.StatusForbidden, &accesslog.AuditTarget{Namespace: "team-a", Pipeline: "checkout",
		Route: "/api/v1/ui/pause", Message: "pause pipeline checkout by mallory"})
	a.Record("ui", "POST", http.StatusOK, &accesslog.AuditTarget{Namespace: "team-a"}) // no name: skipped
	a.Record("ui", "POST", http.StatusOK, &accesslog.AuditTarget{Pipeline: "x"})       // no namespace: skipped
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, a.Start(ctx)) // drains the queue
	var list v1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("team-a")))
	require.Len(t, list.Items, 2)
	by := map[string]v1alpha1.AuditEvent{}
	for _, e := range list.Items {
		by[e.Spec.Outcome] = e
	}
	ok := by["Success"]
	assert.Equal(t, "APIWrite", ok.Spec.Action)
	assert.Equal(t, bob, ok.Spec.Actor)
	assert.Equal(t, &v1alpha1.AuditRequest{Route: "/api/v1/ui/pause", Method: "POST"}, ok.Spec.Request)
	assert.Equal(t, "checkout", ok.Labels["kardinal.io/pipeline"])
	assert.Equal(t, "APIWrite", ok.Labels["kardinal.io/action"])
	assert.Equal(t, "refused (403): pause pipeline checkout by mallory", by["Failure"].Spec.Message)

	// The budget: apiAuditPerSecond a second, then dropped until the next.
	b := newAPIAudit(c, zerolog.Nop())
	b.now = func() time.Time { return now }
	for range apiAuditPerSecond + 5 {
		b.Record("ui", "POST", http.StatusOK, &accesslog.AuditTarget{Namespace: "team-b", Bundle: "x"})
	}
	assert.Len(t, b.queue, apiAuditPerSecond)
	now = now.Add(time.Second)
	b.Record("ui", "POST", http.StatusOK, &accesslog.AuditTarget{Namespace: "team-b", Bundle: "x"})
	assert.Len(t, b.queue, apiAuditPerSecond+1)
}

// TestAgentIndexReady: the controller is ready only once its AgentIndex
// exists and is built, in either format (the actor policy fails closed
// without it).
// Covers ACTOR-INDEX-04.
func TestAgentIndexReady(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	check := agentIndexReady(c, "kardinal-promoter")
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	assert.Error(t, check(req))
	idx := &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}
	require.NoError(t, c.Create(context.Background(), idx))
	assert.Error(t, check(req), "an index without status is not ready")
	idx.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"}}
	require.NoError(t, c.Update(context.Background(), idx))
	assert.NoError(t, check(req))

	// A later release's compact index (#2071): built while CompactReady is
	// True, whatever Ready says.
	idx.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: actor.ReasonCompactFormat},
		{Type: actor.ConditionCompactReady, Status: metav1.ConditionFalse, Reason: actor.ReasonIndexFull}}
	require.NoError(t, c.Update(context.Background(), idx))
	assert.Error(t, check(req), "a full compact index is not ready")
	idx.Status.Conditions[1] = metav1.Condition{Type: actor.ConditionCompactReady, Status: metav1.ConditionTrue, Reason: "Built"}
	require.NoError(t, c.Update(context.Background(), idx))
	assert.NoError(t, check(req), "a built compact index is ready")
}

// TestUIAPI_ActorRefusals: a suspended agent is refused (403). While the
// release's AgentIndex is missing or not Ready (#2451) a ServiceAccount's
// stamped write answers 503: the index alone can clear it, and the
// controller, exempt from the actor policy, fails closed itself. A person
// is served, stamped unverified (via the UI), never refused because of the
// index and never stamped human from an empty registry.
// Covers ACTOR-INDEX-06.
func TestUIAPI_ActorRefusals(t *testing.T) {
	c, actors := actorFixture(t)
	s := newUIAPIServer(c, zerolog.Nop())
	s.actors = actors
	sink := &recordingSink{}
	body := `{"pipeline":"checkout","image":"ghcr.io/acme/checkout:1.5","namespace":"payments-eu"}`
	w := uiPost(t, s, sink, "system:serviceaccount:ci:parked", "/api/v1/ui/bundles", body)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "agent parked may not act")

	var idx v1alpha1.AgentIndex
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	idx.Status = v1alpha1.AgentIndexStatus{}
	require.NoError(t, c.Update(context.Background(), &idx))
	w = uiPost(t, s, sink, "system:serviceaccount:ci:parked", "/api/v1/ui/bundles", body)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "agent index is not ready")
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	assert.Empty(t, bundles.Items, "no version from a ServiceAccount")

	stamped := func(user string) *v1alpha1.Actor {
		t.Helper()
		w := uiPost(t, s, sink, user, "/api/v1/ui/bundles", body)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		require.NoError(t, c.List(context.Background(), &bundles))
		for i := range bundles.Items {
			if a := bundles.Items[i].Spec.Actor; a != nil && a.Principal == user {
				return a
			}
		}
		t.Fatalf("no version by %s", user)
		return nil
	}
	assert.Equal(t, &v1alpha1.Actor{Kind: v1alpha1.ActorUnverified, Principal: "bob", Via: v1alpha1.ViaUI}, stamped("bob"),
		"a person, while the index is not Ready")
	require.NoError(t, c.Delete(context.Background(), &idx))
	assert.Equal(t, &v1alpha1.Actor{Kind: v1alpha1.ActorUnverified, Principal: "carol", Via: v1alpha1.ViaUI}, stamped("carol"),
		"a person, while the index is missing")
}
