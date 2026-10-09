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
	// defaultBaseBackoff is how long the circuit first stays open. Each
	// failed half-open probe doubles it, so the open time follows the
	// outage: a short outage is over after a few seconds of waiting (#1476).
	defaultBaseBackoff = 5 * time.Second
	// defaultMaxBackoff caps the exponential backoff: the circuit lets a
	// probe through at least this often during a long outage, so it closes
	// at most this long after the SCM is back. A server's Retry-After or
	// rate-limit reset can keep it open longer.
	defaultMaxBackoff = 2 * time.Minute
	// defaultHalfOpenTimeout is how long a half-open probe may take before
	// another caller is allowed to probe (the first one never reported back):
	// the providers' HTTP timeout and a margin.
	defaultHalfOpenTimeout = providerHTTPTimeout + 15*time.Second
)

// CircuitBreaker implements the circuit-breaker pattern for SCM API calls.
// It tracks consecutive failures and opens the circuit when the threshold is
// exceeded. Respects Retry-After and, when the quota is exhausted, the
// rate-limit reset headers.
//
// State transitions:
//
//	Closed → Open: on N consecutive failures (N = FailureThreshold)
//	Open → HalfOpen: when the backoff (or the server's retry time) elapses
//	HalfOpen → Closed: on one success
//	HalfOpen → Open: when the probe fails
//
// In half-open only one caller at a time is let through as the probe.
//
// The backoff grows per opening, not per failed call (#1476): it is
// BaseBackoff when the circuit opens and doubles each time a probe fails, up
// to MaxBackoff. A call admitted while the circuit was closed that fails
// after it opened (a request in flight when the SCM went away) says nothing
// new and is not counted, so a burst of concurrent failures cannot push the
// backoff to its cap.
type CircuitBreaker struct {
	// FailureThreshold is the number of consecutive failures before opening.
	FailureThreshold int
	// BaseBackoff is the base duration for exponential backoff calculation.
	BaseBackoff time.Duration
	// MaxBackoff caps the exponential backoff.
	MaxBackoff time.Duration
	// HalfOpenTimeout is how long a half-open probe may run before another
	// caller may probe, so a probe that never reports back cannot wedge the
	// breaker.
	HalfOpenTimeout time.Duration

	mu               sync.Mutex
	state            CircuitState
	consecutiveFails int
	// openings counts the failed probes since the circuit opened; the
	// backoff is BaseBackoff * 2^openings.
	openings int
	openUntil        time.Time // when to transition Open → HalfOpen
	probeStarted     time.Time // when the current half-open probe was admitted; zero if none
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
// blocked. In half-open only the first caller is admitted as the probe; the
// others are blocked until it calls RecordSuccess or RecordFailure, or until
// HalfOpenTimeout passes.
func (cb *CircuitBreaker) Allow() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.currentState() {
	case CircuitOpen:
		return &ErrCircuitOpen{RetryAfter: cb.openUntil}
	case CircuitHalfOpen:
		now := time.Now()
		if !cb.probeStarted.IsZero() && now.Sub(cb.probeStarted) < cb.HalfOpenTimeout {
			return &ErrCircuitOpen{RetryAfter: cb.probeStarted.Add(cb.HalfOpenTimeout)}
		}
		cb.probeStarted = now
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
	cb.openings = 0
	cb.state = CircuitClosed
	cb.probeStarted = time.Time{}
}

// cancelProbe gives back the half-open probe slot of a call that Allow
// admitted but that was not made, or whose outcome says nothing about this
// circuit (CircuitRegistry), so the next caller may probe at once.
func (cb *CircuitBreaker) cancelProbe() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.probeStarted = time.Time{}
}

// pristine reports whether the breaker holds no state: closed, no failures
// counted and no probe running. Dropping it loses nothing (CircuitRegistry).
func (cb *CircuitBreaker) pristine() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state == CircuitClosed && cb.consecutiveFails == 0 && cb.probeStarted.IsZero()
}

// RecordResponse records the outcome of an HTTP call that returned resp.
// Rate limits and server errors (see IsTransientResponse) count as failures;
// any other response, including a 4xx, counts as success because retrying
// cannot fix it.
func (cb *CircuitBreaker) RecordResponse(resp *http.Response) {
	if IsTransientResponse(resp) {
		cb.RecordFailure(RetryAfterFromResponse(resp))
		return
	}
	cb.RecordSuccess()
}

// RecordFailure records a failed call that started just now: it always
// counts. Providers use RecordFailureFrom with the call's start time.
func (cb *CircuitBreaker) RecordFailure(retryAfter time.Time) {
	cb.RecordFailureFrom(time.Now(), retryAfter)
}

// RecordFailureFrom records a failed call that started at started.
// retryAfter, when known (from the SCM's response headers), is the earliest
// time the circuit may let a call through; otherwise the backoff decides.
//
//   - Closed: the failure is counted; FailureThreshold in a row open the
//     circuit for BaseBackoff.
//   - Half-open, and the call is the probe: the circuit opens again for
//     twice its previous backoff.
//   - Open, or half-open and the call started before the probe: a call
//     that was in flight when the circuit opened failed late. It is not
//     counted (#1476); a retryAfter later than the open window extends it.
func (cb *CircuitBreaker) RecordFailureFrom(started, retryAfter time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.currentState() {
	case CircuitOpen:
		cb.openUntil = latestTime(retryAfter, cb.openUntil)
		return
	case CircuitHalfOpen:
		if cb.probeStarted.IsZero() || started.Before(cb.probeStarted) {
			cb.openUntil = latestTime(retryAfter, cb.openUntil)
			return
		}
		cb.openings++
		cb.openLocked(retryAfter)
		return
	}
	cb.consecutiveFails++
	if cb.consecutiveFails >= cb.FailureThreshold {
		cb.openings = 0
		cb.openLocked(retryAfter)
	}
}

// openLocked opens the circuit for the current backoff, or until retryAfter
// if that is later. Must be called with cb.mu held.
func (cb *CircuitBreaker) openLocked(retryAfter time.Time) {
	cb.openUntil = latestTime(retryAfter, time.Now().Add(cb.backoffDuration(cb.openings)))
	cb.state = CircuitOpen
	cb.probeStarted = time.Time{}
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
// It reads Retry-After (seconds or HTTP-date) and, only when the quota is
// exhausted (remaining is "0"), the rate-limit window reset: X-RateLimit-Reset
// (GitHub, Forgejo) or RateLimit-Reset (GitLab), as a Unix timestamp. The reset
// is sent on every response, so using it for a plain 5xx would keep the
// circuit open for up to an hour. Returns zero time if nothing applies.
func RetryAfterFromResponse(resp *http.Response) time.Time {
	if resp == nil {
		return time.Time{}
	}

	for _, prefix := range []string{"X-RateLimit-", "RateLimit-"} {
		if resp.Header.Get(prefix+"Remaining") != "0" {
			continue
		}
		if v := resp.Header.Get(prefix + "Reset"); v != "" {
			if unix, err := strconv.ParseInt(v, 10, 64); err == nil && unix > 0 {
				return time.Unix(unix, 0)
			}
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

// IsTransientResponse returns true if the response is a rate limit or a
// transient server error: 429, 5xx, or a 403 that carries a rate-limit signal
// (GitHub sends primary and secondary rate limits as 403 with
// X-RateLimit-Remaining: 0 or Retry-After).
func IsTransientResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return true
	case resp.StatusCode >= 500 && resp.StatusCode < 600:
		return true
	case resp.StatusCode == http.StatusForbidden:
		return resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""
	}
	return false
}

// latestTime returns the later of two times.
func latestTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
