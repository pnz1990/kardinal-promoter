//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// createHook creates NotificationHook ns/name with spec.
func createHook(t *testing.T, e *framework.Env, ns, name string, spec v1alpha1.NotificationHookSpec) {
	t.Helper()
	h := &v1alpha1.NotificationHook{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: spec}
	require.NoError(t, e.Client.Create(context.Background(), h), "create NotificationHook %s", name)
}

// hookCondition is condition typ of NotificationHook ns/name, or nil.
func hookCondition(t *testing.T, e *framework.Env, ns, name, typ string) *metav1.Condition {
	t.Helper()
	return meta.FindStatusCondition(getHook(t, e, ns, name).Status.Conditions, typ)
}

// eventKeys are the X-Kardinal-Event-Key headers of recs, in order.
func eventKeys(recs []framework.Received) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Header("X-Kardinal-Event-Key"))
	}
	return out
}

// TestNotify_SlackTeamsTemplate checks spec.format slack, teams and template
// against the receiver's Slack and Teams validators (hack/e2e/receiver),
// which answer like the real services: 200 "ok" for a valid Slack message,
// 202 for a valid Teams Workflows card, 400 otherwise. A json hook posted to
// a Slack path gets Slack's 400 no_text, the reason the formats exist.
//
// Covers NOTIF-SLACK-01, NOTIF-TEAMS-01, NOTIF-TEMPLATE-01.
func TestNotify_SlackTeamsTemplate(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := holdApp(t, e, e.Namespace(t), pipelineName)
	ev := []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventPolicyGateBlocked}
	hook := func(name, bucket, path string, format v1alpha1.NotificationHookFormat, tmpl *v1alpha1.NotificationTemplate) {
		createHook(t, e, a.ns, name, v1alpha1.NotificationHookSpec{
			Webhook: v1alpha1.NotificationWebhookConfig{URL: rcv.URL(bucket, path)},
			Events:  ev, Format: format, Template: tmpl,
		})
	}
	hook("slack", a.ns+"-slack", "slack/services/T0/B0/x", v1alpha1.NotificationFormatSlack, nil)
	hook("teams", a.ns+"-teams", "teams/workflows/abc/triggers/manual", v1alpha1.NotificationFormatTeams, nil)
	hook("template", a.ns+"-tmpl", "events", v1alpha1.NotificationFormatTemplate, &v1alpha1.NotificationTemplate{
		Body: `{"kind": "kardinal", "what": {{ json .Event }}, "where": {{ print .Pipeline "/" .Environment | json }}, ` +
			`"text": {{ json .Message }}, "id": {{ json .Key }}, "from": {{ json .Hook }}}`,
	})
	hook("raw-json-to-slack", a.ns+"-raw", "slack/services/T0/B0/y", "", nil)

	bundle, gate := holdBundle(t, a, pipelineName)
	key := gateKey(gate)
	msg := "PolicyGate " + gate.Name + " is blocking: " + gate.Status.Reason

	slack := waitRecords(t, rcv, a.ns+"-slack", 1, time.Minute)[0]
	assert.Equal(t, http.StatusOK, slack.Status, "Slack accepts it: %s", slack.Body)
	var sm struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type string `json:"type"`
			Text *struct {
				Type, Text string
			} `json:"text"`
			Fields []struct{ Text string } `json:"fields"`
		} `json:"blocks"`
	}
	require.NoError(t, json.Unmarshal([]byte(slack.Body), &sm), slack.Body)
	assert.Equal(t, "Promotion blocked by a policy gate: "+msg, sm.Text)
	require.Len(t, sm.Blocks, 4)
	assert.Equal(t, ":no_entry: Promotion blocked by a policy gate", sm.Blocks[0].Text.Text)
	assert.Contains(t, sm.Blocks[1].Text.Text, "PolicyGate "+gate.Name+" is blocking: ")
	assert.Equal(t, []struct{ Text string }{{"*Pipeline*\n" + pipelineName}, {"*Bundle*\n" + bundle}, {"*Environment*\ntest"}},
		sm.Blocks[2].Fields)
	assert.Equal(t, key, slack.Header("X-Kardinal-Event-Key"))
	assert.Equal(t, "PolicyGate.Blocked", slack.Header("X-Kardinal-Event"))

	teams := waitRecords(t, rcv, a.ns+"-teams", 1, time.Minute)[0]
	assert.Equal(t, http.StatusAccepted, teams.Status, "Teams accepts it: %s", teams.Body)
	var tm struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Version string `json:"version"`
				Body    []struct {
					Type, Text string
					Facts      []struct{ Title, Value string }
				} `json:"body"`
			} `json:"content"`
		} `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal([]byte(teams.Body), &tm), teams.Body)
	require.Len(t, tm.Attachments, 1)
	card := tm.Attachments[0].Content
	assert.Equal(t, "1.4", card.Version)
	require.Len(t, card.Body, 4)
	assert.Equal(t, "Promotion blocked by a policy gate", card.Body[0].Text)
	assert.Equal(t, msg, card.Body[1].Text)
	assert.Equal(t, []struct{ Title, Value string }{{"Pipeline", pipelineName}, {"Bundle", bundle}, {"Environment", "test"}},
		card.Body[2].Facts)

	tmpl := waitRecords(t, rcv, a.ns+"-tmpl", 1, time.Minute)[0]
	assert.Equal(t, "application/json", tmpl.Header("Content-Type"))
	var got map[string]string
	require.NoError(t, json.Unmarshal([]byte(tmpl.Body), &got), tmpl.Body)
	assert.Equal(t, map[string]string{"kind": "kardinal", "what": "PolicyGate.Blocked", "where": pipelineName + "/test",
		"text": msg, "id": key, "from": "template"}, got)

	raw := waitRecords(t, rcv, a.ns+"-raw", 1, time.Minute)[0]
	assert.Equal(t, http.StatusBadRequest, raw.Status, "the kardinal JSON payload is not a Slack message")
	for _, name := range []string{"slack", "teams", "template"} {
		h := waitHook(t, e, a.ns, name, "the delivery recorded", func(s v1alpha1.NotificationHookStatus) bool { return s.LastEventKey == key })
		assert.Empty(t, h.Status.FailureMessage, name)
		c := meta.FindStatusCondition(h.Status.Conditions, "Ready")
		if assert.NotNil(t, c, name) {
			assert.Equal(t, metav1.ConditionTrue, c.Status, name)
		}
	}
	h := waitHook(t, e, a.ns, "raw-json-to-slack", "the failed delivery", func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts == 1 })
	assert.Equal(t, fmt.Sprintf("delivery of %s failed (attempt 1 of 10): webhook returned HTTP 400", key), h.Status.FailureMessage)
}

// TestNotify_SecretRef checks spec.webhook.secretRef: the Secret's url key
// is the URL posted to (the spec has none) and its authorization key the
// Authorization header, and neither shows in status. A hook whose Secret is
// missing is Ready=False SecretNotFound and sends nothing; once the Secret
// exists the waiting event is delivered. A Secret without the
// kardinal.io/referenceable=true label is not used (Ready=False
// SecretNotReferenceable, nothing sent) until it is labeled. The deprecated
// authorizationHeader still delivers, with PlaintextCredential=True.
//
// Covers NOTIF-SECRET-01, NOTIF-SECRET-LABEL-01, NOTIF-PLAINTEXT-01.
func TestNotify_SecretRef(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	ctx := context.Background()
	a := holdApp(t, e, e.Namespace(t), pipelineName)
	ev := []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventPolicyGateBlocked}
	secret := func(name, bucket string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.ns,
			Labels: map[string]string{"kardinal.io/referenceable": "true"}}, StringData: map[string]string{
			"url":           rcv.URL(bucket, "services/T0E2E/B0E2E/SECRETPATH") + "\n",
			"authorization": "Bearer SECRETTOKEN-" + name,
		}}
	}
	require.NoError(t, e.Client.Create(ctx, secret("creds", a.ns)))
	createHook(t, e, a.ns, "secret", v1alpha1.NotificationHookSpec{
		Webhook: v1alpha1.NotificationWebhookConfig{SecretRef: &v1alpha1.NotificationSecretRef{Name: "creds"}}, Events: ev})
	createHook(t, e, a.ns, "late", v1alpha1.NotificationHookSpec{
		Webhook: v1alpha1.NotificationWebhookConfig{SecretRef: &v1alpha1.NotificationSecretRef{Name: "late-creds"}}, Events: ev})
	unlabeled := secret("unlabeled-creds", a.ns+"-unlabeled")
	unlabeled.Labels = nil
	require.NoError(t, e.Client.Create(ctx, unlabeled))
	createHook(t, e, a.ns, "unlabeled", v1alpha1.NotificationHookSpec{
		Webhook: v1alpha1.NotificationWebhookConfig{SecretRef: &v1alpha1.NotificationSecretRef{Name: "unlabeled-creds"}}, Events: ev})
	createHook(t, e, a.ns, "plaintext", v1alpha1.NotificationHookSpec{
		Webhook: v1alpha1.NotificationWebhookConfig{URL: rcv.URL(a.ns+"-plain", "hook"), AuthorizationHeader: "Bearer PLAINTOKEN"},
		Events:  ev})

	_, gate := holdBundle(t, a, pipelineName)
	key := gateKey(gate)

	r := waitRecords(t, rcv, a.ns, 1, time.Minute)[0]
	assert.Equal(t, "/"+a.ns+"/services/T0E2E/B0E2E/SECRETPATH", r.Path, "the Secret's url, trimmed")
	assert.Equal(t, "Bearer SECRETTOKEN-creds", r.Header("Authorization"))
	p, _ := decode(t, r)
	assert.Equal(t, "PolicyGate.Blocked", p.Event)

	plain := waitRecords(t, rcv, a.ns+"-plain", 1, time.Minute)[0]
	assert.Equal(t, "Bearer PLAINTOKEN", plain.Header("Authorization"), "the deprecated field still works")
	waitHook(t, e, a.ns, "plaintext", "the delivery recorded", func(s v1alpha1.NotificationHookStatus) bool { return s.LastEventKey == key })
	pc := hookCondition(t, e, a.ns, "plaintext", "PlaintextCredential")
	require.NotNil(t, pc, "PlaintextCredential condition")
	assert.Equal(t, metav1.ConditionTrue, pc.Status)
	assert.Equal(t, "AuthorizationHeaderInSpec", pc.Reason)
	assert.Nil(t, hookCondition(t, e, a.ns, "secret", "PlaintextCredential"))

	framework.Eventually(t, time.Minute, "hook late to report its missing Secret", func(context.Context) (bool, string) {
		c := hookCondition(t, e, a.ns, "late", "Ready")
		return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "SecretNotFound", fmt.Sprintf("%+v", c)
	})
	assert.Equal(t, "Secret late-creds named by spec.webhook.secretRef does not exist in namespace "+a.ns,
		hookCondition(t, e, a.ns, "late", "Ready").Message)
	assert.Empty(t, rcv.MustRecords(t, a.ns+"-late"))
	late := getHook(t, e, a.ns, "late")
	assert.Zero(t, late.Status.FailedAttempts, "a missing Secret uses up no attempt")
	assert.Empty(t, late.Status.LastSentAt)

	require.NoError(t, e.Client.Create(ctx, secret("late-creds", a.ns+"-late")))
	lr := waitRecords(t, rcv, a.ns+"-late", 1, 90*time.Second)[0]
	assert.Equal(t, "Bearer SECRETTOKEN-late-creds", lr.Header("Authorization"))
	waitHook(t, e, a.ns, "late", "the waiting event delivered", func(s v1alpha1.NotificationHookStatus) bool { return s.LastEventKey == key })
	assert.Equal(t, metav1.ConditionTrue, hookCondition(t, e, a.ns, "late", "Ready").Status)

	framework.Eventually(t, time.Minute, "hook unlabeled to refuse its Secret", func(context.Context) (bool, string) {
		c := hookCondition(t, e, a.ns, "unlabeled", "Ready")
		return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "SecretNotReferenceable", fmt.Sprintf("%+v", c)
	})
	keepRecords(t, rcv, a.ns+"-unlabeled", 0, 10*time.Second)
	assert.Zero(t, getHook(t, e, a.ns, "unlabeled").Status.FailedAttempts)
	var us corev1.Secret
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: "unlabeled-creds"}, &us))
	us.Labels = map[string]string{"kardinal.io/referenceable": "true"}
	require.NoError(t, e.Client.Update(ctx, &us))
	ur := waitRecords(t, rcv, a.ns+"-unlabeled", 1, 90*time.Second)[0]
	assert.Equal(t, "Bearer SECRETTOKEN-unlabeled-creds", ur.Header("Authorization"), "sent once labeled")
	waitHook(t, e, a.ns, "unlabeled", "the waiting event delivered", func(s v1alpha1.NotificationHookStatus) bool { return s.LastEventKey == key })

	for _, name := range []string{"secret", "late", "plaintext", "unlabeled"} {
		status, err := json.Marshal(getHook(t, e, a.ns, name).Status)
		require.NoError(t, err)
		for _, s := range []string{"SECRETTOKEN", "SECRETPATH", "B0E2E", "PLAINTOKEN"} {
			assert.NotContains(t, string(status), s, "%s: status", name)
		}
	}
	keepRecords(t, rcv, a.ns, 1, 10*time.Second)
}

// TestNotify_GateUnblocked checks PolicyGate.Unblocked: a gate that blocks
// and then allows sends Blocked and then Unblocked, keyed on each episode's
// transition time, and the promotion that follows sends Bundle.Verified.
//
// Covers NOTIF-UNBLOCK-01.
func TestNotify_GateUnblocked(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := newArgoApp(t, e, "test")
	e.CreateGate(t, framework.Gate(a.ns, "needs-open-label", "test", openExpr, recheck))
	a.apply(t, a.pipeline(nil))
	createHook(t, e, a.ns, "gates", v1alpha1.NotificationHookSpec{
		Webhook: v1alpha1.NotificationWebhookConfig{URL: rcv.URL(a.ns, "hook")},
		Events: []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventPolicyGateBlocked,
			v1alpha1.NotificationEventPolicyGateUnblocked, v1alpha1.NotificationEventBundleVerified},
	})

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	blocked := e.WaitGateReady(t, a.ns, bundle, "test", "needs-open-label", false, "= false", gateTimeout)
	waitRecords(t, rcv, a.ns, 1, time.Minute)

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	allowed := e.WaitGateReady(t, a.ns, bundle, "test", "needs-open-label", true, "= true", gateTimeout)
	c := meta.FindStatusCondition(allowed.Status.Conditions, "Ready")
	require.NotNil(t, c)
	assert.Equal(t, "Unblocked", c.Reason, "the gate records that it allows after a block")
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", promoteTimeout)
	recs := waitRecords(t, rcv, a.ns, 3, time.Minute)
	keepRecords(t, rcv, a.ns, 3, 15*time.Second)

	assert.Equal(t, []string{
		gateKey(blocked),
		"PolicyGate.Unblocked/" + allowed.Name + "/" + c.LastTransitionTime.UTC().Format(time.RFC3339),
		"Bundle.Verified/" + bundle,
	}, eventKeys(recs))
	p, keys := decode(t, recs[1])
	assert.Equal(t, envKeys, keys)
	assert.Equal(t, payload{Event: "PolicyGate.Unblocked", Pipeline: pipelineName, Bundle: bundle, Environment: "test",
		Message: "PolicyGate " + allowed.Name + " is no longer blocking: " + allowed.Status.Reason, Timestamp: p.Timestamp}, p)
}

// TestNotify_PROpenedAndWaitingForApproval checks the PR events of a
// pr-review environment: PromotionStep.PROpened and
// PromotionStep.WaitingForApproval, each once, carrying the PR's URL in
// prURL; after the merge the Bundle is Verified and nothing more is sent.
//
// Covers NOTIF-PR-01.
func TestNotify_PROpenedAndWaitingForApproval(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))
	createHook(t, e, a.ns, "prs", v1alpha1.NotificationHookSpec{
		Webhook: v1alpha1.NotificationWebhookConfig{URL: rcv.URL(a.ns, "hook")},
		Events: []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventPromotionStepPROpened,
			v1alpha1.NotificationEventPromotionStepWaitingForApproval},
	})

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, bundle, "test")
	require.NotEmpty(t, ps.Status.PRURL)
	recs := waitRecords(t, rcv, a.ns, 2, time.Minute)
	assert.Equal(t, []string{"PromotionStep.PROpened/" + ps.Name, "PromotionStep.WaitingForApproval/" + ps.Name}, eventKeys(recs))

	type prPayload struct {
		Event, Pipeline, Bundle, Environment, Message, PRURL string
	}
	var got []prPayload
	for _, r := range recs {
		var p prPayload
		require.NoError(t, json.Unmarshal([]byte(r.Body), &p), r.Body)
		got = append(got, p)
	}
	assert.Equal(t, []prPayload{
		{"PromotionStep.PROpened", pipelineName, bundle, "test",
			"PromotionStep " + ps.Name + " opened a pull request for test: " + ps.Status.PRURL, ps.Status.PRURL},
		{"PromotionStep.WaitingForApproval", pipelineName, bundle, "test",
			"PromotionStep " + ps.Name + " is waiting for approval: merge its pull request to promote to test: " + ps.Status.PRURL,
			ps.Status.PRURL},
	}, got)
	assert.True(t, strings.HasSuffix(ps.Status.PRURL, fmt.Sprintf("/%d", pr.Number)), "prURL %s is PR #%d", ps.Status.PRURL, pr.Number)

	a.merge(t, pr)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", promoteTimeout)
	keepRecords(t, rcv, a.ns, 2, 15*time.Second)
}

// TestNotify_Superseded checks Bundle.Superseded: a Bundle held at a gate is
// superseded by a newer one and the hook says so once, with no environment.
//
// Covers NOTIF-SUPERSEDE-01.
func TestNotify_Superseded(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := holdApp(t, e, e.Namespace(t), pipelineName)
	createHook(t, e, a.ns, "superseded", v1alpha1.NotificationHookSpec{
		Webhook: v1alpha1.NotificationWebhookConfig{URL: rcv.URL(a.ns, "hook")},
		Events:  []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventBundleSuperseded},
	})

	older, _ := holdBundle(t, a, pipelineName)
	newer := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitBundlePhase(t, a.ns, older, "Superseded", time.Minute)
	recs := waitRecords(t, rcv, a.ns, 1, time.Minute)
	keepRecords(t, rcv, a.ns, 1, 15*time.Second)
	p, keys := decode(t, recs[0])
	assert.Equal(t, bundleKeys, keys)
	assert.Equal(t, payload{Event: "Bundle.Superseded", Pipeline: pipelineName, Bundle: older,
		Message: "Bundle " + older + " is Superseded", Timestamp: p.Timestamp}, p)
	assert.Equal(t, "Bundle.Superseded/"+older, recs[0].Header("X-Kardinal-Event-Key"))
	assert.NotEqual(t, older, newer)
}

// TestNotify_Rollback checks Bundle.RollbackStarted and Bundle.RolledBack:
// `kardinal rollback` of test creates a rollback Bundle; the hook gets
// RollbackStarted and, once it is Verified, RolledBack, both with the
// environment and what it restores. The Bundles promoted before it send
// neither.
//
// Covers NOTIF-ROLLBACK-01.
func TestNotify_Rollback(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	createHook(t, e, a.ns, "rollbacks", v1alpha1.NotificationHookSpec{
		Webhook: v1alpha1.NotificationWebhookConfig{URL: rcv.URL(a.ns, "hook")},
		Events: []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventBundleRollbackStarted,
			v1alpha1.NotificationEventBundleRolledBack},
	})

	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	rbVerified(t, a, b2, "test")
	b3 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	rbVerified(t, a, b3, "test")
	assert.Empty(t, rcv.MustRecords(t, a.ns), "no rollback yet")

	_, rb := rbRollback(t, a, "test")
	rbVerified(t, a, rb, "test")
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("test"), fixtures.Image+":"+fixtures.V2, syncTimeout)
	recs := waitRecords(t, rcv, a.ns, 2, time.Minute)
	keepRecords(t, rcv, a.ns, 2, 15*time.Second)
	assert.Equal(t, []string{"Bundle.RollbackStarted/" + rb, "Bundle.RolledBack/" + rb}, eventKeys(recs))
	detail := fmt.Sprintf("it restores Bundle %s in test (rolling back %s)", b2, b3)
	for i, want := range []payload{
		{Event: "Bundle.RollbackStarted", Message: "Rollback Bundle " + rb + " started: " + detail},
		{Event: "Bundle.RolledBack", Message: "Rollback Bundle " + rb + " is Verified: " + detail},
	} {
		p, keys := decode(t, recs[i])
		assert.Equal(t, envKeys, keys)
		want.Pipeline, want.Bundle, want.Environment, want.Timestamp = pipelineName, rb, "test", p.Timestamp
		assert.Equal(t, want, p)
	}
}
