// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestEngine_Timings: ExecuteFrom records when each step it ran started and
// returned, in order, including a pending step, and a new call starts over.
func TestEngine_Timings(t *testing.T) {
	a := &countingStep{name: "test-timings-a", statuses: []steps.StepStatus{steps.StepSuccess}}
	b := &countingStep{name: "test-timings-b", statuses: []steps.StepStatus{steps.StepSuccess}}
	c := &countingStep{name: "test-timings-c", statuses: []steps.StepStatus{steps.StepPending}}
	for _, s := range []*countingStep{a, b, c} {
		steps.Register(s)
	}
	eng := steps.NewEngine([]string{a.name, b.name, c.name})

	next, result, err := eng.ExecuteFrom(context.Background(), &steps.StepState{}, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, next)
	assert.Equal(t, steps.StepPending, result.Status)
	timings := eng.Timings()
	require.Len(t, timings, 3)
	for i := 0; i < 3; i++ {
		assert.False(t, timings[i].Started.IsZero(), "step %d started", i)
		assert.False(t, timings[i].Finished.Before(timings[i].Started), "step %d finished after it started", i)
		if i > 0 {
			assert.False(t, timings[i].Started.Before(timings[i-1].Finished), "step %d started after step %d", i, i-1)
		}
	}

	// The next reconcile resumes at the pending step: only it is timed.
	_, _, err = eng.ExecuteFrom(context.Background(), &steps.StepState{}, 2)
	require.NoError(t, err)
	assert.Len(t, eng.Timings(), 1)
	assert.Contains(t, eng.Timings(), 2)
}

// TestEngine_TimingsUseNowFn: the timings come from the engine's clock, so a
// test, or the reconciler, sets them through NowFn instead of the wall clock.
func TestEngine_TimingsUseNowFn(t *testing.T) {
	a := &countingStep{name: "test-timings-clock-a", statuses: []steps.StepStatus{steps.StepSuccess}}
	b := &countingStep{name: "test-timings-clock-b", statuses: []steps.StepStatus{steps.StepPending}}
	steps.Register(a)
	steps.Register(b)
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	tick := 0
	eng := steps.NewEngine([]string{a.name, b.name})
	eng.NowFn = func() time.Time {
		tick++
		return t0.Add(time.Duration(tick) * time.Second)
	}

	_, _, err := eng.ExecuteFrom(context.Background(), &steps.StepState{}, 0)
	require.NoError(t, err)
	assert.Equal(t, map[int]steps.StepTiming{
		0: {Started: t0.Add(1 * time.Second), Finished: t0.Add(2 * time.Second)},
		1: {Started: t0.Add(3 * time.Second), Finished: t0.Add(4 * time.Second)},
	}, eng.Timings())
}
