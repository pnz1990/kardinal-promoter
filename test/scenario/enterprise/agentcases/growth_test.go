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
// the square of the index's entries) fails, also under overcommit.
func TestGrowthShape(t *testing.T) {
	// The enterprise index's sizes in bytes: empty, half, full.
	const e, h, f = 665, 6938, 13023
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
	for _, tc := range []struct {
		name        string
		rounds      []growthRound
		superlinear bool
	}{
		{"linear", linear, false},
		{"linear, an empty-index round stalled", stalledEmpty, false},
		{"linear, 3x CPU overcommit", overcommitted, false},
		{"linear, 3x CPU overcommit, the other one", ms([3]float64{1.68, 14.94, 17.26}, [3]float64{1.93, 11.17, 24.28},
			[3]float64{2.07, 11.77, 32.51}, [3]float64{2.16, 15.72, 31.33}, [3]float64{1.75, 11.57, 23.09}), false},
		{"linear, 3x CPU overcommit, a half-index round stalled", stalledHalf, false},
		{"quadratic, about 80% of the growth", ms([3]float64{1.57, 8.73, 23.97}, [3]float64{1.56, 8.77, 23.97},
			[3]float64{1.56, 9.61, 27.33}, [3]float64{2.58, 11.23, 25.57}, [3]float64{1.57, 10.53, 25.89}), true},
		{"quadratic, about all of the growth", ms([3]float64{2.37, 17.85, 60.76}, [3]float64{1.62, 17.25, 57.63},
			[3]float64{1.58, 17.18, 55.46}, [3]float64{1.97, 17.06, 61.34}, [3]float64{1.55, 18.3, 56.55}), true},
		{"quadratic, about 80% of the growth, 3x CPU overcommit", ms([3]float64{2.44, 35.85, 78.28},
			[3]float64{2.08, 24.93, 83.73}, [3]float64{2.57, 15.69, 40.01}, [3]float64{1.57, 19.0, 27.51},
			[3]float64{1.67, 10.61, 36.67}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ratio, bound, ratios := growthShape(tc.rounds, e, h, f)
			assert.InDelta(t, 1.58, bound, 0.005, "half the growth quadratic")
			assert.Len(t, ratios, len(tc.rounds))
			if tc.superlinear {
				assert.GreaterOrEqual(t, ratio, bound, "round ratios %.2f", ratios)
			} else {
				assert.Less(t, ratio, bound, "round ratios %.2f", ratios)
			}
		})
	}
}
