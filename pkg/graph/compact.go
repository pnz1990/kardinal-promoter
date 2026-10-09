// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// AnnotationGraphShape on a Pipeline chooses the Graph shape of its Bundles:
// GraphShapeNodes, GraphShapeCompact, or empty for the default (compact above
// CompactAbove environments).
const AnnotationGraphShape = "kardinal.io/graph-shape"

const (
	// GraphShapeNodes builds one PromotionStep node per environment.
	GraphShapeNodes = "nodes"
	// GraphShapeCompact builds every PromotionStep from one collection node,
	// admitted by a def node over the promotion DAG (see compactNodes).
	GraphShapeCompact = "compact"
)

// DefaultCompactAbove is the environment count above which a Bundle's Graph
// uses the compact shape when the Pipeline does not choose one. Up to 100
// environments (the Pipeline limit before v0.10.0) the shape is unchanged.
const DefaultCompactAbove = 100

// Node IDs and the iterator of the compact shape. Like the other collection
// IDs they start with an uppercase letter, which no environment gets.
const (
	// NodePromotionDAG holds the promotion DAG as data: one entry per
	// environment with its step name, PRStatus, upstreams and gates.
	NodePromotionDAG = "PromotionDAG"
	// NodeStepsObserved reads the Bundle's PromotionSteps back by label.
	NodeStepsObserved = "StepsObserved"
	// NodePromotionState is what the observed steps and gates say: the
	// environments started and Verified, and the gate instances ready.
	NodePromotionState = "PromotionState"
	// NodePromotionWave is the DAG entries whose PromotionStep may exist.
	NodePromotionWave = "PromotionWave"
	// NodePromotionSteps creates one PromotionStep per NodePromotionWave entry.
	NodePromotionSteps = "PromotionSteps"
	// NodePromotionProgress is ready once every environment is Verified.
	NodePromotionProgress = "PromotionProgress"

	iterStep = "Step"
)

// compactShape reports whether the Graph for pipeline, promoting envs
// environments, uses the compact shape.
func (b *Builder) compactShape(pipeline *kardinalv1alpha1.Pipeline, envs int) (bool, error) {
	switch v := pipeline.Annotations[AnnotationGraphShape]; v {
	case GraphShapeCompact:
		return true, nil
	case GraphShapeNodes:
		return false, nil
	case "":
		return envs > b.CompactAbove, nil
	default:
		return false, fmt.Errorf("build: Pipeline annotation %s=%q: use %q, %q or remove it",
			AnnotationGraphShape, v, GraphShapeCompact, GraphShapeNodes)
	}
}

// compactStep is one environment of the compact shape's promotion DAG.
type compactStep struct {
	env, name, prStatus string
	upstreams           []string // environment names
	gates               []string // gate instance names
}

// compactNodes builds the compact shape's PromotionStep nodes: the DAG as data
// and one collection that creates the PromotionSteps the DAG admits.
//
// A collection is all-or-nothing on pending data (ledger gap G11), so the
// steps are not held back field by field as in the node shape. Instead
// NodePromotionWave admits an environment once every upstream is Verified,
// every gate is ready and the Bundle is not Superseded, reading the steps
// back through NodeStepsObserved (a selector ref: no CEL edge to the
// collection, so no cycle). An environment whose step exists stays admitted,
// so a later change (a gate turning false, a Superseded Bundle) never prunes
// a step. Verified on kind (docs/design/16-graph-capability-ledger.md, G11).
//
// The Graph's Ready condition is held by NodePromotionProgress until every
// environment is Verified: a collection with only the first steps in it is
// otherwise ready as soon as they are.
func compactNodes(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	steps []compactStep, gateCollections []string) []GraphNode {
	entries := make([]interface{}, len(steps))
	for i, s := range steps {
		upstreamStates := make([]interface{}, len(s.upstreams))
		for j := range upstreamStates {
			upstreamStates[j] = "Verified"
		}
		entries[i] = map[string]interface{}{
			"environment":    s.env,
			"name":           s.name,
			"prStatus":       s.prStatus,
			"upstreams":      toInterfaces(s.upstreams),
			"upstreamStates": upstreamStates,
			"gates":          toInterfaces(s.gates),
		}
	}

	readyGates := "[]"
	if len(gateCollections) > 0 {
		parts := make([]string, len(gateCollections))
		for i, id := range gateCollections {
			parts[i] = fmt.Sprintf("%s.filter(g, g.?status.?ready.orValue(false) == true).map(g, g.metadata.name)", id)
		}
		readyGates = strings.Join(parts, " + ")
	}

	state := NodePromotionState + "."
	// Admit an environment that has a step, or, while the Bundle is not
	// Superseded, whose upstreams are Verified and gates ready. The PRStatuses
	// collection is not referenced: kro publishes a collection only when every
	// item applied (G11), and a step names its PRStatus literally and waits
	// for it in WaitingForMerge.
	wave := fmt.Sprintf("${%s.steps.filter(e, e.environment in %sstarted || "+
		"(%shold == false && e.upstreams.all(u, u in %sverified) && e.gates.all(g, g in %sreadyGates)))}",
		NodePromotionDAG, state, state, state, state)

	step := func(f string) string { return "${" + iterStep + "." + f + "}" }
	return []GraphNode{
		{ID: NodePromotionDAG, Def: map[string]interface{}{"steps": entries}},
		{
			ID: NodeStepsObserved,
			Ref: map[string]interface{}{
				"apiVersion": "kardinal.io/v1alpha1",
				"kind":       "PromotionStep",
				"metadata": map[string]interface{}{
					"namespace": bundle.Namespace,
					"selector": map[string]interface{}{"matchLabels": map[string]interface{}{
						"kardinal.io/pipeline": pipeline.Name,
						"kardinal.io/bundle":   bundle.Name,
					}},
				},
			},
		},
		{ID: NodePromotionState, Def: map[string]interface{}{
			"started": fmt.Sprintf(`${%s.map(s, s.metadata.labels["kardinal.io/environment"])}`, NodeStepsObserved),
			"verified": fmt.Sprintf(`${%s.filter(s, s.?status.?state.orValue("") == "Verified").map(s, s.metadata.labels["kardinal.io/environment"])}`,
				NodeStepsObserved),
			"readyGates": "${" + readyGates + "}",
			// A Superseded Bundle starts no new environment (E2E-R20); the
			// steps it has keep running or stay as history.
			"hold": `${bundle.?status.?phase.orValue("") == "Superseded"}`,
		}},
		{ID: NodePromotionWave, Def: map[string]interface{}{"steps": wave}},
		{
			ID:      NodePromotionSteps,
			ForEach: []map[string]string{{iterStep: "${" + NodePromotionWave + ".steps}"}},
			Template: map[string]interface{}{
				"apiVersion": "kardinal.io/v1alpha1",
				"kind":       "PromotionStep",
				"metadata": map[string]interface{}{
					"name": step("name"),
					"labels": map[string]interface{}{
						"kardinal.io/pipeline":    pipeline.Name,
						"kardinal.io/bundle":      bundle.Name,
						"kardinal.io/environment": step("environment"),
					},
				},
				"spec": map[string]interface{}{
					"pipelineName":   pipeline.Name,
					"bundleName":     bundle.Name,
					"environment":    step("environment"),
					"stepType":       defaultStepType(bundle.Spec.Type),
					"prStatusRef":    step("prStatus"),
					"upstreamStates": step("upstreamStates"),
					"requiredGates":  step("gates"),
				},
			},
			ReadyWhen: []string{`${each.?status.?state.orValue("") == "Verified"}`},
		},
		{
			ID: NodePromotionProgress,
			Def: map[string]interface{}{
				"verified": fmt.Sprintf("${size(%sverified)}", state),
				"total":    len(steps),
			},
			ReadyWhen: []string{fmt.Sprintf("${%s.verified == %s.total}", NodePromotionProgress, NodePromotionProgress)},
		},
	}
}

func toInterfaces(s []string) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
