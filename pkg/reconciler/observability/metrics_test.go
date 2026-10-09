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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

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

// TestMetricNames verifies that every kardinal collector is registered with
// the controller-runtime registry, so /metrics serves it, under a kardinal_
// name (C04-gates-38: this used to check a local list of strings). It also
// checks that the kardinal_bundles_total help lists the phases it is
// incremented with (C04-gates-33).
func TestMetricNames(t *testing.T) {
	t.Parallel()

	collectors := map[string]prometheus.Collector{
		"kardinal_bundles_total":                  observability.BundlesTotal,
		"kardinal_steps_total":                    observability.StepsTotal,
		"kardinal_gate_evaluations_total":         observability.GateEvaluationsTotal,
		"kardinal_pr_duration_seconds":            observability.PRDurationSeconds,
		"kardinal_step_duration_seconds":          observability.StepDurationSeconds,
		"kardinal_gate_blocking_duration_seconds": observability.GateBlockingDurationSeconds,
		"kardinal_promotionstep_age_seconds":      observability.PromotionStepAgeSeconds,
		"kardinal_auditevents_pruned_total":       observability.AuditEventsPrunedTotal,
	}
	for name, c := range collectors {
		descs := make(chan *prometheus.Desc, 1)
		c.Describe(descs)
		desc := (<-descs).String()
		assert.Contains(t, desc, `fqName: "`+name+`"`)

		var already prometheus.AlreadyRegisteredError
		assert.ErrorAs(t, ctrlmetrics.Registry.Register(c), &already, "%s must be registered", name)
		if name == "kardinal_bundles_total" {
			for _, phase := range []string{"Promoting", "Verified", "Failed", "Superseded"} {
				assert.Contains(t, desc, phase, "help text lists the phase label values")
			}
			assert.NotContains(t, desc, "terminal phase (")
		}
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
