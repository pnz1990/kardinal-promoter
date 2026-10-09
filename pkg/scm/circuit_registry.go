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
	"net/url"
	"strings"
	"sync"
	"time"
)

// CircuitRegistry holds the circuit breakers of one SCM API host (one
// provider): one per repository owner, and one for the token's rate-limit
// quota (#1274).
//
//   - A network error, or a 5xx or 429 that is not a rate-limit exhaustion,
//     counts against the owner of the repository the call was for, so one
//     failing org or user opens only its own circuit and calls for other
//     owners on the same host go on.
//   - A rate-limit exhaustion (X-RateLimit-Remaining or RateLimit-Remaining
//     is 0, a 429, or a 403 with Retry-After: GitHub's secondary limit)
//     opens the shared quota circuit at once, until the server's reset or
//     Retry-After: the quota belongs to the token, so every owner waits.
//
// A DynamicProvider keeps its registry and hands it to every provider it
// builds, so a token rotation during an outage does not reset the circuits.
// A rotation while the quota circuit is open still waits for the reset; the
// new token is first used by the half-open probe.
//
// Safe for concurrent use.
type CircuitRegistry struct {
	mu     sync.Mutex
	owners map[string]*CircuitBreaker
	quota  *CircuitBreaker
}

// NewCircuitRegistry returns a registry with no owner circuits yet and a
// closed quota circuit.
func NewCircuitRegistry() *CircuitRegistry {
	quota := NewCircuitBreaker()
	// The server says when the quota resets: one answer is enough.
	quota.FailureThreshold = 1
	return &CircuitRegistry{owners: map[string]*CircuitBreaker{}, quota: quota}
}

// maxIdleOwners bounds the owner circuits the registry keeps: when a new
// owner would make it hold more, the circuits that hold no state (closed, no
// failure counted, no probe) are dropped. Only owners whose calls are
// failing keep a circuit beyond it, so the registry does not grow with every
// owner a controller ever called.
const maxIdleOwners = 256

// owner returns the circuit of owner, creating it on first use. Owners are
// compared case-insensitively, as GitHub, GitLab and Forgejo compare them.
func (r *CircuitRegistry) owner(owner string) *CircuitBreaker {
	key := strings.ToLower(owner)
	r.mu.Lock()
	defer r.mu.Unlock()
	cb, ok := r.owners[key]
	if !ok {
		if len(r.owners) >= maxIdleOwners {
			r.pruneLocked()
		}
		cb = NewCircuitBreaker()
		r.owners[key] = cb
	}
	return cb
}

// pruneLocked drops the owner circuits that hold no state. A call that took
// such a circuit before it was dropped records into the dropped copy: at
// most one failure is not counted. Must be called with r.mu held.
func (r *CircuitRegistry) pruneLocked() {
	for k, cb := range r.owners {
		if cb.pristine() {
			delete(r.owners, k)
		}
	}
}

// Owners returns how many owner circuits the registry holds.
func (r *CircuitRegistry) Owners() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.owners)
}

// Allow returns nil if a call for a repository of owner may proceed, or an
// *ErrCircuitOpen when the owner's circuit or the quota circuit is open. A
// half-open circuit admits one probe, as CircuitBreaker.Allow does.
func (r *CircuitRegistry) Allow(owner string) error {
	ob := r.owner(owner)
	if err := ob.Allow(); err != nil {
		return err
	}
	if err := r.quota.Allow(); err != nil {
		// No call is made: give the owner's probe slot back.
		ob.cancelProbe()
		return err
	}
	return nil
}

// Record records the outcome of a call for a repository of owner that Allow
// admitted and that started at started: callErr for a request that got no
// response, else resp.
func (r *CircuitRegistry) Record(owner string, started time.Time, resp *http.Response, callErr error) {
	ob := r.owner(owner)
	switch {
	case callErr != nil || resp == nil:
		// Says nothing about the quota.
		ob.RecordFailureFrom(started, time.Time{})
		r.quota.cancelProbe()
	case IsQuotaExhausted(resp):
		r.quota.RecordFailureFrom(started, RetryAfterFromResponse(resp))
		ob.cancelProbe()
	case IsTransientResponse(resp):
		ob.RecordFailureFrom(started, RetryAfterFromResponse(resp))
		// The quota answered, but a call that started before the quota
		// circuit opened does not close it (RecordSuccessFrom).
		r.quota.RecordSuccessFrom(started)
	default:
		ob.RecordSuccessFrom(started)
		r.quota.RecordSuccessFrom(started)
	}
}

// RecordAPIError records an error response the way Record does, but also
// reads apiErr: GitHub can send a secondary rate limit as a 403 whose JSON
// message is the only signal (APIError.Transient, isGitHubRateLimitMessage;
// other providers' bodies are not read), and that counts against the shared
// quota circuit like any other exhausted limit. started is the call's start
// time, as for Record.
func (r *CircuitRegistry) RecordAPIError(owner string, started time.Time, resp *http.Response, apiErr *APIError) {
	if resp != nil && resp.StatusCode == http.StatusForbidden && apiErr != nil && apiErr.Transient &&
		!IsTransientResponse(resp) && !IsQuotaExhausted(resp) {
		r.quota.RecordFailureFrom(started, time.Time{})
		r.owner(owner).cancelProbe()
		return
	}
	r.Record(owner, started, resp, nil)
}

// IsQuotaExhausted reports whether resp says the token's rate limit is used
// up: X-RateLimit-Remaining (GitHub, Forgejo) or RateLimit-Remaining (GitLab)
// is 0 on an error response, the response is a 429, or it is a 403 with
// Retry-After (GitHub's secondary rate limit).
func IsQuotaExhausted(resp *http.Response) bool {
	if resp == nil || resp.StatusCode < 400 {
		return false
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return true
	case resp.Header.Get("X-RateLimit-Remaining") == "0", resp.Header.Get("RateLimit-Remaining") == "0":
		return true
	case resp.StatusCode == http.StatusForbidden:
		return resp.Header.Get("Retry-After") != ""
	}
	return false
}

// ownerFromPath returns the repository owner in an API path that starts with
// prefix and then the owner (GitHub /repos/<owner>/..., Forgejo
// /api/v1/repos/<owner>/..., Bitbucket /2.0/repositories/<workspace>/...,
// Azure DevOps /<organization>/...), or the URL-escaped project path (GitLab
// /api/v4/projects/<group%2Fproject>/...: the top-level group). It returns ""
// for a path without one; those calls share one circuit.
func ownerFromPath(path, prefix string) string {
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return ""
	}
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	if u, err := url.PathUnescape(rest); err == nil {
		rest = u
	}
	owner, _, _ := strings.Cut(rest, "/")
	return owner
}

// states returns the state of owner's circuit and of the quota circuit, for
// the kardinal_scm_circuit_state metric.
func (r *CircuitRegistry) states(owner string) (CircuitState, CircuitState) {
	return r.owner(owner).State(), r.quota.State()
}
