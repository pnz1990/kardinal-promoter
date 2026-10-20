// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/notificationhook"
)

// verify is the receiver docs/notifications.md#signed-requests shows, line
// for line: TestSignedRequestsDoc_GoVerifier keeps the two the same and
// checks it against Sign.
func verify(key []byte, header http.Header, body []byte, now time.Time) bool {
	ts := header.Get("X-Kardinal-Timestamp")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if d := now.Sub(time.Unix(sec, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return false // a replay, or a skewed clock
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(header.Get("X-Kardinal-Signature")))
}

// signedRequestsSection is the "Signed requests" section of
// docs/notifications.md.
func signedRequestsSection(t *testing.T) string {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "notifications.md"))
	require.NoError(t, err)
	s := string(doc)
	i := strings.Index(s, "\n## Signed requests\n")
	require.GreaterOrEqual(t, i, 0, "docs/notifications.md has a Signed requests section")
	s = s[i+1:]
	if j := strings.Index(s[1:], "\n## "); j >= 0 {
		s = s[:j+1]
	}
	return s
}

// funcSource is the text of func name in src: from its "func name(" line to
// the first line that is a lone "}".
func funcSource(t *testing.T, src, name string) string {
	t.Helper()
	i := strings.Index(src, "\nfunc "+name+"(")
	require.GreaterOrEqual(t, i, 0, "no func %s", name)
	rest := src[i+1:]
	j := strings.Index(rest, "\n}\n")
	require.GreaterOrEqual(t, j, 0, "func %s has no closing brace", name)
	return rest[:j+2]
}

// fence is the body of the first fenced block of lang in s that contains
// substr.
func fence(t *testing.T, s, lang, substr string) string {
	t.Helper()
	for _, part := range strings.Split(s, "```"+lang+"\n")[1:] {
		body, _, ok := strings.Cut(part, "```")
		if ok && strings.Contains(body, substr) {
			return body
		}
	}
	require.FailNow(t, "no ```"+lang+" block containing "+substr)
	return ""
}

// signedRequest is a request the controller would send at t: its headers
// and body, signed with Sign.
func signedRequest(key, body []byte, at time.Time) http.Header {
	h := http.Header{}
	h.Set(notificationhook.HeaderTimestamp, strconv.FormatInt(at.Unix(), 10))
	h.Set(notificationhook.HeaderSignature, notificationhook.Sign(key, body, at))
	return h
}

// TestSignedRequestsDoc_GoVerifier: the Go verifier the docs show is this
// file's verify, and it accepts what the controller signs and refuses a
// changed body, another key, a stale or future timestamp and a malformed one.
func TestSignedRequestsDoc_GoVerifier(t *testing.T) {
	t.Parallel()
	block := fence(t, signedRequestsSection(t), "go", "func verify(")
	self, err := os.ReadFile("signing_doc_test.go")
	require.NoError(t, err)
	require.Equal(t, funcSource(t, string(self), "verify"), funcSource(t, "\n"+block, "verify"),
		"docs/notifications.md's verify and this file's differ: change both")

	key := []byte(signingKey)
	body := []byte(`{"event":"Bundle.Verified","bundle":"my-app-abc12"}`)
	now := time.Unix(1_800_000_000, 0)
	h := signedRequest(key, body, now)
	tests := []struct {
		name   string
		key    []byte
		header http.Header
		body   []byte
		now    time.Time
		want   bool
	}{
		{"what the controller signs", key, h, body, now, true},
		{"4 minutes later", key, h, body, now.Add(4 * time.Minute), true},
		{"a changed body", key, h, []byte(`{"event":"Bundle.Failed"}`), now, false},
		{"another key", []byte(strings.Repeat("x", 32)), h, body, now, false},
		{"6 minutes later: a replay", key, h, body, now.Add(6 * time.Minute), false},
		{"6 minutes early: a skewed clock", key, h, body, now.Add(-6 * time.Minute), false},
		{"no timestamp", key, http.Header{notificationhook.HeaderSignature: h[notificationhook.HeaderSignature]}, body, now, false},
		{"a timestamp that is not a number", key, func() http.Header {
			c := h.Clone()
			c.Set(notificationhook.HeaderTimestamp, "yesterday")
			return c
		}(), body, now, false},
		{"no signature", key, http.Header{notificationhook.HeaderTimestamp: h[notificationhook.HeaderTimestamp]}, body, now, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, verify(tc.key, tc.header, tc.body, tc.now))
			// The reference implementation agrees.
			err := notificationhook.Verify(tc.key, tc.body, tc.header.Get(notificationhook.HeaderTimestamp),
				tc.header.Get(notificationhook.HeaderSignature), tc.now, 5*time.Minute)
			assert.Equal(t, tc.want, err == nil, "Verify: %v", err)
		})
	}
}

// TestSignedRequestsDoc_ShellLine: the docs' openssl line, run as written,
// prints the X-Kardinal-Signature the controller sends.
func TestSignedRequestsDoc_ShellLine(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"bash", "openssl", "sed"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed: %v", tool, err)
		}
	}
	block := fence(t, signedRequestsSection(t), "bash", "openssl dgst")
	line := strings.TrimSpace(block)
	require.NotContains(t, line, "\n", "the docs' shell check is one line")

	key := signingKey
	body := []byte(`{"event":"Bundle.Verified","bundle":"my-app-abc12"}`)
	at := time.Unix(1_800_000_000, 0)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "body.json"), body, 0o600))
	cmd := exec.Command("bash", "-c", line)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TIMESTAMP="+strconv.FormatInt(at.Unix(), 10), "KEY="+key)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, notificationhook.Sign([]byte(key), body, at), strings.TrimSpace(string(out)))
}
