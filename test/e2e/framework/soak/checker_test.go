// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package soak

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChecker_Passes (#2403): the checks run every Interval, each over what
// changed since the start of the pass before (the first since the soak's
// start), and Final runs them once more over the whole soak.
func TestChecker_Passes(t *testing.T) {
	var mu sync.Mutex
	var sinces []time.Time
	c := &Checker{Interval: 10 * time.Millisecond}
	c.Add("records", func(_ context.Context, since time.Time) error {
		mu.Lock()
		defer mu.Unlock()
		sinces = append(sinces, since)
		return nil
	})
	ctx := c.Start(context.Background())
	require.Eventually(t, func() bool { return c.Passes() >= 3 }, 5*time.Second, time.Millisecond)
	assert.NoError(t, ctx.Err(), "no violation: the soak goes on")
	assert.Nil(t, c.Final(context.Background()))
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(sinces), 4)
	assert.Equal(t, c.start, sinces[0], "the first pass looks since the soak's start")
	for i := 1; i < len(sinces)-1; i++ {
		assert.True(t, sinces[i].After(sinces[i-1]), "each pass looks since the one before")
	}
	assert.Equal(t, c.start, sinces[len(sinces)-1], "Final looks over the whole soak")
}

// TestChecker_FailFast (#2403): the first violation cancels the soak's
// context at once, dumps the estate once with that violation, stops the
// passes, and is what Final returns, naming the invariant.
func TestChecker_FailFast(t *testing.T) {
	var dumps atomic.Int64
	var dumped Violation
	c := &Checker{Interval: 5 * time.Millisecond, Dump: func(ctx context.Context, v Violation) {
		require.NoError(t, ctx.Err(), "Dump has a live context of its own")
		dumps.Add(1)
		dumped = v
	}}
	var calls atomic.Int64
	c.Add("holds", func(context.Context, time.Time) error { return nil })
	c.Add("receipt-signed", func(context.Context, time.Time) error {
		if calls.Add(1) == 3 {
			return errors.New("PromotionStep team-a/web-prod has no receipt")
		}
		return nil
	})
	ctx := c.Start(context.Background())
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the soak's context was not cancelled by the violation")
	}
	v := c.Final(context.Background())
	require.NotNil(t, v)
	assert.Equal(t, "receipt-signed", v.Invariant)
	assert.Contains(t, v.Error(), "invariant receipt-signed violated at ")
	assert.Contains(t, v.Error(), "team-a/web-prod has no receipt")
	assert.EqualValues(t, 1, dumps.Load(), "dumped once")
	assert.Equal(t, *v, dumped)
	assert.EqualValues(t, 3, calls.Load(), "no pass after the violation, and Final runs none")
}

// TestChecker_FinalFinds (#2403): a violation only the whole soak shows
// (memory over the run) is found by Final, dumped and returned; a soak whose
// parent context ends is not a violation.
func TestChecker_FinalFinds(t *testing.T) {
	var dumps atomic.Int64
	c := &Checker{Interval: time.Hour, Dump: func(context.Context, Violation) { dumps.Add(1) }}
	c.Add("memory-flat", func(_ context.Context, since time.Time) error {
		if since.Equal(c.start) {
			return errors.New("heap grew 40% over the soak")
		}
		return nil
	})
	parent, cancel := context.WithCancel(context.Background())
	ctx := c.Start(parent)
	cancel()
	<-ctx.Done()
	assert.Nil(t, c.Violation(), "the parent's end is not a violation")
	v := c.Final(context.Background())
	require.NotNil(t, v)
	assert.Equal(t, "memory-flat", v.Invariant)
	assert.EqualValues(t, 1, dumps.Load())
}

// TestChecker_Fail (#2403): a violation an episode finds itself (Fail) ends
// the soak and dumps the estate as a pass's does; only the first counts,
// and Final returns it without another pass.
func TestChecker_Fail(t *testing.T) {
	var dumps atomic.Int64
	c := &Checker{Interval: time.Hour, Dump: func(context.Context, Violation) { dumps.Add(1) }}
	var passes atomic.Int64
	c.Add("holds", func(context.Context, time.Time) error {
		passes.Add(1)
		return nil
	})
	ctx := c.Start(context.Background())
	c.Fail("episode-ha", errors.New("no new leader within 2m0s"))
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Fail did not cancel the soak's context")
	}
	c.Fail("episode-consent", errors.New("a later violation"))
	v := c.Final(context.Background())
	require.NotNil(t, v)
	assert.Equal(t, "episode-ha", v.Invariant)
	assert.Contains(t, v.Error(), "no new leader within 2m0s")
	assert.EqualValues(t, 1, dumps.Load(), "dumped once")
	assert.Zero(t, passes.Load(), "Final runs no pass after a violation")
}
