// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

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
