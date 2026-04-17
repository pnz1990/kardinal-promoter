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
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// CircuitState represents the state of the circuit breaker.
type CircuitState int

const (
	// CircuitClosed is the normal operating state — requests flow through.
	CircuitClosed CircuitState = iota
	// CircuitOpen is the tripped state — requests are rejected immediately.
	CircuitOpen
	// CircuitHalfOpen allows one probe request to test if the SCM is healthy.
	CircuitHalfOpen
)

// String returns the human-readable name of the circuit state.
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return fmt.Sprintf("CircuitState(%d)", int(s))
	}
}

const (
	// defaultFailureThreshold is the number of consecutive failures before opening.
	defaultFailureThreshold = 5
	// defaultBaseBackoff is the base backoff duration for exponential backoff.
	defaultBaseBackoff = 30 * time.Second
	// defaultMaxBackoff caps the exponential backoff.
	defaultMaxBackoff = 10 * time.Minute
	// defaultHalfOpenTimeout is how long the circuit stays open before moving to half-open.
	defaultHalfOpenTimeout = 2 * time.Minute
)

// CircuitBreaker implements the circuit-breaker pattern for SCM API calls.
// It tracks consecutive failures and opens the circuit when the threshold is
// exceeded. Respects Retry-After and X-RateLimit-Reset headers.
//
// State transitions:
//
//	Closed → Open: on N consecutive failures (N = FailureThreshold)
//	Open → HalfOpen: after HalfOpenTimeout elapses
//	HalfOpen → Closed: on one success
//	HalfOpen → Open: on one failure
type CircuitBreaker struct {
	// FailureThreshold is the number of consecutive failures before opening.
	FailureThreshold int
	// BaseBackoff is the base duration for exponential backoff calculation.
	BaseBackoff time.Duration
	// MaxBackoff caps the exponential backoff.
	MaxBackoff time.Duration
	// HalfOpenTimeout is how long the circuit stays open before half-open probe.
	HalfOpenTimeout time.Duration

	mu               sync.Mutex
	state            CircuitState
	consecutiveFails int
	openUntil        time.Time // when to transition Open → HalfOpen
}

// NewCircuitBreaker creates a circuit breaker with sensible defaults.
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		FailureThreshold: defaultFailureThreshold,
		BaseBackoff:      defaultBaseBackoff,
		MaxBackoff:       defaultMaxBackoff,
		HalfOpenTimeout:  defaultHalfOpenTimeout,
	}
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.currentState()
}

// currentState returns the current state, potentially transitioning Open → HalfOpen.
// Must be called with cb.mu held.
func (cb *CircuitBreaker) currentState() CircuitState {
	if cb.state == CircuitOpen && time.Now().After(cb.openUntil) {
		cb.state = CircuitHalfOpen
	}
	return cb.state
}

// ErrCircuitOpen is returned when the circuit is open and requests are blocked.
type ErrCircuitOpen struct {
	// RetryAfter is when the circuit will allow probes.
	RetryAfter time.Time
}

func (e *ErrCircuitOpen) Error() string {
	return fmt.Sprintf("SCM circuit open until %s", e.RetryAfter.UTC().Format(time.RFC3339))
}

// Allow returns nil if the call may proceed, or ErrCircuitOpen if it must be
// blocked. It also returns the probe flag — true when this call is the
// half-open probe (the caller should call RecordSuccess/RecordFailure when done).
func (cb *CircuitBreaker) Allow() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.currentState() {
	case CircuitClosed:
		return nil
	case CircuitOpen:
		return &ErrCircuitOpen{RetryAfter: cb.openUntil}
	case CircuitHalfOpen:
		// Allow the probe through; transition will be decided by RecordSuccess/RecordFailure
		return nil
	default:
		return nil
	}
}

// RecordSuccess records a successful call and resets the failure counter.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFails = 0
	cb.state = CircuitClosed
}

// RecordFailure records a failed call.
// If the failure count exceeds FailureThreshold, the circuit opens.
// If a retryAfter time is known (from SCM response headers), it is used
// as the minimum open duration; otherwise exponential backoff is used.
func (cb *CircuitBreaker) RecordFailure(retryAfter time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.currentState() == CircuitHalfOpen {
		// Probe failed — reopen the circuit with doubled timeout
		backoff := cb.backoffDuration(cb.consecutiveFails)
		cb.openUntil = latestTime(retryAfter, time.Now().Add(backoff))
		cb.state = CircuitOpen
		cb.consecutiveFails++
		return
	}

	cb.consecutiveFails++
	if cb.consecutiveFails >= cb.FailureThreshold {
		backoff := cb.backoffDuration(cb.consecutiveFails - cb.FailureThreshold)
		cb.openUntil = latestTime(retryAfter, time.Now().Add(backoff))
		cb.state = CircuitOpen
	}
}

// backoffDuration returns the exponential backoff for the given step.
// Step 0 = BaseBackoff, step 1 = 2*BaseBackoff, step 2 = 4*BaseBackoff, ...
// Capped at MaxBackoff.
func (cb *CircuitBreaker) backoffDuration(step int) time.Duration {
	if step < 0 {
		step = 0
	}
	// 2^step * BaseBackoff, capped at MaxBackoff
	factor := math.Pow(2, float64(step))
	d := time.Duration(float64(cb.BaseBackoff) * factor)
	if d > cb.MaxBackoff {
		d = cb.MaxBackoff
	}
	return d
}

// RetryAfterFromResponse extracts the retry-after time from HTTP response headers.
// Checks Retry-After (seconds or HTTP-date) and X-RateLimit-Reset (Unix timestamp).
// Returns zero time if no header is present or parseable.
func RetryAfterFromResponse(resp *http.Response) time.Time {
	if resp == nil {
		return time.Time{}
	}

	// X-RateLimit-Reset is a Unix timestamp (GitHub, GitLab)
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0)
		}
	}

	// Retry-After can be seconds or HTTP-date
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs > 0 {
			return time.Now().Add(time.Duration(secs) * time.Second)
		}
		// Try HTTP-date format
		if t, err := http.ParseTime(v); err == nil {
			return t
		}
	}

	return time.Time{}
}

// IsRateLimitError returns true if the HTTP status code indicates a rate limit
// (429 Too Many Requests) or a transient server error (5xx).
func IsRateLimitError(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		(statusCode >= 500 && statusCode < 600)
}

// latestTime returns the later of two times.
func latestTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
