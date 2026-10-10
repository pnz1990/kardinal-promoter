// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"strconv"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Rendered manifests (layout: branch) in the compact shape.
//
// The node shape gives each layout: branch environment a RenderRun node whose
// name resolves once the step asked for its render, and mirrors the
// RenderRun's status onto the step through a patch node. The compact shape
// has no step node per environment, so the RenderRuns follow the pattern of
// its AnalysisRuns (compact_extras.go): every RenderRun is an item of data
// (NodeRenderRunData), a def admits the items whose step asked for its render
// (status.renderRequestedAt, read back through NodeStepsObserved) while the
// Bundle is not Superseded, and a collection per MaxCollectionItems creates
// them. An item stays admitted once its RenderRun exists (read back through
// refRenderRuns), so a later change never prunes a render in flight (G11).
//
// Only a step the Graph created asks for a render, so in a Pipeline with
// fleets pacing decides which targets render: no RenderRun starts for a
// target pacing holds back. The step reads the result through its own
// template: the PromotionSteps collection renders spec.live.renders from
// refRenderRuns, filtered to the item's RenderRun (renderRun).

// Node IDs and the iterator of the compact shape's RenderRuns.
const (
	// NodeRenderRunData holds every RenderRun as data.
	NodeRenderRunData = "RenderRunData"
	// NodePromotionRenders is the RenderRuns that may exist.
	NodePromotionRenders = "PromotionRenders"
	// NodeRenderRuns creates one RenderRun per NodePromotionRenders item.
	NodeRenderRuns = "RenderRuns"
	// NodeRenderState holds what the admission reads back: the RenderRuns
	// that exist and the steps that asked for their render.
	NodeRenderState = "RenderState"

	iterRender = "Render"
)

// compactRender is one RenderRun of the compact shape.
type compactRender struct {
	name, env, step string
	spec            map[string]interface{}
}

// buildCompactRender returns env's RenderRun in the compact shape, or nil
// when env does not render to a branch.
func buildCompactRender(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	env kardinalv1alpha1.EnvironmentSpec, stepK8sName string) (*compactRender, error) {
	if !kardinalv1alpha1.RendersToBranch(pipeline.Spec, env) {
		return nil, nil
	}
	spec, err := renderRunSpec(pipeline, bundle, env)
	if err != nil {
		return nil, err
	}
	if _, ok := spec["render"]; !ok {
		spec["render"] = map[string]interface{}{} // the same as unset; every item has the field
	}
	return &compactRender{name: RenderRunName(pipeline.Name, bundle.Name, env.Name), env: env.Name,
		step: stepK8sName, spec: spec}, nil
}

// compactRenderNodes builds the RenderRun data, admission, read-back state
// and collections.
func compactRenderNodes(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle, renders []compactRender) []GraphNode {
	if len(renders) == 0 {
		return nil
	}
	// Only RenderRuns kro applied for this Bundle keep an item admitted.
	applied := fmt.Sprintf(`r.metadata.?labels[?%q].hasValue() && r.metadata.?labels[?%q].orValue("") == %s`,
		LabelKRONodeID, LabelBundleUID, strconv.Quote(string(bundle.UID)))
	state := map[string]interface{}{
		"renderRuns": fmt.Sprintf(`${%s.filter(r, %s).map(r, r.metadata.name)}`, refRenderRunsNodeID, applied),
		"requested": fmt.Sprintf(`${%s.filter(s, s.?status.?renderRequestedAt.hasValue()).map(s, s.metadata.name)}`,
			NodeStepsObserved),
		"superseded": `${bundle.?status.?phase.orValue("") == "Superseded"}`,
	}
	rs := NodeRenderState + "."
	data := map[string]interface{}{}
	admit := map[string]interface{}{}
	var collections []GraphNode
	for i := 0; i*MaxCollectionItems < len(renders); i++ {
		chunk := renders[i*MaxCollectionItems : min(len(renders), (i+1)*MaxCollectionItems)]
		items := make([]interface{}, len(chunk))
		for j, r := range chunk {
			items[j] = map[string]interface{}{"name": r.name, "environment": r.env, "step": r.step, "spec": r.spec}
		}
		field := chunkID("items", i)
		data[field] = items
		admit[field] = fmt.Sprintf(`${%[1]s.%[2]s.filter(r, r.name in %[3]srenderRuns || `+
			`(r.step in %[3]srequested && %[3]ssuperseded == false))}`, NodeRenderRunData, field, rs)
		item := func(f string) string { return "${" + iterRender + "." + f + "}" }
		git := map[string]interface{}{
			"url":            item("spec.git.url"),
			"sourceBranch":   item("spec.git.sourceBranch"),
			"renderedBranch": item("spec.git.renderedBranch"),
			"pullRequest":    "${" + renderPullRequestCond(NodeStepsObserved, iterRender+".step") + "}",
		}
		if ref := pipeline.Spec.Git.SecretRef; ref != nil && ref.Name != "" {
			git["secretName"] = item("spec.git.secretName")
		}
		collections = append(collections, GraphNode{
			ID:      chunkID(NodeRenderRuns, i),
			ForEach: []map[string]string{{iterRender: fmt.Sprintf("${%s.%s}", NodePromotionRenders, field)}},
			Template: map[string]interface{}{
				"apiVersion": "kardinal.io/v1alpha1",
				"kind":       "RenderRun",
				"metadata": map[string]interface{}{
					"name": item("name"),
					"labels": map[string]interface{}{
						"kardinal.io/pipeline":    pipeline.Name,
						"kardinal.io/bundle":      bundle.Name,
						"kardinal.io/environment": item("environment"),
						LabelBundleUID:            string(bundle.UID),
					},
				},
				"spec": map[string]interface{}{
					"pipelineName": pipeline.Name,
					"bundleName":   bundle.Name,
					"environment":  item("environment"),
					"path":         item("spec.path"),
					"git":          git,
					"bundle":       item("spec.bundle"),
					"update":       item("spec.update"),
					"render":       item("spec.render"),
				},
			},
		})
	}
	return append([]GraphNode{
		{ID: NodeRenderState, Def: state},
		{ID: NodeRenderRunData, Def: data},
		{ID: NodePromotionRenders, Def: admit},
	}, collections...)
}

// compactLiveRenders is the compact PromotionSteps template's
// spec.live.renders: the item's RenderRun (renderRun, "" for an environment
// that does not render), applied by kro for this Bundle.
func compactLiveRenders(bundle *kardinalv1alpha1.Bundle) string {
	return fmt.Sprintf(`${%s.filter(r, r.metadata.name == %s.renderRun && r.spec.environment == %s.environment && `+
		`r.metadata.?labels[?%q].hasValue() && r.metadata.?labels[?%q].orValue("") == %s).map(r, %s)}`,
		refRenderRunsNodeID, iterStep, iterStep, LabelKRONodeID, LabelBundleUID, strconv.Quote(string(bundle.UID)),
		renderRecord())
}
