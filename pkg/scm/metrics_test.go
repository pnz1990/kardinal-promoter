// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSCMOperation (#1529): an operation label keeps the method and the API
// resource words of the path, never owners, repositories, branches, numbers
// or SHAs, for every provider's paths.
func TestSCMOperation(t *testing.T) {
	tests := []struct{ method, path, want string }{
		{"POST", "/repos/acme/app/pulls", "POST pulls"},
		{"GET", "/repos/acme/app/pulls?state=open&head=acme:kardinal/1a2b3c4d/b1/prod", "GET pulls"},
		{"PATCH", "/repos/acme/app/pulls/42", "PATCH pulls"},
		{"POST", "/repos/acme/app/issues/42/labels", "POST issues labels"},
		{"DELETE", "/repos/acme/app/git/refs/heads/kardinal/1a2b3c4d/bundle-1/prod", "DELETE git refs heads"},
		{"POST", "/repos/acme/app/statuses/0123456789abcdef0123456789abcdef01234567", "POST statuses"},
		{"POST", "/api/v4/projects/group%2Fapp/merge_requests/7/notes", "POST merge_requests notes"},
		{"PUT", "/api/v1/repos/acme/app/pulls/3/merge", "PUT pulls merge"},
		{"POST", "/2.0/repositories/workspace/app/pullrequests/9/decline", "POST pullrequests decline"},
		{"GET", "/org/project/_apis/git/repositories/app/pullrequests/5?api-version=7.1", "GET git pullrequests"},
		{"GET", "/rate_limit", "GET rate_limit"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, scmOperation(tt.method, tt.path), "%s %s", tt.method, tt.path)
	}
}

// TestLabelCap (#1529): a label keeps its first max values; the rest are
// "other", so a controller that sees many owners has a bounded series count.
func TestLabelCap(t *testing.T) {
	c := &labelCap{max: 3}
	for i := range 3 {
		assert.Equal(t, fmt.Sprintf("o%d", i), c.value(fmt.Sprintf("o%d", i)))
	}
	assert.Equal(t, "other", c.value("o3"))
	assert.Equal(t, "o1", c.value("o1"), "a value seen before keeps its own label")
	assert.Equal(t, "none", ownerLabel(""))
	assert.Equal(t, ownerLabel("acme-cap-test"), ownerLabel("ACME-Cap-Test"), "owners compare case-insensitively")
}

// TestSCMMetrics_GitHubCalls (#1529): every GitHub API call counts by
// provider, owner, operation and result, observes its latency, and sets the
// rate-limit gauges from the response headers; a 5xx is server_error, a 429
// rate_limited, and once the circuit opens a refused call is circuit_open
// with the circuit gauge at 2.
func TestSCMMetrics_GitHubCalls(t *testing.T) {
	reset := time.Now().Add(30 * time.Minute).Unix()
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4321")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()
	g := NewGitHubProvider("t", srv.URL, "")
	ctx := context.Background()
	const owner = "metrics-owner-gh"
	count := func(op, result string) float64 {
		return testutil.ToFloat64(SCMRequestsTotal.WithLabelValues("github", owner, op, result))
	}
	before := count("PATCH pulls", "ok")
	require.NoError(t, g.ClosePR(ctx, owner+"/app", 1))
	assert.Equal(t, before+1, count("PATCH pulls", "ok"))
	assert.Equal(t, 4321.0, testutil.ToFloat64(SCMRateLimitRemaining.WithLabelValues("github", owner)))
	assert.Equal(t, 5000.0, testutil.ToFloat64(SCMRateLimitLimit.WithLabelValues("github", owner)))
	assert.Equal(t, float64(reset), testutil.ToFloat64(SCMRateLimitResetTimestamp.WithLabelValues("github", owner)))
	assert.Equal(t, 0.0, testutil.ToFloat64(SCMCircuitState.WithLabelValues("github", owner)), "closed")
	assert.Positive(t, testutil.CollectAndCount(SCMRequestDurationSeconds), "latency observed")

	status = http.StatusTooManyRequests
	before = count("PATCH pulls", "rate_limited")
	require.Error(t, g.ClosePR(ctx, owner+"/app", 1))
	assert.Equal(t, before+1, count("PATCH pulls", "rate_limited"))
	assert.Equal(t, 2.0, testutil.ToFloat64(SCMCircuitState.WithLabelValues("github", "quota")), "the quota circuit opened")
	before = count("PATCH pulls", "circuit_open")
	require.Error(t, g.ClosePR(ctx, owner+"/app", 1))
	assert.Equal(t, before+1, count("PATCH pulls", "circuit_open"), "refused before any call")

	// A server error on a fresh provider (fresh circuits).
	g2 := NewGitHubProvider("t", srv.URL, "")
	status = http.StatusBadGateway
	before = count("POST issues comments", "server_error")
	require.Error(t, g2.CommentOnPR(ctx, owner+"/app", 1, "x"))
	assert.Equal(t, before+1, count("POST issues comments", "server_error"))
}

// TestGitMetrics_CloneAndPush (#1529): clones and pushes count by result and
// observe their duration; a refused non-fast-forward push is
// non_fast_forward, not error.
func TestGitMetrics_CloneAndPush(t *testing.T) {
	ctx := context.Background()
	c := NewGoGitClient()
	remote := t.TempDir()
	seed := t.TempDir()
	require.NoError(t, initRepo(seed))
	require.NoError(t, c.CommitAll(ctx, seed, "seed", "t", "t@example.com"))
	require.NoError(t, cloneBare(seed, remote))

	ok := func(op, result string) float64 { return testutil.ToFloat64(GitOperationsTotal.WithLabelValues(op, result)) }
	clones, pushes, nonFF := ok("clone", "ok"), ok("push", "ok"), ok("push", "non_fast_forward")
	a, b := t.TempDir()+"/a", t.TempDir()+"/b"
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", a, ""))
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", b, ""))
	assert.Equal(t, clones+2, ok("clone", "ok"))
	for _, d := range []string{a, b} {
		require.NoError(t, writeFile(d, "f-"+filepath.Base(d), "x\n"))
		require.NoError(t, c.CommitAll(ctx, d, "change", "t", "t@example.com"))
	}
	require.NoError(t, c.Push(ctx, a, "origin", "main", "", false))
	require.ErrorIs(t, c.Push(ctx, b, "origin", "main", "", false), ErrNonFastForward)
	assert.Equal(t, pushes+1, ok("push", "ok"))
	assert.Equal(t, nonFF+1, ok("push", "non_fast_forward"))
}

// fakeRoundTripper answers every request with a fixed body.
type fakeRoundTripper struct{ body string }

func (f fakeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(f.body)), Header: http.Header{}, Request: r}, nil
}

// TestCountingTransport (#1529): the bytes of smart-HTTP exchanges count by
// service (upload-pack is fetch, receive-pack is push) and direction; other
// requests are not counted.
func TestCountingTransport(t *testing.T) {
	tr := &countingTransport{base: fakeRoundTripper{body: "0123456789"}}
	bytes := func(svc, dir string) float64 { return testutil.ToFloat64(GitTransferBytesTotal.WithLabelValues(svc, dir)) }
	fetchIn, pushOut, pushIn := bytes("fetch", "received"), bytes("push", "sent"), bytes("push", "received")
	do := func(method, url, body string) {
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		require.NoError(t, err)
		if body == "" {
			req.Body = http.NoBody
		}
		resp, err := tr.RoundTrip(req)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	do("GET", "https://git.example/o/r.git/info/refs?service=git-upload-pack", "")
	do("POST", "https://git.example/o/r.git/git-receive-pack", "PACKDATA!")
	do("GET", "https://git.example/o/r.git/HEAD", "")
	assert.Equal(t, fetchIn+10, bytes("fetch", "received"))
	assert.Equal(t, pushOut+9, bytes("push", "sent"))
	assert.Equal(t, pushIn+10, bytes("push", "received"))
}

func initRepo(dir string) error {
	if _, err := gogit.PlainInitWithOptions(dir, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}}); err != nil {
		return err
	}
	return writeFile(dir, "README.md", "seed\n")
}

func cloneBare(src, dst string) error {
	_, err := gogit.PlainClone(dst, true, &gogit.CloneOptions{URL: src})
	return err
}

func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
}

// TestSCMMetrics_EveryProvider (#1529): every provider counts its calls
// under its own provider label, and GitLab's RateLimit-* headers set the
// rate-limit gauge like GitHub's X-RateLimit-*.
func TestSCMMetrics_EveryProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Remaining", "77")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	providers := map[string]SCMProvider{
		"gitlab":      NewGitLabProvider("t", srv.URL, ""),
		"forgejo":     NewForgejoProvider("t", srv.URL, ""),
		"bitbucket":   NewBitbucketProvider("t", srv.URL, ""),
		"azuredevops": NewAzureDevOpsProvider("t", srv.URL, ""),
	}
	sum := func(name string) float64 {
		total := 0.0
		for _, m := range collectRequests(t) {
			if m.provider == name {
				total += m.value
			}
		}
		return total
	}
	for name, p := range providers {
		before := sum(name)
		_ = p.CommentOnPR(context.Background(), "org/proj/app", 1, "x")
		assert.Greater(t, sum(name), before, "%s counts its calls", name)
	}
	assert.Equal(t, 77.0, testutil.ToFloat64(SCMRateLimitRemaining.WithLabelValues("gitlab", "org")))
}

type requestSample struct {
	provider string
	value    float64
}

// collectRequests reads every kardinal_scm_requests_total series.
func collectRequests(t *testing.T) []requestSample {
	t.Helper()
	ch := make(chan prometheus.Metric, 1024)
	SCMRequestsTotal.Collect(ch)
	close(ch)
	var out []requestSample
	for m := range ch {
		var pb dto.Metric
		require.NoError(t, m.Write(&pb))
		s := requestSample{value: pb.GetCounter().GetValue()}
		for _, l := range pb.GetLabel() {
			if l.GetName() == "provider" {
				s.provider = l.GetValue()
			}
		}
		out = append(out, s)
	}
	return out
}
