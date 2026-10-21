// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package soak checks invariants while a long live test runs (the
// enterprise-day soak, #2403): every Interval, each check looks at what
// changed since the pass before. The first violation ends the soak at once
// (it cancels the soak's context, so the load and the episodes stop) and
// dumps the estate, so an hour's run never ends in an unexplained failure.
package soak

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Violation is an invariant that did not hold: which, when, and what broke
// it (Err names the object).
type Violation struct {
	Invariant string
	At        time.Time
	Err       error
}

func (v Violation) Error() string {
	return fmt.Sprintf("invariant %s violated at %s: %v", v.Invariant, v.At.UTC().Format(time.RFC3339), v.Err)
}

// check is one invariant: run reports what breaks it among what changed
// since since, or nil.
type check struct {
	name string
	run  func(ctx context.Context, since time.Time) error
}

// Checker runs its checks every Interval from Start, and once more over the
// whole soak at Final. The first violation cancels the soak's context and
// calls Dump once, with a context of its own (the soak's is done by then).
type Checker struct {
	// Interval is the time between the starts of two passes.
	Interval time.Duration
	// Dump records the estate when the first violation is found: the
	// relevant objects, the controller logs, a metrics snapshot.
	Dump func(ctx context.Context, v Violation)
	// DumpTimeout bounds Dump (default 5 minutes).
	DumpTimeout time.Duration

	checks []check
	now    func() time.Time

	mu     sync.Mutex
	first  *Violation
	passes int
	start  time.Time
	cancel context.CancelFunc
	done   chan struct{}
}

// Add registers an invariant. run must look only at what changed since
// since (the start of the pass before, or of the soak), and return the first
// thing it finds that breaks the invariant.
func (c *Checker) Add(name string, run func(ctx context.Context, since time.Time) error) {
	c.checks = append(c.checks, check{name: name, run: run})
}

func (c *Checker) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Start begins the passes and returns the soak's context: done at the first
// violation, or when parent is.
func (c *Checker) Start(parent context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	c.start, c.cancel, c.done = c.clock(), cancel, make(chan struct{})
	c.mu.Unlock()
	go func() {
		defer close(c.done)
		since := c.start
		t := time.NewTicker(c.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			at := c.clock()
			if c.pass(ctx, since) {
				return
			}
			since = at
		}
	}()
	return ctx
}

// pass runs every check once; true when one was violated (the soak is then
// over).
func (c *Checker) pass(ctx context.Context, since time.Time) bool {
	defer func() {
		c.mu.Lock()
		c.passes++
		c.mu.Unlock()
	}()
	for _, ch := range c.checks {
		err := ch.run(ctx, since)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return true // the soak ended meanwhile: not a violation
		}
		c.violated(Violation{Invariant: ch.name, At: c.clock(), Err: err})
		return true
	}
	return false
}

// violated records the first violation, ends the soak and dumps the estate.
func (c *Checker) violated(v Violation) {
	c.mu.Lock()
	if c.first != nil {
		c.mu.Unlock()
		return
	}
	c.first = &v
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c.Dump != nil {
		d := c.DumpTimeout
		if d == 0 {
			d = 5 * time.Minute
		}
		ctx, done := context.WithTimeout(context.Background(), d)
		defer done()
		c.Dump(ctx, v)
	}
}

// Final stops the passes and, unless one already found a violation, runs
// every check once more over the whole soak. It returns the first
// violation, or nil.
func (c *Checker) Final(ctx context.Context) *Violation {
	c.mu.Lock()
	cancel, done, start := c.cancel, c.done, c.start
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	if v := c.Violation(); v != nil {
		return v
	}
	c.pass(ctx, start)
	return c.Violation()
}

// Violation is the first violation found, or nil.
func (c *Checker) Violation() *Violation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.first
}

// Passes is how many passes have run.
func (c *Checker) Passes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.passes
}
