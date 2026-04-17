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
