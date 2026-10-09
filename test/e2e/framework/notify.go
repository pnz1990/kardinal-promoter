// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Environment variables hack/e2e/components/webhook-receiver.sh sets.
const (
	// EnvReceiverURL is the receiver's base URL in the cluster (what a
	// NotificationHook's spec.webhook.url points at).
	EnvReceiverURL = "KARDINAL_E2E_RECEIVER_URL"
	// EnvReceiverAPI is the same receiver from the host.
	EnvReceiverAPI = "KARDINAL_E2E_RECEIVER_API"
)

// Received is one request the webhook receiver recorded.
type Received struct {
	Time    time.Time           `json:"time"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
	// Status is what the receiver answered.
	Status int `json:"status"`
	// Signature is "valid" or why verification failed, for a bucket set to
	// verify signatures (VerifySignatures).
	Signature string `json:"signature,omitempty"`
}

// Header is the first value of header name (canonical form).
func (r Received) Header(name string) string {
	if v := r.Headers[http.CanonicalHeaderKey(name)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Receiver is the suite's webhook receiver (hack/e2e/receiver). Each test
// uses its own bucket, the first path segment.
type Receiver struct {
	url, api string
}

// NewReceiver reads the receiver from the env file; it fails the test when
// the suite has none.
func NewReceiver(t *testing.T) *Receiver {
	t.Helper()
	r := &Receiver{
		url: strings.TrimRight(os.Getenv(EnvReceiverURL), "/"),
		api: strings.TrimRight(os.Getenv(EnvReceiverAPI), "/"),
	}
	if r.url == "" || r.api == "" {
		t.Fatalf("%s and %s must be set; the core, github and chart suites run hack/e2e/components/webhook-receiver.sh",
			EnvReceiverURL, EnvReceiverAPI)
	}
	return r
}

// URL is the in-cluster URL of path in bucket.
func (r *Receiver) URL(bucket, path string) string {
	return r.url + "/" + bucket + "/" + strings.TrimLeft(path, "/")
}

// Records returns what bucket received, oldest first.
func (r *Receiver) Records(ctx context.Context, bucket string) ([]Received, error) {
	res, err := doHTTP(ctx, http.MethodGet, r.api+"/_records/"+bucket, nil, nil)
	if err != nil {
		return nil, err
	}
	if res.Status != http.StatusOK {
		return nil, fmt.Errorf("receiver records %s: HTTP %d %s", bucket, res.Status, clip(res.Body))
	}
	var out []Received
	if err := json.Unmarshal([]byte(res.Body), &out); err != nil {
		return nil, fmt.Errorf("decode receiver records %s: %w", bucket, err)
	}
	return out, nil
}

// MustRecords is Records that fails the test on error.
func (r *Receiver) MustRecords(t *testing.T, bucket string) []Received {
	t.Helper()
	recs, err := r.Records(context.Background(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// Fail makes the receiver answer bucket's next times requests (0: every
// request) with status, and a Location header when location is set.
// Status 0 restores 200.
func (r *Receiver) Fail(t *testing.T, bucket string, status, times int, location string) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"status": status, "times": times, "location": location})
	res := HTTP(t, http.MethodPost, r.api+"/_mode/"+bucket, map[string]string{"Content-Type": "application/json"}, body)
	if res.Status != http.StatusNoContent {
		t.Fatalf("set receiver mode for %s: HTTP %d %s", bucket, res.Status, clip(res.Body))
	}
	t.Logf("receiver bucket %s answers HTTP %d (times %d, location %q)", bucket, status, times, location)
}

// FailRetryAfter makes the receiver answer bucket's next times requests (0:
// every request) with status and a Retry-After header of retryAfter, the way
// a rate-limited API answers.
func (r *Receiver) FailRetryAfter(t *testing.T, bucket string, status, times int, retryAfter string) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"status": status, "times": times, "retryAfter": retryAfter})
	res := HTTP(t, http.MethodPost, r.api+"/_mode/"+bucket, map[string]string{"Content-Type": "application/json"}, body)
	if res.Status != http.StatusNoContent {
		t.Fatalf("set receiver mode for %s: HTTP %d %s", bucket, res.Status, clip(res.Body))
	}
	t.Logf("receiver bucket %s answers HTTP %d with Retry-After %s (times %d)", bucket, status, retryAfter, times)
}

// VerifySignatures makes the receiver check X-Kardinal-Signature on bucket's
// requests with secret, the way docs/notifications.md#signed-requests says a
// receiver should, answering 401 when it fails.
func (r *Receiver) VerifySignatures(t *testing.T, bucket, secret string) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"secret": secret, "maxSkewSeconds": 300})
	res := HTTP(t, http.MethodPost, r.api+"/_signing/"+bucket, map[string]string{"Content-Type": "application/json"}, body)
	if res.Status != http.StatusNoContent {
		t.Fatalf("set receiver signing for %s: HTTP %d %s", bucket, res.Status, clip(res.Body))
	}
}
