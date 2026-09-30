// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// DeployedBundle returns the Bundle whose change landed last in env of
// pipeline, judged from steps: the Bundle of the newest PromotionStep there
// that is past its merge (HealthChecking, Verified, AbortedByAlarm,
// RollingBack, or Failed after its health check started). It is the Bundle a
// rollback rolls back from. It returns "" when no change has landed there.
// Steps of other pipelines or environments are ignored, so callers can pass
// every step of the namespace.
func DeployedBundle(steps []v1alpha1.PromotionStep, pipeline, env string) string {
	return historyOf(steps, pipeline, env).deployed()
}

// historyOf groups the steps of one pipeline environment by Bundle.
func historyOf(steps []v1alpha1.PromotionStep, pipeline, env string) *envHistory {
	h := &envHistory{byBundle: map[string][]v1alpha1.PromotionStep{}}
	for _, s := range steps {
		if s.Spec.PipelineName != pipeline || s.Spec.Environment != env || s.Spec.BundleName == "" {
			continue
		}
		h.byBundle[s.Spec.BundleName] = append(h.byBundle[s.Spec.BundleName], s)
	}
	return h
}
