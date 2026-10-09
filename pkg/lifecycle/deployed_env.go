// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// DeployedEnv is what runs in one environment. Bundle is the Bundle whose
// change landed there last (DeployedBundle). Image and config Bundles do not
// supersede each other, so when Bundle is an image Bundle, ConfigFrom is the
// last Bundle that deployed a config commit there, and when it is a config
// Bundle, ImagesFrom is the last one that deployed images (#1353). Each is ""
// when there is none, or when Bundle is gone and its type is not known.
type DeployedEnv struct {
	Bundle     string
	ConfigFrom string
	ImagesFrom string
}

// DeployedIn returns the DeployedEnv of env in pipeline. byName holds the
// pipeline's Bundles by name: their types say what each one deployed. kardinal
// status and explain, and the UI fleet board, report the same.
func DeployedIn(steps []v1alpha1.PromotionStep, pipeline, env string, byName map[string]*v1alpha1.Bundle) DeployedEnv {
	ptrs := make([]*v1alpha1.PromotionStep, len(steps))
	for i := range steps {
		ptrs[i] = &steps[i]
	}
	return DeployedInSteps(ptrs, pipeline, env, byName)
}

// DeployedInSteps is DeployedIn over step pointers, for callers that index
// many steps and must not copy them (the UI pipeline list).
func DeployedInSteps(steps []*v1alpha1.PromotionStep, pipeline, env string, byName map[string]*v1alpha1.Bundle) DeployedEnv {
	deploys := func(want func(*v1alpha1.Bundle) bool) func(string) bool {
		return func(name string) bool { b := byName[name]; return b != nil && want(b) }
	}
	d := DeployedEnv{Bundle: newestLanded(steps, pipeline, env, nil)}
	if b := byName[d.Bundle]; b != nil {
		switch {
		case !DeploysConfig(b):
			d.ConfigFrom = newestLanded(steps, pipeline, env, deploys(DeploysConfig))
		case !DeploysImages(b):
			d.ImagesFrom = newestLanded(steps, pipeline, env, deploys(DeploysImages))
		}
	}
	return d
}

// newestLanded is the Bundle of the newest step of env in pipeline whose
// change landed (stepLanded), among the Bundles match accepts (all when nil):
// what DeployedBundle and DeployedBundleMatching return, without grouping.
func newestLanded(steps []*v1alpha1.PromotionStep, pipeline, env string, match func(string) bool) string {
	var newest *v1alpha1.PromotionStep
	for _, s := range steps {
		if s.Spec.PipelineName != pipeline || s.Spec.Environment != env || s.Spec.BundleName == "" {
			continue
		}
		if !stepLanded(s) || (match != nil && !match(s.Spec.BundleName)) {
			continue
		}
		if newest == nil || newerStep(s, newest) {
			newest = s
		}
	}
	if newest == nil {
		return ""
	}
	return newest.Spec.BundleName
}

// DeploysConfig reports whether b deploys a config commit: config and mixed
// Bundles do.
func DeploysConfig(b *v1alpha1.Bundle) bool {
	return (b.Spec.Type == "config" || b.Spec.Type == "mixed") && b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != ""
}

// DeploysImages reports whether b deploys images: every Bundle but a config
// Bundle does.
func DeploysImages(b *v1alpha1.Bundle) bool {
	return b.Spec.Type != "config" && len(b.Spec.Images) > 0
}
