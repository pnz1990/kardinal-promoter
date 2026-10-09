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

package scm

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreaker_ClosedAllowsCalls(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < 10; i++ {
		err := cb.Allow()
		assert.NoError(t, err)
	}
	assert.Equal(t, CircuitClosed, cb.State())
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < cb.FailureThreshold-1; i++ {
		cb.RecordFailure(time.Time{})
		assert.Equal(t, CircuitClosed, cb.State())
	}
	cb.RecordFailure(time.Time{})
	assert.Equal(t, CircuitOpen, cb.State())
	err := cb.Allow()
	require.Error(t, err)
	var openErr *ErrCircuitOpen
	require.ErrorAs(t, err, &openErr)
	assert.True(t, openErr.RetryAfter.After(time.Now()))
}

func TestCircuitBreaker_SuccessResetsFails(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < cb.FailureThreshold-1; i++ {
		cb.RecordFailure(time.Time{})
	}
	assert.Equal(t, CircuitClosed, cb.State())
	cb.RecordSuccess()
	cb.RecordFailure(time.Time{})
	assert.Equal(t, CircuitClosed, cb.State())
}

func TestCircuitBreaker_HalfOpenOnProbe(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	require.Equal(t, CircuitOpen, cb.State())
	cb.mu.Lock()
	cb.openUntil = time.Now().Add(-time.Second)
	cb.mu.Unlock()
	err := cb.Allow()
	assert.NoError(t, err)
	assert.Equal(t, CircuitHalfOpen, cb.State())
}

func TestCircuitBreaker_ProbeSuccessCloses(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	cb.mu.Lock()
	cb.openUntil = time.Now().Add(-time.Second)
	cb.mu.Unlock()
	_ = cb.Allow()
	cb.RecordSuccess()
	assert.Equal(t, CircuitClosed, cb.State())
}

func TestCircuitBreaker_ProbeFailureReopens(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	cb.mu.Lock()
	cb.openUntil = time.Now().Add(-time.Second)
	cb.mu.Unlock()
	_ = cb.Allow()
	cb.RecordFailure(time.Time{})
	assert.Equal(t, CircuitOpen, cb.State())
}

func TestRetryAfterFromResponse_RetryAfterHeader(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"120"}},
	}
	retryAfter := RetryAfterFromResponse(resp)
	delta := time.Until(retryAfter)
	assert.Greater(t, delta, 110*time.Second)
	assert.Less(t, delta, 130*time.Second)
}

func TestRetryAfterFromResponse_RateLimitResetHeader(t *testing.T) {
	resetTime := time.Now().Add(5 * time.Minute)
	h := http.Header{}
	h.Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
	h.Set("X-RateLimit-Remaining", "0")
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     h,
	}
	retryAfter := RetryAfterFromResponse(resp)
	delta := time.Until(retryAfter)
	assert.Greater(t, delta, 4*time.Minute)
	assert.Less(t, delta, 6*time.Minute)
}

func TestRetryAfterFromResponse_NilResponse(t *testing.T) {
	retryAfter := RetryAfterFromResponse(nil)
	assert.True(t, retryAfter.IsZero())
}

func TestCircuitBreaker_RecordFailure_WithRetryAfter(t *testing.T) {
	cb := NewCircuitBreaker()
	retryAt := time.Now().Add(5 * time.Minute)
	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(retryAt)
	}
	require.Equal(t, CircuitOpen, cb.State())
	err := cb.Allow()
	var openErr *ErrCircuitOpen
	require.ErrorAs(t, err, &openErr)
	delta := time.Until(openErr.RetryAfter)
	assert.Greater(t, delta, 4*time.Minute)
	assert.Less(t, delta, 6*time.Minute)
}

// TestCircuitBreaker_HalfOpenAdmitsOneProbe proves that when the backoff
// elapses only one caller probes the SCM, and that a probe that never reports
// back does not wedge the breaker (C06-scm-health-18).
func TestCircuitBreaker_HalfOpenAdmitsOneProbe(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	cb.mu.Lock()
	cb.openUntil = time.Now().Add(-time.Second)
	cb.mu.Unlock()

	admitted := 0
	for i := 0; i < 20; i++ {
		if cb.Allow() == nil {
			admitted++
		}
	}
	assert.Equal(t, 1, admitted)

	cb.mu.Lock()
	cb.probeStarted = time.Now().Add(-cb.HalfOpenTimeout - time.Second)
	cb.mu.Unlock()
	assert.NoError(t, cb.Allow(), "an expired probe lets the next caller probe")
	assert.Error(t, cb.Allow())
}

// TestIsTransientResponse covers which responses count against the breaker
// (C06-scm-health-17).
func TestIsTransientResponse(t *testing.T) {
	tests := []struct {
		name   string
		status int
		header map[string]string
		want   bool
	}{
		{"429", http.StatusTooManyRequests, nil, true},
		{"500", http.StatusInternalServerError, nil, true},
		{"502", http.StatusBadGateway, nil, true},
		{"503", http.StatusServiceUnavailable, nil, true},
		{"403 rate limit exhausted", http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0"}, true},
		{"403 secondary rate limit", http.StatusForbidden, map[string]string{"Retry-After": "60"}, true},
		{"403 permission denied", http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "4999"}, false},
		{"404", http.StatusNotFound, nil, false},
		{"422", http.StatusUnprocessableEntity, nil, false},
		{"200", http.StatusOK, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.header {
				h.Set(k, v)
			}
			assert.Equal(t, tc.want, IsTransientResponse(&http.Response{StatusCode: tc.status, Header: h}))
		})
	}
	assert.False(t, IsTransientResponse(nil))
}

// TestRetryAfterFromResponse_ResetOnlyWhenExhausted proves a 5xx with quota
// left does not open the circuit until the hourly reset, and that GitLab's
// RateLimit-Reset is honoured when the quota is gone (C06-scm-health-18).
func TestRetryAfterFromResponse_ResetOnlyWhenExhausted(t *testing.T) {
	reset := strconv.FormatInt(time.Now().Add(50*time.Minute).Unix(), 10)

	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "4000")
	h.Set("X-RateLimit-Reset", reset)
	assert.True(t, RetryAfterFromResponse(&http.Response{StatusCode: http.StatusBadGateway, Header: h}).IsZero())

	g := http.Header{}
	g.Set("RateLimit-Remaining", "0")
	g.Set("RateLimit-Reset", reset)
	delta := time.Until(RetryAfterFromResponse(&http.Response{StatusCode: http.StatusTooManyRequests, Header: g}))
	assert.Greater(t, delta, 45*time.Minute)
	assert.Less(t, delta, 55*time.Minute)
}

// TestDCProjectOf: the circuit owner of a Data Center path is its project
// key, in upper case, for the 1.0 and latest versions of every REST API.
//
// Covers SCM-BBDC-06.
func TestDCProjectOf(t *testing.T) {
	for path, want := range map[string]string{
		"/rest/api/1.0/projects/plat/repos/web/pull-requests":      "PLAT",
		"/rest/api/latest/projects/Plat/repos/web/pull-requests/1": "PLAT",
		"/rest/branch-utils/1.0/projects/PLAT/repos/web/branches":  "PLAT",
		"/rest/branch-utils/latest/projects/plat/repos/web":        "PLAT",
		"/rest/api/latest/projects/~alice/repos/web?limit=1":       "~ALICE",
		"/rest/api/1.0/projects/%7Ealice/repos/web":                "~ALICE",
		"/rest/api/1.0/application-properties":                     "",
		"/rest/api/latest/users/alice":                             "",
		"/projects/PLAT/repos/web":                                 "",
	} {
		assert.Equal(t, want, dcProjectOf(path), path)
	}
}

// openFor is how long cb stays open from now.
func openFor(cb *CircuitBreaker) time.Duration {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return time.Until(cb.openUntil)
}

// expire makes cb's open window end now, so the next Allow probes.
func expire(cb *CircuitBreaker) {
	cb.mu.Lock()
	cb.openUntil = time.Now().Add(-time.Millisecond)
	cb.mu.Unlock()
}

// TestCircuitBreaker_BackoffFollowsTheOutage replays #1476: a 60-second
// outage with dozens of calls in flight. The calls that fail after the
// circuit opened do not count, each failed probe doubles the backoff from
// BaseBackoff, and the first probe after the outage closes the circuit,
// which then starts again from BaseBackoff.
func TestCircuitBreaker_BackoffFollowsTheOutage(t *testing.T) {
	cb := NewCircuitBreaker()
	inFlight := time.Now()
	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailureFrom(time.Now(), time.Time{})
	}
	require.Equal(t, CircuitOpen, cb.State())
	assert.InDelta(t, cb.BaseBackoff.Seconds(), openFor(cb).Seconds(), 1, "opens for BaseBackoff")

	// 40 more calls that were in flight fail late: the window stays.
	for i := 0; i < 40; i++ {
		cb.RecordFailureFrom(inFlight, time.Time{})
	}
	assert.InDelta(t, cb.BaseBackoff.Seconds(), openFor(cb).Seconds(), 1, "late failures do not extend it")

	for i, want := range []time.Duration{2 * cb.BaseBackoff, 4 * cb.BaseBackoff, 8 * cb.BaseBackoff} {
		expire(cb)
		require.NoError(t, cb.Allow(), "probe %d", i)
		// A call in flight since before the probe fails during it: ignored.
		cb.RecordFailureFrom(inFlight, time.Time{})
		require.Equal(t, CircuitHalfOpen, cb.State(), "probe %d: a late failure is not the probe's", i)
		cb.RecordFailureFrom(time.Now(), time.Time{})
		require.Equal(t, CircuitOpen, cb.State())
		assert.InDelta(t, want.Seconds(), openFor(cb).Seconds(), 1, "probe %d failed: backoff doubles", i)
	}

	expire(cb)
	require.NoError(t, cb.Allow())
	cb.RecordSuccess()
	assert.Equal(t, CircuitClosed, cb.State())
	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	assert.InDelta(t, cb.BaseBackoff.Seconds(), openFor(cb).Seconds(), 1, "after closing, the next opening starts from BaseBackoff")
}

// TestCircuitBreaker_BackoffCap: failed probes never keep the circuit open
// past MaxBackoff, so it closes at most MaxBackoff after the SCM is back;
// a server's Retry-After is still honored past it.
func TestCircuitBreaker_BackoffCap(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	for i := 0; i < 20; i++ {
		expire(cb)
		require.NoError(t, cb.Allow())
		cb.RecordFailure(time.Time{})
	}
	assert.InDelta(t, cb.MaxBackoff.Seconds(), openFor(cb).Seconds(), 1)
	assert.LessOrEqual(t, cb.MaxBackoff, 2*time.Minute, "a long outage is noticed over within two minutes")

	later := time.Now().Add(30 * time.Minute)
	cb.RecordFailure(later)
	assert.InDelta(t, 30*time.Minute.Seconds(), openFor(cb).Seconds(), 1, "Retry-After extends an open circuit")
}

// TestCircuitBreaker_HalfOpenTimeout: a probe that hangs frees the slot
// soon after the providers' HTTP timeout, not minutes later.
func TestCircuitBreaker_HalfOpenTimeout(t *testing.T) {
	cb := NewCircuitBreaker()
	assert.Greater(t, cb.HalfOpenTimeout, providerHTTPTimeout)
	assert.LessOrEqual(t, cb.HalfOpenTimeout, providerHTTPTimeout+30*time.Second)
}

// TestCircuitBreaker_HalfOpenWaitersLookAgainSoon: while one caller probes,
// the others are told to come back in a couple of seconds, not when the
// probe would time out.
//
// Covers SCM-BREAKER-HALFOPEN-01.
func TestCircuitBreaker_HalfOpenWaitersLookAgainSoon(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	expire(cb)
	require.NoError(t, cb.Allow(), "the probe")
	var open *ErrCircuitOpen
	require.ErrorAs(t, cb.Allow(), &open)
	assert.InDelta(t, halfOpenRecheck.Seconds(), time.Until(open.RetryAfter).Seconds(), 0.5)
}

// TestCircuitBreaker_LateSuccessDoesNotClose: a call that started before
// the circuit opened and succeeds late does not close it; one that started
// after does.
//
// Covers SCM-BREAKER-LATE-01.
func TestCircuitBreaker_LateSuccessDoesNotClose(t *testing.T) {
	cb := NewCircuitBreaker()
	before := time.Now().Add(-time.Second)
	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	cb.RecordSuccessFrom(before)
	assert.Equal(t, CircuitOpen, cb.State())
	expire(cb)
	require.NoError(t, cb.Allow())
	cb.RecordSuccessFrom(before)
	assert.Equal(t, CircuitHalfOpen, cb.State(), "a late success is not the probe's")
	cb.RecordSuccessFrom(time.Now())
	assert.Equal(t, CircuitClosed, cb.State())
}
