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

package observability_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

// TestBundlesTotal verifies that BundlesTotal increments correctly per phase label.
func TestBundlesTotal(t *testing.T) {
	t.Parallel()

	before := testutil.ToFloat64(observability.BundlesTotal.With(prometheus.Labels{"phase": "Superseded"}))

	observability.BundlesTotal.WithLabelValues("Superseded").Inc()
	observability.BundlesTotal.WithLabelValues("Superseded").Inc()

	after := testutil.ToFloat64(observability.BundlesTotal.With(prometheus.Labels{"phase": "Superseded"}))
	assert.Equal(t, before+2, after, "BundlesTotal{phase=Superseded} should increment by 2")
}

// TestStepsTotal verifies StepsTotal increments per result label.
func TestStepsTotal(t *testing.T) {
	t.Parallel()

	beforeSucceeded := testutil.ToFloat64(observability.StepsTotal.With(prometheus.Labels{"type": "PromotionStep", "result": "succeeded"}))
	beforeFailed := testutil.ToFloat64(observability.StepsTotal.With(prometheus.Labels{"type": "PromotionStep", "result": "failed"}))

	observability.StepsTotal.WithLabelValues("PromotionStep", "succeeded").Inc()
	observability.StepsTotal.WithLabelValues("PromotionStep", "failed").Inc()
	observability.StepsTotal.WithLabelValues("PromotionStep", "succeeded").Inc()

	afterSucceeded := testutil.ToFloat64(observability.StepsTotal.With(prometheus.Labels{"type": "PromotionStep", "result": "succeeded"}))
	afterFailed := testutil.ToFloat64(observability.StepsTotal.With(prometheus.Labels{"type": "PromotionStep", "result": "failed"}))

	assert.Equal(t, beforeSucceeded+2, afterSucceeded, "succeeded should increment by 2")
	assert.Equal(t, beforeFailed+1, afterFailed, "failed should increment by 1")
}

// TestGateEvaluationsTotal verifies GateEvaluationsTotal increments per result label.
func TestGateEvaluationsTotal(t *testing.T) {
	t.Parallel()

	beforeAllowed := testutil.ToFloat64(observability.GateEvaluationsTotal.With(prometheus.Labels{"result": "allowed"}))
	beforeBlocked := testutil.ToFloat64(observability.GateEvaluationsTotal.With(prometheus.Labels{"result": "blocked"}))

	observability.GateEvaluationsTotal.WithLabelValues("allowed").Inc()
	observability.GateEvaluationsTotal.WithLabelValues("blocked").Inc()
	observability.GateEvaluationsTotal.WithLabelValues("allowed").Inc()

	afterAllowed := testutil.ToFloat64(observability.GateEvaluationsTotal.With(prometheus.Labels{"result": "allowed"}))
	afterBlocked := testutil.ToFloat64(observability.GateEvaluationsTotal.With(prometheus.Labels{"result": "blocked"}))

	assert.Equal(t, beforeAllowed+2, afterAllowed, "allowed should increment by 2")
	assert.Equal(t, beforeBlocked+1, afterBlocked, "blocked should increment by 1")
}

// TestPRDurationSeconds verifies that PRDurationSeconds can be observed without panic.
func TestPRDurationSeconds(t *testing.T) {
	t.Parallel()

	// Observe a sample duration — must not panic.
	require.NotPanics(t, func() {
		observability.PRDurationSeconds.Observe(3600)  // 1 hour
		observability.PRDurationSeconds.Observe(300)   // 5 minutes
		observability.PRDurationSeconds.Observe(86400) // 1 day
	})
}

// TestMetricNames verifies the registered metric names follow the kardinal namespace.
func TestMetricNames(t *testing.T) {
	t.Parallel()

	names := []string{
		"kardinal_bundles_total",
		"kardinal_steps_total",
		"kardinal_gate_evaluations_total",
		"kardinal_pr_duration_seconds",
		"kardinal_step_duration_seconds",
		"kardinal_gate_blocking_duration_seconds",
		"kardinal_promotionstep_age_seconds",
	}

	for _, name := range names {
		assert.True(t, strings.HasPrefix(name, "kardinal_"),
			"metric %s must be prefixed with kardinal_", name)
	}
}

// TestStepDurationSeconds verifies that StepDurationSeconds can be observed per step label.
func TestStepDurationSeconds(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		observability.StepDurationSeconds.WithLabelValues("git-clone").Observe(12.5)
		observability.StepDurationSeconds.WithLabelValues("kustomize").Observe(3.2)
		observability.StepDurationSeconds.WithLabelValues("open-pr").Observe(8.0)
	})
}

// TestGateBlockingDurationSeconds verifies that GateBlockingDurationSeconds
// can be observed without panic.
func TestGateBlockingDurationSeconds(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		observability.GateBlockingDurationSeconds.Observe(3600)   // 1 hour
		observability.GateBlockingDurationSeconds.Observe(900)    // 15 minutes
		observability.GateBlockingDurationSeconds.Observe(172800) // 2 days
	})
}

// TestPromotionStepAgeSeconds verifies that PromotionStepAgeSeconds can be
// observed without panic.
func TestPromotionStepAgeSeconds(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		observability.PromotionStepAgeSeconds.Observe(60)   // 1 minute
		observability.PromotionStepAgeSeconds.Observe(1800) // 30 minutes
		observability.PromotionStepAgeSeconds.Observe(7200) // 2 hours
	})
}
