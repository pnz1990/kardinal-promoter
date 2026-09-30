// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Environment variables hack/e2e/components/ui.sh writes for the ui suite.
const (
	// EnvUINodePortURL is the main release's UI port on a NodePort Service:
	// reached from off the pod, so not a loopback peer.
	EnvUINodePortURL = "KARDINAL_E2E_UI_NODEPORT_URL"
	// EnvUITokenURL is the kui-token release (static UI token, CORS origin
	// UICORSOrigin, allowed host UIAllowedHost).
	EnvUITokenURL = "KARDINAL_E2E_UI_TOKEN_URL"
	// EnvUITokenFile holds the static UI token of kui-token and kui-tls.
	EnvUITokenFile = "KARDINAL_E2E_UI_TOKEN_FILE"
	// EnvUITRURL is the kui-tr release (TokenReview auth), which watches
	// EnvUITRNamespace only.
	EnvUITRURL       = "KARDINAL_E2E_UI_TR_URL"
	EnvUITRNamespace = "KARDINAL_E2E_UI_TR_NAMESPACE"
	// EnvUITRNoRBACURL is kui-tr-norbac: TokenReview auth without the RBAC to
	// create reviews.
	EnvUITRNoRBACURL = "KARDINAL_E2E_UI_TR_NORBAC_URL"
	// EnvUITLSURL and EnvUITLSWebhookURL are kui-tls's UI and webhook ports,
	// served over TLS with the self-signed certificate in EnvUITLSCA.
	EnvUITLSURL        = "KARDINAL_E2E_UI_TLS_URL"
	EnvUITLSWebhookURL = "KARDINAL_E2E_UI_TLS_WEBHOOK_URL"
	EnvUITLSCA         = "KARDINAL_E2E_UI_TLS_CA"
	// EnvNPX is the npx that runs Playwright in web/.
	EnvNPX = "KARDINAL_E2E_NPX"
)

// Settings of the ui suite's releases (hack/e2e/up.sh, components/ui.sh).
const (
	// UIAllowedHost is in ui.allowedHosts of the main release and kui-token.
	UIAllowedHost = "kardinal-ui.test"
	// UICORSOrigin is kui-token's ui.corsAllowedOrigins.
	UICORSOrigin = "http://allowed.example"
	// UIPort is the chart's UI port.
	UIPort = 8082
	// ControllerService is the main release's Service.
	ControllerService = "kardinal-promoter"
)

// MustEnv returns the environment variable name, failing the test when it is
// unset: the suite's setup did not run.
func MustEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set; run hack/e2e/up.sh ui", name)
	}
	return v
}

// UIToken returns the static UI token of kui-token and kui-tls. It is never
// logged.
func UIToken(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(MustEnv(t, EnvUITokenFile))
	if err != nil {
		t.Fatalf("read the UI token: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// ansi matches the colour codes in Playwright's error messages.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

var forwarding = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+) ->`)

// PortForward runs kubectl port-forward to port of Service svc in ns for the
// rest of the test and returns http://127.0.0.1:<local port>. Requests through
// it reach the pod from loopback, as a user's kubectl port-forward does.
func (e *Env) PortForward(t *testing.T, ns, svc string, port int) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "kubectl", "--context", e.Context, "-n", ns,
		"port-forward", "svc/"+svc, fmt.Sprintf("0:%d", port))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("port-forward: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start kubectl port-forward: %v", err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	found := make(chan string, 1)
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stdout)
		sent := false
		for sc.Scan() {
			if m := forwarding.FindStringSubmatch(sc.Text()); m != nil && !sent {
				found <- m[1]
				sent = true
			}
		}
		_ = cmd.Wait()
	}()
	select {
	case p := <-found:
		return "http://127.0.0.1:" + p
	case <-done:
		t.Fatalf("kubectl port-forward svc/%s -n %s exited: %s", svc, ns, stderr.String())
	case <-time.After(30 * time.Second):
		t.Fatalf("kubectl port-forward svc/%s -n %s: no local port after 30s: %s", svc, ns, stderr.String())
	}
	return ""
}

// UIClient calls a kardinal UI server. The zero value of every field but
// BaseURL is fine; copy it to change one setting for one request.
type UIClient struct {
	// BaseURL is scheme://host:port, without a path.
	BaseURL string
	// Token is sent as "Authorization: Bearer <Token>" when set.
	Token string
	// Host overrides the Host header.
	Host string
	// Header holds extra request headers.
	Header http.Header
	// HTTP is the client to use (default: a 30s timeout).
	HTTP *http.Client
}

// UIResponse is what a UI server answered.
type UIResponse struct {
	Status int
	Header http.Header
	Body   string
}

// JSON decodes the body into v, failing the test when it is not JSON.
func (r UIResponse) JSON(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.Body), v); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
}

// String is the status and body, for assertion messages.
func (r UIResponse) String() string {
	return fmt.Sprintf("HTTP %d: %s", r.Status, strings.TrimSpace(r.Body))
}

// With returns a copy of c with the header key set to value.
func (c UIClient) With(key, value string) UIClient {
	h := c.Header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set(key, value)
	c.Header = h
	return c
}

// Do sends method path with body (nil for none) and fails the test only on a
// transport error.
func (c UIClient) Do(t *testing.T, method, path string, body io.Reader) UIResponse {
	t.Helper()
	req, err := http.NewRequest(method, c.BaseURL+path, body)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, path, err)
	}
	for k, v := range c.Header {
		req.Header[k] = v
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Host != "" {
		req.Host = c.Host
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, c.BaseURL+path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return UIResponse{Status: resp.StatusCode, Header: resp.Header, Body: string(b)}
}

// Get sends GET path.
func (c UIClient) Get(t *testing.T, path string) UIResponse {
	t.Helper()
	return c.Do(t, http.MethodGet, path, nil)
}

// Post sends POST path with v as the JSON body.
func (c UIClient) Post(t *testing.T, path string, v any) UIResponse {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode %T: %v", v, err)
	}
	return c.Do(t, http.MethodPost, path, bytes.NewReader(b))
}

// TLSClient is an HTTP client that trusts only the CA certificate in caFile.
func TLSClient(t *testing.T, caFile string) *http.Client {
	t.Helper()
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatalf("read CA %s: %v", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatalf("no certificate in %s", caFile)
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
}

// webDir is the checkout's web/ directory.
func webDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate web/: no caller information")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "web")
}

// playwrightReport is the part of Playwright's JSON reporter output the
// harness reads.
type playwrightReport struct {
	Suites []playwrightSuite `json:"suites"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type playwrightSuite struct {
	Title  string            `json:"title"`
	Specs  []playwrightSpec  `json:"specs"`
	Suites []playwrightSuite `json:"suites"`
}

type playwrightSpec struct {
	Title string `json:"title"`
	Tests []struct {
		Status  string `json:"status"` // expected, unexpected, flaky or skipped
		Results []struct {
			Status string `json:"status"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"results"`
	} `json:"tests"`
}

// Playwright runs one spec of web/test/e2e/live with playwright.live.config.ts
// and env on top of the test's environment. The test fails when a spec test
// fails, skips or is flaky, or when none ran: the browser tests follow the
// live suite's rules. The report and failure traces go to the artifacts dir.
func Playwright(t *testing.T, spec string, env map[string]string) {
	t.Helper()
	npx := MustEnv(t, EnvNPX)
	out, err := filepath.Abs(filepath.Join(artifactsDir(), "playwright", nonSlug.ReplaceAllString(strings.ToLower(t.Name()), "-")))
	if err != nil {
		t.Fatalf("artifacts dir: %v", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("artifacts dir: %v", err)
	}
	report := filepath.Join(out, "report.json")
	_ = os.Remove(report)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, npx, "playwright", "test", "--config", "playwright.live.config.ts",
		"--reporter", "json", "--output", filepath.Join(out, "results"), "test/e2e/live/"+spec)
	cmd.Dir = webDir(t)
	cmd.Env = append(os.Environ(), "PLAYWRIGHT_JSON_OUTPUT_NAME="+report, "PLAYWRIGHT_HTML_OPEN=never")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	runErr := cmd.Run()
	t.Logf("$ npx playwright test test/e2e/live/%s (exit %v); report %s", spec, runErr, report)

	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("playwright %s: no JSON report (%v): %v\n%s", spec, runErr, err, stderr.String())
	}
	var r playwrightReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("playwright %s: decode report: %v", spec, err)
	}
	for _, e := range r.Errors {
		t.Errorf("playwright %s: %s", spec, e.Message)
	}
	ran := 0
	var walk func(path string, s playwrightSuite)
	walk = func(path string, s playwrightSuite) {
		if s.Title != "" && !strings.HasSuffix(s.Title, ".ts") {
			path += s.Title + " > "
		}
		for _, sp := range s.Specs {
			for _, pt := range sp.Tests {
				ran++
				if pt.Status == "expected" {
					t.Logf("playwright PASS: %s%s", path, sp.Title)
					continue
				}
				msg := ""
				for _, res := range pt.Results {
					for _, e := range res.Errors {
						msg += "\n" + ansi.ReplaceAllString(e.Message, "")
					}
				}
				t.Errorf("playwright %s: %s%s%s", pt.Status, path, sp.Title, msg)
			}
		}
		for _, sub := range s.Suites {
			walk(path, sub)
		}
	}
	for _, s := range r.Suites {
		walk("", s)
	}
	if ran == 0 {
		t.Errorf("playwright %s: no test ran\n%s", spec, stderr.String())
	}
	if runErr != nil && !t.Failed() {
		t.Errorf("playwright %s: %v\n%s", spec, runErr, stderr.String())
	}
}
