// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// breakerProviders lists every provider type with a repo owned by "bad" and
// one owned by "good", in each provider's repo format.
var breakerProviders = []struct {
	typ       string
	bad, good string
}{
	{"github", "bad/r", "good/r"},
	{"gitlab", "bad/sub/p", "good/p"},
	{"forgejo", "bad/r", "good/r"},
	{"bitbucket", "bad/r", "good/r"},
	{"azuredevops", "bad/proj/r", "good/proj/r"},
	{"bitbucket-datacenter", "bad/r", "good/r"},
}

// breakerServer answers requests for owner "bad" with badStatus and badHeader,
// and every other request with 404 (a final answer, which the breaker counts
// as a success). It counts the requests per owner.
type breakerServer struct {
	*httptest.Server
	badHits, goodHits atomic.Int32
}

func newBreakerServer(t *testing.T, badStatus int, badHeader map[string]string) *breakerServer {
	t.Helper()
	s := &breakerServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.EscapedPath(), "/bad") {
			s.badHits.Add(1)
			for k, v := range badHeader {
				w.Header().Set(k, v)
			}
			w.WriteHeader(badStatus)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		s.goodHits.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func isCircuitOpen(err error) bool {
	var open *scm.ErrCircuitOpen
	return errors.As(err, &open)
}

// openCircuit calls GetPRStatus on repo until the provider answers with an
// open circuit, and fails the test if it never does.
func openCircuit(t *testing.T, p scm.SCMProvider, repo string) {
	t.Helper()
	for i := 0; i < 10; i++ {
		if _, _, err := p.GetPRStatus(context.Background(), repo, 1); isCircuitOpen(err) {
			return
		}
	}
	t.Fatalf("the circuit for %s never opened", repo)
}

// TestCircuitRegistry_PerOwner covers #1274: a repository owner whose API
// calls fail with 5xx opens only its own circuit. Calls for another owner on
// the same API host still reach the server. Covers SCM-BREAKER-03.
func TestCircuitRegistry_PerOwner(t *testing.T) {
	for _, p := range breakerProviders {
		t.Run(p.typ, func(t *testing.T) {
			srv := newBreakerServer(t, http.StatusBadGateway, nil)
			prov, err := scm.NewProvider(p.typ, "t", srv.URL, "")
			require.NoError(t, err)
			openCircuit(t, prov, p.bad)
			bad := srv.badHits.Load()

			_, _, err = prov.GetPRStatus(context.Background(), p.good, 1)
			require.Error(t, err)
			assert.False(t, isCircuitOpen(err), "another owner's call is not blocked: %v", err)
			assert.EqualValues(t, 1, srv.goodHits.Load(), "the call for %s reached the API", p.good)

			_, _, err = prov.GetPRStatus(context.Background(), p.bad, 1)
			assert.True(t, isCircuitOpen(err), "the failing owner stays blocked: %v", err)
			assert.Equal(t, bad, srv.badHits.Load(), "no request for the failing owner while its circuit is open")
		})
	}
}

// TestCircuitRegistry_RateLimitIsShared covers #1274: an exhausted rate limit
// belongs to the token, not to one owner, so one exhausted response opens a
// circuit every owner shares until the reset. Covers SCM-BREAKER-03.
func TestCircuitRegistry_RateLimitIsShared(t *testing.T) {
	reset := strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10)
	for _, p := range breakerProviders {
		t.Run(p.typ, func(t *testing.T) {
			srv := newBreakerServer(t, http.StatusForbidden, map[string]string{
				"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset,
				"RateLimit-Remaining": "0", "RateLimit-Reset": reset,
			})
			prov, err := scm.NewProvider(p.typ, "t", srv.URL, "")
			require.NoError(t, err)
			_, _, err = prov.GetPRStatus(context.Background(), p.bad, 1)
			require.Error(t, err)
			assert.False(t, isCircuitOpen(err), "the first call reaches the API")

			_, _, err = prov.GetPRStatus(context.Background(), p.good, 1)
			var open *scm.ErrCircuitOpen
			require.ErrorAs(t, err, &open, "the exhausted quota blocks every owner")
			assert.WithinDuration(t, time.Now().Add(30*time.Minute), open.RetryAfter, time.Minute,
				"open until the rate-limit reset")
			assert.Zero(t, srv.goodHits.Load(), "no request while the quota is exhausted")
		})
	}
}

// TestCircuitRegistry_SurvivesReload covers #1274: rotating the token of a
// DynamicProvider keeps the circuits. A reload during an outage used to build
// a provider with a closed circuit, which sent requests at once.
func TestCircuitRegistry_SurvivesReload(t *testing.T) {
	for _, p := range breakerProviders {
		t.Run(p.typ, func(t *testing.T) {
			srv := newBreakerServer(t, http.StatusServiceUnavailable, nil)
			dp, err := scm.NewDynamicProvider(p.typ, "token-v1", srv.URL, "")
			require.NoError(t, err)
			openCircuit(t, dp, p.bad)
			bad := srv.badHits.Load()

			require.NoError(t, dp.Reload("token-v2"))
			_, _, err = dp.GetPRStatus(context.Background(), p.bad, 1)
			assert.True(t, isCircuitOpen(err), "the circuit stays open after the token reload: %v", err)
			assert.Equal(t, bad, srv.badHits.Load(), "no request after the reload while the circuit is open")
		})
	}
}

// TestCircuitRegistry_Concurrent drives one registry from many goroutines
// (go test -race): owners are created on first use, once.
func TestCircuitRegistry_Concurrent(t *testing.T) {
	reg := scm.NewCircuitRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner := "o" + strconv.Itoa(i%5)
			if err := reg.Allow(owner); err == nil {
				reg.Record(owner, time.Now(), &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{}}, nil)
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 5, reg.Owners())
}

// TestCircuitRegistry_Bounded covers the QA finding on #1483: a controller
// that calls many owners does not keep a circuit for each one. Circuits that
// hold no state are dropped once the registry would hold more than its
// bound; an open circuit is kept, and stays open.
func TestCircuitRegistry_Bounded(t *testing.T) {
	reg := scm.NewCircuitRegistry()
	fail := &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{}}
	ok := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	for i := 0; i < 5; i++ {
		require.NoError(t, reg.Allow("failing"))
		reg.Record("failing", time.Now(), fail, nil)
	}
	require.Error(t, reg.Allow("failing"), "the failing owner's circuit is open")
	for i := 0; i < 2000; i++ {
		owner := "o" + strconv.Itoa(i)
		require.NoError(t, reg.Allow(owner))
		reg.Record(owner, time.Now(), ok, nil)
	}
	assert.LessOrEqual(t, reg.Owners(), 257, "idle owners are dropped")
	assert.True(t, isCircuitOpen(reg.Allow("failing")), "an open circuit is never dropped")
}

// TestCircuitRegistry_DCProjectKeyIgnoresCase: Bitbucket Data Center project
// keys are case-insensitive, so "bad" and "BAD" are one owner with one
// circuit. Covers SCM-BREAKER-03.
func TestCircuitRegistry_DCProjectKeyIgnoresCase(t *testing.T) {
	srv := newBreakerServer(t, http.StatusBadGateway, nil)
	prov, err := scm.NewProvider("bitbucket-datacenter", "t", srv.URL, "")
	require.NoError(t, err)
	openCircuit(t, prov, "bad/r")
	_, _, err = prov.GetPRStatus(context.Background(), "BAD/r", 1)
	assert.True(t, isCircuitOpen(err), "the same project in another case shares the circuit: %v", err)
}

// TestCircuitRegistry_InFlightFailures: requests in flight when an owner's
// circuit opens fail with it; recorded with their start time, they do not
// lengthen its backoff (#1476).
func TestCircuitRegistry_InFlightFailures(t *testing.T) {
	reg := scm.NewCircuitRegistry()
	started := time.Now()
	for i := 0; i < 60; i++ {
		reg.Record("acme", started, nil, errors.New("connection refused"))
	}
	err := reg.Allow("acme")
	var open *scm.ErrCircuitOpen
	require.ErrorAs(t, err, &open)
	assert.LessOrEqual(t, time.Until(open.RetryAfter), scm.NewCircuitBreaker().BaseBackoff+time.Second)
}

// TestCircuitRegistry_LateTransientKeepsQuotaOpen: a 5xx answered by a call
// that started before the quota circuit opened does not close it.
//
// Covers SCM-BREAKER-LATE-01.
func TestCircuitRegistry_LateTransientKeepsQuotaOpen(t *testing.T) {
	reg := scm.NewCircuitRegistry()
	before := time.Now().Add(-time.Second)
	exhausted := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"600"}}}
	reg.Record("acme", time.Now(), exhausted, nil)
	var open *scm.ErrCircuitOpen
	require.ErrorAs(t, reg.Allow("acme"), &open)
	reg.Record("other", before, &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{}}, nil)
	reg.Record("other", before, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, nil)
	require.ErrorAs(t, reg.Allow("acme"), &open, "late answers leave the quota circuit open")
	assert.Greater(t, time.Until(open.RetryAfter), 9*time.Minute)
}

// TestCircuitRegistry_RateLimitBodyCounts: GitHub can send a secondary rate
// limit as a 403 with no rate-limit header, only the message in the body
// (APIError.Transient). Such responses count against the shared quota
// circuit, so a stream of them stops calls for every owner; a plain 403
// (missing permission) counts as an answer.
func TestCircuitRegistry_RateLimitBodyCounts(t *testing.T) {
	forbidden := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
	reg := scm.NewCircuitRegistry()
	for i := 0; i < 10; i++ {
		require.NoError(t, reg.Allow("acme"))
		reg.RecordAPIError("acme", time.Now(), forbidden, &scm.APIError{StatusCode: http.StatusForbidden})
	}
	assert.NoError(t, reg.Allow("other"), "a permission 403 opens nothing")

	opened := false
	for i := 0; i < 10 && !opened; i++ {
		if err := reg.Allow("acme"); err != nil {
			opened = true
			break
		}
		reg.RecordAPIError("acme", time.Now(), forbidden, &scm.APIError{StatusCode: http.StatusForbidden, Transient: true,
			Body: `{"message":"You have exceeded a secondary rate limit"}`})
	}
	require.True(t, opened, "rate-limit 403s open the quota circuit")
	assert.True(t, isCircuitOpen(reg.Allow("other")), "the quota circuit is shared by every owner")
}

// TestCircuitRegistry_RateLimitBodyGitHubOnly: only GitHub's 403 JSON
// message is read for a rate limit. A GitHub body-only secondary limit opens
// the shared quota circuit; the same 403 from Forgejo or GitLab is a
// permission error and opens nothing.
func TestCircuitRegistry_RateLimitBodyGitHubOnly(t *testing.T) {
	for _, tc := range []struct {
		typ, bad, good string
		opens          bool
	}{
		{"github", "bad/r", "good/r", true},
		{"forgejo", "bad/r", "good/r", false},
		{"gitlab", "bad/p", "good/p", false},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			var goodHits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.EscapedPath(), "/bad") {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit"}`))
					return
				}
				goodHits.Add(1)
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			}))
			t.Cleanup(srv.Close)
			prov, err := scm.NewProvider(tc.typ, "t", srv.URL, "")
			require.NoError(t, err)
			for i := 0; i < 10; i++ {
				_, _, _ = prov.GetPRStatus(context.Background(), tc.bad, 1)
			}
			_, _, err = prov.GetPRStatus(context.Background(), tc.good, 1)
			assert.Equal(t, tc.opens, isCircuitOpen(err), "%v", err)
			assert.Equal(t, !tc.opens, goodHits.Load() == 1)
		})
	}
}
