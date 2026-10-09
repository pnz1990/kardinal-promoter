// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &receiver{records: map[string][]record{}, modes: map[string]*mode{}}
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
