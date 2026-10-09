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

// LabelBundleUID is the label that holds the Bundle's UID: on a compact
// Graph's PromotionSteps (the Graph reads back only the steps that carry it)
// and on HookRuns (the HookRun reconciler and the mirror ignore one whose
// value is not its Bundle's UID, a HookRun someone created by hand).
const LabelBundleUID = "kardinal.io/bundle-uid"

// LabelGraphShape is the label on a Graph that shows its shape. It is
// informational: a re-translation keeps the shape the Graph's spec has
// (ShapeOf), and the builder writes the label again from it.
const LabelGraphShape = "kardinal.io/graph-shape"

// ShapeOf returns the shape of g from its spec: GraphShapeCompact when it has
// the NodePromotionSteps collection, GraphShapeNodes otherwise. Its
// LabelGraphShape label is not read: it is informational.
func ShapeOf(g *Graph) string {
	for _, n := range g.Spec.Nodes {
		if n.ID == NodePromotionSteps {
			return GraphShapeCompact
		}
	}
	return GraphShapeNodes
}

// compactShape reports whether the Graph for pipeline, promoting envs
// environments, uses the compact shape. pinned, the shape of the Bundle's
// existing Graph, wins over the threshold and the annotation.
func (b *Builder) compactShape(pipeline *kardinalv1alpha1.Pipeline, envs int, pinned string) (bool, error) {
	switch pinned {
	case GraphShapeCompact:
		return true, nil
	case GraphShapeNodes:
		return false, nil
	case "":
	default:
		return false, fmt.Errorf("build: unknown Graph shape %q", pinned)
	}
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

// compactUnsupported holds a check per feature the compact shape does not
// carry yet. Each returns the feature's name when the build input (the
// Pipeline, the Bundle, the PolicyGates, and whatever later inputs a feature
// adds to BuildInput: analyses, MetricCheck templates) uses it, and ""
// otherwise. A feature that adds Graph nodes or objects in the node shape
// (hooks, analyses, per-promotion MetricChecks, mirror patches) adds its
// check here until it has a compact implementation, so a compact Graph never
// silently drops it: the Bundle fails with GraphBuildFailed instead.
// TestCompact_SameObjectKinds fails for a feature that does neither.
//
// A check must handle a partial BuildInput: Bundle and PolicyGates may be
// nil (the Pipeline reconciler calls CompactUnsupported with the Pipeline
// alone, before any Bundle exists).
var compactUnsupported []func(BuildInput) string

// RegisterCompactUnsupported adds check to compactUnsupported and returns a
// function that removes it again (for tests). Not safe for concurrent use:
// register from package init or test setup.
func RegisterCompactUnsupported(check func(BuildInput) string) (unregister func()) {
	compactUnsupported = append(compactUnsupported, check)
	n := len(compactUnsupported) - 1
	return func() {
		compactUnsupported = append(compactUnsupported[:n:n], compactUnsupported[n+1:]...)
	}
}

// CompactUnsupported returns the features in in that the compact shape does
// not carry yet (compactUnsupported). The Pipeline reconciler reports them on
// a Pipeline whose new Bundles would get a compact Graph.
func CompactUnsupported(in BuildInput) []string {
	var features []string
	for _, check := range compactUnsupported {
		if f := check(in); f != "" {
			features = append(features, f)
		}
	}
	return features
}

// checkCompactSupport refuses a compact Graph for an input that uses a
// feature the compact shape does not carry yet (compactUnsupported). The
// message for a Bundle whose Graph is already compact (in.Shape) says that
// only new Bundles can get the node shape.
func checkCompactSupport(in BuildInput) error {
	features := CompactUnsupported(in)
	if len(features) == 0 {
		return nil
	}
	list := strings.Join(features, ", ")
	if in.Shape == GraphShapeCompact {
		return fmt.Errorf("build: this Bundle's Graph is compact, and a Bundle keeps the shape its Graph was "+
			"created with; the compact shape does not support %s yet. Remove the feature for this Bundle, or "+
			"create a new Bundle once the Pipeline uses the node shape (fewer environments than "+
			"--graph-compact-above, or the annotation %s: %s): only new Bundles can switch", list,
			AnnotationGraphShape, GraphShapeNodes)
	}
	return fmt.Errorf("build: this Bundle's Graph is compact (more environments than --graph-compact-above, or the %s "+
		"annotation), and the compact shape does not support %s yet; use the node shape for this Pipeline (fewer "+
		"environments, or the annotation %s: %s) or remove the feature",
		AnnotationGraphShape, list, AnnotationGraphShape, GraphShapeNodes)
}

// WouldBeCompact reports whether a new Bundle of pipeline promoting envs
// environments would get a compact Graph from b: the annotation, or more
// environments than b.CompactAbove. An invalid annotation reports false.
func (b *Builder) WouldBeCompact(pipeline *kardinalv1alpha1.Pipeline, envs int) bool {
	compact, err := b.compactShape(pipeline, envs, "")
	return err == nil && compact
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
			// The Pipeline holds the environment on another Bundle's
			// rollback (spec.holds, #1528; the node shape's heldCond): it
			// is not admitted. A hold change rebuilds the Graph in place.
			"held": heldBundle(pipeline, s.env) != "" && heldBundle(pipeline, s.env) != bundle.Name,
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
	// Superseded and the environment not held on another Bundle (held),
	// whose upstreams are Verified and gates ready. The PRStatuses
	// collection is not referenced: kro publishes a collection only when every
	// item applied (G11), and a step names its PRStatus literally and waits
	// for it in WaitingForMerge.
	wave := fmt.Sprintf("${%s.steps.filter(e, e.environment in %sstarted || "+
		"(%shold == false && e.held == false && e.upstreams.all(u, u in %sverified) && e.gates.all(g, g in %sreadyGates)))}",
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
					// Only the steps this Graph made count: the Bundle's UID
					// is in their labels, so a PromotionStep someone else
					// labelled cannot admit or verify an environment.
					"selector": map[string]interface{}{"matchLabels": map[string]interface{}{
						"kardinal.io/pipeline": pipeline.Name,
						"kardinal.io/bundle":   bundle.Name,
						LabelBundleUID:         string(bundle.UID),
						"kro.run/node-id":      NodePromotionSteps,
					}},
				},
			},
		},
		{ID: NodePromotionState, Def: map[string]interface{}{
			"started": fmt.Sprintf(`${%s.map(s, s.metadata.labels["kardinal.io/environment"])}`, NodeStepsObserved),
			"verified": fmt.Sprintf(`${%s.filter(s, s.?status.?state.orValue("") == "Verified").map(s, s.metadata.labels["kardinal.io/environment"])}`,
				NodeStepsObserved),
			"readyGates": "${" + readyGates + "}",
			// A Superseded or Rejected Bundle starts no new environment
			// (E2E-R20, #1451), nor does a Failed one waiting for a
			// maxConcurrentPromotions slot (WaitingForSlot, #1349; the node
			// shape's bundleHeld). The steps it has keep running or stay as
			// history.
			"hold": `${bundle.?status.?phase.orValue("") in ["Superseded", "Rejected"] || ` +
				`bundle.?status.?conditions.orValue([]).exists(c_, c_.type == "` + CondBundleWaitingForSlot +
				`" && c_.status == "True")}`,
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
						LabelBundleUID:            string(bundle.UID),
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

// Node IDs and the iterator of the compact shape's per-promotion MetricCheck
// instances (MetricChecks2, ... and so on for more than MaxCollectionItems).
const (
	// NodeMetricCheckData holds every instance's rendered object as data.
	NodeMetricCheckData = "MetricCheckData"
	// NodePromotionMetrics is the instances that may exist: those whose
	// environment's upstreams are all Verified, or whose environment has
	// started.
	NodePromotionMetrics = "PromotionMetrics"
	// NodeMetricChecks creates one MetricCheck per NodePromotionMetrics item.
	NodeMetricChecks = "MetricChecks"

	iterMetric = "Metric"
)

// compactMetric is one per-promotion MetricCheck instance of the compact
// shape.
type compactMetric struct {
	name, env, template string
	upstreams           []string // environment names
	spec                map[string]interface{}
}

// compactMetricNodes builds the compact shape's per-promotion MetricCheck
// instances: the rendered instances as data, a def that admits them, and a
// collection per MaxCollectionItems instances.
//
// The node shape holds an instance's query until every upstream PromotionStep
// is Verified (resolvableWhen, buildMetricCheckNode), so the analysis starts
// when the upstream deployment is done and before the environment's own step
// exists: its gates read the instance. A collection cannot hold one item
// (G11), so the admission is data instead: PromotionMetrics keeps an instance
// whose upstreams are all Verified (NodePromotionState.verified), or whose
// environment has started, so a started environment never loses its
// instance. spec.suspend, in the data, follows the Bundle's phase as in the
// node shape.
func compactMetricNodes(pipelineName, bundleName string, metrics []compactMetric) []GraphNode {
	if len(metrics) == 0 {
		return nil
	}
	data := map[string]interface{}{}
	admit := map[string]interface{}{}
	var collections []GraphNode
	state := NodePromotionState + "."
	for i := 0; i*MaxCollectionItems < len(metrics); i++ {
		chunk := metrics[i*MaxCollectionItems : min(len(metrics), (i+1)*MaxCollectionItems)]
		items := make([]interface{}, len(chunk))
		for j, m := range chunk {
			items[j] = map[string]interface{}{
				"name":        m.name,
				"environment": m.env,
				"template":    m.template,
				"upstreams":   toInterfaces(m.upstreams),
				"spec":        m.spec,
			}
		}
		field := chunkID("items", i)
		data[field] = items
		admit[field] = fmt.Sprintf("${%s.%s.filter(m, m.environment in %sstarted || m.upstreams.all(u, u in %sverified))}",
			NodeMetricCheckData, field, state, state)
		item := func(f string) string { return "${" + iterMetric + "." + f + "}" }
		collections = append(collections, GraphNode{
			ID:      chunkID(NodeMetricChecks, i),
			ForEach: []map[string]string{{iterMetric: fmt.Sprintf("${%s.%s}", NodePromotionMetrics, field)}},
			Template: map[string]interface{}{
				"apiVersion": "kardinal.io/v1alpha1",
				"kind":       "MetricCheck",
				"metadata": map[string]interface{}{
					"name": item("name"),
					"labels": map[string]interface{}{
						"kardinal.io/pipeline":    pipelineName,
						"kardinal.io/bundle":      bundleName,
						"kardinal.io/environment": item("environment"),
						LabelMetricTemplate:       item("template"),
					},
				},
				"spec": item("spec"),
			},
		})
	}
	return append([]GraphNode{
		{ID: NodeMetricCheckData, Def: data},
		{ID: NodePromotionMetrics, Def: admit},
	}, collections...)
}

func toInterfaces(s []string) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
