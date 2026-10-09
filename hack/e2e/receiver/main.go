// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// hack/e2e/receiver is the live e2e suites' webhook receiver
// (hack/e2e/components/webhook-receiver.sh). It records every request it gets
// and answers as a test tells it to, so tests can assert what the controller
// sent and make deliveries fail.
//
// Requests are grouped in buckets, the first path segment; each test uses its
// own (its namespace name):
//
//	ANY  /<bucket>/...      recorded; answered 200 "OK" unless a mode is set
//	POST /<bucket>/slack/...  also validated as a Slack incoming-webhook
//	                        message, answered 200 "ok", or 400 with Slack's
//	                        error code (invalid_payload, no_text,
//	                        invalid_blocks)
//	POST /<bucket>/teams/...  also validated as a Teams Workflows webhook
//	                        message with an Adaptive Card, answered 202, or
//	                        400 with the reason
//	POST /<bucket>/cloudevents/...  also validated as a CloudEvents 1.0
//	                        structured event, answered 200, or 400 with the reason
//	POST /_signing/<bucket> {"secret":"...","maxSkewSeconds":300}: verify
//	                        X-Kardinal-Signature on the bucket's requests as
//	                        docs/notifications.md#signed-requests says, and
//	                        answer 401 with the reason when it fails
//	                        (record.signature: "valid" or the reason)
//	GET  /_records/<bucket> the bucket's records, oldest first, as JSON
//	POST /_mode/<bucket>    {"status":503,"times":2,"location":"...",
//	                        "retryAfter":"600"}: answer the next times
//	                        requests (0: all) with status, with a Location or
//	                        Retry-After header when set; {"status":0} resets
//	GET  /_healthz          200
//
// Only the standard library, so it builds without downloading modules.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxRecords = 1000
	maxBody    = 1 << 20
)

// record is one request as the receiver got it.
type record struct {
	Time    time.Time           `json:"time"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query,omitempty"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
	// Status is what the receiver answered.
	Status int `json:"status"`
	// Signature is "valid" or why verification failed, when the bucket
	// verifies signatures.
	Signature string `json:"signature,omitempty"`
}

// signing makes the receiver verify a bucket's request signatures.
type signing struct {
	Secret         string `json:"secret"`
	MaxSkewSeconds int64  `json:"maxSkewSeconds"`
}

// mode makes the receiver answer a bucket's requests with Status.
type mode struct {
	Status   int    `json:"status"`
	Times    int    `json:"times"`
	Location string `json:"location,omitempty"`
	// RetryAfter is the Retry-After header value (seconds or an HTTP date).
	RetryAfter string `json:"retryAfter,omitempty"`
}

type receiver struct {
	mu      sync.Mutex
	records map[string][]record
	modes   map[string]*mode
	signing map[string]*signing
}

func main() {
	addr := flag.String("listen", ":8080", "listen address")
	flag.Parse()
	r := &receiver{records: map[string][]record{}, modes: map[string]*mode{}, signing: map[string]*signing{}}
	srv := &http.Server{Addr: *addr, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("receiver listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(req.URL.Path, "/"), "/", 3)
	switch {
	case parts[0] == "_healthz":
		w.WriteHeader(http.StatusOK)
	case parts[0] == "_records" && len(parts) > 1 && req.Method == http.MethodGet:
		r.mu.Lock()
		recs := append([]record{}, r.records[parts[1]]...)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(recs)
	case parts[0] == "_mode" && len(parts) > 1 && req.Method == http.MethodPost:
		var m mode
		if err := json.NewDecoder(io.LimitReader(req.Body, maxBody)).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		if m.Status == 0 {
			delete(r.modes, parts[1])
		} else {
			r.modes[parts[1]] = &m
		}
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case parts[0] == "_signing" && len(parts) > 1 && req.Method == http.MethodPost:
		var sg signing
		if err := json.NewDecoder(io.LimitReader(req.Body, maxBody)).Decode(&sg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sg.MaxSkewSeconds <= 0 {
			sg.MaxSkewSeconds = 300
		}
		r.mu.Lock()
		r.signing[parts[1]] = &sg
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case parts[0] == "" || strings.HasPrefix(parts[0], "_"):
		http.NotFound(w, req)
	default:
		r.record(w, req, parts[0])
	}
}

// record stores the request in its bucket and answers it.
func (r *receiver) record(w http.ResponseWriter, req *http.Request, bucket string) {
	body, _ := io.ReadAll(io.LimitReader(req.Body, maxBody))
	status, location, retryAfter := http.StatusOK, "", ""
	answer := ""
	if parts := strings.SplitN(strings.TrimPrefix(req.URL.Path, "/"), "/", 3); len(parts) > 1 {
		switch parts[1] {
		case "slack":
			status, answer = validateSlack(req, body)
		case "teams":
			status, answer = validateTeams(req, body)
		case "cloudevents":
			status, answer = validateCloudEvent(req, body)
		}
	}
	r.mu.Lock()
	sigResult := ""
	if sg := r.signing[bucket]; sg != nil {
		sigResult = "valid"
		if err := verifySignature([]byte(sg.Secret), body, req.Header.Get("X-Kardinal-Timestamp"),
			req.Header.Get("X-Kardinal-Signature"), time.Duration(sg.MaxSkewSeconds)*time.Second); err != "" {
			sigResult, status, answer = err, http.StatusUnauthorized, err
		}
	}
	if m := r.modes[bucket]; m != nil {
		status, location, retryAfter = m.Status, m.Location, m.RetryAfter
		if m.Times > 0 {
			m.Times--
			if m.Times == 0 {
				delete(r.modes, bucket)
			}
		}
	}
	recs := r.records[bucket]
	recs = append(recs, record{
		Time: time.Now().UTC(), Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery,
		Headers: req.Header.Clone(), Body: string(body), Status: status, Signature: sigResult,
	})
	if len(recs) > maxRecords {
		recs = recs[len(recs)-maxRecords:]
	}
	r.records[bucket] = recs
	r.mu.Unlock()
	if location != "" {
		w.Header().Set("Location", location)
	}
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	if answer == "" || status != http.StatusOK && status != http.StatusAccepted && status != http.StatusBadRequest &&
		status != http.StatusUnauthorized {
		answer = http.StatusText(status)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, answer)
}

// validateSlack checks body the way Slack's incoming webhooks do for the
// parts kardinal uses: a JSON object with a text or blocks; every block has a
// known type and its texts are within Slack's limits.
// https://api.slack.com/messaging/webhooks, https://api.slack.com/reference/block-kit/blocks
func validateSlack(req *http.Request, body []byte) (int, string) {
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		return http.StatusBadRequest, "invalid_payload"
	}
	var msg struct {
		Text   string                   `json:"text"`
		Blocks []map[string]interface{} `json:"blocks"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return http.StatusBadRequest, "invalid_payload"
	}
	if msg.Text == "" && len(msg.Blocks) == 0 {
		return http.StatusBadRequest, "no_text"
	}
	limits := map[string]int{"header": 150, "section": 3000, "context": 2000, "actions": 0}
	for _, b := range msg.Blocks {
		typ, _ := b["type"].(string)
		limit, ok := limits[typ]
		if !ok {
			return http.StatusBadRequest, "invalid_blocks"
		}
		if t, ok := b["text"].(map[string]interface{}); ok {
			tt, _ := t["type"].(string)
			text, _ := t["text"].(string)
			if (tt != "plain_text" && tt != "mrkdwn") || text == "" || len([]rune(text)) > limit {
				return http.StatusBadRequest, "invalid_blocks"
			}
			if typ == "header" && tt != "plain_text" {
				return http.StatusBadRequest, "invalid_blocks"
			}
		}
		if f, ok := b["fields"].([]interface{}); ok && len(f) > 10 {
			return http.StatusBadRequest, "invalid_blocks"
		}
		if typ == "actions" || typ == "context" {
			if els, _ := b["elements"].([]interface{}); len(els) == 0 {
				return http.StatusBadRequest, "invalid_blocks"
			}
		}
	}
	return http.StatusOK, "ok"
}

// validateTeams checks body is what a Teams Workflows webhook ("When a Teams
// webhook request is received") posts to a channel: a message whose
// attachments are Adaptive Cards with a version and a body.
func validateTeams(req *http.Request, body []byte) (int, string) {
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		return http.StatusBadRequest, "content type is not application/json"
	}
	var msg struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Type    string                   `json:"type"`
				Version string                   `json:"version"`
				Body    []map[string]interface{} `json:"body"`
			} `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return http.StatusBadRequest, "body is not JSON: " + err.Error()
	}
	if msg.Type != "message" || len(msg.Attachments) == 0 {
		return http.StatusBadRequest, "not a message with attachments"
	}
	for _, a := range msg.Attachments {
		if a.ContentType != "application/vnd.microsoft.card.adaptive" || a.Content.Type != "AdaptiveCard" ||
			a.Content.Version == "" || len(a.Content.Body) == 0 {
			return http.StatusBadRequest, "attachment is not an Adaptive Card"
		}
		for _, el := range a.Content.Body {
			if t, _ := el["type"].(string); t == "" {
				return http.StatusBadRequest, "card element without a type"
			}
		}
	}
	return http.StatusAccepted, ""
}

// verifySignature is the receiver side of docs/notifications.md#signed-requests:
// the timestamp is fresh and X-Kardinal-Signature is the HMAC-SHA256 of
// "<timestamp>.<body>". It returns "" or the reason.
func verifySignature(key, body []byte, timestamp, signature string, maxSkew time.Duration) string {
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return "X-Kardinal-Timestamp is not a Unix time"
	}
	if d := time.Since(time.Unix(sec, 0)); d > maxSkew || d < -maxSkew {
		return "X-Kardinal-Timestamp is too old or in the future"
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(signature)) {
		return "X-Kardinal-Signature does not match"
	}
	return ""
}

// validateCloudEvent checks a CloudEvents 1.0 structured-mode event: the
// media type and the required context attributes.
func validateCloudEvent(req *http.Request, body []byte) (int, string) {
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "application/cloudevents+json") {
		return http.StatusBadRequest, "content type is not application/cloudevents+json"
	}
	var ev map[string]interface{}
	if err := json.Unmarshal(body, &ev); err != nil {
		return http.StatusBadRequest, "body is not JSON"
	}
	for _, k := range []string{"specversion", "id", "source", "type"} {
		if v, _ := ev[k].(string); v == "" {
			return http.StatusBadRequest, "missing " + k
		}
	}
	if ev["specversion"] != "1.0" {
		return http.StatusBadRequest, "specversion is not 1.0"
	}
	if t, ok := ev["time"].(string); ok {
		if _, err := time.Parse(time.RFC3339, t); err != nil {
			return http.StatusBadRequest, "time is not RFC 3339"
		}
	}
	return http.StatusOK, "ok"
}
