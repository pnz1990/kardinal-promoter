// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"fmt"
	"slices"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// UnpinnedArtifacts lists b's artifacts that are not pinned by content:
// images without a digest, and a chart without one. A tag can be pushed
// again with other bytes, so a verification of the same tag may not be of
// these artifacts, and a targetOnly Bundle with any of them is promoted
// through its upstream environments (#1311, #2122). A config commit is
// content-addressed.
func UnpinnedArtifacts(b *v1alpha1.Bundle) []string {
	var out []string
	for _, img := range b.Spec.Images {
		if img.Digest == "" {
			out = append(out, img.Repository+":"+img.Tag)
		}
	}
	if c := b.Spec.Chart; c != nil && c.Version != "" && c.Digest == "" {
		out = append(out, "chart "+c.Name+" "+c.Version)
	}
	return out
}

// UpstreamOfTarget lists, in promotion order, the environments b's Graph
// would promote before its target without upstream evidence: the path to
// intent.targetEnvironment, without the target (for a fleet, its targets).
// A fleet target's sibling targets are not on its path.
func UpstreamOfTarget(pipeline *v1alpha1.Pipeline, b *v1alpha1.Bundle) ([]string, error) {
	full := b.DeepCopy()
	full.Status.UpstreamEvidence = nil
	path, err := graph.PromotedEnvironments(pipeline, full)
	if err != nil {
		return nil, fmt.Errorf("upstream evidence: %w", err)
	}
	target := b.Spec.Intent.TargetEnvironment
	targets := []string{target}
	if byFleet, err := graph.FleetTargetEnvironments(pipeline); err == nil && len(byFleet[target]) > 0 {
		targets = byFleet[target]
	}
	return slices.DeleteFunc(path, func(e string) bool { return slices.Contains(targets, e) }), nil
}
