// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package invariants

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func TestTagIn(t *testing.T) {
	const img = "ghcr.io/kardinal-scale/app"
	cases := []struct {
		name, in, want, err string
	}{
		{"tag", "images:\n  - name: " + img + "\n    newTag: b1\n", "b1", ""},
		{"digest wins", "images:\n  - name: " + img + "\n    newTag: b1\n    digest: sha256:abc\n", "sha256:abc", ""},
		{"by newName", "images:\n  - name: other\n    newName: " + img + "\n    newTag: b2\n", "b2", ""},
		{"missing", "images:\n  - name: other\n    newTag: b2\n", "", "no images entry"},
		{"not yaml", ":\n  - [", "", "yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tagIn([]byte(tc.in), img)
			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestGraphErrorReason(t *testing.T) {
	for reason, want := range map[string]bool{
		"WaitingForReadiness": false, "Compiled": false, "Ready": false,
		"ReconcileError": true, "InvalidGraph": true, "ApplyFailed": true, "Forbidden": true,
	} {
		assert.Equal(t, want, graphErrorReason(reason), reason)
	}
}

func step(bundle, env, state, msg string) v1alpha1.PromotionStep {
	return v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: bundle + "-" + env},
		Spec:       v1alpha1.PromotionStepSpec{BundleName: bundle, Environment: env, PipelineName: "p"},
		Status:     v1alpha1.PromotionStepStatus{State: state, Message: msg},
	}
}

func bundle(name, phase string) v1alpha1.Bundle {
	return v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: v1alpha1.BundleStatus{Phase: phase}}
}

func testState(bundles []v1alpha1.Bundle, steps []v1alpha1.PromotionStep) *state {
	st := &state{bundles: bundles, steps: steps, byName: map[string]*v1alpha1.Bundle{}, stepsOf: map[string]map[string]*v1alpha1.PromotionStep{}}
	for i := range st.bundles {
		st.byName[st.bundles[i].Name] = &st.bundles[i]
	}
	for i := range st.steps {
		s := &st.steps[i]
		if st.stepsOf[s.Spec.BundleName] == nil {
			st.stepsOf[s.Spec.BundleName] = map[string]*v1alpha1.PromotionStep{}
		}
		st.stepsOf[s.Spec.BundleName][s.Spec.Environment] = s
	}
	return st
}

func TestCheckTerminalAndPhases(t *testing.T) {
	st := testState(
		[]v1alpha1.Bundle{bundle("a", "Verified"), bundle("b", "Promoting"), bundle("c", "Superseded")},
		[]v1alpha1.PromotionStep{
			step("a", "test", "Verified", ""), step("a", "prod", "Failed", "x"),
			step("b", "test", "Promoting", "cloning"),
			step("c", "test", "HealthChecking", ""),
		})
	term := checkTerminal(st)
	require.Len(t, term.Violations, 1)
	assert.Contains(t, term.Violations[0], `Bundle b`)
	assert.Contains(t, term.Violations[0], "test=Promoting (cloning)")
	ph := checkPhases(st)
	assert.Len(t, ph.Violations, 2, "a Verified Bundle with a Failed step, a Superseded one with a running step")
}

func TestLatencyAndQuantiles(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	mk := func(b, env string, created, verified time.Duration) v1alpha1.PromotionStep {
		s := step(b, env, "Verified", "")
		s.CreationTimestamp = metav1.NewTime(t0.Add(created))
		s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(t0.Add(verified))}}
		return s
	}
	b := bundle("a", "Verified")
	b.CreationTimestamp = metav1.NewTime(t0)
	l := latency(testState([]v1alpha1.Bundle{b}, []v1alpha1.PromotionStep{
		mk("a", "test", 0, 2*time.Second), mk("a", "prod", 2*time.Second, 10*time.Second),
	}))
	assert.Equal(t, Quantiles{N: 2, P50: 2, P99: 8, Max: 8}, l.Overall)
	assert.Equal(t, Quantiles{N: 1, P50: 10, P99: 10, Max: 10}, l.Bundle)
	assert.Equal(t, 8.0, l.Stage["prod"].P50)

	q := quantiles([]float64{5, 1, 3, 2, 4, 6, 7, 8, 9, 10})
	assert.Equal(t, Quantiles{N: 10, P50: 5, P99: 10, Max: 10}, q)
	assert.Equal(t, Quantiles{}, quantiles(nil))
}

func TestReportSkipAndMarkdown(t *testing.T) {
	r := &Report{Test: "TestScale_X", skip: map[string]string{"env-content": "the namespace is gone"}}
	r.add(Result{Name: "env-content", Violations: []string{"wrong"}})
	r.add(Result{Name: "graphs-not-stuck", Violations: []string{"Graph g | outlived"}})
	r.add(Result{Name: "audit-consistent", Note: "3 AuditEvents"})
	require.Len(t, r.Checks, 3)
	assert.True(t, r.Checks[0].Pass)
	assert.Equal(t, "skipped: the namespace is gone", r.Checks[0].Note)
	assert.False(t, r.Checks[1].Pass)
	md := r.Markdown()
	assert.Contains(t, md, "| graphs-not-stuck | **FAIL** (1) | Graph g \\| outlived |")
	assert.Contains(t, md, "| audit-consistent | pass | 3 AuditEvents |")
}

func TestNotEmpty(t *testing.T) {
	r := &Report{}
	r.add(Result{Name: "not-empty", Violations: []string{"no Pipeline to check"}})
	assert.False(t, r.Checks[0].Pass)
}

func TestLeaks(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(30 * time.Minute)
	pod := func(from, to time.Time, rss0, rss1, g0, g1 float64) PodSeries {
		return PodSeries{Pod: "p", From: from, To: to, RSSStartMiB: rss0, RSSEndMiB: rss1, RSSMaxMiB: math.Max(rss0, rss1),
			GoroutinesStart: g0, GoroutinesEnd: g1, GoroutinesMax: math.Max(g0, g1)}
	}
	cases := []struct {
		name   string
		pods   []PodSeries
		shared bool
		race   bool
		want   int
	}{
		{"flat", []PodSeries{pod(t0, t1, 300, 500, 300, 320)}, false, false, 0},
		{"race: steady growth measured over the full profile", []PodSeries{pod(t0, t1, 165, 380, 300, 320)}, false, true, 0},
		{"race: growth past 2.5x + 500 MiB", []PodSeries{pod(t0, t1, 300, 1300, 300, 320)}, false, true, 1},
		{"RSS more than doubled plus 200 MiB", []PodSeries{pod(t0, t1, 300, 900, 300, 320)}, false, false, 1},
		{"goroutines grew", []PodSeries{pod(t0, t1, 300, 300, 300, 500)}, false, false, 1},
		{"shared controller: growth only reported", []PodSeries{pod(t0, t1, 300, 900, 300, 500)}, true, false, 0},
		{"a Pod that started late is not compared", []PodSeries{pod(t0.Add(10*time.Minute), t1, 100, 900, 40, 400)}, false, false, 0},
		{"near the limit", []PodSeries{pod(t0, t1, 300, 3800, 300, 300)}, true, false, 1},
		{"no series", nil, false, false, 1},
		{"a standby that took over the lead", []PodSeries{func() PodSeries {
			p := pod(t0, t1, 100, 300, 43, 325)
			p.LeaderEnd = true
			return p
		}()}, false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Len(t, leaks(tc.pods, t0, t1, 4096, tc.shared, tc.race), tc.want)
		})
	}
}

func TestCheckSLO(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	pl := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Spec: v1alpha1.PipelineSpec{
		Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod", Approval: "pr-review"}}}}
	mk := func(env string, d time.Duration) v1alpha1.PromotionStep {
		s := step("a", env, "Verified", "")
		s.CreationTimestamp = metav1.NewTime(t0)
		s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(t0.Add(d))}}
		return s
	}
	b := bundle("a", "Verified")
	b.CreationTimestamp = metav1.NewTime(t0)
	st := testState([]v1alpha1.Bundle{b}, []v1alpha1.PromotionStep{mk("test", 4*time.Second), mk("prod", 10*time.Minute)})
	o := Options{Targets: []Target{{Pipeline: pl}}}

	o.SLO = &SLO{StepP50: 5 * time.Second, StepP99: 15 * time.Second}
	assert.Empty(t, checkSLO(st, o).Violations, "the pr-review step's wait for its reviewer does not count")

	o.SLO = &SLO{StepP99: 3 * time.Second, BundleP99: time.Minute}
	v := checkSLO(st, o).Violations
	require.Len(t, v, 2)
	assert.Contains(t, v[0], "auto step p99 is 4s")
	assert.Contains(t, v[1], "Bundle end to end p99 is 600s")

	assert.Contains(t, checkSLO(testState(nil, nil), o).Violations[0], "nothing to measure")
}

// TestPushEfficiency (#1578): refused pushes above the ratio fail the run,
// none or a few do not, and a shared controller only reports.
//
// Covers SCALE-INV-PUSH-01.
func TestPushEfficiency(t *testing.T) {
	assert.Empty(t, pushEfficiency(150, 0, 0.5, false))
	assert.Empty(t, pushEfficiency(150, 75, 0.5, false))
	assert.Len(t, pushEfficiency(318, 4023, 0.5, false), 1, "the O(N²) wave of #1578")
	assert.Len(t, pushEfficiency(0, 2, 0.5, false), 1, "refusals with nothing landed")
	assert.Empty(t, pushEfficiency(318, 4023, 0.5, true), "shared controller: reported only")
}

// TestRetiredSteps: the steps of a retired Graph come back from the Bundle's
// status.retiredSteps, so the phase and audit checks still see them, and a
// step that still exists is not counted twice.
func TestRetiredSteps(t *testing.T) {
	verifiedAt := metav1.NewTime(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	b := v1alpha1.Bundle{}
	b.Name, b.Namespace, b.Spec.Pipeline = "app-v1", "ns", "app"
	b.Status.RetiredSteps = []v1alpha1.RetiredStep{
		{Name: "app-v1-test", Environment: "test", State: "Verified", PRURL: "https://x/pr/1", VerifiedAt: &verifiedAt},
		{Name: "app-v1-prod", Environment: "prod", State: "Failed", Message: "superseded"},
	}
	live := []v1alpha1.PromotionStep{{}}
	live[0].Name = "app-v1-prod"
	got := retiredSteps([]v1alpha1.Bundle{b}, live)
	if len(got) != 1 {
		t.Fatalf("got %d steps, want 1 (app-v1-prod still exists)", len(got))
	}
	s := got[0]
	if s.Name != "app-v1-test" || s.Spec.BundleName != "app-v1" || s.Spec.Environment != "test" ||
		s.Status.State != "Verified" || s.Status.PRURL != "https://x/pr/1" || s.Namespace != "ns" {
		t.Fatalf("rebuilt step %+v", s)
	}
	// The Verified time survives the rebuild (latency checks read it).
	if at, ok := lifecycle.VerifiedTime(&s); !ok || !at.Equal(verifiedAt.Time) {
		t.Fatalf("verifiedAt = %v, %v; want %v", at, ok, verifiedAt.Time)
	}
}
