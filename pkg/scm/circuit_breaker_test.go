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
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func newTestCircuit() *scm.CircuitBreaker {
	cb := scm.NewCircuitBreaker()
	cb.FailureThreshold = 3
	cb.BaseBackoff = 100 * time.Millisecond
	cb.MaxBackoff = 1 * time.Second
	cb.HalfOpenTimeout = 100 * time.Millisecond
	return cb
}

// TestCircuitBreaker_ClosedToOpen verifies the circuit opens after FailureThreshold
// consecutive failures.
func TestCircuitBreaker_ClosedToOpen(t *testing.T) {
	cb := newTestCircuit()

	// Initial state: closed
	assert.Equal(t, scm.CircuitClosed, cb.State())
	assert.NoError(t, cb.Allow())

	// Record failures up to (but not including) threshold — still closed
	for i := 0; i < cb.FailureThreshold-1; i++ {
		cb.RecordFailure(time.Time{})
	}
	assert.Equal(t, scm.CircuitClosed, cb.State())
	assert.NoError(t, cb.Allow())

	// Final failure trips the circuit
	cb.RecordFailure(time.Time{})
	assert.Equal(t, scm.CircuitOpen, cb.State())

	// Allow returns ErrCircuitOpen
	err := cb.Allow()
	require.Error(t, err)
	var circuitErr *scm.ErrCircuitOpen
	assert.True(t, errors.As(err, &circuitErr))
}

// TestCircuitBreaker_SuccessResetsFails verifies that a success resets the counter.
func TestCircuitBreaker_SuccessResetsFails(t *testing.T) {
	cb := newTestCircuit()

	// Two failures — below threshold
	cb.RecordFailure(time.Time{})
	cb.RecordFailure(time.Time{})
	assert.Equal(t, scm.CircuitClosed, cb.State())

	// Success resets
	cb.RecordSuccess()
	assert.Equal(t, scm.CircuitClosed, cb.State())

	// Two more failures — below threshold again (count reset to 0)
	cb.RecordFailure(time.Time{})
	cb.RecordFailure(time.Time{})
	assert.Equal(t, scm.CircuitClosed, cb.State())
}

// TestCircuitBreaker_OpenToHalfOpen verifies the circuit transitions to HalfOpen
// after HalfOpenTimeout.
func TestCircuitBreaker_OpenToHalfOpen(t *testing.T) {
	cb := newTestCircuit()

	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	assert.Equal(t, scm.CircuitOpen, cb.State())

	// Wait for half-open timeout
	time.Sleep(cb.HalfOpenTimeout + 20*time.Millisecond)

	assert.Equal(t, scm.CircuitHalfOpen, cb.State())
	assert.NoError(t, cb.Allow(), "half-open circuit should allow one probe")
}

// TestCircuitBreaker_HalfOpenSuccess verifies the circuit closes on probe success.
func TestCircuitBreaker_HalfOpenSuccess(t *testing.T) {
	cb := newTestCircuit()

	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	time.Sleep(cb.HalfOpenTimeout + 20*time.Millisecond)
	assert.Equal(t, scm.CircuitHalfOpen, cb.State())

	cb.RecordSuccess()
	assert.Equal(t, scm.CircuitClosed, cb.State())
	assert.NoError(t, cb.Allow())
}

// TestCircuitBreaker_HalfOpenFailure verifies the circuit reopens on probe failure.
func TestCircuitBreaker_HalfOpenFailure(t *testing.T) {
	cb := newTestCircuit()

	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(time.Time{})
	}
	time.Sleep(cb.HalfOpenTimeout + 20*time.Millisecond)
	assert.Equal(t, scm.CircuitHalfOpen, cb.State())

	cb.RecordFailure(time.Time{})
	assert.Equal(t, scm.CircuitOpen, cb.State())
}

// TestCircuitBreaker_RetryAfterHeader verifies the circuit respects the
// RetryAfter hint from the SCM response.
func TestCircuitBreaker_RetryAfterHeader(t *testing.T) {
	cb := newTestCircuit()

	// Set a retry-after 5 seconds from now
	retryAfter := time.Now().Add(5 * time.Second)

	for i := 0; i < cb.FailureThreshold; i++ {
		cb.RecordFailure(retryAfter)
	}
	assert.Equal(t, scm.CircuitOpen, cb.State())

	// The circuit should still be open (retryAfter hasn't elapsed)
	err := cb.Allow()
	require.Error(t, err)
	var circuitErr *scm.ErrCircuitOpen
	require.True(t, errors.As(err, &circuitErr))
	// The openUntil should be at least retryAfter
	assert.True(t, circuitErr.RetryAfter.After(time.Now().Add(4*time.Second)),
		"circuit should be open at least until retryAfter")
}

// TestRetryAfterFromResponse verifies parsing of X-RateLimit-Reset and Retry-After headers.
func TestRetryAfterFromResponse(t *testing.T) {
	t.Run("X-RateLimit-Reset unix timestamp", func(t *testing.T) {
		future := time.Now().Add(60 * time.Second)
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("X-RateLimit-Reset", fmt.Sprintf("%d", future.Unix()))

		got := scm.RetryAfterFromResponse(resp)
		require.False(t, got.IsZero())
		assert.WithinDuration(t, future, got, time.Second)
	})

	t.Run("Retry-After seconds", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("Retry-After", "30")

		got := scm.RetryAfterFromResponse(resp)
		require.False(t, got.IsZero())
		assert.WithinDuration(t, time.Now().Add(30*time.Second), got, 2*time.Second)
	})

	t.Run("no header", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		got := scm.RetryAfterFromResponse(resp)
		assert.True(t, got.IsZero())
	})

	t.Run("nil response", func(t *testing.T) {
		got := scm.RetryAfterFromResponse(nil)
		assert.True(t, got.IsZero())
	})
}

// TestIsRateLimitError verifies detection of rate-limit and transient errors.
func TestIsRateLimitError(t *testing.T) {
	assert.True(t, scm.IsRateLimitError(429))
	assert.True(t, scm.IsRateLimitError(500))
	assert.True(t, scm.IsRateLimitError(503))
	assert.False(t, scm.IsRateLimitError(404))
	assert.False(t, scm.IsRateLimitError(422))
	assert.False(t, scm.IsRateLimitError(200))
}
