// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// C09b-cli-14: the controller's metrics (last environment, 30 days) are shown
// only when that is what was asked; other --env/--days are computed.
func TestMetrics_EnvAndDaysHonoured(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	pipe := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec:       v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "uat"}, {Name: "prod"}}},
	}
	pipe.Status.DeploymentMetrics = &v1alpha1.PipelineDeploymentMetrics{SampleSize: 3, P50CommitToProdMinutes: 40}
	bundle := func(name string, created time.Time) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(created)},
			Spec:       v1alpha1.BundleSpec{Pipeline: "demo"},
		}
	}
	verified := func(bundleName, env string, at time.Time) *v1alpha1.PromotionStep {
		s := &v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: bundleName + "-" + env, Namespace: "default",
				Labels: map[string]string{"kardinal.io/pipeline": "demo"}},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "demo", BundleName: bundleName, Environment: env},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"},
		}
		s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(at)}}
		return s
	}
	objs := []sigs_client.Object{
		pipe,
		bundle("recent", now.Add(-2*time.Hour)), verified("recent", "uat", now.Add(-time.Hour)),
		bundle("old", now.Add(-10*24*time.Hour)), verified("old", "uat", now.Add(-10*24*time.Hour+30*time.Minute)),
	}
	c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(objs...).Build()

	run := func(env string, days int) string {
		var buf bytes.Buffer
		require.NoError(t, metricsFn(&buf, c, "default", "demo", env, days, now))
		return buf.String()
	}

	for _, env := range []string{"", "prod"} {
		out := run(env, 30)
		assert.Contains(t, out, "(controller: last 3 bundles verified in prod)")
		assert.Contains(t, out, "p50_commit_to_prod")
	}

	out := run("uat", 1)
	assert.NotContains(t, out, "p50_commit_to_prod")
	assert.Equal(t, "1", metricValue(out, "bundles_total"))
	assert.Equal(t, "1.00/day", metricValue(out, "deployment_frequency"))
	assert.Equal(t, "1h0m", metricValue(out, "lead_time_avg"))

	out = run("uat", 30)
	assert.Equal(t, "2", metricValue(out, "bundles_total"))
	assert.Contains(t, out, "(2 verified in target env)")

	var buf bytes.Buffer
	err := metricsFn(&buf, c, "default", "missing", "", 30, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pipeline "missing" not found`)
	require.Error(t, metricsFn(&buf, c, "default", "demo", "", 0, now))
}

// metricValue returns the VALUE cell of the METRIC row name.
func metricValue(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == name {
			return f[1]
		}
	}
	return ""
}

// TestRenderFromCRD_Stability: the controller's change failure rate and
// time to restore rows, and "-" when no failure has been restored.
func TestRenderFromCRD_Stability(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		dm           v1alpha1.PipelineDeploymentMetrics
		cfr, cfrNote string
		ttr, ttrNote string
	}{
		{"restored", v1alpha1.PipelineDeploymentMetrics{Deployments: 4, FailedDeployments: 1, ChangeFailureRateMillis: 250,
			MeanTimeToRestoreMinutes: 42, RestoredFailures: 1},
			"25.0%", "(1 of 4 deployments failed)", "42m", "(mean of 1 restored failures)"},
		{"none restored", v1alpha1.PipelineDeploymentMetrics{Deployments: 3, FailedDeployments: 1, ChangeFailureRateMillis: 333},
			"33.3%", "(1 of 3 deployments failed)", "-", "(no restored failure)"},
		{"healthy", v1alpha1.PipelineDeploymentMetrics{Deployments: 2},
			"0.0%", "(0 of 2 deployments failed)", "-", "(no restored failure)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, renderFromCRD(&buf, "demo", "prod", &tt.dm, now))
			out := buf.String()
			assert.Equal(t, tt.cfr, metricValue(out, "change_failure_rate"))
			assert.Contains(t, out, tt.cfrNote)
			assert.Equal(t, tt.ttr, metricValue(out, "time_to_restore"))
			assert.Contains(t, out, tt.ttrNote)
		})
	}
}

// TestRenderFromCRD_NothingVerified: when every deployment failed, the
// lead-time and staleness rows read "-" instead of 0.
func TestRenderFromCRD_NothingVerified(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	dm := v1alpha1.PipelineDeploymentMetrics{Deployments: 2, FailedDeployments: 2, ChangeFailureRateMillis: 1000}
	require.NoError(t, renderFromCRD(&buf, "demo", "prod", &dm, time.Now()))
	out := buf.String()
	for _, row := range []string{"p50_commit_to_prod", "p90_commit_to_prod", "stale_prod_days"} {
		assert.Equal(t, "-", metricValue(out, row), row)
	}
	assert.Equal(t, "100.0%", metricValue(out, "change_failure_rate"))
}

// TestRenderFromCRD_LeadTimeAndRollbacks (#1942): the lead time rows say
// where they start, and the automatic and overall rollback rates are two
// rows.
func TestRenderFromCRD_LeadTimeAndRollbacks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fromCommit int
		note       string
	}{
		{0, "(from Bundle creation)"},
		{4, "(from the commit)"},
		// The sample also counts versions with no lead time (rollbacks), so
		// the others' count is not known (#1985, QA).
		{1, "(from the commit for 1, Bundle creation for any others)"},
	}
	for _, tc := range tests {
		var buf bytes.Buffer
		dm := v1alpha1.PipelineDeploymentMetrics{SampleSize: 4, P50CommitToProdMinutes: 7, LeadTimeFromCommit: tc.fromCommit,
			AutoRollbackRateMillis: 0, RollbackRateMillis: 250}
		require.NoError(t, renderFromCRD(&buf, "demo", "prod", &dm, time.Now()))
		out := buf.String()
		assert.Contains(t, out, tc.note)
		assert.Equal(t, "0.0%", metricValue(out, "auto_rollback_rate"))
		assert.Equal(t, "25.0%", metricValue(out, "rollback_rate"))
	}
}

// TestRenderFromCRD_OnlyRollbacks: a sample of rollbacks only has no lead
// time, so the rows read "-" instead of 0 "from Bundle creation".
func TestRenderFromCRD_OnlyRollbacks(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	dm := v1alpha1.PipelineDeploymentMetrics{SampleSize: 2, RollbackRateMillis: 1000}
	require.NoError(t, renderFromCRD(&buf, "demo", "prod", &dm, time.Now()))
	out := buf.String()
	for _, row := range []string{"p50_commit_to_prod", "p90_commit_to_prod"} {
		assert.Equal(t, "-", metricValue(out, row), row)
	}
	assert.Contains(t, out, "(only rollbacks Verified; they have no lead time)")
}

// TestMetrics_LeadTimeNote (#1985, QA): the computed lead_time_avg counts
// the versions measured from their commit, and leaves rollbacks out of the
// samples (a rollback's commit is the restored build's).
func TestMetrics_LeadTimeNote(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	pipe := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec:       v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "uat"}, {Name: "prod"}}},
	}
	bundle := func(name string, created time.Time, prov *v1alpha1.BundleProvenance) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(created)},
			Spec:       v1alpha1.BundleSpec{Pipeline: "demo", Provenance: prov},
		}
	}
	verified := func(bundleName string, at time.Time) *v1alpha1.PromotionStep {
		s := &v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: bundleName + "-uat", Namespace: "default",
				Labels: map[string]string{"kardinal.io/pipeline": "demo"}},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "demo", BundleName: bundleName, Environment: "uat"},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"},
		}
		s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(at)}}
		return s
	}
	created := now.Add(-3 * time.Hour)
	commit := metav1.NewTime(created.Add(-time.Hour))
	objs := []sigs_client.Object{
		pipe,
		// 2h from its commit.
		bundle("a", created, &v1alpha1.BundleProvenance{CommitTime: &commit}), verified("a", created.Add(time.Hour)),
		// 1h from creation.
		bundle("b", created, nil), verified("b", created.Add(time.Hour)),
		// A rollback: not a sample, though its commit time is set.
		bundle("rb", created, &v1alpha1.BundleProvenance{RollbackOf: "a", CommitTime: &commit}), verified("rb", created.Add(time.Minute)),
	}
	c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(objs...).Build()
	var buf bytes.Buffer
	require.NoError(t, metricsFn(&buf, c, "default", "demo", "uat", 1, now))
	out := buf.String()
	assert.Equal(t, "1h30m", metricValue(out, "lead_time_avg"))
	assert.Contains(t, out, "(from the commit for 1, Bundle creation for 1 to uat verified, 2 samples)")
	assert.Equal(t, "1", metricValue(out, "rollback_count"))
}
