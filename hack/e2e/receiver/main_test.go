// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func post(t *testing.T, r *receiver, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestShapeValidation(t *testing.T) {
	long := strings.Repeat("x", 151)
	tests := []struct {
		name, path, ct, body string
		status               int
		answer               string
	}{
		{"slack ok", "/b/slack/services/T/B/x", "application/json",
			`{"text":"hi","blocks":[{"type":"header","text":{"type":"plain_text","text":"Title"}},{"type":"section","text":{"type":"mrkdwn","text":"*x*"}},{"type":"context","elements":[{"type":"mrkdwn","text":"k"}]}]}`,
			200, "ok"},
		{"slack text only", "/b/slack/x", "application/json", `{"text":"hi"}`, 200, "ok"},
		{"slack kardinal json", "/b/slack/x", "application/json", `{"event":"Bundle.Verified","message":"m"}`, 400, "no_text"},
		{"slack not json", "/b/slack/x", "application/json", `nope`, 400, "invalid_payload"},
		{"slack wrong content type", "/b/slack/x", "text/plain", `{"text":"hi"}`, 400, "invalid_payload"},
		{"slack long header", "/b/slack/x", "application/json",
			`{"blocks":[{"type":"header","text":{"type":"plain_text","text":"` + long + `"}}]}`, 400, "invalid_blocks"},
		{"slack mrkdwn header", "/b/slack/x", "application/json",
			`{"blocks":[{"type":"header","text":{"type":"mrkdwn","text":"x"}}]}`, 400, "invalid_blocks"},
		{"slack unknown block", "/b/slack/x", "application/json", `{"blocks":[{"type":"table"}]}`, 400, "invalid_blocks"},
		{"teams ok", "/b/teams/workflows/x", "application/json",
			`{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","contentUrl":null,"content":{"type":"AdaptiveCard","version":"1.4","body":[{"type":"TextBlock","text":"x"}]}}]}`,
			202, "Accepted"},
		{"teams kardinal json", "/b/teams/x", "application/json", `{"event":"Bundle.Verified"}`, 400, "not a message with attachments"},
		{"teams not a card", "/b/teams/x", "application/json",
			`{"type":"message","attachments":[{"contentType":"text/html","content":{}}]}`, 400, "attachment is not an Adaptive Card"},
		{"plain bucket", "/b/hook", "text/plain", `anything`, 200, "OK"},
		{"cloudevent ok", "/b/cloudevents/x", "application/cloudevents+json; charset=utf-8",
			`{"specversion":"1.0","id":"Bundle.Verified/a","source":"/x","type":"io.kardinal.bundle.verified","time":"2026-10-09T12:00:00Z","data":{}}`, 200, "ok"},
		{"cloudevent wrong media type", "/b/cloudevents/x", "application/json", `{}`, 400, "content type is not application/cloudevents+json"},
		{"cloudevent missing id", "/b/cloudevents/x", "application/cloudevents+json",
			`{"specversion":"1.0","source":"/x","type":"t"}`, 400, "missing id"},
		{"cloudevent bad time", "/b/cloudevents/x", "application/cloudevents+json",
			`{"specversion":"1.0","id":"i","source":"/x","type":"t","time":"today"}`, 400, "time is not RFC 3339"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestReceiver()
			w := post(t, r, tt.path, tt.ct, tt.body)
			if w.Code != tt.status || w.Body.String() != tt.answer {
				t.Fatalf("got %d %q, want %d %q", w.Code, w.Body.String(), tt.status, tt.answer)
			}
			if got := r.records["b"]; len(got) != 1 || got[0].Status != tt.status {
				t.Fatalf("records: %+v", got)
			}
		})
	}
}

// TestModeOverridesValidation: a failure mode set by a test answers instead
// of the validator.
func TestModeOverridesValidation(t *testing.T) {
	r := &receiver{records: map[string][]record{}, modes: map[string]*mode{"b": {Status: 500, Times: 1}}}
	w := post(t, r, "/b/slack/x", "application/json", `{"text":"hi"}`)
	if w.Code != 500 || w.Body.String() != "Internal Server Error" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	w = post(t, r, "/b/slack/x", "application/json", `{"text":"hi"}`)
	if w.Code != 200 {
		t.Fatalf("got %d after the mode ran out", w.Code)
	}
	var recs []record
	b, _ := json.Marshal(r.records["b"])
	_ = json.Unmarshal(b, &recs)
	if len(recs) != 2 {
		t.Fatalf("%d records", len(recs))
	}
}

func newTestReceiver() *receiver {
	return &receiver{records: map[string][]record{}, modes: map[string]*mode{}, signing: map[string]*signing{}}
}

// TestSigningVerification: a bucket set to verify answers 401 to a missing,
// stale or wrong signature and records the reason; a valid one passes.
func TestSigningVerification(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	sign := func(ts, body string) string {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(ts + "." + body))
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)
	old := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	body := `{"event":"Bundle.Verified"}`
	tests := []struct {
		name, ts, sig, body string
		status              int
		result              string
	}{
		{"valid", now, sign(now, body), body, 200, "valid"},
		{"no signature", now, "", body, 401, "X-Kardinal-Signature does not match"},
		{"stale", old, sign(old, body), body, 401, "X-Kardinal-Timestamp is too old or in the future"},
		{"changed body", now, sign(now, body), `{"event":"Bundle.Failed"}`, 401, "X-Kardinal-Signature does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestReceiver()
			if w := post(t, r, "/_signing/b", "application/json", `{"secret":"`+key+`"}`); w.Code != http.StatusNoContent {
				t.Fatalf("configure: %d", w.Code)
			}
			req := httptest.NewRequest(http.MethodPost, "/b/hook", strings.NewReader(tt.body))
			req.Header.Set("X-Kardinal-Timestamp", tt.ts)
			req.Header.Set("X-Kardinal-Signature", tt.sig)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tt.status {
				t.Fatalf("got %d %q, want %d", w.Code, w.Body.String(), tt.status)
			}
			if got := r.records["b"]; len(got) != 1 || got[0].Signature != tt.result {
				t.Fatalf("records: %+v", got)
			}
		})
	}
}
