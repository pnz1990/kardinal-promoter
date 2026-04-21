// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

// Package observability registers Prometheus metrics for kardinal-promoter.
//
// All metrics required by Stage 19 acceptance criteria plus Lens 3 observability gaps:
//   - kardinal_bundles_total{phase}
//   - kardinal_steps_total{type,result}
//   - kardinal_gate_evaluations_total{result}
//   - kardinal_pr_duration_seconds histogram
//   - kardinal_step_duration_seconds{step} histogram (git-clone, kustomize, etc.)
//   - kardinal_gate_blocking_duration_seconds histogram
//   - kardinal_promotionstep_age_seconds histogram
//
// Metrics are registered once at init time into the controller-runtime
// default registry (which is the prometheus.DefaultRegisterer). They are
// safe to call from any reconciler.
//
// Graph-first compliance: metric emission is a write side effect at
// reconcile time — it does NOT drive any branching logic. Counters are
// incremented after CRD status has been written, never before.
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// BundlesTotal counts Bundle phase transitions. The "phase" label carries
	// the terminal phase: "Verified", "Failed", "Superseded".
	// Only terminal or significant transitions are counted to avoid double-
	// counting on re-reconcile of the same phase.
	BundlesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kardinal_bundles_total",
			Help: "Total number of Bundle promotions by terminal phase (Verified, Failed, Superseded).",
		},
		[]string{"phase"},
	)

	// StepsTotal counts PromotionStep terminal state transitions.
	// "type" is always "PromotionStep". "result" is "succeeded" or "failed".
	StepsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kardinal_steps_total",
			Help: "Total number of PromotionStep terminal states (succeeded, failed).",
		},
		[]string{"type", "result"},
	)

	// GateEvaluationsTotal counts PolicyGate evaluations.
	// "result" is "allowed" (gate passed) or "blocked" (gate failed).
	GateEvaluationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kardinal_gate_evaluations_total",
			Help: "Total PolicyGate evaluations by result (allowed, blocked).",
		},
		[]string{"result"},
	)

	// PRDurationSeconds records the elapsed time between a PromotionStep
	// entering WaitingForMerge and transitioning to Verified (PR merged).
	// This histogram models the human review latency in the promotion loop.
	PRDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kardinal_pr_duration_seconds",
			Help:    "Duration from PR open (WaitingForMerge) to PR merge (Verified), in seconds.",
			Buckets: prometheus.ExponentialBuckets(30, 2, 10), // 30s → ~8.5h in 10 buckets
		},
	)

	// StepDurationSeconds records the execution duration of individual promotion
	// step types (git-clone, kustomize, git-commit, open-pr, health-check, etc.).
	// The "step" label carries the step type name from StepState.Name.
	// Enables per-step latency analysis (e.g. "git-clone p99 is 45s on our SCM host").
	StepDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kardinal_step_duration_seconds",
			Help:    "Execution duration of individual promotion step types, in seconds.",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 12), // 0.1s → ~400s in 12 buckets
		},
		[]string{"step"},
	)

	// GateBlockingDurationSeconds records how long a PolicyGate has been blocking
	// when it transitions from blocked (status.ready=false) to passing (status.ready=true).
	// Without this metric, a Grafana dashboard cannot answer
	// "which gates are blocking prod right now and for how long?" — the most
	// common on-call question for a promotion delay.
	GateBlockingDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kardinal_gate_blocking_duration_seconds",
			Help:    "Duration a PolicyGate was blocked before transitioning to allowed, in seconds.",
			Buckets: prometheus.ExponentialBuckets(60, 2, 10), // 1m → ~17h in 10 buckets
		},
	)

	// PromotionStepAgeSeconds records the age of a PromotionStep when it reaches
	// a terminal state (Verified or Failed). This answers "how old is the oldest
	// in-flight step?" on a Grafana dashboard by observing at terminal transitions.
	PromotionStepAgeSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kardinal_promotionstep_age_seconds",
			Help:    "Age of PromotionStep objects at terminal state (Verified or Failed), in seconds.",
			Buckets: prometheus.ExponentialBuckets(30, 2, 12), // 30s → ~34h in 12 buckets
		},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		BundlesTotal,
		StepsTotal,
		GateEvaluationsTotal,
		PRDurationSeconds,
		StepDurationSeconds,
		GateBlockingDurationSeconds,
		PromotionStepAgeSeconds,
	)
}
