// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// DeployedBundle returns the Bundle whose change landed last in env of
// pipeline, judged from steps: the Bundle of the newest PromotionStep there
// that is past its merge (HealthChecking, Verifying, Verified, AbortedByAlarm,
// RollingBack, or Failed after its health check started). It is the Bundle a
// rollback rolls back from. It returns "" when no change has landed there.
// Steps of other pipelines or environments are ignored, so callers can pass
// every step of the namespace.
func DeployedBundle(steps []v1alpha1.PromotionStep, pipeline, env string) string {
	return historyOf(steps, pipeline, env).deployed()
}

// DeployedBundleMatching is DeployedBundle restricted to the Bundles match
// accepts: the Bundle of the newest landed step in env of pipeline whose
// Bundle name match returns true for, or "". Image and config Bundles do not
// supersede each other, so an environment runs the images of the last Bundle
// that deployed images and the config commit of the last Bundle that deployed
// one; callers find the second with this (#1353).
func DeployedBundleMatching(steps []v1alpha1.PromotionStep, pipeline, env string, match func(bundle string) bool) string {
	h := historyOf(steps, pipeline, env)
	for name := range h.byBundle {
		if !match(name) {
			delete(h.byBundle, name)
		}
	}
	return h.deployed()
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
