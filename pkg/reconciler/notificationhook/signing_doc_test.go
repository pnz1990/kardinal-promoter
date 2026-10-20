// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
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
	// The comparison is constant time: a plain == would leak where the
	// signatures first differ.
	assert.Contains(t, block, "hmac.Equal(", "the docs' verify compares the signatures with hmac.Equal")
	// The block compiles as it is shown, imports included: a missing or an
	// unused import is an error.
	typeCheck(t, block)

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
		requireTool(t, tool, "the docs' openssl line")
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

// typeCheck type-checks src, a Go snippet with its imports, as the only file
// of a package, against the standard library's export data.
func typeCheck(t *testing.T, src string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "verify.go", "package receiver\n\n"+src, 0)
	require.NoError(t, err, "the docs' Go block parses")
	conf := types.Config{Importer: importer.ForCompiler(fset, "gc", nil)}
	_, err = conf.Check("receiver", fset, []*ast.File{f}, nil)
	require.NoError(t, err, "the docs' Go block type-checks as written")
}

// scriptCase is one request a script verifier is run on, and whether it
// must accept it.
type scriptCase struct {
	Name    string            `json:"name"`
	Key     string            `json:"key"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Want    bool              `json:"want"`
}

// scriptCases are requests signed with Sign at the real clock (the scripts
// read it), and the malformed ones a receiver must refuse without raising.
// tsHeader and sigHeader are the header names as the language's framework
// presents them.
func scriptCases(tsHeader, sigHeader string) []scriptCase {
	key := signingKey
	body := `{"event":"Bundle.Verified","bundle":"my-app-abc12"}`
	now := time.Now()
	signed := func(at time.Time) map[string]string {
		return map[string]string{
			tsHeader:  strconv.FormatInt(at.Unix(), 10),
			sigHeader: notificationhook.Sign([]byte(key), []byte(body), at),
		}
	}
	with := func(h map[string]string, k, v string) map[string]string {
		c := map[string]string{}
		for hk, hv := range h {
			c[hk] = hv
		}
		if v == "" {
			delete(c, k)
		} else {
			c[k] = v
		}
		return c
	}
	good := signed(now)
	return []scriptCase{
		{"what the controller signs", key, good, body, true},
		{"a changed body", key, good, `{"event":"Bundle.Failed"}`, false},
		{"another key", strings.Repeat("x", 32), good, body, false},
		{"6 minutes late: a replay", key, signed(now.Add(-6 * time.Minute)), body, false},
		{"6 minutes early: a skewed clock", key, signed(now.Add(6 * time.Minute)), body, false},
		{"a timestamp that is not a number", key, with(good, tsHeader, "yesterday"), body, false},
		{"a signed number", key, with(good, tsHeader, "+"+good[tsHeader]), body, false},
		{"non-ASCII digits", key, with(good, tsHeader, "\u0661\u0662\u0663"), body, false},
		{"no timestamp", key, with(good, tsHeader, ""), body, false},
		{"no signature", key, with(good, sigHeader, ""), body, false},
		{"a non-ASCII signature as long as a real one", key, with(good, sigHeader, "sha256="+strings.Repeat("\u00e9", 64)), body, false},
	}
}

// requireTool skips the test when tool is not on PATH, with the reason, and
// fails it under CI (GitHub Actions sets CI): the runner has the tool, so a
// runner image that loses it must not turn the test into a silent skip.
func requireTool(t *testing.T, tool, what string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is not on PATH under CI, so %s cannot be checked: %v", tool, what, err)
		}
		t.Skipf("%s is not on PATH, so %s is not checked: %v", tool, what, err)
	}
}

// runScriptVerifier runs the docs' verifier of a language: the fenced block
// of lang that contains defines, then harness, which reads the cases as JSON
// on stdin and prints a JSON list of verify's answers. A verifier that raises
// fails the run. The block must compare with constantTime, the language's
// constant-time compare: a plain == would leak where the signatures differ.
func runScriptVerifier(t *testing.T, tool, lang, defines, constantTime, harness, file string, cases []scriptCase) {
	t.Helper()
	requireTool(t, tool, "the docs' "+lang+" verifier")
	block := fence(t, signedRequestsSection(t), lang, defines)
	assert.Contains(t, block, constantTime, "the docs' %s verifier compares the signatures with %s", lang, constantTime)
	dir := t.TempDir()
	script := filepath.Join(dir, file)
	require.NoError(t, os.WriteFile(script, []byte(block+"\n"+harness), 0o600))
	in, err := json.Marshal(cases)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	args := []string{script}
	if tool == "python3" {
		args = append([]string{"-I"}, args...) // isolated: no site packages, no cwd on sys.path
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.Output()
	var stderr string
	if ee, ok := err.(*exec.ExitError); ok {
		stderr = string(ee.Stderr)
	}
	require.NoError(t, err, "the docs' %s verifier raised:\n%s", lang, stderr)
	var got []bool
	require.NoError(t, json.Unmarshal(out, &got), "%s printed %q", tool, out)
	require.Len(t, got, len(cases))
	for i, c := range cases {
		assert.Equal(t, c.Want, got[i], "%s verifier, %s", lang, c.Name)
	}
}

// TestSignedRequestsDoc_PythonVerifier: the docs' Python verify, run with
// python3, accepts what Sign signs and refuses a tampered or malformed
// request without raising.
func TestSignedRequestsDoc_PythonVerifier(t *testing.T) {
	t.Parallel()
	runScriptVerifier(t, "python3", "python", "def verify(", "hmac.compare_digest(", `
import json, sys
cases = json.load(sys.stdin)
print(json.dumps([verify(c["key"].encode(), c["headers"], c["body"].encode()) is True for c in cases]))
`, "verify.py", scriptCases("X-Kardinal-Timestamp", "X-Kardinal-Signature"))
}

// TestSignedRequestsDoc_NodeVerifier: the docs' Node.js verify, run with
// node, accepts what Sign signs and refuses a tampered or malformed request
// without throwing.
func TestSignedRequestsDoc_NodeVerifier(t *testing.T) {
	t.Parallel()
	runScriptVerifier(t, "node", "js", "function verify(", "crypto.timingSafeEqual(", `
const cases = JSON.parse(require("fs").readFileSync(0, "utf8"));
process.stdout.write(JSON.stringify(cases.map((c) => verify(c.key, c.headers, Buffer.from(c.body, "utf8")) === true)));
`, "verify.js", scriptCases("x-kardinal-timestamp", "x-kardinal-signature"))
}
