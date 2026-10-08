// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/notificationhook"
)

// request is one POST a recordingServer received.
type request struct {
	path   string
	header http.Header
	body   string
}

// recordingServer records every request, answering 200.
type recordingServer struct {
	mu   sync.Mutex
	reqs []request
}

func (s *recordingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.reqs = append(s.reqs, request{path: r.URL.Path, header: r.Header.Clone(), body: string(b)})
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *recordingServer) all() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]request(nil), s.reqs...)
}

func newRecorder(t *testing.T) (*recordingServer, string) {
	t.Helper()
	srv := &recordingServer{}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts.URL
}

func readyCondition(t *testing.T, h v1alpha1.NotificationHook) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(h.Status.Conditions, "Ready")
	require.NotNil(t, c, "Ready condition")
	return c
}

func jsonMap(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(body), &m), body)
	return m
}

// TestDelivery_Formats checks each spec.format: the Content-Type, the event
// headers sent with every format, and the body shape Slack, Teams and a
// template receiver expect.
func TestDelivery_Formats(t *testing.T) {
	tests := []struct {
		name     string
		format   v1alpha1.NotificationHookFormat
		template *v1alpha1.NotificationTemplate
		wantCT   string
		check    func(t *testing.T, body string)
	}{
		{name: "default is json", wantCT: "application/json", check: func(t *testing.T, body string) {
			m := jsonMap(t, body)
			assert.Equal(t, "Bundle.Failed", m["event"])
			assert.Equal(t, "app-v0", m["bundle"])
			assert.Equal(t, "Bundle app-v0 is Failed", m["message"])
			assert.NotContains(t, m, "prURL", "no PR on a Bundle event")
		}},
		{name: "slack", format: v1alpha1.NotificationFormatSlack, wantCT: "application/json", check: func(t *testing.T, body string) {
			m := jsonMap(t, body)
			assert.Equal(t, "Bundle failed: Bundle app-v0 is Failed", m["text"], "fallback text")
			blocks, ok := m["blocks"].([]interface{})
			require.True(t, ok, "blocks")
			types := []string{}
			for _, b := range blocks {
				types = append(types, b.(map[string]interface{})["type"].(string))
			}
			assert.Equal(t, []string{"header", "section", "section", "context"}, types)
			header := blocks[0].(map[string]interface{})["text"].(map[string]interface{})
			assert.Equal(t, "plain_text", header["type"])
			assert.Equal(t, ":x: Bundle failed", header["text"])
			fields := blocks[2].(map[string]interface{})["fields"].([]interface{})
			assert.Equal(t, "*Pipeline*\napp", fields[0].(map[string]interface{})["text"])
			assert.Equal(t, "*Bundle*\napp-v0", fields[1].(map[string]interface{})["text"])
			ctx := blocks[3].(map[string]interface{})["elements"].([]interface{})[0].(map[string]interface{})
			assert.Contains(t, ctx["text"], "Bundle.Failed/app-v0")
		}},
		{name: "teams", format: v1alpha1.NotificationFormatTeams, wantCT: "application/json", check: func(t *testing.T, body string) {
			m := jsonMap(t, body)
			assert.Equal(t, "message", m["type"])
			att := m["attachments"].([]interface{})
			require.Len(t, att, 1)
			a := att[0].(map[string]interface{})
			assert.Equal(t, "application/vnd.microsoft.card.adaptive", a["contentType"])
			assert.Contains(t, a, "contentUrl")
			card := a["content"].(map[string]interface{})
			assert.Equal(t, "AdaptiveCard", card["type"])
			assert.Equal(t, "1.4", card["version"])
			assert.Equal(t, "http://adaptivecards.io/schemas/adaptive-card.json", card["$schema"])
			cbody := card["body"].([]interface{})
			assert.Equal(t, "Bundle failed", cbody[0].(map[string]interface{})["text"])
			assert.Equal(t, "Attention", cbody[0].(map[string]interface{})["color"])
			assert.Equal(t, "Bundle app-v0 is Failed", cbody[1].(map[string]interface{})["text"])
			facts := cbody[2].(map[string]interface{})["facts"].([]interface{})
			assert.Equal(t, map[string]interface{}{"title": "Pipeline", "value": "app"}, facts[0])
			assert.NotContains(t, card, "actions", "no PR, no button")
		}},
		{name: "template json", format: v1alpha1.NotificationFormatTemplate,
			template: &v1alpha1.NotificationTemplate{Body: `{"summary": {{ json .Message }}, "key": {{ json .Key }}, "hook": "{{ .Namespace }}/{{ .Hook }}", "who": {{ .Pipeline | upper | json }}}`},
			wantCT:   "application/json", check: func(t *testing.T, body string) {
				assert.Equal(t, map[string]interface{}{"summary": "Bundle app-v0 is Failed", "key": "Bundle.Failed/app-v0",
					"hook": "default/hook", "who": "APP"}, jsonMap(t, body))
			}},
		{name: "template text", format: v1alpha1.NotificationFormatTemplate,
			template: &v1alpha1.NotificationTemplate{Body: `{{ .Event }} {{ printf "%-8s" .Pipeline }}|{{ if .Environment }}env{{ else }}no env{{ end }}|{{ truncate 5 .Bundle }}`,
				ContentType: "text/plain; charset=utf-8"},
			wantCT: "text/plain; charset=utf-8", check: func(t *testing.T, body string) {
				assert.Equal(t, "Bundle.Failed app     |no env|app-…", body)
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, url := newRecorder(t)
			hook := newHook(url+"/hook", v1alpha1.NotificationEventBundleFailed)
			hook.Spec.Format, hook.Spec.Template = tt.format, tt.template
			f := newFixture(t, hook, failedBundle("app-v0", saturday))
			f.reconcileHook()

			reqs := srv.all()
			require.Len(t, reqs, 1)
			r := reqs[0]
			assert.Equal(t, tt.wantCT, r.header.Get("Content-Type"))
			assert.Equal(t, "Bundle.Failed", r.header.Get("X-Kardinal-Event"))
			assert.Equal(t, "Bundle.Failed/app-v0", r.header.Get("X-Kardinal-Event-Key"))
			tt.check(t, r.body)
			h := f.hook()
			assert.Equal(t, metav1.ConditionTrue, readyCondition(t, h).Status)
			assert.Equal(t, "Bundle.Failed/app-v0", h.Status.LastEventKey)
		})
	}
}

// TestDelivery_SlackEscapesAndPRButton: Slack mrkdwn control characters in
// the message are escaped, and an event with a PR gets a button to it; the
// Teams card gets an Action.OpenUrl.
func TestDelivery_SlackEscapesAndPRButton(t *testing.T) {
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "app-app-v1-prod", Namespace: ns, CreationTimestamp: metav1.NewTime(saturday)},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: "prod"},
		Status:     v1alpha1.PromotionStepStatus{State: "Failed", Message: "health <alarm> & co", PRURL: "https://git.example/pr/7"},
	}
	for _, format := range []v1alpha1.NotificationHookFormat{v1alpha1.NotificationFormatSlack, v1alpha1.NotificationFormatTeams} {
		t.Run(string(format), func(t *testing.T) {
			srv, url := newRecorder(t)
			hook := newHook(url, v1alpha1.NotificationEventPromotionStepPROpened)
			hook.Spec.Format = format
			f := newFixture(t, hook, step.DeepCopy())
			f.reconcileHook()
			reqs := srv.all()
			require.Len(t, reqs, 1)
			body := reqs[0].body
			if format == v1alpha1.NotificationFormatSlack {
				assert.Contains(t, body, `"type":"button"`)
				assert.Contains(t, body, `"url":"https://git.example/pr/7"`)
				assert.Contains(t, body, "*Environment*\\nprod")
				return
			}
			assert.Contains(t, body, `"actions":[{"type":"Action.OpenUrl","title":"Open pull request","url":"https://git.example/pr/7"}]`)
		})
	}

	srv, url := newRecorder(t)
	hook := newHook(url, v1alpha1.NotificationEventPromotionStepFailed)
	hook.Spec.Format = v1alpha1.NotificationFormatSlack
	f := newFixture(t, hook, step.DeepCopy())
	f.reconcileHook()
	reqs := srv.all()
	require.Len(t, reqs, 1)
	m := jsonMap(t, reqs[0].body)
	section := m["blocks"].([]interface{})[1].(map[string]interface{})["text"].(map[string]interface{})
	assert.Equal(t, "PromotionStep app-app-v1-prod failed: health &lt;alarm&gt; &amp; co", section["text"])
	assert.Equal(t, "Promotion step failed: PromotionStep app-app-v1-prod failed: health <alarm> & co", m["text"],
		"the fallback text is plain text, not mrkdwn")
}

func webhookSecret(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

// TestDelivery_SecretRef checks spec.webhook.secretRef: the Secret's url and
// authorization keys are used (trimmed), a missing Secret or key makes the
// hook Ready=False without counting an attempt or dropping the event, and a
// rotated value is used for the next delivery.
func TestDelivery_SecretRef(t *testing.T) {
	srv, url := newRecorder(t)
	hook := newHook("", v1alpha1.NotificationEventBundleFailed)
	hook.Spec.Webhook.SecretRef = &v1alpha1.NotificationSecretRef{Name: "hook-creds"}
	f := newFixture(t, hook, failedBundle("app-v0", saturday))

	res := f.reconcileHook()
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "a hook that cannot deliver is checked again")
	h := f.hook()
	c := readyCondition(t, h)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, "SecretNotFound", c.Reason)
	assert.Equal(t, "Secret hook-creds named by spec.webhook.secretRef does not exist in namespace default", c.Message)
	assert.Zero(t, h.Status.FailedAttempts, "no attempt is counted")
	assert.Empty(t, h.Status.ProcessedEventKeys, "the event waits")
	assert.Empty(t, srv.all())

	f.create(webhookSecret("hook-creds", map[string]string{"other": "x"}))
	f.reconcileHook()
	c = readyCondition(t, f.hook())
	assert.Equal(t, "SecretKeyMissing", c.Reason)
	assert.Equal(t, `Secret hook-creds has neither an "authorization" nor a "url" key`, c.Message)

	var secret corev1.Secret
	require.NoError(t, f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "hook-creds"}, &secret))
	secret.Data = map[string][]byte{"authorization": []byte("Bearer s3cret\n")}
	require.NoError(t, f.c.Update(context.Background(), &secret))
	f.reconcileHook()
	c = readyCondition(t, f.hook())
	assert.Equal(t, "URLMissing", c.Reason, "no url in the spec or the Secret")

	secret.Data["url"] = []byte(url + "/services/T0/B0/TOKEN\n")
	require.NoError(t, f.c.Update(context.Background(), &secret))
	f.reconcileHook()
	reqs := srv.all()
	require.Len(t, reqs, 1, "the waiting event is delivered once the Secret is fixed")
	assert.Equal(t, "/services/T0/B0/TOKEN", reqs[0].path)
	assert.Equal(t, "Bearer s3cret", reqs[0].header.Get("Authorization"))
	h = f.hook()
	c = readyCondition(t, h)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Nil(t, meta.FindStatusCondition(h.Status.Conditions, "PlaintextCredential"))
	status, err := json.Marshal(h.Status)
	require.NoError(t, err)
	assert.NotContains(t, string(status), "s3cret")
	assert.NotContains(t, string(status), "TOKEN")

	// Rotation: the next event uses the new header.
	secret.Data["authorization"] = []byte("Bearer rotated")
	require.NoError(t, f.c.Update(context.Background(), &secret))
	f.create(failedBundle("app-v2", saturday.Add(time.Hour)))
	f.reconcileHook()
	reqs = srv.all()
	require.Len(t, reqs, 2)
	assert.Equal(t, "Bearer rotated", reqs[1].header.Get("Authorization"))
}

// TestDelivery_PlaintextCredentialCondition: the deprecated
// authorizationHeader still delivers, with PlaintextCredential=True; the
// condition goes away when the field is removed.
func TestDelivery_PlaintextCredentialCondition(t *testing.T) {
	srv, url := newRecorder(t)
	hook := newHook(url, v1alpha1.NotificationEventBundleFailed)
	hook.Spec.Webhook.AuthorizationHeader = "Bearer plain"
	f := newFixture(t, hook, failedBundle("app-v0", saturday))
	f.reconcileHook()
	reqs := srv.all()
	require.Len(t, reqs, 1)
	assert.Equal(t, "Bearer plain", reqs[0].header.Get("Authorization"))
	h := f.hook()
	assert.Equal(t, metav1.ConditionTrue, readyCondition(t, h).Status)
	pc := meta.FindStatusCondition(h.Status.Conditions, "PlaintextCredential")
	require.NotNil(t, pc)
	assert.Equal(t, metav1.ConditionTrue, pc.Status)
	assert.Equal(t, "AuthorizationHeaderInSpec", pc.Reason)
	assert.Contains(t, pc.Message, "deprecated")
	assert.NotContains(t, pc.Message, "plain\"")

	orig := h.DeepCopy()
	h.Spec.Webhook.AuthorizationHeader = ""
	require.NoError(t, f.c.Patch(context.Background(), &h, client.MergeFrom(orig)))
	f.reconcileHook()
	assert.Nil(t, meta.FindStatusCondition(f.hook().Status.Conditions, "PlaintextCredential"))
}

// TestDelivery_InvalidTemplate: a body that does not parse, or uses range,
// define, block or template, makes the hook Ready=False InvalidTemplate.
func TestDelivery_InvalidTemplate(t *testing.T) {
	for name, body := range map[string]string{
		"parse error":   `{{ .Event `,
		"range":         `{{ range 1000000000 }}x{{ end }}`,
		"define":        `{{ define "x" }}a{{ end }}{{ .Event }}`,
		"self template": `{{ template "body" . }}`,
		"block":         `{{ block "b" . }}x{{ end }}`,
		"unknown func":  `{{ env "HOME" }}`,
		"nested range":  `{{ if .Event }}{{ range .Event }}{{ end }}{{ end }}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, url := newRecorder(t)
			hook := newHook(url, v1alpha1.NotificationEventBundleFailed)
			hook.Spec.Format = v1alpha1.NotificationFormatTemplate
			hook.Spec.Template = &v1alpha1.NotificationTemplate{Body: body}
			f := newFixture(t, hook, failedBundle("app-v0", saturday))
			f.reconcileHook()
			c := readyCondition(t, f.hook())
			assert.Equal(t, metav1.ConditionFalse, c.Status)
			assert.Equal(t, "InvalidTemplate", c.Reason)
			assert.True(t, strings.HasPrefix(c.Message, "spec.template.body: "), c.Message)
			assert.Empty(t, srv.all())
		})
	}
}

// TestDelivery_TemplateRenderFailureGivesUpAtOnce: a template that renders
// invalid JSON, a missing field, or a body over 64 KiB for an event cannot
// succeed on a retry, so the event is given up on at once (failureMessage),
// and the next event is still delivered.
func TestDelivery_TemplateRenderFailureGivesUpAtOnce(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"invalid JSON": {`{"text": "{{ .Message }}`, "rendered body is not valid JSON (content type application/json); quote values with {{ json .Field }}"},
		"missing field": {`{{ .Nope }}`, "render: "},
		"too large": {`{{ printf "%999s" .Message }}{{ printf "%999s" .Message }}` + strings.Repeat(`{{ printf "%999s" .Message }}`, 70),
			"rendered body is over 65536 bytes"},
		"huge width": {`{{ printf "%9999999d" 1 }}`, "printf: width or precision 9999999 is over 999"},
		"star width": {`{{ printf "%*d" 99999999 1 }}`, "printf: * widths are not allowed"},
	} {
		t.Run(name, func(t *testing.T) {
			srv, url := newRecorder(t)
			hook := newHook(url, v1alpha1.NotificationEventBundleFailed)
			hook.Spec.Format = v1alpha1.NotificationFormatTemplate
			hook.Spec.Template = &v1alpha1.NotificationTemplate{Body: tc.body}
			f := newFixture(t, hook, failedBundle("app-v0", saturday))
			f.reconcileHook()
			h := f.hook()
			assert.Equal(t, metav1.ConditionTrue, readyCondition(t, h).Status, "the template parses")
			assert.Contains(t, h.Status.FailureMessage, "gave up on Bundle.Failed/app-v0: template")
			assert.Contains(t, h.Status.FailureMessage, tc.want)
			assert.Zero(t, h.Status.FailedAttempts)
			assert.Contains(t, h.Status.ProcessedEventKeys, "Bundle.Failed/app-v0")
			assert.Empty(t, srv.all())
		})
	}
}

// TestDelivery_NewEvents checks the events added in v0.10.0 and their keys,
// payloads and order: Bundle.Superseded, the rollback pair, the PR pair and
// PolicyGate.Unblocked (from the real PolicyGate reconciler). Reconciling
// again, also with a new reconciler (a controller restart), sends nothing
// more.
func TestDelivery_NewEvents(t *testing.T) {
	srv, url := newRecorder(t)
	hook := newHook(url,
		v1alpha1.NotificationEventBundleSuperseded,
		v1alpha1.NotificationEventBundleRollbackStarted,
		v1alpha1.NotificationEventBundleRolledBack,
		v1alpha1.NotificationEventPromotionStepPROpened,
		v1alpha1.NotificationEventPromotionStepWaitingForApproval,
		v1alpha1.NotificationEventPolicyGateBlocked,
		v1alpha1.NotificationEventPolicyGateUnblocked,
	)
	f := newFixture(t, hook, gateInstance("app-v1-prod-no-weekend"))
	f.reconcileHook() // created before any event: nothing to backfill

	f.create(&v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v0", Namespace: ns, CreationTimestamp: metav1.NewTime(saturday.Add(-2 * time.Hour))},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
		Status:     v1alpha1.BundleStatus{Phase: "Superseded"},
	})
	f.create(&v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-rollback-x1", Namespace: ns, CreationTimestamp: metav1.NewTime(saturday.Add(-time.Hour)),
			Labels:      map[string]string{"kardinal.io/rollback": "true"},
			Annotations: map[string]string{"kardinal.io/rollback-from": "app-v3"}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "app",
			Intent:     &v1alpha1.BundleIntent{TargetEnvironment: "prod"},
			Provenance: &v1alpha1.BundleProvenance{RollbackOf: "app-v2"}},
		Status: v1alpha1.BundleStatus{Phase: "Verified"},
	})
	f.create(&v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "app-app-v1-prod", Namespace: ns, CreationTimestamp: metav1.NewTime(saturday.Add(-30 * time.Minute))},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: "prod"},
		Status:     v1alpha1.PromotionStepStatus{State: "WaitingForMerge", PRURL: "https://git.example/pr/9"},
	})
	f.evalGate("app-v1-prod-no-weekend", saturday) // blocks
	f.reconcileHook()
	f.evalGate("app-v1-prod-no-weekend", monday) // allows again
	f.reconcileHook()
	f.evalGate("app-v1-prod-no-weekend", monday.Add(5*time.Minute)) // still allowed: no new event
	f.reconcileHook()

	type got struct{ key, env, pr, msg string }
	var gots []got
	for _, r := range srv.all() {
		var p struct{ Event, Bundle, Environment, PRURL, Message string }
		require.NoError(t, json.Unmarshal([]byte(r.body), &p))
		gots = append(gots, got{r.header.Get("X-Kardinal-Event-Key"), p.Environment, p.PRURL, p.Message})
	}
	blocked := "PolicyGate.Blocked/app-v1-prod-no-weekend/" + saturday.Format(time.RFC3339)
	unblocked := "PolicyGate.Unblocked/app-v1-prod-no-weekend/" + monday.Format(time.RFC3339)
	assert.Equal(t, []got{
		{"Bundle.Superseded/app-v0", "", "", "Bundle app-v0 is Superseded"},
		{"Bundle.RollbackStarted/app-rollback-x1", "prod", "",
			"Rollback Bundle app-rollback-x1 started: it restores Bundle app-v2 in prod (rolling back app-v3)"},
		{"Bundle.RolledBack/app-rollback-x1", "prod", "",
			"Rollback Bundle app-rollback-x1 is Verified: it restores Bundle app-v2 in prod (rolling back app-v3)"},
		{"PromotionStep.PROpened/app-app-v1-prod", "prod", "https://git.example/pr/9",
			"PromotionStep app-app-v1-prod opened a pull request for prod: https://git.example/pr/9"},
		{"PromotionStep.WaitingForApproval/app-app-v1-prod", "prod", "https://git.example/pr/9",
			"PromotionStep app-app-v1-prod is waiting for approval: merge its pull request to promote to prod: https://git.example/pr/9"},
		{blocked, "prod", "", ""},
		{unblocked, "prod", "", ""},
	}, func() []got {
		// Gate messages carry the evaluator's reason; compare them by prefix.
		out := append([]got(nil), gots...)
		for i := range out {
			if strings.HasPrefix(out[i].key, "PolicyGate.") {
				out[i].msg = ""
			}
		}
		return out
	}())
	require.Len(t, gots, 7)
	assert.True(t, strings.HasPrefix(gots[5].msg, "PolicyGate app-v1-prod-no-weekend is blocking: "), gots[5].msg)
	assert.True(t, strings.HasPrefix(gots[6].msg, "PolicyGate app-v1-prod-no-weekend is no longer blocking: "), gots[6].msg)

	// Idempotent: again, and after a restart (new reconciler, same status).
	f.reconcileHook()
	restarted := &notificationhook.Reconciler{Client: f.c, HTTPClient: loopbackClient, NowFn: func() time.Time { return monday }}
	_, err := restarted.Reconcile(context.Background(), reqFor(ns, "hook"))
	require.NoError(t, err)
	assert.Len(t, srv.all(), 7, "no event is sent twice")
}

// TestDelivery_FirstAllowIsNotUnblocked: a gate that allows on its first
// evaluation sends no PolicyGate.Unblocked.
func TestDelivery_FirstAllowIsNotUnblocked(t *testing.T) {
	srv, url := newRecorder(t)
	f := newFixture(t, newHook(url, v1alpha1.NotificationEventPolicyGateUnblocked), gateInstance("app-v1-prod-no-weekend"))
	f.reconcileHook()
	f.evalGate("app-v1-prod-no-weekend", monday)
	f.reconcileHook()
	assert.Empty(t, srv.all())
}

// TestDelivery_RollbackStartedWhilePromoting: a rollback Bundle that is
// still promoting sends RollbackStarted only; a non-rollback Bundle sends
// neither rollback event.
func TestDelivery_RollbackStartedWhilePromoting(t *testing.T) {
	srv, url := newRecorder(t)
	rb := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-rollback-y", Namespace: ns, CreationTimestamp: metav1.NewTime(saturday)},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "app", Provenance: &v1alpha1.BundleProvenance{RollbackOf: "app-v0"}},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	f := newFixture(t, newHook(url, v1alpha1.NotificationEventBundleRollbackStarted, v1alpha1.NotificationEventBundleRolledBack), rb)
	f.reconcileHook()
	reqs := srv.all()
	require.Len(t, reqs, 1)
	assert.Equal(t, "Bundle.RollbackStarted/app-rollback-y", reqs[0].header.Get("X-Kardinal-Event-Key"))
	assert.Equal(t, "Rollback Bundle app-rollback-y started: it restores Bundle app-v0", jsonMap(t, reqs[0].body)["message"])
}

// TestDocsTemplateExamples renders every format: template example in
// docs/notifications.md for a PromotionStep.Failed event: it must parse,
// render valid JSON, and send the Secret's Authorization header.
func TestDocsTemplateExamples(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "notifications.md"))
	require.NoError(t, err)
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(string(doc), -1)
	found := 0
	for _, b := range blocks {
		if !strings.Contains(b[1], "format: template") {
			continue
		}
		found++
		var ex struct {
			Spec v1alpha1.NotificationHookSpec `json:"spec"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(b[1]), &ex), b[1])
		srv, url := newRecorder(t)
		hook := newHook(url, v1alpha1.NotificationEventPromotionStepFailed)
		hook.Spec.Format, hook.Spec.Template = ex.Spec.Format, ex.Spec.Template
		hook.Spec.Webhook.SecretRef = ex.Spec.Webhook.SecretRef
		objs := []client.Object{hook, &v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: "app-app-v1-prod", Namespace: ns, CreationTimestamp: metav1.NewTime(saturday)},
			Spec:       v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: "prod"},
			Status:     v1alpha1.PromotionStepStatus{State: "Failed", Message: `health "timeout"`},
		}}
		if ref := ex.Spec.Webhook.SecretRef; ref != nil {
			objs = append(objs, webhookSecret(ref.Name, map[string]string{"authorization": "GenieKey test"}))
		}
		f := newFixture(t, objs...)
		f.reconcileHook()
		h := f.hook()
		require.Equal(t, metav1.ConditionTrue, readyCondition(t, h).Status, "%+v", h.Status)
		reqs := srv.all()
		require.Len(t, reqs, 1, "%+v", h.Status)
		assert.True(t, json.Valid([]byte(reqs[0].body)), reqs[0].body)
		if ex.Spec.Webhook.SecretRef != nil {
			assert.Equal(t, "GenieKey test", reqs[0].header.Get("Authorization"))
		}
	}
	assert.Positive(t, found, "docs/notifications.md has a template example")
}
