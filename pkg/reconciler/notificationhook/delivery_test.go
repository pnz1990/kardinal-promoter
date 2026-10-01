// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/notificationhook"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

const ns = "default"

var (
	// 2026-04-11 is a Saturday: !schedule.isWeekend blocks. 2026-04-13 is a Monday.
	saturday = time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	monday   = time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
)

// webhookServer records every POST and answers with the queued status codes;
// the last one repeats. No queued codes means 200.
type webhookServer struct {
	mu       sync.Mutex
	statuses []int
	events   []string // "<event>/<bundle-or-gate>"
	bodies   []string
}

func (s *webhookServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var p map[string]string
	_ = json.Unmarshal(body, &p)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, p["event"]+"/"+p["bundle"])
	s.bodies = append(s.bodies, string(body))
	code := http.StatusOK
	if n := len(s.statuses); n > 0 {
		code = s.statuses[0]
		if n > 1 {
			s.statuses = s.statuses[1:]
		}
	}
	w.WriteHeader(code)
}

func (s *webhookServer) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func newHook(url string, events ...v1alpha1.NotificationHookEventType) *v1alpha1.NotificationHook {
	return &v1alpha1.NotificationHook{
		ObjectMeta: metav1.ObjectMeta{Name: "hook", Namespace: ns},
		Spec: v1alpha1.NotificationHookSpec{
			Webhook: v1alpha1.NotificationWebhookConfig{URL: url},
			Events:  events,
		},
	}
}

func failedBundle(name string, created time.Time) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created)},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
		Status:     v1alpha1.BundleStatus{Phase: "Failed"},
	}
}

// gateInstance is a PolicyGate instance as the translator creates it.
func gateInstance(name string) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels: map[string]string{
				"kardinal.io/bundle":      "app-v1",
				"kardinal.io/pipeline":    "app",
				"kardinal.io/environment": "prod",
			},
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend", RecheckInterval: "5m"},
	}
}

// fixture is a fake cluster with a NotificationHook reconciler and the real
// PolicyGate reconciler, so gate status is exactly what production writes.
type fixture struct {
	t     *testing.T
	c     client.Client
	hooks *notificationhook.Reconciler
	gates *policygate.Reconciler
	now   time.Time
}

func newFixture(t *testing.T, objs ...client.Object) *fixture {
	t.Helper()
	base := []client.Object{
		&v1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns},
			Spec:       v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}},
		},
		&v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: ns, CreationTimestamp: metav1.NewTime(saturday.Add(-24 * time.Hour))},
			Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
			Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(nhScheme()).
		WithObjects(append(base, objs...)...).
		WithStatusSubresource(&v1alpha1.NotificationHook{}, &v1alpha1.PolicyGate{}).
		Build()
	f := &fixture{t: t, c: c, now: saturday}
	gates, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	gates.NowFn = func() time.Time { return f.now }
	f.gates = gates
	f.hooks = &notificationhook.Reconciler{Client: c, HTTPClient: loopbackClient, NowFn: func() time.Time { return f.now }}
	return f
}

// evalGate runs the PolicyGate reconciler for name at time at.
func (f *fixture) evalGate(name string, at time.Time) {
	f.t.Helper()
	f.now = at
	_, err := f.gates.Reconcile(context.Background(), reqFor(ns, name))
	require.NoError(f.t, err)
}

func (f *fixture) reconcileHook() ctrl.Result {
	f.t.Helper()
	res, err := f.hooks.Reconcile(context.Background(), reqFor(ns, "hook"))
	require.NoError(f.t, err)
	return res
}

func (f *fixture) hook() v1alpha1.NotificationHook {
	f.t.Helper()
	var h v1alpha1.NotificationHook
	require.NoError(f.t, f.c.Get(context.Background(), k8stypes.NamespacedName{Namespace: ns, Name: "hook"}, &h))
	return h
}

func (f *fixture) create(obj client.Object) {
	f.t.Helper()
	require.NoError(f.t, f.c.Create(context.Background(), obj))
}

// TestDelivery_BlockedGateDoesNotMaskOtherEvents is the C04-gates-09 repro: a
// gate that stays blocked is re-evaluated on every recheck, and that must not
// hide a Bundle.Failed that happens meanwhile.
func TestDelivery_BlockedGateDoesNotMaskOtherEvents(t *testing.T) {
	srv := &webhookServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	f := newFixture(t,
		newHook(ts.URL, v1alpha1.NotificationEventBundleFailed, v1alpha1.NotificationEventPolicyGateBlocked),
		gateInstance("app-v1-prod-no-weekend"))
	f.reconcileHook() // hook created before any event

	f.evalGate("app-v1-prod-no-weekend", saturday)
	f.reconcileHook()

	// An older Bundle fails while the gate stays blocked.
	f.create(failedBundle("app-v0", saturday.Add(-48*time.Hour)))
	for i := 1; i <= 3; i++ {
		f.evalGate("app-v1-prod-no-weekend", saturday.Add(time.Duration(i)*5*time.Minute))
		f.reconcileHook()
	}

	assert.Equal(t, []string{"PolicyGate.Blocked/app-v1", "Bundle.Failed/app-v0"}, srv.received(),
		"each event is delivered exactly once, and a blocked gate does not mask a Bundle.Failed")
}

// TestDelivery_OncePerBlockingEpisode covers C04-gates-10: two gates that stay
// blocked are notified once each however often they are re-evaluated, and a
// gate that is allowed and then blocks again is notified again.
func TestDelivery_OncePerBlockingEpisode(t *testing.T) {
	srv := &webhookServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	f := newFixture(t,
		newHook(ts.URL, v1alpha1.NotificationEventPolicyGateBlocked),
		gateInstance("app-v1-prod-no-weekend"), gateInstance("app-v1-prod-soak"))
	f.reconcileHook()

	for i := 0; i < 6; i++ {
		name := "app-v1-prod-no-weekend"
		if i%2 == 1 {
			name = "app-v1-prod-soak"
		}
		f.evalGate(name, saturday.Add(time.Duration(i)*time.Minute))
		f.reconcileHook()
	}
	require.Len(t, srv.received(), 2, "two blocked gates are two notifications, not one per re-evaluation")

	f.evalGate("app-v1-prod-no-weekend", monday)
	f.reconcileHook()
	assert.Len(t, srv.received(), 2, "a gate that allows is not a block")

	f.evalGate("app-v1-prod-no-weekend", saturday.Add(7*24*time.Hour))
	f.reconcileHook()
	f.reconcileHook()
	assert.Len(t, srv.received(), 3, "blocking again is a new episode and is notified once")
}

// TestDelivery_TemplateGateIsNotNotified covers C04-gates-11: a template is
// only syntax-checked, so it has no lastEvaluatedAt and no Ready condition, and
// the hook does not report it as blocking.
func TestDelivery_TemplateGateIsNotNotified(t *testing.T) {
	srv := &webhookServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	tmpl := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "no-weekend-deploys", Namespace: ns,
			Labels: map[string]string{"kardinal.io/scope": "org", "kardinal.io/applies-to": "prod"},
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	f := newFixture(t, newHook(ts.URL, v1alpha1.NotificationEventPolicyGateBlocked), tmpl)
	f.evalGate(tmpl.Name, saturday)
	f.reconcileHook()

	var got v1alpha1.PolicyGate
	require.NoError(t, f.c.Get(context.Background(), k8stypes.NamespacedName{Namespace: ns, Name: tmpl.Name}, &got))
	assert.Nil(t, got.Status.LastEvaluatedAt, "a template is not evaluated")
	assert.Empty(t, got.Status.Conditions)
	assert.Contains(t, got.Status.Reason, "valid CEL syntax")
	assert.Empty(t, srv.received(), "a template gate is not blocking any promotion")
}

// TestDelivery_RetriesWithBackoff covers C04-gates-12: a failed POST is retried
// with exponential backoff until it succeeds.
func TestDelivery_RetriesWithBackoff(t *testing.T) {
	srv := &webhookServer{statuses: []int{503, 503, 200}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	f := newFixture(t, newHook(ts.URL, v1alpha1.NotificationEventBundleFailed), failedBundle("app-v0", saturday))

	res := f.reconcileHook()
	assert.Equal(t, 30*time.Second, res.RequeueAfter)
	h := f.hook()
	assert.Equal(t, int32(1), h.Status.FailedAttempts)
	assert.Contains(t, h.Status.FailureMessage, "HTTP 503")
	assert.Contains(t, h.Status.FailureMessage, "attempt 1 of 10")
	assert.Empty(t, h.Status.LastEventKey)
	assert.Equal(t, "2026-04-11T10:00:30Z", h.Status.NextRetryAt)

	f.now = f.now.Add(res.RequeueAfter)
	res = f.reconcileHook()
	assert.Equal(t, time.Minute, res.RequeueAfter)
	assert.Equal(t, int32(2), f.hook().Status.FailedAttempts)
	assert.Equal(t, "2026-04-11T10:01:30Z", f.hook().Status.NextRetryAt)

	f.now = f.now.Add(res.RequeueAfter)
	res = f.reconcileHook()
	assert.Zero(t, res.RequeueAfter)
	h = f.hook()
	assert.Zero(t, h.Status.FailedAttempts)
	assert.Empty(t, h.Status.FailureMessage)
	assert.Equal(t, "Bundle.Failed/app-v0", h.Status.LastEventKey)
	assert.Equal(t, []string{"Bundle.Failed/app-v0"}, h.Status.ProcessedEventKeys)
	assert.Empty(t, h.Status.NextRetryAt)

	f.reconcileHook()
	assert.Len(t, srv.received(), 3, "delivered once, then not again")
}

// TestDelivery_GivesUpAfterMaxAttempts: an endpoint that always fails does not
// block the hook forever; after 10 attempts the event is given up on.
func TestDelivery_GivesUpAfterMaxAttempts(t *testing.T) {
	srv := &webhookServer{statuses: []int{503}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	f := newFixture(t, newHook(ts.URL, v1alpha1.NotificationEventBundleFailed), failedBundle("app-v0", saturday))

	wantDelays := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		10 * time.Minute, 10 * time.Minute, 10 * time.Minute, 10 * time.Minute,
	}
	for i, want := range wantDelays {
		got := f.reconcileHook().RequeueAfter
		assert.Equal(t, want, got, "attempt %d", i+1)
		f.now = f.now.Add(got)
	}
	f.reconcileHook() // attempt 10
	h := f.hook()
	assert.Contains(t, h.Status.FailureMessage, "gave up on Bundle.Failed/app-v0 after 10 attempts")
	assert.Zero(t, h.Status.FailedAttempts)
	assert.Empty(t, h.Status.NextRetryAt)
	assert.Contains(t, h.Status.ProcessedEventKeys, "Bundle.Failed/app-v0")

	f.reconcileHook()
	assert.Len(t, srv.received(), 10, "no POST after giving up")
}

// TestDelivery_NoRetryBeforeNextRetryAt: the hook is reconciled whenever a
// Bundle, PolicyGate or PromotionStep in its namespace changes, far more often
// than the backoff. Those reconciles must not POST before status.nextRetryAt
// (the live e2e run saw 10 attempts in 4m20s, often two a second). A spec edit
// still retries at once.
func TestDelivery_NoRetryBeforeNextRetryAt(t *testing.T) {
	srv := &webhookServer{statuses: []int{503, 503, 503, 200}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	f := newFixture(t, newHook(ts.URL, v1alpha1.NotificationEventBundleFailed), failedBundle("app-v0", saturday))

	assert.Equal(t, 30*time.Second, f.reconcileHook().RequeueAfter)
	require.Len(t, srv.received(), 1)

	// Watch-triggered reconciles before the retry time only requeue.
	for _, after := range []time.Duration{0, time.Second, 29 * time.Second} {
		f.now = saturday.Add(after)
		assert.Equal(t, 30*time.Second-after, f.reconcileHook().RequeueAfter, "reconcile at +%s", after)
	}
	assert.Len(t, srv.received(), 1, "no POST before status.nextRetryAt")
	assert.Equal(t, int32(1), f.hook().Status.FailedAttempts)

	f.now = saturday.Add(30 * time.Second)
	assert.Equal(t, time.Minute, f.reconcileHook().RequeueAfter)
	assert.Len(t, srv.received(), 2, "retried once the backoff elapsed")
	f.reconcileHook()
	assert.Len(t, srv.received(), 2)

	// A spec edit (for example a corrected URL) retries at once.
	// The fake client does not manage metadata.generation, so bump it as the
	// API server does.
	h := f.hook()
	require.Equal(t, int64(1), h.Status.ObservedGeneration)
	h.Spec.Webhook.AuthorizationHeader = "Bearer fixed"
	h.Generation = 2
	require.NoError(t, f.c.Update(context.Background(), &h))
	require.Equal(t, int64(2), f.hook().Generation)
	f.reconcileHook()
	assert.Len(t, srv.received(), 3, "a spec edit retries without waiting")
	h = f.hook()
	assert.Equal(t, int32(1), h.Status.FailedAttempts, "the count restarts after a spec edit")
	assert.Equal(t, "2026-04-11T10:01:00Z", h.Status.NextRetryAt)
}

// staleCache is a client whose reads of the NotificationHook return hook, a
// copy taken before the last reconcile, as an informer cache that has not yet
// seen the reconcile's status write does. Everything else, writes included,
// goes to the cluster.
type staleCache struct {
	client.Client
	hook *v1alpha1.NotificationHook
}

func (s *staleCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if h, ok := obj.(*v1alpha1.NotificationHook); ok {
		s.hook.DeepCopyInto(h)
		return nil
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

// TestDelivery_StaleCacheDoesNotResend: the informer cache can lag the hook's
// own status write, and a Bundle change in that window reconciles the hook
// again (the live e2e run saw two POSTs 11ms apart, both logged as attempt 1).
// The hook is read through the API reader, so that reconcile sees the failed
// attempt's nextRetryAt, or the delivered event's key, and does not POST.
func TestDelivery_StaleCacheDoesNotResend(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"after a failed POST", http.StatusServiceUnavailable},
		{"after a delivered POST", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &webhookServer{statuses: []int{tc.status}}
			ts := httptest.NewServer(srv)
			defer ts.Close()

			f := newFixture(t, newHook(ts.URL, v1alpha1.NotificationEventBundleFailed), failedBundle("app-v0", saturday))
			before := f.hook()
			f.hooks.Client = &staleCache{Client: f.c, hook: &before}
			f.hooks.APIReader = f.c

			f.reconcileHook()
			require.Len(t, srv.received(), 1)
			f.now = f.now.Add(10 * time.Millisecond)
			f.reconcileHook()
			assert.Len(t, srv.received(), 1, "the reconcile right after the status write does not POST again")
			h := f.hook()
			if tc.status == http.StatusOK {
				assert.Equal(t, "Bundle.Failed/app-v0", h.Status.LastEventKey)
				assert.Zero(t, h.Status.FailedAttempts)
			} else {
				assert.Equal(t, int32(1), h.Status.FailedAttempts)
				assert.Equal(t, "2026-04-11T10:00:30Z", h.Status.NextRetryAt)
			}
		})
	}
}

// TestDelivery_RedirectIsNotFollowed covers C04-gates-14: the controller does
// not follow a redirect to another address.
func TestDelivery_RedirectIsNotFollowed(t *testing.T) {
	target := &webhookServer{}
	targetSrv := httptest.NewServer(target)
	defer targetSrv.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetSrv.URL+"/internal", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	f := newFixture(t, newHook(redirector.URL, v1alpha1.NotificationEventBundleFailed), failedBundle("app-v0", saturday))
	f.reconcileHook()

	assert.Empty(t, target.received(), "the redirect target must not receive the POST")
	h := f.hook()
	assert.Contains(t, h.Status.FailureMessage, "HTTP 307")
	assert.Contains(t, h.Status.FailureMessage, "redirects are not followed")
}

// TestDelivery_DefaultClientRefusesLoopback covers #1267: without an
// HTTPClient override the reconciler uses the egress guard, so a hook aimed at
// loopback (for example the controller's own UI API on 127.0.0.1:8082) is
// refused at dial time and the server never sees the POST.
func TestDelivery_DefaultClientRefusesLoopback(t *testing.T) {
	srv := &webhookServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	require.NoError(t, err)

	for _, target := range []string{ts.URL, "http://localhost:" + port} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t, newHook(target, v1alpha1.NotificationEventBundleFailed), failedBundle("app-v0", saturday))
			f.hooks.HTTPClient = nil
			f.reconcileHook()

			assert.Empty(t, srv.received(), "the loopback server must not receive the POST")
			h := f.hook()
			assert.Contains(t, h.Status.FailureMessage, "not allowed")
			assert.Contains(t, h.Status.FailureMessage, "loopback")
		})
	}
}

// TestDelivery_URLTokenNotInStatusOrLogs covers C04-gates-14: incoming-webhook
// URLs (Slack, Teams) embed their token in the path, so neither status nor the
// logs may contain the URL. Neither may they contain the Authorization header.
func TestDelivery_URLTokenNotInStatusOrLogs(t *testing.T) {
	const secretPath = "/services/T000/B000/SECRETTOKEN"
	ok := httptest.NewServer(&webhookServer{})
	defer ok.Close()
	failing := httptest.NewServer(&webhookServer{statuses: []int{500}})
	defer failing.Close()
	closed := httptest.NewServer(&webhookServer{})
	closedURL := closed.URL
	closed.Close()

	tests := []struct {
		name        string
		url         string
		wantFailure bool
	}{
		{name: "delivered", url: ok.URL + secretPath},
		{name: "http error", url: failing.URL + secretPath, wantFailure: true},
		{name: "connection refused", url: closedURL + secretPath + "?token=SECRETTOKEN", wantFailure: true},
		{name: "invalid URL", url: "http://[::1" + secretPath, wantFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hook := newHook(tt.url, v1alpha1.NotificationEventBundleFailed)
			hook.Spec.Webhook.AuthorizationHeader = "Bearer SECRETHEADER"
			f := newFixture(t, hook, failedBundle("app-v0", saturday))

			var logs bytes.Buffer
			ctx := zerolog.New(&logs).WithContext(context.Background())
			_, err := f.hooks.Reconcile(ctx, reqFor(ns, "hook"))
			require.NoError(t, err)

			h := f.hook()
			status, err := json.Marshal(h.Status)
			require.NoError(t, err)
			if tt.wantFailure {
				assert.NotEmpty(t, h.Status.FailureMessage)
			} else {
				assert.Empty(t, h.Status.FailureMessage)
			}
			require.NotEmpty(t, logs.String(), "the delivery is logged")
			for _, secret := range []string{"SECRETTOKEN", "SECRETHEADER"} {
				assert.NotContains(t, string(status), secret, "status")
				assert.NotContains(t, logs.String(), secret, "logs")
			}
		})
	}
}

// TestDelivery_FirstReconcileDoesNotBackfill: a new hook reports the newest
// existing event, as before, rather than the whole history.
func TestDelivery_FirstReconcileDoesNotBackfill(t *testing.T) {
	srv := &webhookServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	f := newFixture(t, newHook(ts.URL, v1alpha1.NotificationEventBundleFailed),
		failedBundle("app-v2", saturday.Add(-3*time.Hour)),
		failedBundle("app-v3", saturday.Add(-2*time.Hour)),
		failedBundle("app-v4", saturday.Add(-1*time.Hour)))
	f.reconcileHook()
	assert.Equal(t, []string{"Bundle.Failed/app-v4"}, srv.received())
	assert.Len(t, f.hook().Status.ProcessedEventKeys, 3)

	f.create(failedBundle("app-v5", saturday))
	f.reconcileHook()
	f.reconcileHook()
	assert.Equal(t, []string{"Bundle.Failed/app-v4", "Bundle.Failed/app-v5"}, srv.received())
}

// TestDelivery_BoundedPerReconcile: a burst of events is sent in batches of 10,
// with an immediate requeue for the rest.
func TestDelivery_BoundedPerReconcile(t *testing.T) {
	srv := &webhookServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	f := newFixture(t, newHook(ts.URL, v1alpha1.NotificationEventBundleFailed))
	f.reconcileHook()
	for i := 0; i < 12; i++ {
		f.create(failedBundle(fmt.Sprintf("app-v%02d", i), saturday.Add(time.Duration(i)*time.Minute)))
	}

	assert.Equal(t, time.Second, f.reconcileHook().RequeueAfter)
	assert.Len(t, srv.received(), 10)
	assert.Zero(t, f.reconcileHook().RequeueAfter)
	got := srv.received()
	require.Len(t, got, 12)
	assert.Equal(t, "Bundle.Failed/app-v00", got[0], "oldest first")
	assert.Equal(t, "Bundle.Failed/app-v11", got[11])
}

// TestDelivery_PipelineSelectorMatchesSpecPipeline: Bundles created by the CLI
// or kubectl have no kardinal.io/pipeline label; the selector matches spec.pipeline.
func TestDelivery_PipelineSelectorMatchesSpecPipeline(t *testing.T) {
	srv := &webhookServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	hook := newHook(ts.URL, v1alpha1.NotificationEventBundleFailed)
	hook.Spec.PipelineSelector = "app"
	other := failedBundle("other-v1", saturday.Add(time.Minute))
	other.Spec.Pipeline = "other"
	f := newFixture(t, hook)
	f.reconcileHook()
	f.create(failedBundle("app-v0", saturday))
	f.create(other)
	f.reconcileHook()

	assert.Equal(t, []string{"Bundle.Failed/app-v0"}, srv.received())
}

// TestDelivery_UpgradedHookKeepsLegacyKey: a hook that delivered a block before
// per-episode keys existed recorded "PolicyGate.Blocked/<gate>"; that block is
// not re-sent after the upgrade.
func TestDelivery_UpgradedHookKeepsLegacyKey(t *testing.T) {
	srv := &webhookServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	hook := newHook(ts.URL, v1alpha1.NotificationEventPolicyGateBlocked)
	hook.Status = v1alpha1.NotificationHookStatus{
		LastEvent:    "PolicyGate.Blocked",
		LastEventKey: "PolicyGate.Blocked/app-v1-prod-no-weekend",
		LastSentAt:   saturday.Add(-time.Hour).Format(time.RFC3339),
	}
	f := newFixture(t, hook, gateInstance("app-v1-prod-no-weekend"))
	f.evalGate("app-v1-prod-no-weekend", saturday)
	f.reconcileHook()
	f.evalGate("app-v1-prod-no-weekend", saturday.Add(5*time.Minute))
	f.reconcileHook()

	assert.Empty(t, srv.received())
	keys := f.hook().Status.ProcessedEventKeys
	require.Len(t, keys, 1)
	assert.True(t, strings.HasPrefix(keys[0], "PolicyGate.Blocked/app-v1-prod-no-weekend/"), keys[0])
}
