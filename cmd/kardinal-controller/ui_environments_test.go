// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// TestPipelineEnvironmentsMatchDeployed (P1): Pipeline status.environments
// (the PipelineReconciler's projection) names the same Bundle and version per
// environment as the UI API's deployed map, and a Verified environment's
// since is the UI's verifiedAt, so the CLI and the UI can read either.
//
// Covers PIPE-ENVSTATUS-02.
func TestPipelineEnvironmentsMatchDeployed(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	bundle := func(name, tag, phase string, at time.Time) v1alpha1.Bundle {
		return v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(at)},
			Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "app",
				Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/x/app", Tag: tag}}},
			Status: v1alpha1.BundleStatus{Phase: phase},
		}
	}
	step := func(b, env, state string, at time.Time) v1alpha1.PromotionStep {
		s := v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: "app-" + b + "-" + env, Namespace: "default", CreationTimestamp: metav1.NewTime(at)},
			Spec:       v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: b, Environment: env},
			Status:     v1alpha1.PromotionStepStatus{State: state},
		}
		if state == "Verified" {
			s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue,
				Reason: "Verified", LastTransitionTime: metav1.NewTime(at.Add(time.Minute))}}
		}
		return s
	}
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"}}
	for _, e := range []string{"dev", "test", "uat", "prod"} {
		p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{Name: e})
	}
	t1, t2 := t0.Add(time.Hour), t0.Add(2*time.Hour)
	bundles := []v1alpha1.Bundle{
		bundle("b1", "1.4.0", "Superseded", t0),
		bundle("b2", "1.5.0", "Failed", t1),
		bundle("b3", "1.6.0", "Promoting", t2),
	}
	steps := []v1alpha1.PromotionStep{
		step("b1", "dev", "Verified", t0), step("b1", "test", "Verified", t0),
		step("b1", "uat", "Verified", t0), step("b1", "prod", "Verified", t0),
		step("b2", "dev", "Verified", t1), step("b2", "test", "Verified", t1), step("b2", "uat", "Failed", t1),
		step("b3", "dev", "Verified", t2), step("b3", "test", "HealthChecking", t2),
	}

	envs := pipeline.ProjectEnvironments(p, bundles, steps, nil, nil, t2.Add(time.Hour))
	byName := make(map[string]*v1alpha1.Bundle, len(bundles))
	for i := range bundles {
		byName[bundles[i].Name] = &bundles[i]
	}
	idx := make([]int, len(steps))
	for i := range idx {
		idx[i] = i
	}
	deployed := deployedByEnv(p, stepsByEnv(steps, idx), byName)

	require.Len(t, envs, len(p.Spec.Environments))
	for _, e := range envs {
		d := deployed[e.Name]
		assert.Equal(t, d.Bundle, e.Bundle, e.Name)
		assert.Equal(t, d.Version, e.Version, e.Name)
		if e.State == "Verified" && e.IncomingBundle == "" {
			require.NotNil(t, e.Since, e.Name)
			assert.Equal(t, d.VerifiedAt, e.Since.UTC().Format(time.RFC3339), e.Name)
		}
	}
	// The fixture covers the cases that differ: the newest Bundle live (dev),
	// health checking over an older one (test), failed over an older one
	// (uat), and an older Bundle still running where nothing newer reached
	// (prod).
	assert.Equal(t, []string{"b3", "b3", "b1", "b1"}, []string{envs[0].Bundle, envs[1].Bundle, envs[2].Bundle, envs[3].Bundle})
	assert.Equal(t, "HealthChecking", envs[1].State)
	assert.Equal(t, "b2", envs[2].IncomingBundle)
}
