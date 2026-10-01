// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// stepDurationSamples returns the sample count and sum of
// kardinal_step_duration_seconds for one step label.
func stepDurationSamples(t *testing.T, step string) (uint64, float64) {
	t.Helper()
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(observability.StepDurationSeconds))
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "step" && l.GetValue() == step {
					return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
				}
			}
		}
	}
	return 0, 0
}

// TestUpdateStepStatuses_DurationOfStepsRunInOneReconcile: steps that start
// and finish in the same reconcile (all but wait-for-merge) get the times the
// engine measured, a durationMs, and one kardinal_step_duration_seconds
// sample each. Before, both times were time.Now() of the status update, so
// durationMs stayed 0 and the histogram was never observed.
func TestUpdateStepStatuses_DurationOfStepsRunInOneReconcile(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	tests := []struct {
		name        string
		nextIdx     int
		failed      bool
		timings     map[int]steps.StepTiming
		wantState   []v1alpha1.StepExecutionState
		wantMs      []int64
		wantSamples []uint64
	}{
		{name: "every step completes in one reconcile", nextIdx: 3,
			timings: map[int]steps.StepTiming{
				0: {Started: at(0), Finished: at(1500)},
				1: {Started: at(1500), Finished: at(1700)},
				2: {Started: at(1700), Finished: at(4200)},
			},
			wantState: []v1alpha1.StepExecutionState{v1alpha1.StepExecutionCompleted,
				v1alpha1.StepExecutionCompleted, v1alpha1.StepExecutionCompleted},
			wantMs: []int64{1500, 200, 2500}, wantSamples: []uint64{1, 1, 1}},
		{name: "the last step waits for a merge", nextIdx: 2,
			timings: map[int]steps.StepTiming{
				0: {Started: at(0), Finished: at(800)},
				1: {Started: at(800), Finished: at(900)},
				2: {Started: at(900), Finished: at(1000)},
			},
			wantState: []v1alpha1.StepExecutionState{v1alpha1.StepExecutionCompleted,
				v1alpha1.StepExecutionCompleted, v1alpha1.StepExecutionInProgress},
			wantMs: []int64{800, 100, 0}, wantSamples: []uint64{1, 1, 0}},
		{name: "a step fails", nextIdx: 1, failed: true,
			timings: map[int]steps.StepTiming{
				0: {Started: at(0), Finished: at(300)},
				1: {Started: at(300), Finished: at(2300)},
			},
			wantState: []v1alpha1.StepExecutionState{v1alpha1.StepExecutionCompleted,
				v1alpha1.StepExecutionFailed, v1alpha1.StepExecutionPending},
			wantMs: []int64{300, 2000, 0}, wantSamples: []uint64{1, 1, 0}},
		{name: "a step that took no measurable time is still observed", nextIdx: 1,
			timings: map[int]steps.StepTiming{
				0: {Started: at(0), Finished: at(0)},
				1: {Started: at(0), Finished: at(5)},
			},
			wantState: []v1alpha1.StepExecutionState{v1alpha1.StepExecutionCompleted,
				v1alpha1.StepExecutionInProgress, v1alpha1.StepExecutionPending},
			wantMs: []int64{0, 0, 0}, wantSamples: []uint64{1, 0, 0}},
	}
	for n, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq := make([]string, 3)
			for i := range seq {
				seq[i] = fmt.Sprintf("test-duration-%d-%d", n, i)
			}
			ps := &v1alpha1.PromotionStep{Status: v1alpha1.PromotionStepStatus{Steps: initStepStatuses(seq)}}

			updateStepStatuses(ps, seq, tt.nextIdx, tt.failed, "boom", tt.timings).record()
			// A second update for the same result must not observe again.
			updateStepStatuses(ps, seq, tt.nextIdx, tt.failed, "boom", tt.timings).record()

			for i, s := range ps.Status.Steps {
				assert.Equal(t, tt.wantState[i], s.State, "step %d state", i)
				assert.Equal(t, tt.wantMs[i], s.DurationMs, "step %d durationMs", i)
				count, sum := stepDurationSamples(t, seq[i])
				assert.Equal(t, tt.wantSamples[i], count, "step %d samples", i)
				if tt.wantSamples[i] == 1 {
					assert.InDelta(t, float64(tt.wantMs[i])/1000, sum, 0.001, "step %d observed seconds", i)
					assert.Equal(t, tt.timings[i].Started, s.StartedAt.Time, "step %d startedAt", i)
					assert.Equal(t, tt.timings[i].Finished, s.CompletedAt.Time, "step %d completedAt", i)
				}
				if s.State == v1alpha1.StepExecutionInProgress {
					assert.Equal(t, tt.timings[i].Started, s.StartedAt.Time, "step %d startedAt", i)
				}
			}
		})
	}
}

// TestUpdateStepStatuses_StepAcrossReconciles: a step that started in an
// earlier reconcile (wait-for-merge) keeps that startedAt, so its duration
// covers the whole wait, and it is observed once when it completes.
func TestUpdateStepStatuses_StepAcrossReconciles(t *testing.T) {
	seq := []string{"test-across-0", "test-across-1"}
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	ps := &v1alpha1.PromotionStep{Status: v1alpha1.PromotionStepStatus{Steps: initStepStatuses(seq)}}

	// Reconcile 1: step 0 completes, step 1 starts and is pending.
	updateStepStatuses(ps, seq, 1, false, "", map[int]steps.StepTiming{
		0: {Started: t0, Finished: t0.Add(time.Second)},
		1: {Started: t0.Add(time.Second), Finished: t0.Add(2 * time.Second)},
	}).record()
	// Reconcile 2, a minute later: step 1 completes.
	updateStepStatuses(ps, seq, 2, false, "", map[int]steps.StepTiming{
		1: {Started: t0.Add(time.Minute), Finished: t0.Add(time.Minute + time.Second)},
	}).record()

	step := ps.Status.Steps[1]
	assert.Equal(t, v1alpha1.StepExecutionCompleted, step.State)
	assert.Equal(t, metav1.NewTime(t0.Add(time.Second)), *step.StartedAt, "startedAt of the first reconcile")
	assert.Equal(t, metav1.NewTime(t0.Add(time.Minute+time.Second)), *step.CompletedAt)
	assert.Equal(t, int64(60000), step.DurationMs)
	count, sum := stepDurationSamples(t, seq[1])
	assert.Equal(t, uint64(1), count)
	assert.InDelta(t, 60.0, sum, 0.001)
	count, _ = stepDurationSamples(t, seq[0])
	assert.Equal(t, uint64(1), count, "step 0 is observed once, in the reconcile it completed")
}

// TestStepDuration_StateMachineSteps: wait-for-merge and the health check are
// closed by the state machine, not the engine. Each is observed once with its
// real duration when it closes; the engine's placeholder health-check step is
// not observed; a step that never started is not observed.
func TestStepDuration_StateMachineSteps(t *testing.T) {
	seq := []string{"git-clone", "open-pr", "wait-for-merge", "health-check"}
	samples := func() map[string]uint64 {
		m := map[string]uint64{}
		for _, n := range seq {
			m[n], _ = stepDurationSamples(t, n)
		}
		return m
	}
	delta := func(before map[string]uint64) map[string]uint64 {
		m := map[string]uint64{}
		for n, c := range samples() {
			if c != before[n] {
				m[n] = c - before[n]
			}
		}
		return m
	}
	now := time.Now()
	at := now.Add

	t.Run("pr-review: merge, health check, Verified", func(t *testing.T) {
		before := samples()
		ps := &v1alpha1.PromotionStep{Status: v1alpha1.PromotionStepStatus{Steps: initStepStatuses(seq)}}
		updateStepStatuses(ps, seq, 2, false, "", map[int]steps.StepTiming{
			0: {Started: at(-10 * time.Minute), Finished: at(-10*time.Minute + time.Second)},
			1: {Started: at(-10*time.Minute + time.Second), Finished: at(-10*time.Minute + 2*time.Second)},
			2: {Started: at(-10*time.Minute + 2*time.Second), Finished: at(-10*time.Minute + 2*time.Second)},
		}).record()
		assert.Equal(t, map[string]uint64{"git-clone": 1, "open-pr": 1}, delta(before))

		closeStepStatuses(ps, StateHealthChecking).record()
		wfm := ps.Status.Steps[2]
		assert.Equal(t, v1alpha1.StepExecutionCompleted, wfm.State)
		assert.InDelta(t, (10*time.Minute - 2*time.Second).Milliseconds(), wfm.DurationMs, 1000, "wait-for-merge lasts until the merge")
		assert.Equal(t, map[string]uint64{"git-clone": 1, "open-pr": 1, "wait-for-merge": 1}, delta(before))

		hc := &ps.Status.Steps[3]
		require.Equal(t, v1alpha1.StepExecutionInProgress, hc.State)
		started := metav1.NewTime(at(-3 * time.Minute)) // the health check began 3m ago
		hc.StartedAt = &started
		closeStepStatuses(ps, StateVerified).record()
		assert.Equal(t, v1alpha1.StepExecutionCompleted, hc.State)
		assert.InDelta(t, (3 * time.Minute).Milliseconds(), hc.DurationMs, 1000)
		assert.Equal(t, map[string]uint64{"git-clone": 1, "open-pr": 1, "wait-for-merge": 1, "health-check": 1}, delta(before))

		closeStepStatuses(ps, StateVerified).record()
		assert.Len(t, delta(before), 4, "closing again observes nothing")
	})

	t.Run("auto: the placeholder health-check is not observed", func(t *testing.T) {
		auto := []string{"git-clone", "health-check"}
		before := samples()
		ps := &v1alpha1.PromotionStep{Status: v1alpha1.PromotionStepStatus{Steps: initStepStatuses(auto)}}
		updateStepStatuses(ps, auto, 2, false, "", map[int]steps.StepTiming{
			0: {Started: at(-2 * time.Second), Finished: at(-time.Second)},
			1: {Started: at(-time.Second), Finished: at(-time.Second)},
		}).record()
		assert.Equal(t, v1alpha1.StepExecutionCompleted, ps.Status.Steps[1].State)
		assert.Equal(t, map[string]uint64{"git-clone": 1}, delta(before))

		closeStepStatuses(ps, StateHealthChecking).record()
		closeStepStatuses(ps, StateFailed).record()
		assert.Equal(t, v1alpha1.StepExecutionFailed, ps.Status.Steps[1].State)
		assert.Equal(t, map[string]uint64{"git-clone": 1, "health-check": 1}, delta(before), "the failed health check is observed")
	})

	t.Run("a step that never started is not observed", func(t *testing.T) {
		before := samples()
		ps := &v1alpha1.PromotionStep{Status: v1alpha1.PromotionStepStatus{Steps: initStepStatuses(seq)}}
		closeStepStatuses(ps, StateFailed).record()
		assert.Equal(t, v1alpha1.StepExecutionFailed, ps.Status.Steps[0].State)
		assert.Empty(t, delta(before))
	})
}
