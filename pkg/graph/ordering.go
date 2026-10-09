// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// EnvironmentOrder returns the pipeline's environments in the topological
// order the Graph builder uses. Together with EnvironmentDependencies it lets
// callers that draw the pipeline, such as the UI graph, match what the Graph
// executes.
func EnvironmentOrder(pipeline *kardinalv1alpha1.Pipeline) ([]string, error) {
	order, _, err := resolveOrdering(pipeline)
	return order, err
}

// Ordering is EnvironmentOrder and EnvironmentDependencies from one
// resolution of the pipeline's ordering, for a caller that needs both (the
// CLI's pipeline table, once per Pipeline).
func Ordering(pipeline *kardinalv1alpha1.Pipeline) (order []string, deps map[string][]string, err error) {
	return resolveOrdering(pipeline)
}
