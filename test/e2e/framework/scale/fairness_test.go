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

// unjudged leaves B's steps against A's backlog unjudged, as the ci profile
// does; nightly judges them at 0.9.
var (
	unjudged = BacklogCheck{Why: "the ci profile"}
	nightly  = BacklogCheck{Ratio: 0.9}
)

const (
	starved     = "waited for all of tenant A's load"
	backlog     = "waited behind A's backlog"
	notMeasured = "contention not measured"
	late        = "not measured under contention"
)

func TestJudgeFairness_Baseline(t *testing.T) {
	f := JudgeFairness(alone, nil, load(15, 2), nightly)
	assert.Equal(t, 3*time.Second, f.Alone)
	assert.Equal(t, time.Second, f.AloneStep)
	assert.False(t, f.Backlog, "A's median step of 2 s is no backlog against B's 1 s alone")
	assert.True(t, JudgeFairness(alone, nil, load(15, 4), nightly).Backlog)
	assert.False(t, JudgeFairness(alone, nil, load(15, 40), unjudged).Backlog, "a profile with no ratio judges no backlog")
}

// TestJudgeFairness_Checks: a check that is not judged says so, and why.
func TestJudgeFairness_Checks(t *testing.T) {
	loaded := []TenantBundle{tb("b1-2", 12, 21, 33, 8)}
	f := JudgeFairness(alone, loaded, load(75, 12), unjudged)
	assert.Equal(t, []string{
		"B's steps against A's backlog: not judged: the ci profile",
		"starvation: judged on 1 of 1 B Bundles (A's load outlasted them by more than 5s)",
	}, f.Checks)

	f = JudgeFairness(alone, loaded, load(75, 3), nightly)
	assert.Equal(t, "B's steps against A's backlog: not judged: A's median step (3s) is not more than 3 times B's slowest step alone (1s), so A's load built no backlog", f.Checks[0])

	f = JudgeFairness(alone, append(loaded, tb("b2-2", 14, 22, 34, 6)), load(75, 40), nightly)
	assert.Equal(t, "B's steps against A's backlog: judged: B's slowest step 8s, at most 0.90 times A's median step (40s) plus 2s (38s)", f.Checks[0])
	assert.Equal(t, 8*time.Second, f.SlowestStep)
}

// TestJudgeFairness judges timelines of the hosts #2252 and #2257 measured:
// the fair queue, a queue that holds B until A drains, controller-runtime's
// default queue (no fairqueue), and QA-2's probes.
func TestJudgeFairness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		loaded []TenantBundle
		a      TenantLoad
		bc     BacklogCheck
		judged int
		want   []string // the kinds of violation, in order
	}{
		{
			// #2252's host: A's 30 repositories are done in 15 s, B's
			// Bundles end after A. A's median step (2 s) is no backlog.
			name:   "fast host, fair, B ends after A",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 7, 4), tb("b2-2", 4, 6, 10, 4), tb("b3-2", 6, 8, 13, 5)},
			a:      load(15, 2),
			bc:     nightly,
			judged: 3,
		},
		{
			name:   "fast host, B held until A drains",
			loaded: []TenantBundle{tb("b1-2", 2, 16, 19, 14), tb("b2-2", 4, 17, 20, 13), tb("b3-2", 6, 17, 21, 11)},
			a:      load(15, 2),
			bc:     nightly,
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
			bc:     nightly,
			judged: 1,
		},
		{
			// e2e-live run 39429895828, 60 repositories: B's slowest step
			// 23 s, A's median 37 s (0.62).
			name:   "GitHub runner, 60 repositories, fair",
			loaded: []TenantBundle{tb("b1-2", 12, 30, 69, 23), tb("b2-2", 14, 33, 70, 21), tb("b3-2", 16, 35, 72, 22)},
			a:      load(220, 37),
			bc:     nightly,
			judged: 3,
		},
		{
			// e2e-live run 39429899992, 60 repositories, fairqueue removed:
			// B's steps 47-54 s against A's median of 39 s (1.2-1.38).
			name:   "GitHub runner, 60 repositories, no fairqueue",
			loaded: []TenantBundle{tb("b1-2", 12, 52, 160, 49), tb("b2-2", 14, 55, 162, 47), tb("b3-2", 16, 60, 166, 54)},
			a:      load(230, 39),
			bc:     nightly,
			judged: 3,
			want:   []string{backlog, backlog, backlog},
		},
		{
			// e2e-live run 39436920566, 60 repositories: A's shortest
			// median of the three fair runs, 27 s; B's slowest 15 s.
			name:   "GitHub runner, 60 repositories, fair, A's shortest median",
			loaded: []TenantBundle{tb("b1-2", 12, 22, 52, 15)},
			a:      load(160, 27),
			bc:     nightly,
			judged: 1,
		},
		{
			// e2e-live run 39436928686, 60 repositories, no fairqueue: the
			// B Bundle closest to the bound, 42 s against A's median of
			// 37 s (1.14); the bound is 35.3 s.
			name:   "GitHub runner, 60 repositories, no fairqueue, closest",
			loaded: []TenantBundle{tb("b1-2", 12, 50, 158, 43), tb("b2-2", 14, 52, 160, 50), tb("b3-2", 16, 54, 162, 42)},
			a:      load(215, 37),
			bc:     nightly,
			judged: 3,
			want:   []string{backlog, backlog, backlog},
		},
		{
			// e2e-live runs 39429904153 and 39429908128, 90 repositories.
			name:   "GitHub runner, 90 repositories, fair",
			loaded: []TenantBundle{tb("b1-2", 12, 30, 66, 23)},
			a:      load(230, 38),
			bc:     nightly,
			judged: 1,
		},
		{
			name:   "GitHub runner, 90 repositories, no fairqueue",
			loaded: []TenantBundle{tb("b1-2", 12, 80, 222, 67), tb("b2-2", 14, 84, 220, 70), tb("b3-2", 16, 88, 226, 72)},
			a:      load(320, 59),
			bc:     nightly,
			judged: 3,
			want:   []string{backlog, backlog, backlog},
		},
		{
			// e2e-live run 39405172131, ci's 30 repositories, fairqueue
			// removed: the ci profile does not judge the backlog, and
			// JudgeFairness passes it (its TenantStepP99 fails it).
			name:   "GitHub runner, ci's 30 repositories, no fairqueue, unjudged",
			loaded: []TenantBundle{tb("b1-2", 12, 37, 81, 24), tb("b2-2", 14, 35, 78, 22), tb("b3-2", 16, 36, 80, 23)},
			a:      load(115, 19),
			bc:     unjudged,
			judged: 3,
		},
		{
			// QA-2's probe (a) where A's load is a backlog: the first
			// environment at once, the other two held until A drains.
			name:   "GitHub runner, first environment served and the rest held",
			loaded: []TenantBundle{tb("b1-2", 12, 13, 220, 200)},
			a:      load(220, 37),
			bc:     nightly,
			judged: 1,
			want:   []string{backlog},
		},
		{
			name:   "GitHub runner, B held until A drains",
			loaded: []TenantBundle{tb("b1-2", 12, 221, 224, 208), tb("b2-2", 14, 222, 225, 207)},
			a:      load(220, 37),
			bc:     nightly,
			judged: 2,
			want:   []string{backlog, starved, backlog, starved},
		},
		{
			// 0.9 times 40 s, plus 2 s: 38 s.
			name:   "a step exactly at the bound",
			loaded: []TenantBundle{tb("b1-2", 12, 20, 60, 38)},
			a:      load(200, 40),
			bc:     nightly,
			judged: 1,
		},
		{
			name:   "a step a second over",
			loaded: []TenantBundle{tb("b1-2", 12, 20, 60, 39)},
			a:      load(200, 40),
			bc:     nightly,
			judged: 1,
			want:   []string{backlog},
		},
		{
			// A's median step exactly 3 times B's slowest step alone: no
			// backlog yet.
			name:   "A's median step at the backlog line",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 12, 10)},
			a:      load(30, 3),
			bc:     nightly,
			judged: 1,
		},
		{
			name:   "A's median step past the backlog line",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 12, 10)},
			a:      load(30, 4),
			bc:     nightly,
			judged: 1,
			want:   []string{backlog},
		},
		{
			// A ends within the window of every B Bundle: nothing tells a
			// starved B from a fair one.
			name:   "A's load too short",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 6, 1), tb("b2-2", 4, 5, 6, 1)},
			a:      load(7, 2),
			bc:     unjudged,
			judged: 0,
			want:   []string{notMeasured},
		},
		{
			name:   "a B Bundle created after A's load",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 7, 1), tb("b2-2", 16, 17, 20, 1)},
			a:      load(15, 2),
			bc:     unjudged,
			judged: 1,
			want:   []string{late},
		},
		{
			// Already reported as not measured: no second violation.
			name:   "every B Bundle created after A's load",
			loaded: []TenantBundle{tb("b1-2", 16, 17, 20, 1), tb("b2-2", 18, 19, 22, 1)},
			a:      load(15, 2),
			bc:     unjudged,
			judged: 0,
			want:   []string{late, late},
		},
		{
			name:   "a B Bundle that did not end Verified",
			loaded: []TenantBundle{tb("b1-2", 2, never, never, 0), tb("b2-2", 4, 6, 10, 1)},
			a:      load(15, 2),
			bc:     unjudged,
			judged: 2,
			want:   []string{"did not end Verified", starved},
		},
		{
			// The timestamps are whole seconds: B's first environment in
			// the second A ended is not before it.
			name:   "first environment in A's last second",
			loaded: []TenantBundle{tb("b1-2", 2, 15, 17, 1)},
			a:      load(15, 2),
			bc:     unjudged,
			judged: 1,
			want:   []string{starved},
		},
		{
			// A's load ends exactly the window (5 s) after B's creation:
			// not judged.
			name:   "exactly the window after creation",
			loaded: []TenantBundle{tb("b1-2", 2, 3, 6, 1)},
			a:      load(7, 2),
			bc:     unjudged,
			judged: 0,
			want:   []string{notMeasured},
		},
		{
			name:   "a second past the window, no environment before A's end",
			loaded: []TenantBundle{tb("b1-2", 2, 8, 10, 1)},
			a:      load(8, 2),
			bc:     unjudged,
			judged: 1,
			want:   []string{starved},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := JudgeFairness(alone, tc.loaded, tc.a, tc.bc)
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
	f := JudgeFairness(nil, loaded, load(15, 2), nightly)
	require.Len(t, f.Violations, 1)
	assert.Contains(t, f.Violations[0], "no Bundle of tenant B ran alone")

	f = JudgeFairness([]TenantBundle{alone[0], tb("b2-1", -28, -27, never, 1)}, loaded, load(15, 2), nightly)
	require.Len(t, f.Violations, 1)
	assert.Contains(t, f.Violations[0], "b2-1, alone, did not end Verified")
}
