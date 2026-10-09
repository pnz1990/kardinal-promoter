// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"hash/fnv"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"
)

// EnvSeed replays a run's random choices (chaos intervals, which Pipeline
// gets the next Bundle, storm pauses): every scale test logs the seed it ran
// with, and KARDINAL_E2E_SCALE_SEED=<seed> runs it with that seed again.
// Each test derives its own stream from the seed and its name, so a test
// replays the same way alone or with the others.
const EnvSeed = "KARDINAL_E2E_SCALE_SEED"

// RNG is a seeded random source safe for concurrent use.
type RNG struct {
	Seed int64

	mu sync.Mutex
	r  *rand.Rand
}

var (
	seedOnce sync.Once
	seed     int64
	seedErr  error
)

// Seed returns KARDINAL_E2E_SCALE_SEED, or else one seed from the clock for
// the whole test binary.
func Seed() (int64, error) {
	seedOnce.Do(func() {
		if v := os.Getenv(EnvSeed); v != "" {
			seed, seedErr = strconv.ParseInt(v, 10, 64)
			return
		}
		seed = time.Now().UnixNano()
	})
	return seed, seedErr
}

// NewRNG is the stream of test name under seed.
func NewRNG(seed int64, name string) *RNG {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return &RNG{Seed: seed, r: rand.New(rand.NewSource(seed ^ int64(h.Sum64())))} //nolint:gosec // test randomness, replayable on purpose
}

// Intn is rand.Intn.
func (g *RNG) Intn(n int) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.r.Intn(n)
}

// Duration is a random duration in [lo, hi).
func (g *RNG) Duration(lo, hi time.Duration) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return lo + time.Duration(g.r.Int63n(int64(hi-lo)))
}

// Between returns a func that draws Duration(lo, hi), for Start.
func (g *RNG) Between(lo, hi time.Duration) func() time.Duration {
	return func() time.Duration { return g.Duration(lo, hi) }
}
