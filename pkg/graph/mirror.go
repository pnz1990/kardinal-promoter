// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Node IDs of the gate mirror (#1452). Uppercase first letter: no
// environment can take them (CELSafeSlug).
const (
	// NodeGateMirrorData lists the PromotionSteps whose gates are mirrored:
	// {steps: [{name, environment}]}.
	NodeGateMirrorData = "GateMirrorData"
	// NodeGateMirror is the patch collection that writes spec.live.gates on
	// each listed step.
	NodeGateMirror = "GateMirror"

	iterMirror = "Mirror"
)

// gateMirrorNodes mirror the live results of the gates of every pr-review
// environment onto its PromotionStep's spec.live.gates, with a patch
// collection over literal step names (DESIGN §7, verified E4).
//
// The step template cannot carry the live values: its spec.requiredGates
// holds it back on the gates, and once a gate turns false the whole step node
// is Unresolved and its spec freezes (ledger G14). A patch node does not
// depend on the step node resolving: it targets the step by its literal name,
// waits (soft not-ready) until the step exists, and re-renders whenever a
// gate instance changes. The step reconciler reads spec.live.gates from its
// own spec and posts the kardinal/gates commit status (G8: the SCM write stays
// in the reconciler).
//
// collections are the gate collection node IDs the Graph has. With no
// pr-review environment nothing is mirrored and nil is returned.
func gateMirrorNodes(steps []interface{}, collections []string) []GraphNode {
	if len(steps) == 0 {
		return nil
	}
	gates := "[]"
	if len(collections) > 0 {
		gates = "(" + strings.Join(collections, " + ") + ")"
	}
	live := fmt.Sprintf(`${%s.filter(g, g.metadata.?labels[?"kardinal.io/environment"].orValue("") == %s.environment)`+
		`.sortBy(g, g.metadata.name)`+
		`.map(g, {"name": g.metadata.name, "ready": g.?status.?ready.orValue(false), "reason": g.?status.?reason.orValue("")})}`,
		gates, iterMirror)
	return []GraphNode{
		{ID: NodeGateMirrorData, Def: map[string]interface{}{"steps": steps}},
		{
			ID:      NodeGateMirror,
			ForEach: []map[string]string{{iterMirror: fmt.Sprintf("${%s.steps}", NodeGateMirrorData)}},
			Patch: map[string]interface{}{
				"apiVersion": "kardinal.io/v1alpha1",
				"kind":       "PromotionStep",
				"metadata":   map[string]interface{}{"name": "${" + iterMirror + ".name}"},
				"spec":       map[string]interface{}{"live": map[string]interface{}{"gates": live}},
			},
		},
	}
}

// envApproval is the approval mode of environment env of pipeline, a fleet
// target included (its fleet's approval).
func envApproval(pipeline *kardinalv1alpha1.Pipeline, env string) string {
	return findEnvSpec(pipeline, env).Approval
}
