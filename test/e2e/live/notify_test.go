//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The NotificationHook tests post to the suite's webhook receiver; each test
// uses buckets named after its namespace. Most of them get their event from a
// team PolicyGate that always blocks (holdApp): a Bundle's gate instance
// blocks within seconds and nothing is promoted.

// holdGateName is the team PolicyGate template holdApp creates.
const holdGateName = "hold"

// holdApp sets up, in ns, a GitOps repo, the team gate holdGateName on
// environment test (expression "false", re-evaluated every 10s) and one
// Pipeline per name over the repo. Every Bundle of those Pipelines is held at
// the gate, so it creates a PolicyGate.Blocked event and promotes nothing.
func holdApp(t *testing.T, e *framework.Env, ns string, pipelines ...string) *app {
	t.Helper()
	envs := []string{"test"}
	a := &app{e: e, ns: ns, envs: envs,
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))}
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: holdGateName, Namespace: ns, Labels: map[string]string{
			"kardinal.io/scope":      "team",
			"kardinal.io/applies-to": "test",
			"kardinal.io/type":       "gate",
		}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "false", Message: "held by the e2e test", RecheckInterval: "10s"},
	}
	require.NoError(t, e.Client.Create(context.Background(), gate), "create PolicyGate %s", holdGateName)
	for _, name := range pipelines {
		p := a.pipeline(nil)
		p.Name = name
		a.apply(t, p)
	}
	return a
}

// holdBundle creates a Bundle of pipeline and waits until its holdGateName
// instance blocks. It returns the Bundle name and the gate instance.
func holdBundle(t *testing.T, a *app, pipeline string) (string, *v1alpha1.PolicyGate) {
	t.Helper()
	bundle := a.e.CreateBundle(t, a.ns, pipeline, "--image", fixtures.Image+":"+fixtures.V2)
	var gate v1alpha1.PolicyGate
	framework.Eventually(t, 2*time.Minute, "the "+holdGateName+" gate of "+bundle+" to block", func(ctx context.Context) (bool, string) {
		var list v1alpha1.PolicyGateList
		if err := a.e.Client.List(ctx, &list, client.InNamespace(a.ns),
			client.MatchingLabels{"kardinal.io/bundle": bundle, "kardinal.io/gate-template": holdGateName}); err != nil {
			return false, err.Error()
		}
		if len(list.Items) != 1 {
			return false, fmt.Sprintf("%d gate instances", len(list.Items))
		}
		gate = list.Items[0]
		c := meta.FindStatusCondition(gate.Status.Conditions, "Ready")
		return c != nil && c.Status == metav1.ConditionFalse, fmt.Sprintf("%+v", gate.Status)
	})
	return bundle, &gate
}

// blockedAt is when gate's current blocking episode started.
func blockedAt(g *v1alpha1.PolicyGate) time.Time {
	if c := meta.FindStatusCondition(g.Status.Conditions, "Ready"); c != nil {
		return c.LastTransitionTime.Time
	}
	return time.Time{}
}

// gateKey is the event key of gate's current block (docs/notifications.md
// §Status).
func gateKey(g *v1alpha1.PolicyGate) string {
	return "PolicyGate.Blocked/" + g.Name + "/" + blockedAt(g).UTC().Format(time.RFC3339)
}

// newHook creates NotificationHook ns/name posting events to url, with the
// authorizationHeader auth and pipelineSelector selector when set.
func newHook(t *testing.T, e *framework.Env, ns, name, url, auth, selector string, events ...v1alpha1.NotificationHookEventType) {
	t.Helper()
	h := &v1alpha1.NotificationHook{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.NotificationHookSpec{
			Webhook:          v1alpha1.NotificationWebhookConfig{URL: url, AuthorizationHeader: auth},
			Events:           events,
			PipelineSelector: selector,
		},
	}
	require.NoError(t, e.Client.Create(context.Background(), h), "create NotificationHook %s", name)
}

// getHook reads NotificationHook ns/name.
func getHook(t *testing.T, e *framework.Env, ns, name string) *v1alpha1.NotificationHook {
	t.Helper()
	var h v1alpha1.NotificationHook
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &h))
	return &h
}

// waitHook waits until cond holds for NotificationHook ns/name.
func waitHook(t *testing.T, e *framework.Env, ns, name, what string, cond func(v1alpha1.NotificationHookStatus) bool) *v1alpha1.NotificationHook {
	t.Helper()
	var h v1alpha1.NotificationHook
	framework.Eventually(t, 2*time.Minute, "NotificationHook "+name+": "+what, func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &h); err != nil {
			return false, err.Error()
		}
		return cond(h.Status), fmt.Sprintf("%+v", h.Status)
	})
	return &h
}

// annotate changes an annotation on obj (a Bundle, a hook, a PromotionStep):
// a write the controller sees, with no other effect. It is
// kardinal.io/force-recheck, the one change the graph-objects admission
// policy lets a user make on a PromotionStep.
func annotate(t *testing.T, e *framework.Env, obj client.Object) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, e.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj))
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	ann := obj.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann["kardinal.io/force-recheck"] = time.Now().UTC().Format(time.RFC3339Nano)
	obj.SetAnnotations(ann)
	require.NoError(t, e.Client.Patch(ctx, obj, patch))
}

// waitRecords waits until bucket has received at least n requests.
func waitRecords(t *testing.T, rcv *framework.Receiver, bucket string, n int, timeout time.Duration) []framework.Received {
	t.Helper()
	var recs []framework.Received
	framework.Eventually(t, timeout, fmt.Sprintf("%d deliveries to %s", n, bucket), func(ctx context.Context) (bool, string) {
		var err error
		if recs, err = rcv.Records(ctx, bucket); err != nil {
			return false, err.Error()
		}
		return len(recs) >= n, fmt.Sprintf("%d deliveries", len(recs))
	})
	return recs
}

// keepRecords checks that bucket keeps exactly n requests for d.
func keepRecords(t *testing.T, rcv *framework.Receiver, bucket string, n int, d time.Duration) {
	t.Helper()
	framework.Consistently(t, d, fmt.Sprintf("%s to keep %d deliveries", bucket, n), func(ctx context.Context) (bool, string) {
		recs, err := rcv.Records(ctx, bucket)
		if err != nil {
			return false, err.Error()
		}
		return len(recs) == n, fmt.Sprintf("%d deliveries", len(recs))
	})
}

// payload is the documented webhook body.
type payload struct {
	Event       string `json:"event"`
	Pipeline    string `json:"pipeline"`
	Bundle      string `json:"bundle"`
	Environment string `json:"environment"`
	Message     string `json:"message"`
	Timestamp   string `json:"timestamp"`
}

// decode checks that r is a JSON POST and returns its payload and the
// payload's keys, sorted.
func decode(t *testing.T, r framework.Received) (payload, []string) {
	t.Helper()
	assert.Equal(t, http.MethodPost, r.Method)
	assert.Equal(t, "application/json", r.Header("Content-Type"))
	var p payload
	require.NoError(t, json.Unmarshal([]byte(r.Body), &p), r.Body)
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(r.Body), &raw))
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ts, err := time.Parse(time.RFC3339, p.Timestamp)
	if assert.NoError(t, err, "timestamp is RFC3339") {
		assert.Equal(t, time.UTC, ts.Location(), "timestamp is UTC")
		assert.WithinDuration(t, r.Time, ts, 5*time.Second, "timestamp is the delivery time")
	}
	return p, keys
}

var (
	bundleKeys = []string{"bundle", "event", "message", "pipeline", "timestamp"}
	envKeys    = []string{"bundle", "environment", "event", "message", "pipeline", "timestamp"}
)

// TestNotify_BundleVerified checks Bundle.Verified: one JSON POST when the
// Bundle is Verified, with exactly the documented fields (event, pipeline,
// bundle, message, timestamp) and no environment, which Bundle events do not
// carry. A hook whose pipelineSelector names another Pipeline gets nothing.
//
// Covers NOTIF-01, NOTIF-PAYLOAD-01.
func TestNotify_BundleVerified(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	newHook(t, e, a.ns, "verified", rcv.URL(a.ns, "hook"), "", "", v1alpha1.NotificationEventBundleVerified)
	newHook(t, e, a.ns, "other", rcv.URL(a.ns+"-other", "hook"), "", "other", v1alpha1.NotificationEventBundleVerified)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", promoteTimeout)
	recs := waitRecords(t, rcv, a.ns, 1, time.Minute)
	keepRecords(t, rcv, a.ns, 1, 10*time.Second)

	r := recs[0]
	assert.Equal(t, "/"+a.ns+"/hook", r.Path)
	assert.Empty(t, r.Header("Authorization"), "no authorizationHeader, no Authorization header")
	p, keys := decode(t, r)
	assert.Equal(t, bundleKeys, keys, "Bundle events have no environment field")
	assert.Equal(t, payload{Event: "Bundle.Verified", Pipeline: pipelineName, Bundle: bundle,
		Message: "Bundle " + bundle + " is Verified", Timestamp: p.Timestamp}, p)

	h := getHook(t, e, a.ns, "verified")
	assert.Equal(t, "Bundle.Verified", h.Status.LastEvent)
	assert.Equal(t, "Bundle.Verified/"+bundle, h.Status.LastEventKey)
	assert.Equal(t, []string{"Bundle.Verified/" + bundle}, h.Status.ProcessedEventKeys)
	assert.Empty(t, rcv.MustRecords(t, a.ns+"-other"), "pipelineSelector: other")
}

// TestNotify_PromotionFailed checks PromotionStep.Failed and Bundle.Failed:
// a rollout that never becomes healthy fails the step at its health timeout
// and then the Bundle, and the hook gets one POST for each. The step event
// carries the environment and the step's message; the Bundle event has no
// environment.
//
// Covers NOTIF-02, NOTIF-04.
func TestNotify_PromotionFailed(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Health.Timeout = "1m"
	a.apply(t, p)
	events := []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventBundleFailed, v1alpha1.NotificationEventPromotionStepFailed}
	newHook(t, e, a.ns, "failures", rcv.URL(a.ns, "hook"), "", "", events...)
	newHook(t, e, a.ns, "other", rcv.URL(a.ns+"-other", "hook"), "", "other", events...)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", promoteTimeout)
	t.Logf("step %s failed: %s", ps.Name, ps.Status.Message)
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	recs := waitRecords(t, rcv, a.ns, 2, time.Minute)
	keepRecords(t, rcv, a.ns, 2, 10*time.Second)

	got := map[string]payload{}
	for _, r := range recs {
		p, keys := decode(t, r)
		got[p.Event] = p
		if p.Event == "PromotionStep.Failed" {
			assert.Equal(t, envKeys, keys)
		} else {
			assert.Equal(t, bundleKeys, keys)
		}
	}
	step := got["PromotionStep.Failed"]
	assert.Equal(t, payload{Event: "PromotionStep.Failed", Pipeline: pipelineName, Bundle: bundle, Environment: "test",
		Message: "PromotionStep " + ps.Name + " failed: " + ps.Status.Message, Timestamp: step.Timestamp}, step)
	// The shape of the docs/notifications.md example.
	assert.Contains(t, step.Message, " failed: health alarm via argocd (onHealthFailure=none): health check timeout after 1m0s; last result: ")
	b := got["Bundle.Failed"]
	assert.Equal(t, payload{Event: "Bundle.Failed", Pipeline: pipelineName, Bundle: bundle,
		Message: "Bundle " + bundle + " is Failed", Timestamp: b.Timestamp}, b)

	assert.ElementsMatch(t, []string{"Bundle.Failed/" + bundle, "PromotionStep.Failed/" + ps.Name},
		getHook(t, e, a.ns, "failures").Status.ProcessedEventKeys)
	assert.Empty(t, rcv.MustRecords(t, a.ns+"-other"), "pipelineSelector: other")
}

// TestNotify_GateBlocked checks PolicyGate.Blocked: a gate instance that
// blocks a Bundle sends one POST with the environment, and its
// re-evaluations while it keeps blocking send nothing more.
//
// Covers NOTIF-03.
func TestNotify_GateBlocked(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := holdApp(t, e, e.Namespace(t), pipelineName)
	newHook(t, e, a.ns, "gates", rcv.URL(a.ns, "hook"), "", "", v1alpha1.NotificationEventPolicyGateBlocked)

	bundle, gate := holdBundle(t, a, pipelineName)
	recs := waitRecords(t, rcv, a.ns, 1, time.Minute)
	p, keys := decode(t, recs[0])
	assert.Equal(t, envKeys, keys)
	assert.Equal(t, payload{Event: "PolicyGate.Blocked", Pipeline: pipelineName, Bundle: bundle, Environment: "test",
		Message: "PolicyGate " + gate.Name + " is blocking: " + gate.Status.Reason, Timestamp: p.Timestamp}, p)

	// recheckInterval is 10s: the gate is evaluated again, still blocking.
	keepRecords(t, rcv, a.ns, 1, 25*time.Second)
	var now v1alpha1.PolicyGate
	require.NoError(t, e.Client.Get(context.Background(), client.ObjectKeyFromObject(gate), &now))
	assert.True(t, now.Status.LastEvaluatedAt.After(gate.Status.LastEvaluatedAt.Time), "the gate was re-evaluated")
	assert.False(t, now.Status.Ready)
	assert.Equal(t, gateKey(gate), gateKey(&now), "the same blocking episode")
	h := getHook(t, e, a.ns, "gates")
	assert.Equal(t, gateKey(gate), h.Status.LastEventKey)
	assert.Equal(t, "PolicyGate.Blocked", h.Status.LastEvent)
}

// TestNotify_Authorization checks spec.webhook.authorizationHeader: the value
// is sent as the Authorization header exactly as written (no expansion of
// ${...}); a hook without it sends no Authorization header, and an
// incoming-webhook URL with its token in the path is posted to as is. The
// token in the path never shows in the hook's status, even in a failure.
//
// Covers NOTIF-AUTH-01.
func TestNotify_Authorization(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := holdApp(t, e, e.Namespace(t), pipelineName)
	holdBundle(t, a, pipelineName)
	token := "T0E2E/B0E2E/" + a.ns
	ev := v1alpha1.NotificationEventPolicyGateBlocked

	newHook(t, e, a.ns, "bearer", rcv.URL(a.ns+"-bearer", "kardinal-events"), "Bearer ${ALERT_TOKEN}", "", ev)
	newHook(t, e, a.ns, "slack", rcv.URL(a.ns+"-slack", "services/"+token), "", "", ev)
	newHook(t, e, a.ns, "refused", "http://127.0.0.1:8082/services/"+token, "", "", ev)

	bearer := waitRecords(t, rcv, a.ns+"-bearer", 1, time.Minute)[0]
	assert.Equal(t, "Bearer ${ALERT_TOKEN}", bearer.Header("Authorization"), "sent verbatim")
	assert.Len(t, bearer.Headers["Authorization"], 1)
	slack := waitRecords(t, rcv, a.ns+"-slack", 1, time.Minute)[0]
	assert.Equal(t, "/"+a.ns+"-slack/services/"+token, slack.Path)
	assert.Empty(t, slack.Header("Authorization"))
	for _, r := range []framework.Received{bearer, slack} {
		p, _ := decode(t, r)
		assert.Equal(t, "PolicyGate.Blocked", p.Event)
	}

	for _, name := range []string{"bearer", "slack"} {
		waitHook(t, e, a.ns, name, "the delivery recorded", func(s v1alpha1.NotificationHookStatus) bool { return s.LastSentAt != "" })
	}
	waitHook(t, e, a.ns, "refused", "the failed delivery recorded", func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts == 1 })
	for _, name := range []string{"bearer", "slack", "refused"} {
		status, err := json.Marshal(getHook(t, e, a.ns, name).Status)
		require.NoError(t, err)
		assert.NotContains(t, string(status), "B0E2E", "%s: status does not carry the URL", name)
		assert.NotContains(t, string(status), "ALERT_TOKEN", "%s: status does not carry the header", name)
	}
}

// TestNotify_PipelineSelector checks spec.pipelineSelector with two Pipelines
// in one namespace: a hook with the selector gets only the events of that
// Pipeline, and a hook without one gets both.
//
// Covers NOTIF-SEL-01.
func TestNotify_PipelineSelector(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := holdApp(t, e, e.Namespace(t), pipelineName, "other")
	ev := v1alpha1.NotificationEventPolicyGateBlocked
	newHook(t, e, a.ns, "all", rcv.URL(a.ns+"-all", "hook"), "", "", ev)
	newHook(t, e, a.ns, "only-other", rcv.URL(a.ns+"-other", "hook"), "", "other", ev)

	mine, _ := holdBundle(t, a, pipelineName)
	theirs, _ := holdBundle(t, a, "other")
	all := waitRecords(t, rcv, a.ns+"-all", 2, time.Minute)
	other := waitRecords(t, rcv, a.ns+"-other", 1, time.Minute)
	keepRecords(t, rcv, a.ns+"-all", 2, 10*time.Second)
	keepRecords(t, rcv, a.ns+"-other", 1, time.Second)

	got := map[string]string{}
	for _, r := range all {
		p, _ := decode(t, r)
		got[p.Pipeline] = p.Bundle
	}
	assert.Equal(t, map[string]string{pipelineName: mine, "other": theirs}, got)
	p, _ := decode(t, other[0])
	assert.Equal(t, "other", p.Pipeline)
	assert.Equal(t, theirs, p.Bundle)
}

// TestNotify_Egress checks the egress guard on hook URLs: loopback (by
// address and as localhost), link-local and unspecified destinations are
// refused before connecting, with "destination address is not allowed" in
// failureMessage; a 302 is a failed delivery and is not followed; a plain
// HTTP cluster Service is allowed.
//
// Covers NOTIF-EGRESS-01.
func TestNotify_Egress(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := holdApp(t, e, e.Namespace(t), pipelineName)
	_, gate := holdBundle(t, a, pipelineName)
	key := gateKey(gate)
	ev := v1alpha1.NotificationEventPolicyGateBlocked

	// 169.254.1.1 is link-local but not a metadata endpoint: the test must not
	// reach a real one if the guard were missing.
	refused := map[string][2]string{
		"loopback":    {"http://127.0.0.1:8082/api/v1/pipelines", "127.0.0.1 is loopback"},
		"localhost":   {"http://localhost:8082/api/v1/pipelines", " is loopback"},
		"link-local":  {"http://169.254.1.1/hook", "169.254.1.1 is link-local (cloud metadata)"},
		"unspecified": {"http://0.0.0.0:8082/api/v1/pipelines", "0.0.0.0 is unspecified"},
	}
	for name, c := range refused {
		newHook(t, e, a.ns, name, c[0], "", "", ev)
	}
	rcv.Fail(t, a.ns+"-redirect", http.StatusFound, 0, rcv.URL(a.ns+"-target", "hook"))
	newHook(t, e, a.ns, "redirect", rcv.URL(a.ns+"-redirect", "hook"), "", "", ev)
	newHook(t, e, a.ns, "allowed", rcv.URL(a.ns, "hook"), "", "", ev)

	attempt1 := fmt.Sprintf("delivery of %s failed (attempt 1 of 10): ", key)
	for name, c := range refused {
		h := waitHook(t, e, a.ns, name, "a failed delivery", func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts == 1 })
		assert.Contains(t, h.Status.FailureMessage, attempt1+"webhook request failed: ", name)
		assert.Contains(t, h.Status.FailureMessage, "destination address is not allowed: ", name)
		assert.Contains(t, h.Status.FailureMessage, c[1], name)
		assert.Empty(t, h.Status.LastSentAt, name)
	}

	h := waitHook(t, e, a.ns, "redirect", "a failed delivery", func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts == 1 })
	assert.Equal(t, attempt1+"webhook returned HTTP 302: webhook redirects are not followed", h.Status.FailureMessage)
	recs := rcv.MustRecords(t, a.ns+"-redirect")
	require.Len(t, recs, 1)
	assert.Equal(t, http.StatusFound, recs[0].Status)
	assert.Empty(t, rcv.MustRecords(t, a.ns+"-target"), "the redirect is not followed")

	waitRecords(t, rcv, a.ns, 1, time.Minute)
	h = waitHook(t, e, a.ns, "allowed", "the delivery recorded", func(s v1alpha1.NotificationHookStatus) bool { return s.LastEventKey == key })
	assert.Empty(t, h.Status.FailureMessage)
}

// TestNotify_RetryBackoff checks the retry schedule and the delivery status:
// with the receiver answering 503, the first attempt is followed by one 30s
// later and one 1m after that (no POST in between, although the namespace
// keeps changing), with failedAttempts, nextRetryAt and failureMessage
// recorded; the third attempt succeeds and clears them and records the last
// delivery.
//
// Covers NOTIF-RETRY-01, NOTIF-STATUS-01.
func TestNotify_RetryBackoff(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := holdApp(t, e, e.Namespace(t), pipelineName)
	bundle, gate := holdBundle(t, a, pipelineName)
	key := gateKey(gate)
	rcv.Fail(t, a.ns, http.StatusServiceUnavailable, 0, "")
	newHook(t, e, a.ns, "retry", rcv.URL(a.ns, "hook"), "", "", v1alpha1.NotificationEventPolicyGateBlocked)

	// attempt waits for delivery n, changing the Bundle on every poll: each
	// change reconciles the hook, and none may POST before nextRetryAt.
	attempt := func(n int, timeout time.Duration) framework.Received {
		t.Helper()
		var recs []framework.Received
		framework.Eventually(t, timeout, fmt.Sprintf("delivery attempt %d", n), func(ctx context.Context) (bool, string) {
			annotate(t, e, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: bundle, Namespace: a.ns}})
			var err error
			if recs, err = rcv.Records(ctx, a.ns); err != nil {
				return false, err.Error()
			}
			return len(recs) >= n, fmt.Sprintf("%d attempts", len(recs))
		})
		require.Len(t, recs, n, "one POST per attempt")
		return recs[n-1]
	}
	failed := func(n int32, at time.Time, delay time.Duration) {
		t.Helper()
		h := waitHook(t, e, a.ns, "retry", fmt.Sprintf("failed attempt %d recorded", n),
			func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts == n })
		assert.Equal(t, fmt.Sprintf("delivery of %s failed (attempt %d of 10): webhook returned HTTP 503", key, n),
			h.Status.FailureMessage)
		next, err := time.Parse(time.RFC3339, h.Status.NextRetryAt)
		if assert.NoError(t, err, "nextRetryAt") {
			assert.WithinDuration(t, at.Add(delay), next, 3*time.Second, "nextRetryAt after attempt %d", n)
		}
		assert.Empty(t, h.Status.LastSentAt, "nothing delivered yet")
		assert.Empty(t, h.Status.LastEventKey)
	}

	first := attempt(1, time.Minute)
	assert.Equal(t, http.StatusServiceUnavailable, first.Status)
	failed(1, first.Time, 30*time.Second)

	second := attempt(2, 2*time.Minute)
	gap := second.Time.Sub(first.Time)
	assert.GreaterOrEqual(t, gap, 29*time.Second, "the second attempt came %s after the first", gap)
	assert.Less(t, gap, 50*time.Second, "the second attempt came %s after the first", gap)
	failed(2, second.Time, time.Minute)

	rcv.Fail(t, a.ns, 0, 0, "")
	third := attempt(3, 3*time.Minute)
	gap = third.Time.Sub(second.Time)
	assert.GreaterOrEqual(t, gap, 59*time.Second, "the third attempt came %s after the second", gap)
	assert.Less(t, gap, 80*time.Second, "the third attempt came %s after the second", gap)
	assert.Equal(t, http.StatusOK, third.Status)
	p, _ := decode(t, third)
	assert.Equal(t, bundle, p.Bundle)

	h := waitHook(t, e, a.ns, "retry", "the delivery recorded", func(s v1alpha1.NotificationHookStatus) bool { return s.LastEventKey == key })
	sent, err := time.Parse(time.RFC3339, h.Status.LastSentAt)
	if assert.NoError(t, err, "lastSentAt") {
		assert.WithinDuration(t, third.Time, sent, 3*time.Second)
	}
	assert.Equal(t, "PolicyGate.Blocked", h.Status.LastEvent)
	assert.Equal(t, []string{key}, h.Status.ProcessedEventKeys)
	assert.Equal(t, int64(1), h.Status.ObservedGeneration)
	assert.Zero(t, h.Status.FailedAttempts)
	assert.Empty(t, h.Status.NextRetryAt)
	assert.Empty(t, h.Status.FailureMessage, "cleared on success")
	keepRecords(t, rcv, a.ns, 3, 10*time.Second)
}

// TestNotify_GiveUp checks the end of the retry schedule and the spec-edit
// reset. Nine failures take about 23 minutes of backoff, so after the first
// real failure the test records eight more in status.failedAttempts; the
// tenth attempt then gives up: failureMessage says so, the event counts as
// processed and is not sent again. A later event that fails is retried at
// once, well before its nextRetryAt, when the hook's URL is corrected.
//
// Covers NOTIF-GIVEUP-01.
func TestNotify_GiveUp(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := holdApp(t, e, e.Namespace(t), pipelineName, "other")
	_, gate := holdBundle(t, a, pipelineName)
	key := gateKey(gate)
	rcv.Fail(t, a.ns, http.StatusServiceUnavailable, 0, "")
	newHook(t, e, a.ns, "giveup", rcv.URL(a.ns, "hook"), "", "", v1alpha1.NotificationEventPolicyGateBlocked)
	waitRecords(t, rcv, a.ns, 1, time.Minute)
	h := waitHook(t, e, a.ns, "giveup", "the first failure recorded", func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts == 1 })

	orig := h.DeepCopy()
	h.Status.FailedAttempts = 9
	h.Status.NextRetryAt = ""
	require.NoError(t, e.Client.Status().Patch(context.Background(), h, client.MergeFrom(orig)))
	annotate(t, e, &v1alpha1.NotificationHook{ObjectMeta: metav1.ObjectMeta{Name: "giveup", Namespace: a.ns}})
	h = waitHook(t, e, a.ns, "giveup", "the tenth attempt to give up", func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts == 0 })
	assert.Equal(t, fmt.Sprintf("gave up on %s after 10 attempts: webhook returned HTTP 503", key), h.Status.FailureMessage)
	assert.Empty(t, h.Status.NextRetryAt)
	assert.Contains(t, h.Status.ProcessedEventKeys, key)
	assert.Empty(t, h.Status.LastEventKey, "never delivered")
	// The hook requeues 30s after giving up; nothing is left to send.
	keepRecords(t, rcv, a.ns, 2, 40*time.Second)

	_, later := holdBundle(t, a, "other")
	laterKey := gateKey(later)
	recs := waitRecords(t, rcv, a.ns, 3, time.Minute)
	p, _ := decode(t, recs[2])
	assert.Equal(t, "other", p.Pipeline, "the next event, not the given-up one")
	h = waitHook(t, e, a.ns, "giveup", "the later event's failure", func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts == 1 })
	assert.Contains(t, h.Status.FailureMessage, "delivery of "+laterKey+" failed (attempt 1 of 10)")
	next, err := time.Parse(time.RFC3339, h.Status.NextRetryAt)
	require.NoError(t, err)

	orig = h.DeepCopy()
	h.Spec.Webhook.URL = rcv.URL(a.ns+"-fixed", "hook")
	require.NoError(t, e.Client.Patch(context.Background(), h, client.MergeFrom(orig)))
	fixed := waitRecords(t, rcv, a.ns+"-fixed", 1, 20*time.Second)
	assert.True(t, fixed[0].Time.Before(next.Add(-10*time.Second)),
		"the spec edit retried at %s, before nextRetryAt %s", fixed[0].Time.Format(time.RFC3339), h.Status.NextRetryAt)
	p, _ = decode(t, fixed[0])
	assert.Equal(t, "other", p.Pipeline)
	h = waitHook(t, e, a.ns, "giveup", "the delivery recorded", func(s v1alpha1.NotificationHookStatus) bool { return s.LastEventKey == laterKey })
	assert.Zero(t, h.Status.FailedAttempts)
	assert.Empty(t, h.Status.FailureMessage)
	assert.Equal(t, int64(2), h.Status.ObservedGeneration)
	keepRecords(t, rcv, a.ns+"-fixed", 1, 10*time.Second)
	assert.Len(t, rcv.MustRecords(t, a.ns), 3)
}

// TestNotify_NewHookNoBackfill checks a hook created after events exist: it
// delivers only the newest one and records the older one as processed, then
// delivers the events that come later.
//
// Covers NOTIF-NEW-01.
func TestNotify_NewHookNoBackfill(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := holdApp(t, e, e.Namespace(t), pipelineName, "other")
	oldest, g1 := holdBundle(t, a, pipelineName)
	// Events are ordered by block time, which has one-second resolution.
	framework.Eventually(t, 10*time.Second, "a later second than the first block", func(context.Context) (bool, string) {
		return time.Now().After(blockedAt(g1).Add(time.Second)), blockedAt(g1).String()
	})
	newest, g2 := holdBundle(t, a, "other")
	require.True(t, blockedAt(g2).After(blockedAt(g1)), "the second gate blocked later")

	newHook(t, e, a.ns, "new", rcv.URL(a.ns, "hook"), "", "", v1alpha1.NotificationEventPolicyGateBlocked)
	recs := waitRecords(t, rcv, a.ns, 1, time.Minute)
	keepRecords(t, rcv, a.ns, 1, 15*time.Second)
	p, _ := decode(t, recs[0])
	assert.Equal(t, newest, p.Bundle, "only the newest existing event")
	h := getHook(t, e, a.ns, "new")
	assert.ElementsMatch(t, []string{gateKey(g1), gateKey(g2)}, h.Status.ProcessedEventKeys)
	assert.Equal(t, gateKey(g2), h.Status.LastEventKey)

	later, _ := holdBundle(t, a, pipelineName)
	recs = waitRecords(t, rcv, a.ns, 2, time.Minute)
	keepRecords(t, rcv, a.ns, 2, 10*time.Second)
	p, _ = decode(t, recs[1])
	assert.Equal(t, later, p.Bundle, "a later event is delivered")
	for _, r := range recs {
		p, _ := decode(t, r)
		assert.NotEqual(t, oldest, p.Bundle, "the older event is never sent")
	}
}
