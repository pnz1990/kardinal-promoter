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
