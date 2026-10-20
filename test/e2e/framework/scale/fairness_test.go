// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// t0 is A's load start in the timelines below; times are seconds after it.
var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func at(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

// never is a first or last environment that was never Verified.
const never = -1e6

// tb is a B Bundle created at c, its first environment Verified at first and
// its last at last (seconds after t0, or never).
func tb(name string, c, first, last float64) TenantBundle {
	b := TenantBundle{Name: name, Created: at(c)}
	if first != never {
		b.First = at(first)
	}
	if last != never {
		b.Last = at(last)
	}
	return b
}

// alone is B alone, before A's load: its slowest Bundle 4 s, as a 32-core
// host and a 4-CPU kind node both measure (#2252). So the bound is 62 s and
// the starvation window 6 s.
var alone = []TenantBundle{tb("b1-1", -30, -29, -27), tb("b2-1", -28, -27, -24), tb("b3-1", -26, -25.5, -23)}

const (
	starved     = "waited for all of tenant A's load"
	slow        = "under tenant A's load, over"
	notMeasured = "contention not measured"
	late        = "not measured under contention"
)

// The baseline is the slowest Bundle alone, the bound FairnessFactor times
// it plus the slack.
func TestJudgeFairness_Baseline(t *testing.T) {
	f := JudgeFairness(alone, nil, at(15))
	assert.Equal(t, 4*time.Second, f.Alone)
	assert.Equal(t, 62*time.Second, f.Bound)
}

// TestJudgeFairness judges timelines of the hosts #2252 measured, fair and
// with B held until A drains.
func TestJudgeFairness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		loaded []TenantBundle
		aEnd   float64
		judged int
		want   []string // the kinds of violation, in order
	}{
		{
			// #2252's host: A's 30 repositories are done in 15 s, B's
			// Bundles take 5-7 s each and end after A. That is fair.
			name:   "fast host, fair, B ends after A",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 7), tb("b2-2", 4, 6, 10), tb("b3-2", 6, 8, 13)},
			aEnd:   15,
			judged: 3,
		},
		{
			// B is processed only after A drains. On a fast host A's load
			// is too short for B's latency to show it. Only the starvation
			// check sees it.
			name:   "fast host, B held until A drains",
			loaded: []TenantBundle{tb("b1-2", 2, 16, 19), tb("b2-2", 4, 17, 20), tb("b3-2", 6, 17, 21)},
			aEnd:   15,
			judged: 3,
			want:   []string{starved, starved, starved},
		},
		{
			// A GitHub runner under ManyRepos' load: the fair queue, and
			// B 13 times slower than alone, from CPU contention (the
			// slowest nightly run, 39 s, against a 3 s baseline).
			name:   "slow host, fair",
			loaded: []TenantBundle{tb("b1-2", 12, 22, 42), tb("b2-2", 14, 24, 43), tb("b3-2", 16, 27, 68)},
			aEnd:   75,
			judged: 3,
		},
		{
			name:   "slow host, B held until A drains",
			loaded: []TenantBundle{tb("b1-2", 12, 76, 78), tb("b2-2", 14, 77, 79), tb("b3-2", 16, 77, 80)},
			aEnd:   75,
			judged: 3,
			want:   []string{slow, starved, slow, starved, slow, starved},
		},
		{
			// B's steps get through, but B waits most of A's load between
			// them: slow, not starved.
			name:   "slow host, B held part of the time",
			loaded: []TenantBundle{tb("b1-2", 12, 20, 80), tb("b2-2", 14, 22, 30), tb("b3-2", 16, 24, 34)},
			aEnd:   100,
			judged: 3,
			want:   []string{slow},
		},
		{
			// A ends within the window of every B Bundle: nothing tells
			// a starved B from a fair one.
			name:   "A's load too short",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 7), tb("b2-2", 4, 6, 10)},
			aEnd:   8,
			judged: 0,
			want:   []string{notMeasured},
		},
		{
			name:   "a B Bundle created after A's load",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 7), tb("b2-2", 16, 17, 20)},
			aEnd:   15,
			judged: 1,
			want:   []string{late},
		},
		{
			// Already reported as not measured: no second violation.
			name:   "every B Bundle created after A's load",
			loaded: []TenantBundle{tb("b1-2", 16, 17, 20), tb("b2-2", 18, 19, 22)},
			aEnd:   15,
			judged: 0,
			want:   []string{late, late},
		},
		{
			name:   "a B Bundle that did not end Verified",
			loaded: []TenantBundle{tb("b1-2", 2, never, never), tb("b2-2", 4, 6, 10)},
			aEnd:   15,
			judged: 2,
			want:   []string{"did not end Verified", starved},
		},
		{
			// The timestamps are whole seconds: B's first environment in
			// the second A ended is not before it.
			name:   "first environment in A's last second",
			loaded: []TenantBundle{tb("b1-2", 2, 15, 17)},
			aEnd:   15,
			judged: 1,
			want:   []string{starved},
		},
		{
			name:   "exactly at the bound",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 2+62)},
			aEnd:   70,
			judged: 1,
		},
		{
			name:   "a second over the bound",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 3+62)},
			aEnd:   70,
			judged: 1,
			want:   []string{slow},
		},
		{
			// A's load ends exactly the window (6 s) after B's creation:
			// not judged.
			name:   "exactly the window after creation",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 7)},
			aEnd:   8,
			judged: 0,
			want:   []string{notMeasured},
		},
		{
			name:   "a second past the window, no environment before A's end",
			loaded: []TenantBundle{tb("b1-2", 2, 9, 12)},
			aEnd:   9,
			judged: 1,
			want:   []string{starved},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := JudgeFairness(alone, tc.loaded, at(tc.aEnd))
			assert.Equal(t, tc.judged, f.Judged, "judged")
			require.Len(t, f.Violations, len(tc.want), "%q", f.Violations)
			for i, kind := range tc.want {
				assert.Contains(t, f.Violations[i], kind)
			}
		})
	}
}

func TestJudgeFairness_NoBaseline(t *testing.T) {
	loaded := []TenantBundle{tb("b1-2", 2, 3, 7)}
	f := JudgeFairness(nil, loaded, at(15))
	require.Len(t, f.Violations, 1)
	assert.Contains(t, f.Violations[0], "no Bundle of tenant B ran alone")

	f = JudgeFairness([]TenantBundle{alone[0], tb("b2-1", -28, -27, never)}, loaded, at(15))
	require.Len(t, f.Violations, 1)
	assert.Contains(t, f.Violations[0], "b2-1, alone, did not end Verified")
}
