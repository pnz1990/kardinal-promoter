// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// EnvironmentDependencies returns the pipeline's environments in topological
// order and, for each environment, the environments it waits for. It applies
// the same rules as the Graph builder (wave edges, explicit dependsOn, else
// the previous environment in the list), so callers that draw the pipeline,
// such as the UI graph, match what the Graph executes.
func EnvironmentDependencies(pipeline *kardinalv1alpha1.Pipeline) ([]string, map[string][]string, error) {
	return resolveOrdering(pipeline)
}
