// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// DirectUpstreams returns the environments that envName directly depends on in
// the promotion DAG that Build generates for bundle. It uses the same ordering
// (dependsOn, waves, list order) and the same intent filter (targetEnvironment,
// skipEnvironments) as Build: a skipped environment is bridged to its own
// upstreams, exactly as the Graph does. A root environment returns nil.
//
// It returns an error when the Pipeline ordering is invalid or when envName is
// not part of the bundle's promotion.
func DirectUpstreams(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	envName string) ([]string, error) {
	if pipeline == nil || bundle == nil {
		return nil, fmt.Errorf("direct upstreams: pipeline and bundle are required")
	}
	ordered, deps, err := resolveOrdering(pipeline)
	if err != nil {
		return nil, fmt.Errorf("direct upstreams: %w", err)
	}
	filtered, err := filterByIntent(ordered, deps, bundle)
	if err != nil {
		return nil, fmt.Errorf("direct upstreams: %w", err)
	}
	filteredSet := make(map[string]bool, len(filtered))
	for _, e := range filtered {
		filteredSet[e] = true
	}
	if !filteredSet[envName] {
		return nil, fmt.Errorf("direct upstreams: environment %q is not promoted by bundle %s", envName, bundle.Name)
	}
	return filteredDeps(envName, deps, filteredSet), nil
}

// UpstreamsVerified reports whether the Graph Build generates for bundle has
// released envName: every direct upstream environment (DirectUpstreams) has
// the bundle's PromotionStep Verified. That is the upstream half of the gate
// on envName's PromotionStep (verifiedCond); for a multi-region upstream every
// region's step must exist and be Verified. A root environment is released.
//
// steps may hold PromotionSteps of any bundle; only those with
// spec.bundleName == bundle.Name count. It returns false when envName is not
// part of the bundle's promotion or the Pipeline ordering is invalid.
func UpstreamsVerified(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	envName string, steps []kardinalv1alpha1.PromotionStep) bool {
	ups, err := DirectUpstreams(pipeline, bundle, envName)
	if err != nil {
		return false
	}
	regions := make(map[string]int, len(pipeline.Spec.Environments))
	for _, e := range pipeline.Spec.Environments {
		regions[e.Name] = len(e.Regions)
	}
	for _, up := range ups {
		want := regions[up]
		if want < 2 {
			want = 1
		}
		verified := 0
		for i := range steps {
			s := &steps[i]
			if s.Spec.BundleName != bundle.Name || s.Spec.Environment != up {
				continue
			}
			if s.Status.State != "Verified" {
				return false
			}
			verified++
		}
		if verified < want {
			return false
		}
	}
	return true
}
