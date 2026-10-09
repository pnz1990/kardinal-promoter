// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/notificationhook"
)

var signingKey = strings.Repeat("k", 32)

// TestSign matches the documented recipe: HMAC-SHA256 of "<unix>.<body>".
func TestSign(t *testing.T) {
	at := time.Unix(1_760_000_000, 0)
	body := []byte(`{"event":"Bundle.Verified"}`)
	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte("1760000000." + string(body)))
	assert.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), notificationhook.Sign([]byte(signingKey), body, at))
}

// TestVerify: what a receiver must refuse.
func TestVerify(t *testing.T) {
	key, body := []byte(signingKey), []byte(`{"event":"Bundle.Failed"}`)
	sent := time.Unix(1_760_000_000, 0)
	sig := notificationhook.Sign(key, body, sent)
	ts := strconv.FormatInt(sent.Unix(), 10)
	tests := []struct {
		name, ts, sig string
		body          []byte
		key           []byte
		now           time.Time
		err           string
	}{
		{"valid", ts, sig, body, key, sent.Add(time.Minute), ""},
		{"changed body", ts, sig, []byte(`{"event":"Bundle.Verified"}`), key, sent, "does not match"},
		{"other key", ts, sig, body, []byte(strings.Repeat("x", 32)), sent, "does not match"},
		{"replayed later", ts, sig, body, key, sent.Add(10 * time.Minute), "more than 5m0s"},
		{"timestamp changed to look fresh", strconv.FormatInt(sent.Add(9*time.Minute).Unix(), 10), sig, body, key, sent.Add(10 * time.Minute), "does not match"},
		{"from the future", ts, sig, body, key, sent.Add(-10 * time.Minute), "more than 5m0s"},
		{"not a number", "yesterday", sig, body, key, sent, "not a Unix time"},
		{"no scheme", ts, strings.TrimPrefix(sig, "sha256="), body, key, sent, "sha256="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := notificationhook.Verify(tt.key, tt.body, tt.ts, tt.sig, tt.now, 5*time.Minute)
			if tt.err == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.err)
		})
	}
}

// TestDelivery_SignedCloudEvents: a hook with spec.signing signs every
// request with the delivery time, a retry is signed again with its own time,
// and format cloudevents sends a CloudEvents 1.0 structured event whose id
// is the event key.
func TestDelivery_SignedCloudEvents(t *testing.T) {
	srv, url := newRecorder(t)
	hook := newHook(url+"/hook", v1alpha1.NotificationEventBundleFailed)
	hook.Spec.Format = v1alpha1.NotificationFormatCloudEvents
	hook.Spec.Signing = &v1alpha1.NotificationSigning{SecretRef: v1alpha1.NotificationSigningSecretRef{Name: "signing"}}
	f := newFixture(t, hook, failedBundle("app-v0", saturday),
		webhookSecret("signing", map[string]string{"signing-key": signingKey}))
	f.reconcileHook()
	reqs := srv.all()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, "application/cloudevents+json; charset=utf-8", r.header.Get("Content-Type"))
	assert.Equal(t, strconv.FormatInt(saturday.Unix(), 10), r.header.Get("X-Kardinal-Timestamp"))
	require.NoError(t, notificationhook.Verify([]byte(signingKey), []byte(r.body), r.header.Get("X-Kardinal-Timestamp"),
		r.header.Get("X-Kardinal-Signature"), saturday, time.Minute))

	ce := jsonMap(t, r.body)
	assert.Equal(t, "1.0", ce["specversion"])
	assert.Equal(t, "Bundle.Failed/app-v0", ce["id"])
	assert.Equal(t, r.header.Get("X-Kardinal-Event-Key"), ce["id"])
	assert.Equal(t, "io.kardinal.bundle.failed", ce["type"])
	assert.Equal(t, "/apis/kardinal.io/v1alpha1/namespaces/default/pipelines/app", ce["source"])
	assert.Equal(t, "app-v0", ce["subject"])
	assert.Equal(t, saturday.UTC().Format(time.RFC3339), ce["time"])
	assert.Equal(t, "application/json", ce["datacontenttype"])
	data, ok := ce["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "Bundle.Failed", data["event"])
	assert.Equal(t, "app-v0", data["bundle"])
}

// TestDelivery_SigningSecretErrors: the signing Secret follows the same rules
// as the webhook Secret, plus a minimum key length; the hook is Ready=False
// with the reason, the event waits, and the key never appears in status.
func TestDelivery_SigningSecretErrors(t *testing.T) {
	tests := []struct {
		name   string
		secret *map[string]string
		label  bool
		key    string
		reason string
	}{
		{"missing Secret", nil, true, "", "SecretNotFound"},
		{"not referenceable", &map[string]string{"signing-key": signingKey}, false, "", "SecretNotReferenceable"},
		{"no key", &map[string]string{"other": signingKey}, true, "", "SecretKeyMissing"},
		{"custom key name", &map[string]string{"hmac": signingKey}, true, "hmac", "Configured"},
		{"too short", &map[string]string{"signing-key": "short-s3cret"}, true, "", "SigningKeyTooShort"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, url := newRecorder(t)
			hook := newHook(url, v1alpha1.NotificationEventBundleFailed)
			hook.Spec.Signing = &v1alpha1.NotificationSigning{SecretRef: v1alpha1.NotificationSigningSecretRef{Name: "signing", Key: tt.key}}
			objs := []client.Object{hook, failedBundle("app-v0", saturday)}
			if tt.secret != nil {
				s := webhookSecret("signing", *tt.secret)
				if !tt.label {
					s.Labels = nil
				}
				objs = append(objs, s)
			}
			f := newFixture(t, objs...)
			f.reconcileHook()
			h := f.hook()
			c := readyCondition(t, h)
			assert.Equal(t, tt.reason, c.Reason, c.Message)
			assert.NotContains(t, c.Message, "s3cret")
			if tt.reason == "Configured" {
				require.Len(t, srv.all(), 1)
				assert.NotEmpty(t, srv.all()[0].header.Get("X-Kardinal-Signature"))
				return
			}
			assert.Equal(t, metav1.ConditionFalse, c.Status)
			assert.Empty(t, srv.all(), "nothing is sent unsigned")
			assert.Empty(t, h.Status.ProcessedEventKeys, "the event waits")
		})
	}
}

// flakyServer answers its first request 500 and the rest 200, recording all.
type flakyServer struct {
	mu   sync.Mutex
	reqs []request
}

func (s *flakyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, request{path: r.URL.Path, header: r.Header.Clone(), body: string(b)})
	if len(s.reqs) == 1 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// TestDelivery_RetryIsSignedAgain: a delivery answered 500 is retried after
// the backoff with a new X-Kardinal-Timestamp and a signature over it, so a
// receiver that refuses stale timestamps accepts the retry. The CloudEvent
// keeps its id and its time (when the event happened); only the send-time
// payload timestamp moves.
func TestDelivery_RetryIsSignedAgain(t *testing.T) {
	srv := &flakyServer{}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	hook := newHook(ts.URL+"/hook", v1alpha1.NotificationEventBundleFailed)
	hook.Spec.Format = v1alpha1.NotificationFormatCloudEvents
	hook.Spec.Signing = &v1alpha1.NotificationSigning{SecretRef: v1alpha1.NotificationSigningSecretRef{Name: "signing"}}
	f := newFixture(t, hook, failedBundle("app-v0", saturday),
		webhookSecret("signing", map[string]string{"signing-key": signingKey}))

	f.reconcileHook()
	require.Equal(t, int32(1), f.hook().Status.FailedAttempts)
	f.now = saturday.Add(10 * time.Minute) // past the backoff
	f.reconcileHook()
	srv.mu.Lock()
	reqs := append([]request{}, srv.reqs...)
	srv.mu.Unlock()
	require.Len(t, reqs, 2)

	first, retry := reqs[0], reqs[1]
	assert.Equal(t, strconv.FormatInt(saturday.Unix(), 10), first.header.Get("X-Kardinal-Timestamp"))
	assert.Equal(t, strconv.FormatInt(f.now.Unix(), 10), retry.header.Get("X-Kardinal-Timestamp"))
	assert.NotEqual(t, first.header.Get("X-Kardinal-Signature"), retry.header.Get("X-Kardinal-Signature"))
	require.NoError(t, notificationhook.Verify([]byte(signingKey), []byte(retry.body), retry.header.Get("X-Kardinal-Timestamp"),
		retry.header.Get("X-Kardinal-Signature"), f.now, time.Minute), "the retry verifies at its own send time")
	assert.Error(t, notificationhook.Verify([]byte(signingKey), []byte(first.body), first.header.Get("X-Kardinal-Timestamp"),
		first.header.Get("X-Kardinal-Signature"), f.now, 5*time.Minute), "the first attempt is stale by then")

	a, b := jsonMap(t, first.body), jsonMap(t, retry.body)
	assert.Equal(t, a["id"], b["id"])
	assert.Equal(t, a["time"], b["time"], "the event time does not move")
	assert.NotEqual(t, a["data"].(map[string]interface{})["timestamp"], b["data"].(map[string]interface{})["timestamp"])
}
