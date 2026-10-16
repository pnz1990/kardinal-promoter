// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// retire turns b's steps into status.retiredSteps, as the Bundle reconciler
// does when it deletes b's Graph, and returns no live step: kro deleted them.
func retire(b *kardinalv1alpha1.Bundle, steps ...kardinalv1alpha1.PromotionStep) {
	for i := range steps {
		b.Status.RetiredSteps = append(b.Status.RetiredSteps, lifecycle.RetiredStepOf(&steps[i]))
	}
	at := metav1.NewTime(t0.Add(48 * time.Hour))
	b.Status.RetiredAt = &at
}

// TestComputeDeploymentMetrics_RetiredBundlesCount (#1944): a Bundle whose
// Graph retired keeps its steps only in status.retiredSteps. They still count
// as deployments, failures and restores. Before, the rebuilt step had no
// health-check start, so every retired Bundle dropped out: the live rust
// Pipeline showed "0 of 1 deployments failed" for five deployments.
func TestComputeDeploymentMetrics_RetiredBundlesCount(t *testing.T) {
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	v1, broken, v3, v4 := bundleAt("v1", t0), bundleAt("broken", t0.Add(time.Hour)),
		bundleAt("v3", t0.Add(2*time.Hour)), bundleAt("v4", t0.Add(3*time.Hour))
	broken.Status.Phase = "Failed"
	noop := deployedStep("v3", "Verified", t0.Add(2*time.Hour+5*time.Minute), t0.Add(2*time.Hour+10*time.Minute))
	noop.Status.Outputs = map[string]string{"noChanges": "true"}
	retire(&v1, deployedStep("v1", "Verified", t0.Add(5*time.Minute), t0.Add(10*time.Minute)))
	retire(&broken, deployedStep("broken", "Failed", t0.Add(time.Hour+5*time.Minute), t0.Add(time.Hour+8*time.Minute)))
	retire(&v3, noop)
	live := deployedStep("v4", "Verified", t0.Add(3*time.Hour+5*time.Minute), t0.Add(3*time.Hour+10*time.Minute))
	bundles := []kardinalv1alpha1.Bundle{v1, broken, v3, v4}

	steps := lifecycle.AddRetiredSteps([]kardinalv1alpha1.PromotionStep{live}, bundles, map[string]string{lifecycle.LabelPipeline: "app"})
	require.Len(t, steps, 4, "the live step and three rebuilt from records")
	m := pipeline.ComputeDeploymentMetrics(p, bundles, steps, t0.Add(4*time.Hour))
	require.NotNil(t, m)
	assert.Equal(t, 3, m.Deployments, "v1, broken and v4; v3 changed nothing")
	assert.Equal(t, 1, m.FailedDeployments, "the retired broken Bundle")
	assert.Equal(t, 333, m.ChangeFailureRateMillis)
	assert.Equal(t, 1, m.RestoredFailures, "v4 restored it")
	assert.Equal(t, int64(125), m.MeanTimeToRestoreMinutes, "broken deployed at +1h05, v4 Verified at +3h10")
}

// TestStepFromRetired_DeployedAt: a record keeps the health-check start and
// noChanges; a record written before those fields counts as deployed at its
// creation once it has a healthCheckExpiry, and as never deployed without.
func TestStepFromRetired_DeployedAt(t *testing.T) {
	b := bundleAt("v1", t0)
	created := metav1.NewTime(t0.Add(time.Minute))
	tests := []struct {
		name     string
		record   kardinalv1alpha1.RetiredStep
		deployed bool
		at       time.Time
		noop     bool
	}{
		{"recorded", kardinalv1alpha1.RetiredStep{Name: "s", CreatedAt: created, DeployedAt: mt(t0.Add(5 * time.Minute))}, true, t0.Add(5 * time.Minute), false},
		{"no-op", kardinalv1alpha1.RetiredStep{Name: "s", CreatedAt: created, DeployedAt: mt(t0.Add(5 * time.Minute)), NoChanges: true}, true, t0.Add(5 * time.Minute), true},
		{"older record, health check started", kardinalv1alpha1.RetiredStep{Name: "s", CreatedAt: created, HealthCheckExpiry: mt(t0.Add(time.Hour))}, true, t0.Add(time.Minute), false},
		{"older record, never deployed", kardinalv1alpha1.RetiredStep{Name: "s", CreatedAt: created}, false, time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := lifecycle.StepFromRetired(&b, tc.record)
			at, ok := lifecycle.DeployedAt(&s)
			assert.Equal(t, tc.deployed, ok)
			assert.True(t, tc.at.Equal(at), "deployed at %s, want %s", at, tc.at)
			assert.Equal(t, tc.noop, s.Status.Outputs["noChanges"] == "true")
			assert.Empty(t, s.Status.Steps, "no step sequence is invented")
		})
	}
	// The annotation counts only on a rebuilt step.
	live := deployedStep("v1", "Verified", t0, t0)
	live.Status.Steps = nil
	live.Annotations = map[string]string{"kardinal.io/retired-deployed-at": t0.Format(time.RFC3339)}
	_, ok := lifecycle.DeployedAt(&live)
	assert.False(t, ok, "a live step's annotation is not a deployment")
}

// TestComputeDeploymentMetrics_LeadTimeFromCommit (#1942): the lead time
// starts at spec.provenance.commitTime when CI set it, so the build counts
// (live: commit 03:44:26, Bundle 03:46:57, prod 03:49:01 reported 1 minute,
// not 4); without it, at the Bundle's creation. A rollback has no lead
// time: its commit is the restored build's, and it ships no change.
func TestComputeDeploymentMetrics_LeadTimeFromCommit(t *testing.T) {
	p := makePipelineWithEnvs("app", "default", "prod")
	commit := t0.Add(-150 * time.Second)
	withCommit := bundleAt("v1", t0)
	withCommit.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{CommitTime: mt(commit)}
	plain := bundleAt("v2", t0.Add(time.Hour))
	rb := bundleAt("rb", t0.Add(2*time.Hour))
	rb.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{RollbackOf: "v1", CommitTime: mt(commit)}
	steps := []kardinalv1alpha1.PromotionStep{
		*makeVerifiedStep("v1", "app", "prod", "default", t0.Add(2*time.Minute)),
		*makeVerifiedStep("v2", "app", "prod", "default", t0.Add(time.Hour+2*time.Minute)),
		*makeVerifiedStep("rb", "app", "prod", "default", t0.Add(2*time.Hour+2*time.Minute)),
	}
	m := pipeline.ComputeDeploymentMetrics(p, []kardinalv1alpha1.Bundle{withCommit, plain, rb}, steps, t0.Add(3*time.Hour))
	require.NotNil(t, m)
	assert.Equal(t, 1, m.LeadTimeFromCommit, "only v1 is measured from its commit")
	assert.Equal(t, int64(4), m.P50CommitToProdMinutes, "of v2 (2 minutes from creation) and v1; rb is left out")
	assert.Equal(t, int64(4), m.P90CommitToProdMinutes, "v1: 4.5 minutes from its commit")
}

// TestComputeDeploymentMetrics_RollbacksHaveNoLeadTime (#1985, QA): DORA's
// lead time for changes leaves rollbacks out. Three fast rollbacks among
// three releases pulled the p50 from 30 minutes down to 20. They still
// count in the rollback rates.
func TestComputeDeploymentMetrics_RollbacksHaveNoLeadTime(t *testing.T) {
	p := makePipelineWithEnvs("app", "default", "prod")
	var bundles []kardinalv1alpha1.Bundle
	var steps []kardinalv1alpha1.PromotionStep
	at := t0
	add := func(name, rollbackOf string, lead time.Duration) {
		b := bundleAt(name, at)
		if rollbackOf != "" {
			commit := t0.Add(-24 * time.Hour)
			b.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{RollbackOf: rollbackOf, CommitTime: mt(commit)}
		}
		bundles = append(bundles, b)
		steps = append(steps, *makeVerifiedStep(name, "app", "prod", "default", at.Add(lead)))
		at = at.Add(time.Hour)
	}
	add("v1", "", 20*time.Minute)
	add("rb1", "v1", time.Minute)
	add("v2", "", 30*time.Minute)
	add("rb2", "v2", time.Minute)
	add("v3", "", 40*time.Minute)
	add("rb3", "v3", time.Minute)
	m := pipeline.ComputeDeploymentMetrics(p, bundles, steps, at)
	require.NotNil(t, m)
	assert.Equal(t, 6, m.SampleSize)
	assert.Equal(t, int64(30), m.P50CommitToProdMinutes, "the median of v1, v2 and v3")
	assert.Equal(t, int64(40), m.P90CommitToProdMinutes)
	assert.Equal(t, 0, m.LeadTimeFromCommit, "a rollback's commit time is not counted")
	assert.Equal(t, 500, m.RollbackRateMillis, "rollbacks still count in the rollback rate")
}
