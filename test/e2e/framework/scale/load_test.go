// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestWarmTracker: the warm baseline is when the last of the Pipelines got
// its (HistoryLimit+1)-th Bundle, and never while one has not.
//
// Covers SCALE-INV-LEAK-01.
func TestWarmTracker(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	w := warmTracker{pipelines: 2}
	for i := 1; i <= 3*HistoryLimit; i++ {
		w.created(i, t0.Add(time.Duration(i)*time.Second)) // Pipeline a
	}
	assert.True(t, w.at.IsZero(), "Pipeline b has no Bundle yet")
	for i := 1; i <= HistoryLimit; i++ {
		w.created(i, t0.Add(time.Hour)) // Pipeline b, up to the limit
	}
	assert.True(t, w.at.IsZero(), "Pipeline b is at historyLimit, not past it")
	at := t0.Add(2 * time.Hour)
	w.created(HistoryLimit+1, at)
	w.created(HistoryLimit+2, at.Add(time.Minute))
	assert.Equal(t, at, w.at)
}

// TestRaceMismatch: EnvRace and the controller's build must agree both ways.
//
// Covers SCALE-INV-LEAK-01.
func TestRaceMismatch(t *testing.T) {
	assert.Empty(t, raceMismatch(true, raceVersion))
	assert.Empty(t, raceMismatch(false, "e2e"))
	assert.Contains(t, raceMismatch(true, "e2e"), "not the -race build")
	assert.Contains(t, raceMismatch(false, raceVersion), "is not 1")
}
