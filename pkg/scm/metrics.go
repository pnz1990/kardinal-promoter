// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// SCM API and git metrics (#1529). Label cardinality is bounded:
//   - provider: the five providers.
//   - owner: the repository owner (organization, user, group, workspace),
//     never the repository, at most maxLabelBytes long. An owner gets its
//     own value only once a call for it got a 2xx response, so a stream of
//     made-up owners that only ever fail cannot fill the slots; at most
//     maxOwnerLabels distinct owners, the rest are labelOther. Calls without
//     an owner are labelNone, the quota circuit labelQuota.
//   - operation: the HTTP method and the API resource words of the path
//     (scmOperation), never names, numbers or SHAs; at most
//     maxOperationLabels, the rest labelOther.
//   - result, code class and git operation: fixed sets.
//
// The reserved values (labelOther, labelNone, labelQuota) start with "_",
// which GitHub, Bitbucket and Azure DevOps owner names cannot, so a real
// owner does not report under them.
//
// They are written as calls happen and drive nothing: no reconciler reads them.
var (
	// SCMRequestsTotal counts SCM API calls by outcome: ok, client_error
	// (4xx), server_error (5xx), rate_limited (quota exhausted), network_error
	// (no response) and circuit_open (refused before any call).
	SCMRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kardinal_scm_requests_total",
		Help: "SCM API calls by provider, repository owner, operation and result.",
	}, []string{"provider", "owner", "operation", "result"})

	// SCMRequestDurationSeconds is the latency of SCM API calls that were
	// made (not those a circuit refused).
	SCMRequestDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kardinal_scm_request_duration_seconds",
		Help:    "SCM API call latency by provider and operation.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	}, []string{"provider", "operation"})

	// SCMRateLimitRemaining is the rate-limit quota the provider reported on
	// its last response for the owner's calls (X-RateLimit-Remaining or
	// RateLimit-Remaining). Absent until a response carried the header.
	SCMRateLimitRemaining = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kardinal_scm_rate_limit_remaining",
		Help: "Rate-limit requests remaining, from the provider's last response headers.",
	}, []string{"provider", "owner"})

	// SCMRateLimitLimit is the quota size reported with it (X-RateLimit-Limit
	// or RateLimit-Limit).
	SCMRateLimitLimit = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kardinal_scm_rate_limit_limit",
		Help: "Rate-limit quota size, from the provider's last response headers.",
	}, []string{"provider", "owner"})

	// SCMRateLimitResetTimestamp is when the quota resets, as a Unix time
	// (X-RateLimit-Reset or RateLimit-Reset).
	SCMRateLimitResetTimestamp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kardinal_scm_rate_limit_reset_timestamp_seconds",
		Help: "When the rate-limit quota resets (Unix seconds), from the provider's last response headers.",
	}, []string{"provider", "owner"})

	// SCMCircuitState is the state of a circuit breaker: 0 closed, 1 half-open,
	// 2 open. owner labelQuota is the token's rate-limit circuit.
	SCMCircuitState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kardinal_scm_circuit_state",
		Help: "SCM circuit breaker state: 0 closed, 1 half-open, 2 open (owner \"_quota\": the rate-limit circuit).",
	}, []string{"provider", "owner"})

	// GitOperationsTotal counts git clones and pushes by result (ok, error,
	// non_fast_forward).
	GitOperationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kardinal_git_operations_total",
		Help: "Git clones and pushes by operation and result.",
	}, []string{"operation", "result"})

	// GitOperationDurationSeconds is the duration of git clones and pushes.
	GitOperationDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kardinal_git_operation_duration_seconds",
		Help:    "Git clone and push duration by operation.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"operation"})

	// GitTransferBytesTotal counts the bytes git sent and received over
	// HTTP(S) (smart HTTP: upload-pack for clones and fetches, receive-pack
	// for pushes). Git over ssh is not counted.
	GitTransferBytesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kardinal_git_transfer_bytes_total",
		Help: "Bytes git sent and received over HTTP(S) by git_service (fetch, push) and direction (sent, received).",
	}, []string{"git_service", "direction"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(SCMRequestsTotal, SCMRequestDurationSeconds, SCMRateLimitRemaining,
		SCMRateLimitLimit, SCMRateLimitResetTimestamp, SCMCircuitState, GitOperationsTotal,
		GitOperationDurationSeconds, GitTransferBytesTotal)
}

const (
	maxOwnerLabels     = 50
	maxOperationLabels = 100
	// maxLabelBytes bounds the length of an owner or operation value.
	maxLabelBytes = 64
)

// Reserved label values. A real owner or operation is never one of them.
const (
	labelOther = "_other"
	labelNone  = "_none"
	labelQuota = "_quota"
)

// labelCap keeps the first max distinct values of a label and maps the rest
// to labelOther, so a controller that sees many owners has bounded series.
type labelCap struct {
	mu   sync.Mutex
	max  int
	seen map[string]bool
}

// value admits v, if a slot is free, and returns its label.
func (c *labelCap) value(v string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[v] {
		return v
	}
	if len(c.seen) >= c.max {
		return labelOther
	}
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	c.seen[v] = true
	return v
}

// peek returns v's label without admitting it: v when it has a slot,
// labelOther otherwise.
func (c *labelCap) peek(v string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[v] {
		return v
	}
	return labelOther
}

// truncateLabel cuts v to maxLabelBytes without splitting a UTF-8 sequence.
func truncateLabel(v string) string {
	if len(v) <= maxLabelBytes {
		return v
	}
	cut := maxLabelBytes
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut]
}

var (
	ownerLabels     = &labelCap{max: maxOwnerLabels}
	operationLabels = &labelCap{max: maxOperationLabels}
)

// ownerKey is the owner as a label value would be: lowercased (owners
// compare case-insensitively) and at most maxLabelBytes; labelNone for a
// call without one.
func ownerKey(owner string) string {
	if owner == "" {
		return labelNone
	}
	return truncateLabel(strings.ToLower(owner))
}

// ownerLabel is the owner label of a call that got a 2xx response: it admits
// the owner if a slot is free.
func ownerLabel(owner string) string {
	k := ownerKey(owner)
	if k == labelNone {
		return k
	}
	return ownerLabels.value(k)
}

// knownOwnerLabel is the owner label of any other call: the owner's own
// value only if a 2xx response admitted it before, labelOther otherwise.
func knownOwnerLabel(owner string) string {
	k := ownerKey(owner)
	if k == labelNone {
		return k
	}
	return ownerLabels.peek(k)
}

// apiWords are the path segments an operation label keeps: the resources of
// the five providers' APIs the controller calls. Every other segment (an
// owner, a repository, a branch, a number, a SHA) is dropped.
var apiWords = map[string]bool{
	"repos": true, "repositories": true, "projects": true, "pulls": true, "pullrequests": true,
	"pullrequest": true, "merge_requests": true, "merge": true, "issues": true, "comments": true,
	"labels": true, "reviews": true, "approvals": true, "approve": true, "statuses": true,
	"status": true, "commits": true, "commit": true, "git": true, "refs": true, "heads": true,
	"branches": true, "branch": true, "notes": true, "threads": true, "decline": true,
	"user": true, "rate_limit": true, "reviewers": true, "assignees": true, "requested_reviewers": true,
	"_apis": true, "properties": true, "diffs": true, "pull": true, "contents": true,
}

// scmOperation is the operation label of a call: "<METHOD> <resource words>",
// for example "POST pulls", "GET pulls", "POST issues comments".
func scmOperation(method, path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	words := []string{method}
	for _, seg := range strings.Split(path, "/") {
		seg = strings.ToLower(seg)
		if apiWords[seg] && seg != "repos" && seg != "repositories" && seg != "projects" && seg != "_apis" {
			words = append(words, seg)
		}
	}
	if len(words) > 6 {
		words = words[:6]
	}
	return operationLabels.value(truncateLabel(strings.Join(words, " ")))
}

// scmCall instruments one SCM API call of provider.
type scmCall struct {
	provider, owner, operation string
	start                      time.Time
}

// startSCMCall starts measuring a call. c.owner is the raw owner; its label
// is decided when the outcome is known (done, circuitOpen).
func startSCMCall(provider, owner, method, path string) *scmCall {
	return &scmCall{provider: provider, owner: owner, operation: scmOperation(method, path), start: time.Now()}
}

// circuitOpen records a call the circuit breaker refused.
func (c *scmCall) circuitOpen(reg *CircuitRegistry, owner string) {
	label := knownOwnerLabel(c.owner)
	SCMRequestsTotal.WithLabelValues(c.provider, label, c.operation, "circuit_open").Inc()
	c.circuits(reg, owner, label)
}

// done records the outcome of a call that was made: resp, or callErr when it
// got no response. reg and owner are the call's circuits, recorded after
// CircuitRegistry.Record.
func (c *scmCall) done(resp *http.Response, callErr error, reg *CircuitRegistry, owner string) {
	SCMRequestDurationSeconds.WithLabelValues(c.provider, c.operation).Observe(time.Since(c.start).Seconds())
	result := "ok"
	switch {
	case callErr != nil || resp == nil:
		result = "network_error"
	case IsQuotaExhausted(resp):
		result = "rate_limited"
	case resp.StatusCode >= 500:
		result = "server_error"
	case resp.StatusCode >= 400:
		result = "client_error"
	}
	label := knownOwnerLabel(c.owner)
	if resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		label = ownerLabel(c.owner)
	}
	SCMRequestsTotal.WithLabelValues(c.provider, label, c.operation, result).Inc()
	if resp != nil {
		recordRateLimit(c.provider, label, resp.Header)
	}
	c.circuits(reg, owner, label)
}

func (c *scmCall) circuits(reg *CircuitRegistry, owner, label string) {
	if reg == nil {
		return
	}
	ownerState, quotaState := reg.states(owner)
	SCMCircuitState.WithLabelValues(c.provider, label).Set(circuitValue(ownerState))
	SCMCircuitState.WithLabelValues(c.provider, labelQuota).Set(circuitValue(quotaState))
}

// circuitValue is the gauge value of a circuit state: 0 closed, 1 half-open,
// 2 open (the order of growing trouble, not CircuitState's numbering).
func circuitValue(s CircuitState) float64 {
	switch s {
	case CircuitHalfOpen:
		return 1
	case CircuitOpen:
		return 2
	default:
		return 0
	}
}

// recordRateLimit sets the rate-limit gauges from the headers GitHub,
// Forgejo, Gitea and Azure DevOps (X-RateLimit-*) and GitLab (RateLimit-*)
// send. Headers that are absent or not numbers leave the gauges unchanged.
func recordRateLimit(provider, owner string, h http.Header) {
	get := func(name string) (float64, bool) {
		for _, prefix := range []string{"X-RateLimit-", "RateLimit-"} {
			if v := h.Get(prefix + name); v != "" {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					return f, true
				}
			}
		}
		return 0, false
	}
	if v, ok := get("Remaining"); ok {
		SCMRateLimitRemaining.WithLabelValues(provider, owner).Set(v)
	}
	if v, ok := get("Limit"); ok {
		SCMRateLimitLimit.WithLabelValues(provider, owner).Set(v)
	}
	if v, ok := get("Reset"); ok && v > 1e9 { // a Unix time, not seconds-until
		SCMRateLimitResetTimestamp.WithLabelValues(provider, owner).Set(v)
	}
}

// gitResult is the result label of a git operation.
func gitResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrNonFastForward):
		return "non_fast_forward"
	default:
		return "error"
	}
}

// observeGit records one git clone or push that started at start.
func observeGit(operation string, start time.Time, err error) {
	GitOperationDurationSeconds.WithLabelValues(operation).Observe(time.Since(start).Seconds())
	GitOperationsTotal.WithLabelValues(operation, gitResult(err)).Inc()
}
