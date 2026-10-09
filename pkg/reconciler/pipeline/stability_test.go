// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

var t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func mt(t time.Time) *metav1.Time { m := metav1.NewTime(t); return &m }

// deployedStep is a prod step of bundle whose change reached prod at
// deployed (its health-check step started) and that ended in state at end.
func deployedStep(bundle, state string, deployed, end time.Time) kardinalv1alpha1.PromotionStep {
	s := *makeVerifiedStep(bundle, "app", "prod", "default", end)
	s.Name = bundle + "-prod-" + state
	s.Status.State = state
	hc := kardinalv1alpha1.StepStatus{Name: "health-check", State: kardinalv1alpha1.StepExecutionCompleted,
		StartedAt: mt(deployed), CompletedAt: mt(end)}
	if state != "Verified" {
		s.Status.Conditions = nil
		hc.State = kardinalv1alpha1.StepExecutionFailed
	}
	s.Status.Steps = []kardinalv1alpha1.StepStatus{
		{Name: "git-clone", State: kardinalv1alpha1.StepExecutionCompleted, StartedAt: mt(deployed.Add(-time.Minute)), CompletedAt: mt(deployed)},
		hc,
	}
	return s
}

// undeployedStep failed before its change reached prod (git-push failed).
func undeployedStep(bundle string, at time.Time) kardinalv1alpha1.PromotionStep {
	s := *makeVerifiedStep(bundle, "app", "prod", "default", at)
	s.Status.State, s.Status.Conditions = "Failed", nil
	s.Status.Steps = []kardinalv1alpha1.StepStatus{
		{Name: "git-push", State: kardinalv1alpha1.StepExecutionFailed, StartedAt: mt(at), CompletedAt: mt(at)},
		{Name: "health-check", State: kardinalv1alpha1.StepExecutionPending},
	}
	return s
}

func bundleAt(name string, created time.Time) kardinalv1alpha1.Bundle {
	return *makeVerifiedBundle(name, "default", "app", created)
}

func rollbackBundle(name, from, env string, created time.Time) kardinalv1alpha1.Bundle {
	b := bundleAt(name, created)
	b.Labels = map[string]string{"kardinal.io/rollback": "true"}
	b.Annotations = map[string]string{"kardinal.io/rollback-from": from}
	b.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{RollbackOf: "x"}
	b.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: env}
	return b
}

func TestComputeDeploymentMetrics_ChangeFailureRateAndMTTR(t *testing.T) {
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	tests := []struct {
		name                               string
		bundles                            []kardinalv1alpha1.Bundle
		steps                              []kardinalv1alpha1.PromotionStep
		deployments, failed, cfr, restored int
		mttr                               int64
	}{
		{
			name:    "all healthy",
			bundles: []kardinalv1alpha1.Bundle{bundleAt("v1", t0), bundleAt("v2", t0.Add(time.Hour))},
			steps: []kardinalv1alpha1.PromotionStep{
				deployedStep("v1", "Verified", t0.Add(5*time.Minute), t0.Add(10*time.Minute)),
				deployedStep("v2", "Verified", t0.Add(65*time.Minute), t0.Add(70*time.Minute)),
			},
			deployments: 2,
		},
		{
			name: "a health failure restored by the next Verified Bundle 40 minutes later",
			bundles: []kardinalv1alpha1.Bundle{bundleAt("v1", t0), bundleAt("v2", t0.Add(time.Hour)),
				bundleAt("v3", t0.Add(2*time.Hour))},
			steps: []kardinalv1alpha1.PromotionStep{
				deployedStep("v1", "Verified", t0.Add(5*time.Minute), t0.Add(10*time.Minute)),
				deployedStep("v2", "Failed", t0.Add(65*time.Minute), t0.Add(80*time.Minute)),
				deployedStep("v3", "Verified", t0.Add(115*time.Minute), t0.Add(120*time.Minute)),
			},
			deployments: 3, failed: 1, cfr: 333, restored: 1, mttr: 40,
		},
		{
			name: "AbortedByAlarm and RollingBack count as failures; an unrestored failure has no restore time",
			bundles: []kardinalv1alpha1.Bundle{bundleAt("v0", t0.Add(-time.Hour)), bundleAt("v1", t0),
				bundleAt("v2", t0.Add(time.Hour))},
			steps: []kardinalv1alpha1.PromotionStep{
				deployedStep("v0", "Verified", t0.Add(-55*time.Minute), t0.Add(-50*time.Minute)),
				deployedStep("v1", "AbortedByAlarm", t0.Add(5*time.Minute), t0.Add(10*time.Minute)),
				deployedStep("v2", "RollingBack", t0.Add(65*time.Minute), t0.Add(70*time.Minute)),
			},
			deployments: 3, failed: 2, cfr: 666,
		},
		{
			name:    "a failure before the change reached prod is not a deployment",
			bundles: []kardinalv1alpha1.Bundle{bundleAt("v1", t0), bundleAt("v2", t0.Add(time.Hour))},
			steps: []kardinalv1alpha1.PromotionStep{
				deployedStep("v1", "Verified", t0.Add(5*time.Minute), t0.Add(10*time.Minute)),
				undeployedStep("v2", t0.Add(65*time.Minute)),
			},
			deployments: 1,
		},
		{
			name: "a Verified Bundle rolled back later failed; the rollback restores it",
			bundles: []kardinalv1alpha1.Bundle{bundleAt("v1", t0), bundleAt("v2", t0.Add(time.Hour)),
				rollbackBundle("rb", "v2", "prod", t0.Add(3*time.Hour)),
				// A rollback of another environment does not count.
				rollbackBundle("rb-test", "v1", "test", t0.Add(4*time.Hour))},
			steps: []kardinalv1alpha1.PromotionStep{
				deployedStep("v1", "Verified", t0.Add(5*time.Minute), t0.Add(10*time.Minute)),
				deployedStep("v2", "Verified", t0.Add(65*time.Minute), t0.Add(70*time.Minute)),
				deployedStep("rb", "Verified", t0.Add(3*time.Hour+5*time.Minute), t0.Add(3*time.Hour+20*time.Minute)),
			},
			deployments: 3, failed: 1, cfr: 333, restored: 1, mttr: 20,
		},
		{
			name:    "multi-region prod counts once per Bundle, failed when one region fails",
			bundles: []kardinalv1alpha1.Bundle{bundleAt("v1", t0), bundleAt("v2", t0.Add(time.Hour))},
			steps: func() []kardinalv1alpha1.PromotionStep {
				eu := deployedStep("v1", "Verified", t0.Add(5*time.Minute), t0.Add(10*time.Minute))
				us := deployedStep("v1", "Failed", t0.Add(6*time.Minute), t0.Add(30*time.Minute))
				us.Name += "-us"
				return []kardinalv1alpha1.PromotionStep{eu, us,
					deployedStep("v2", "Verified", t0.Add(65*time.Minute), t0.Add(90*time.Minute))}
			}(),
			deployments: 2, failed: 1, cfr: 500, restored: 1, mttr: 60,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pipeline.ComputeDeploymentMetrics(p, tt.bundles, tt.steps, t0.Add(24*time.Hour))
			require.NotNil(t, m)
			assert.Equal(t, tt.deployments, m.Deployments, "deployments")
			assert.Equal(t, tt.failed, m.FailedDeployments, "failed")
			assert.Equal(t, tt.cfr, m.ChangeFailureRateMillis, "change failure rate")
			assert.Equal(t, tt.restored, m.RestoredFailures, "restored")
			assert.Equal(t, tt.mttr, m.MeanTimeToRestoreMinutes, "time to restore")
		})
	}
}

// TestComputeDeploymentMetrics_StabilitySampleIsTheLast30Deployments: only
// the 30 newest deployments count, so an old failure drops out.
func TestComputeDeploymentMetrics_StabilitySampleIsTheLast30Deployments(t *testing.T) {
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	var bundles []kardinalv1alpha1.Bundle
	var steps []kardinalv1alpha1.PromotionStep
	for i := 0; i < 35; i++ {
		name := fmt.Sprintf("v%02d", i)
		at := t0.Add(time.Duration(i) * time.Hour)
		bundles = append(bundles, bundleAt(name, at))
		state := "Verified"
		if i == 0 {
			state = "Failed"
		}
		steps = append(steps, deployedStep(name, state, at.Add(5*time.Minute), at.Add(10*time.Minute)))
	}
	m := pipeline.ComputeDeploymentMetrics(p, bundles, steps, t0.Add(48*time.Hour))
	require.NotNil(t, m)
	assert.Equal(t, 30, m.Deployments)
	assert.Zero(t, m.FailedDeployments, "the failure is older than the sample")
	assert.Zero(t, m.ChangeFailureRateMillis)
}
