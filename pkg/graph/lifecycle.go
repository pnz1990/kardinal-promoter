// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"sort"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// PromotedEnvironments returns the environments that Build puts in the Graph
// for bundle, in promotion (topological) order. It applies the same ordering
// (dependsOn, waves, list order) and the same intent filter
// (targetEnvironment, skipEnvironments) as Build, so a caller that needs to
// know when a Bundle is finished checks exactly the environments its Graph
// promotes.
func PromotedEnvironments(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) ([]string, error) {
	if pipeline == nil || bundle == nil {
		return nil, fmt.Errorf("promoted environments: pipeline and bundle are required")
	}
	ordered, deps, err := resolveOrdering(pipeline)
	if err != nil {
		return nil, fmt.Errorf("promoted environments: %w", err)
	}
	envs, err := filterByIntent(ordered, deps, bundle)
	if err != nil {
		return nil, fmt.Errorf("promoted environments: %w", err)
	}
	return envs, nil
}

// EnvironmentUpstreams returns the environments envName depends on directly in
// the full Pipeline (explicit dependsOn, wave edges, or the previous list entry
// when neither is set), sorted by name. A root environment returns an empty
// slice. It returns an error when the Pipeline ordering is invalid or envName
// is not an environment of the Pipeline.
func EnvironmentUpstreams(pipeline *kardinalv1alpha1.Pipeline, envName string) ([]string, error) {
	if pipeline == nil {
		return nil, fmt.Errorf("environment upstreams: pipeline is required")
	}
	ordered, deps, err := resolveOrdering(pipeline)
	if err != nil {
		return nil, fmt.Errorf("environment upstreams: %w", err)
	}
	if !containsStr(ordered, envName) {
		return nil, fmt.Errorf("environment upstreams: pipeline %s has no environment %q", pipeline.Name, envName)
	}
	ups := append([]string{}, deps[envName]...)
	sort.Strings(ups)
	return ups, nil
}

// SinkEnvironments returns the environments no other environment depends on
// (explicit dependsOn, wave edges, or list order), in promotion order. A
// linear pipeline returns its last environment; a fan-out returns every leaf.
func SinkEnvironments(pipeline *kardinalv1alpha1.Pipeline) ([]string, error) {
	if pipeline == nil {
		return nil, fmt.Errorf("sink environments: pipeline is required")
	}
	ordered, deps, err := resolveOrdering(pipeline)
	if err != nil {
		return nil, fmt.Errorf("sink environments: %w", err)
	}
	hasDependent := make(map[string]bool, len(ordered))
	for _, ups := range deps {
		for _, up := range ups {
			hasDependent[up] = true
		}
	}
	var sinks []string
	for _, env := range ordered {
		if !hasDependent[env] {
			sinks = append(sinks, env)
		}
	}
	return sinks, nil
}
