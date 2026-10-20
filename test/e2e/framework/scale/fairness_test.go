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
// its last at last (seconds after t0, or never), its slowest step slowest
// seconds.
func tb(name string, c, first, last, slowest float64) TenantBundle {
	b := TenantBundle{Name: name, Created: at(c), SlowestStep: time.Duration(slowest * float64(time.Second))}
	if first != never {
		b.First = at(first)
	}
	if last != never {
		b.Last = at(last)
	}
	return b
}

// load is tenant A's load, ended at end with a median step of median
// seconds.
func load(end, median float64) TenantLoad {
	return TenantLoad{End: at(end), StepMedian: time.Duration(median * float64(time.Second))}
}

// alone is B alone, before A's load, as a GitHub runner measured it (e2e-live
// run 39377163730): its slowest Bundle 3 s, its slowest step 1 s. So the
// starvation window is 5 s, and A's load is a backlog when A's median step
// takes more than 3 s.
var alone = []TenantBundle{tb("b1-1", -30, -29, -27, 1), tb("b2-1", -28, -27, -26, 1), tb("b3-1", -26, -25.5, -24, 0.5)}

const (
	starved     = "waited for all of tenant A's load"
	backlog     = "waited behind A's backlog"
	notMeasured = "contention not measured"
	late        = "not measured under contention"
)

func TestJudgeFairness_Baseline(t *testing.T) {
	f := JudgeFairness(alone, nil, load(15, 2))
	assert.Equal(t, 3*time.Second, f.Alone)
	assert.Equal(t, time.Second, f.AloneStep)
	assert.False(t, f.Backlog, "A's median step of 2 s is no backlog against B's 1 s alone")
	assert.True(t, JudgeFairness(alone, nil, load(15, 4)).Backlog)
}

// TestJudgeFairness judges timelines of the hosts #2252 and #2257 measured:
// the fair queue, a queue that holds B until A drains, controller-runtime's
// default queue (no fairqueue), and QA-2's probes.
func TestJudgeFairness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		loaded []TenantBundle
		a      TenantLoad
		judged int
		want   []string // the kinds of violation, in order
	}{
		{
			// #2252's host: A's 30 repositories are done in 15 s, B's
			// Bundles end after A. A's median step (2 s) is no backlog:
			// B's steps are not judged against it.
			name:   "fast host, fair, B ends after A",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 7, 4), tb("b2-2", 4, 6, 10, 4), tb("b3-2", 6, 8, 13, 5)},
			a:      load(15, 2),
			judged: 3,
		},
		{
			name:   "fast host, B held until A drains",
			loaded: []TenantBundle{tb("b1-2", 2, 16, 19, 14), tb("b2-2", 4, 17, 20, 13), tb("b3-2", 6, 17, 21, 11)},
			a:      load(15, 2),
			judged: 3,
			want:   []string{starved, starved, starved},
		},
		{
			// QA-2's probe (a) on a host with no backlog: the first
			// environment served at once, the other two held until A
			// drains. Nothing tells it from a fair B there.
			name:   "fast host, first environment served and the rest held: not judged",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 17, 13)},
			a:      load(15, 2),
			judged: 1,
		},
		{
			// e2e-live run 39377163730: B's slowest step 8 s, A's median
			// 12 s.
			name:   "GitHub runner, fair",
			loaded: []TenantBundle{tb("b1-2", 12, 21, 33, 8), tb("b2-2", 14, 22, 34, 8), tb("b3-2", 16, 24, 40, 7)},
			a:      load(75, 12),
			judged: 3,
		},
		{
			// The slowest nightly run: B at 39 s, its slowest step 13 s
			// against A's median of 15 s.
			name:   "GitHub runner, fair, slowest night",
			loaded: []TenantBundle{tb("b1-2", 12, 22, 51, 13)},
			a:      load(100, 15),
			judged: 1,
		},
		{
			// e2e-live run 39405172131, fairqueue removed: B's steps wait
			// behind A's backlog (22-24 s against A's median of 19 s), but
			// B starts and ends well within A's load.
			name:   "GitHub runner, no fairqueue",
			loaded: []TenantBundle{tb("b1-2", 12, 37, 81, 24), tb("b2-2", 14, 35, 78, 22), tb("b3-2", 16, 36, 80, 23)},
			a:      load(115, 19),
			judged: 3,
			want:   []string{backlog, backlog, backlog},
		},
		{
			// A kind node capped at 4 CPUs: B's slowest step 8 s, A's
			// median 9 s.
			name:   "4-CPU node, fair",
			loaded: []TenantBundle{tb("b1-2", 12, 22, 42, 8), tb("b2-2", 14, 24, 43, 8)},
			a:      load(70, 9),
			judged: 2,
		},
		{
			// QA-2's probe (a) where A's load is a backlog: the first
			// environment at once, the other two held until A drains.
			name:   "GitHub runner, first environment served and the rest held",
			loaded: []TenantBundle{tb("b1-2", 12, 13, 78, 60)},
			a:      load(75, 12),
			judged: 1,
			want:   []string{backlog},
		},
		{
			name:   "GitHub runner, B held until A drains",
			loaded: []TenantBundle{tb("b1-2", 12, 76, 78, 64), tb("b2-2", 14, 77, 79, 63)},
			a:      load(75, 12),
			judged: 2,
			want:   []string{backlog, starved, backlog, starved},
		},
		{
			name:   "a step exactly A's median plus the slack",
			loaded: []TenantBundle{tb("b1-2", 12, 20, 40, 14)},
			a:      load(75, 12),
			judged: 1,
		},
		{
			name:   "a step a second longer",
			loaded: []TenantBundle{tb("b1-2", 12, 20, 40, 15)},
			a:      load(75, 12),
			judged: 1,
			want:   []string{backlog},
		},
		{
			// A's median step exactly 3 times B's slowest step alone: no
			// backlog yet.
			name:   "A's median step at the backlog line",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 12, 10)},
			a:      load(30, 3),
			judged: 1,
		},
		{
			name:   "A's median step past the backlog line",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 12, 10)},
			a:      load(30, 4),
			judged: 1,
			want:   []string{backlog},
		},
		{
			// A ends within the window of every B Bundle: nothing tells a
			// starved B from a fair one.
			name:   "A's load too short",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 6, 1), tb("b2-2", 4, 5, 6, 1)},
			a:      load(7, 2),
			judged: 0,
			want:   []string{notMeasured},
		},
		{
			name:   "a B Bundle created after A's load",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 7, 1), tb("b2-2", 16, 17, 20, 1)},
			a:      load(15, 2),
			judged: 1,
			want:   []string{late},
		},
		{
			// Already reported as not measured: no second violation.
			name:   "every B Bundle created after A's load",
			loaded: []TenantBundle{tb("b1-2", 16, 17, 20, 1), tb("b2-2", 18, 19, 22, 1)},
			a:      load(15, 2),
			judged: 0,
			want:   []string{late, late},
		},
		{
			name:   "a B Bundle that did not end Verified",
			loaded: []TenantBundle{tb("b1-2", 2, never, never, 0), tb("b2-2", 4, 6, 10, 1)},
			a:      load(15, 2),
			judged: 2,
			want:   []string{"did not end Verified", starved},
		},
		{
			// The timestamps are whole seconds: B's first environment in
			// the second A ended is not before it.
			name:   "first environment in A's last second",
			loaded: []TenantBundle{tb("b1-2", 2, 15, 17, 1)},
			a:      load(15, 2),
			judged: 1,
			want:   []string{starved},
		},
		{
			// A's load ends exactly the window (5 s) after B's creation:
			// not judged.
			name:   "exactly the window after creation",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 6, 1)},
			a:      load(7, 2),
			judged: 0,
			want:   []string{notMeasured},
		},
		{
			name:   "a second past the window, no environment before A's end",
			loaded: []TenantBundle{tb("b1-2", 2, 8, 10, 1)},
			a:      load(8, 2),
			judged: 1,
			want:   []string{starved},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := JudgeFairness(alone, tc.loaded, tc.a)
			assert.Equal(t, tc.judged, f.Judged, "judged")
			require.Len(t, f.Violations, len(tc.want), "%q", f.Violations)
			for i, kind := range tc.want {
				assert.Contains(t, f.Violations[i], kind)
			}
		})
	}
}

func TestJudgeFairness_NoBaseline(t *testing.T) {
	loaded := []TenantBundle{tb("b1-2", 2, 3, 7, 1)}
	f := JudgeFairness(nil, loaded, load(15, 2))
	require.Len(t, f.Violations, 1)
	assert.Contains(t, f.Violations[0], "no Bundle of tenant B ran alone")

	f = JudgeFairness([]TenantBundle{alone[0], tb("b2-1", -28, -27, never, 1)}, loaded, load(15, 2))
	require.Len(t, f.Violations, 1)
	assert.Contains(t, f.Violations[0], "b2-1, alone, did not end Verified")
}
