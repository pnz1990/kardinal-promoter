// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// HeaderSignature carries "sha256=<hex HMAC-SHA256 of timestamp.body>".
	HeaderSignature = "X-Kardinal-Signature"
	// HeaderTimestamp carries the Unix time (seconds) the request was signed at.
	HeaderTimestamp = "X-Kardinal-Timestamp"
	// secretKeySigning is the default key of the signing Secret.
	secretKeySigning = "signing-key"
	// minSigningKey is the shortest signing key accepted, in bytes.
	minSigningKey = 32
	// contentTypeCloudEvents is the structured-mode CloudEvents media type.
	contentTypeCloudEvents = "application/cloudevents+json; charset=utf-8"
)

// Sign returns the X-Kardinal-Signature value for body sent at t: the
// HMAC-SHA256, keyed with key, of the decimal Unix seconds, a ".", and the
// body bytes. Signing the timestamp lets a receiver refuse a replayed request.
func Sign(key, body []byte, t time.Time) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strconv.FormatInt(t.Unix(), 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a signed request the way a receiver should: the timestamp
// is within maxSkew of now and the signature matches, compared in constant
// time. It is what docs/notifications.md#signed-requests describes, and the
// e2e receiver uses the same steps.
func Verify(key, body []byte, timestamp, signature string, now time.Time, maxSkew time.Duration) error {
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("%s is not a Unix time: %q", HeaderTimestamp, timestamp)
	}
	if d := now.Sub(time.Unix(sec, 0)); d > maxSkew || d < -maxSkew {
		return fmt.Errorf("%s is %s from now, more than %s: a replay or a skewed clock", HeaderTimestamp, d.Round(time.Second), maxSkew)
	}
	if !strings.HasPrefix(signature, "sha256=") {
		return fmt.Errorf("%s does not start with sha256=", HeaderSignature)
	}
	want := Sign(key, body, time.Unix(sec, 0))
	if !hmac.Equal([]byte(want), []byte(signature)) {
		return fmt.Errorf("%s does not match the body", HeaderSignature)
	}
	return nil
}

// cloudEvent is a CloudEvents 1.0 event in structured JSON mode.
type cloudEvent struct {
	SpecVersion     string              `json:"specversion"`
	ID              string              `json:"id"`
	Source          string              `json:"source"`
	Type            string              `json:"type"`
	Subject         string              `json:"subject,omitempty"`
	Time            string              `json:"time"`
	DataContentType string              `json:"datacontenttype"`
	Data            notificationPayload `json:"data"`
}

// cloudEventType maps "Bundle.Verified" to "io.kardinal.bundle.verified".
func cloudEventType(event string) string {
	return "io.kardinal." + strings.ToLower(event)
}

// cloudEventsBody wraps the kardinal payload in a CloudEvent. Its time is
// when the event happened (at), not when it was sent (the payload's
// timestamp); a retry keeps it. The id is the
// event key, so a retried delivery has the same id and receivers can drop
// duplicates on it. The source is the Pipeline the event belongs to (or the
// namespace, for an event without one); the subject is the Bundle, with the
// environment when there is one.
func cloudEventsBody(namespace, eventKey string, at time.Time, p notificationPayload) ([]byte, error) {
	source := "/apis/kardinal.io/v1alpha1/namespaces/" + namespace
	if p.Pipeline != "" {
		source += "/pipelines/" + p.Pipeline
	}
	subject := p.Bundle
	if p.Environment != "" {
		if subject != "" {
			subject += "/"
		}
		subject += p.Environment
	}
	b, err := json.Marshal(cloudEvent{
		SpecVersion: "1.0", ID: eventKey, Source: source, Type: cloudEventType(p.Event), Subject: subject,
		Time: eventTime(at, p.Timestamp), DataContentType: "application/json", Data: p,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal CloudEvent: %w", err)
	}
	return b, nil
}

// eventTime is at in RFC 3339, or sent when the event has no time.
func eventTime(at time.Time, sent string) string {
	if at.IsZero() {
		return sent
	}
	return at.UTC().Format(time.RFC3339)
}
