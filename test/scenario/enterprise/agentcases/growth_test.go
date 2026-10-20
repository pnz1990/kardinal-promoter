// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentcases

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestGrowthShape: assertLinearAdmission's verdict on rounds recorded from
// TestEnterprise_AgentDifferential (#2270): a linear cost passes, at normal
// load, under 3x CPU overcommit (six race binaries on two CPUs, where the
// tenth-index check this replaced failed 5 runs of 6), and with a round
// stalled on the empty or the half index (CI's #2284 had an empty-index
// round of 12ms); a superlinear one (the policy's cost plus CPU work in
// the square of the index's entries) fails, also under overcommit and with
// a majority of its rounds stalled timing the empty index. With uneven
// halves (a "half" of 29% of the full index's bytes), costs
// computed from a linear and a quadratic model: the ratio is per byte.
func TestGrowthShape(t *testing.T) {
	// The enterprise index's sizes in bytes: empty, half, full.
	enterprise := [3]int{665, 6938, 13023}
	uneven := [3]int{665, 3754, 13023}
	ms := func(rs ...[3]float64) []growthRound {
		out := make([]growthRound, 0, len(rs))
		for _, r := range rs {
			d := func(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }
			out = append(out, growthRound{empty: d(r[0]), half: d(r[1]), full: d(r[2])})
		}
		return out
	}
	linear := ms([3]float64{1.63, 4.43, 7.03}, [3]float64{1.62, 4.52, 7.09}, [3]float64{1.65, 4.59, 7.18},
		[3]float64{1.59, 4.57, 7.09}, [3]float64{1.56, 4.36, 6.99})
	stalledEmpty := append([]growthRound(nil), linear...)
	stalledEmpty[1].empty = 12 * time.Millisecond
	overcommitted := ms([3]float64{2.19, 14.63, 23.37}, [3]float64{1.85, 15.17, 22.54}, [3]float64{1.91, 14.12, 21.46},
		[3]float64{1.95, 12.94, 21.67}, [3]float64{1.86, 15.05, 23.74})
	stalledHalf := append([]growthRound(nil), overcommitted...)
	stalledHalf[2].half = 30 * time.Millisecond
	quadratic := ms([3]float64{1.57, 8.73, 23.97}, [3]float64{1.56, 8.77, 23.97},
		[3]float64{1.56, 9.61, 27.33}, [3]float64{2.58, 11.23, 25.57}, [3]float64{1.57, 10.53, 25.89})
	quadraticStalledEmpty := append([]growthRound(nil), quadratic...)
	for _, r := range []int{0, 2, 4} {
		quadraticStalledEmpty[r].empty = 12 * time.Millisecond
	}
	// Costs of a model over the uneven sizes, 1.6ms at the empty index and
	// 7.1ms at the full one, in five identical rounds.
	model := func(cost func(bytes float64) float64) []growthRound {
		e, h, f := float64(uneven[0]), float64(uneven[1]), float64(uneven[2])
		at := func(b float64) float64 { return 1.6 + 5.5*(cost(b)-cost(e))/(cost(f)-cost(e)) }
		r := [3]float64{at(e), at(h), at(f)}
		return ms(r, r, r, r, r)
	}
	type growthCase struct {
		name        string
		rounds      []growthRound
		superlinear bool
	}
	check := func(t *testing.T, tc growthCase, sizes [3]int, wantBound float64) {
		t.Helper()
		ratio, bound, ratios := growthShape(tc.rounds, sizes[0], sizes[1], sizes[2])
		assert.InDelta(t, wantBound, bound, 0.005, "half the growth quadratic")
		assert.Len(t, ratios, len(tc.rounds))
		if tc.superlinear {
			assert.GreaterOrEqual(t, ratio, bound, "round ratios %.2f", ratios)
		} else {
			assert.Less(t, ratio, bound, "round ratios %.2f", ratios)
		}
	}
	for _, tc := range []growthCase{
		{"linear", linear, false},
		{"linear, an empty-index round stalled", stalledEmpty, false},
		{"linear, 3x CPU overcommit", overcommitted, false},
		{"linear, 3x CPU overcommit, the other one", ms([3]float64{1.68, 14.94, 17.26}, [3]float64{1.93, 11.17, 24.28},
			[3]float64{2.07, 11.77, 32.51}, [3]float64{2.16, 15.72, 31.33}, [3]float64{1.75, 11.57, 23.09}), false},
		{"linear, 3x CPU overcommit, a half-index round stalled", stalledHalf, false},
		{"quadratic, about 80% of the growth", quadratic, true},
		{"quadratic, about all of the growth", ms([3]float64{2.37, 17.85, 60.76}, [3]float64{1.62, 17.25, 57.63},
			[3]float64{1.58, 17.18, 55.46}, [3]float64{1.97, 17.06, 61.34}, [3]float64{1.55, 18.3, 56.55}), true},
		{"quadratic, about 80% of the growth, 3x CPU overcommit", ms([3]float64{2.44, 35.85, 78.28},
			[3]float64{2.08, 24.93, 83.73}, [3]float64{2.57, 15.69, 40.01}, [3]float64{1.57, 19.0, 27.51},
			[3]float64{1.67, 10.61, 36.67}), true},
		{"quadratic, about 80% of the growth, 3 rounds of 5 stalled timing the empty index", quadraticStalledEmpty, true},
	} {
		t.Run(tc.name, func(t *testing.T) { check(t, tc, enterprise, 1.58) })
	}
	for _, tc := range []growthCase{
		{"linear, uneven halves", model(func(b float64) float64 { return b }), false},
		{"quadratic, uneven halves", model(func(b float64) float64 { return b * b }), true},
	} {
		t.Run(tc.name, func(t *testing.T) { check(t, tc, uneven, 1.68) })
	}
}

// TestShapeBound (#2270): the quick size holds the median to growthShape's
// bound; the full size, until #2071, to three quarters of the growth
// quadratic (about 2.2 from its sizes), which CI's healthy full-size runs
// pass and a quadratic cost, with a runner's noise, fails.
func TestShapeBound(t *testing.T) {
	// The quick size is not the full size, so its bound cannot become the
	// full size's; the full size is.
	assert.False(t, isFullSize(quickAgents))
	assert.True(t, isFullSize(fullAgents))
	bound, meaning := shapeBound(1.58, false, 665, 6938, 13023)
	assert.InDelta(t, 1.58, bound, 1e-9)
	assert.Equal(t, "half the growth quadratic", meaning)

	// The full size's index: empty, half and full, in bytes.
	const e, h, f = 360, 208_900, 417_000
	assert.InDelta(t, 1.66, quadraticShareRatio(0.5, e, h, f), 0.01, "half")
	assert.InDelta(t, 2.33, quadraticShareRatio(0.8, e, h, f), 0.01, "80%")
	assert.InDelta(t, 3.0, quadraticShareRatio(1, e, h, f), 0.01, "wholly quadratic")
	bound, meaning = shapeBound(quadraticShareRatio(0.5, e, h, f), true, e, h, f)
	assert.InDelta(t, 2.2, bound, 0.01)
	assert.Contains(t, meaning, "#2071")
	ms := func(rs ...[3]float64) []growthRound {
		out := make([]growthRound, 0, len(rs))
		for _, r := range rs {
			d := func(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }
			out = append(out, growthRound{empty: d(r[0]), half: d(r[1]), full: d(r[2])})
		}
		return out
	}
	for _, rounds := range [][]growthRound{
		// CI, block timing: median 1.67, over half the growth quadratic.
		ms([3]float64{0.359, 26.02, 52.74}, [3]float64{0.360, 23.58, 62.34}, [3]float64{0.339, 21.52, 60.70},
			[3]float64{0.347, 32.15, 44.00}, [3]float64{0.313, 26.69, 71.08}),
		// CI, interleaved chunks: the highest healthy median, 1.54.
		ms([3]float64{0.164, 10.94, 30.53}, [3]float64{0.155, 16.65, 30.64}, [3]float64{0.163, 15.93, 30.35},
			[3]float64{0.164, 13.48, 34.20}, [3]float64{0.175, 10.89, 27.35}),
	} {
		ratio, _, ratios := growthShape(rounds, e, h, f)
		assert.Less(t, ratio, bound, "round ratios %.2f", ratios)
	}
	// A wholly quadratic cost, 0.35ms empty and 60ms full, with a runner's
	// noise on each round (the half and full p50s off by up to 15%).
	q := func(b float64) float64 { return 0.35 + 59.65*(b*b-e*e)/(f*f-e*e) }
	var noisy []growthRound
	for _, n := range [][2]float64{{1.05, 0.92}, {0.9, 1.1}, {1.12, 1.0}, {0.95, 0.88}, {1.0, 1.15}} {
		noisy = append(noisy, ms([3]float64{q(e), q(h) * n[0], q(f) * n[1]})...)
	}
	ratio, _, ratios := growthShape(noisy, e, h, f)
	assert.GreaterOrEqual(t, ratio, bound, "round ratios %.2f", ratios)
}
