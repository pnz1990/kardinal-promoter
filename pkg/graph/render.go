// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Rendered manifests (layout: branch, docs/rendered-manifests.md).
//
// Rendering never runs in the controller. Each layout: branch environment
// gets a RenderRun node (an owned kardinal CRD: the RenderRun reconciler runs
// the render as a sandboxed Job and records its result). Its metadata.name
// resolves (resolvableWhen) only once the environment's step reached its
// render step (status.renderRequestedAt, read through refSteps), so the Job
// runs when the step needs it: after the step's own preconditions and pre
// hooks. Where it pushes follows the step's recorded step list
// (status.outputs.renderPullRequest), not the live approval.
//
// The environment's mirror patch node (live0<env>, shared with hooks) writes
// the RenderRun's status onto the step's spec.live.renders, which the
// step's render step reads.

// refRenderRunsNodeID is the node ID of the selector ref that reads the
// Bundle's RenderRuns back.
const refRenderRunsNodeID = "refRenderRuns"

// RenderRunName returns the RenderRun (and Job) name of env for bundle:
// "<pipeline>-<bundle>-<env>-render", cut to fit 63 characters (a Job name
// is a label value), then "-" and a hash of the parts.
func RenderRunName(pipeline, bundle, env string) string {
	return boundedName(pipeline+"-"+slugify(bundle)+"-"+slugify(env)+"-render", false,
		nameKey("renderrun", pipeline, bundle, env), validation.DNS1123LabelMaxLength)
}

// renderNodeID is the node ID of an environment's RenderRun node.
func renderNodeID(env string) string {
	return "render0" + CELSafeSlug(env)
}

// rendersAny reports whether any of envs renders to a branch.
func rendersAny(pipeline *kardinalv1alpha1.Pipeline, envs []string) bool {
	for _, name := range envs {
		if kardinalv1alpha1.RendersToBranch(pipeline.Spec, findEnvSpec(pipeline, name)) {
			return true
		}
	}
	return false
}

// renderEnvPath is the environment path a render reads: spec.path, or
// environments/<name>.
func renderEnvPath(env kardinalv1alpha1.EnvironmentSpec) string {
	if env.Path != "" {
		return env.Path
	}
	return path.Join("environments", env.Name)
}

// renderSourceBranch is the DRY source branch: spec.git.branch, or main.
func renderSourceBranch(pipeline *kardinalv1alpha1.Pipeline) string {
	if pipeline.Spec.Git.Branch != "" {
		return pipeline.Spec.Git.Branch
	}
	return "main"
}

// toTemplateValue converts v to the plain JSON form a Graph template holds,
// with every string that contains "${" made a kro literal.
func toTemplateValue(v interface{}) (interface{}, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return literalStrings(dropNulls(out)), nil
}

// dropNulls removes null map values: a zero time or empty pointer the API
// server would refuse as a typed field's value.
func dropNulls(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, e := range t {
			if e == nil {
				delete(t, k)
				continue
			}
			t[k] = dropNulls(e)
		}
	case []interface{}:
		for i, e := range t {
			t[i] = dropNulls(e)
		}
	}
	return v
}

// renderRunSpec is the spec of env's RenderRun, without git.pullRequest,
// which follows the step's recorded step list (each caller adds its
// expression). spec.render is set only when env has render; the compact
// shape sets {} otherwise, the same as unset (buildCompactRender).
func renderRunSpec(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	env kardinalv1alpha1.EnvironmentSpec) (map[string]interface{}, error) {
	rb := kardinalv1alpha1.RenderRunBundle{Type: bundle.Spec.Type, Images: bundle.Spec.Images, ConfigRef: bundle.Spec.ConfigRef}
	if p := bundle.Spec.Provenance; p != nil {
		rb.RollbackOf = p.RollbackOf
	}
	bundleSpec, err := toTemplateValue(rb)
	if err != nil {
		return nil, fmt.Errorf("build: environment %q: bundle: %w", env.Name, err)
	}
	update, err := toTemplateValue(env.Update)
	if err != nil {
		return nil, fmt.Errorf("build: environment %q: update: %w", env.Name, err)
	}
	git := map[string]interface{}{
		"url":            literalStrings(pipeline.Spec.Git.URL),
		"sourceBranch":   renderSourceBranch(pipeline),
		"renderedBranch": env.RenderedBranch(),
	}
	if ref := pipeline.Spec.Git.SecretRef; ref != nil && ref.Name != "" {
		git["secretName"] = ref.Name
	}
	spec := map[string]interface{}{
		"pipelineName": pipeline.Name,
		"bundleName":   bundle.Name,
		"environment":  env.Name,
		"path":         literalStrings(renderEnvPath(env)),
		"git":          git,
		"bundle":       bundleSpec,
		"update":       update,
	}
	if env.Render != nil {
		r, err := toTemplateValue(env.Render)
		if err != nil {
			return nil, fmt.Errorf("build: environment %q: render: %w", env.Name, err)
		}
		spec["render"] = r
	}
	return spec, nil
}

// renderPullRequestCond is the condition "the step's recorded step list
// pushes the render through a PR" (status.outputs.renderPullRequest) over
// steps, a list of PromotionSteps, for the step named by step (a CEL
// expression).
func renderPullRequestCond(steps, step string) string {
	return fmt.Sprintf(`%s.exists(s, s.metadata.name == %s && s.?status.?outputs[?"renderPullRequest"].orValue("") == "true")`,
		steps, step)
}

// buildRenderRunNode builds the RenderRun node of a layout: branch
// environment.
func buildRenderRunNode(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	env kardinalv1alpha1.EnvironmentSpec, stepK8sName string) (GraphNode, error) {
	name := RenderRunName(pipeline.Name, bundle.Name, env.Name)
	stepRef := func(cond string) string {
		return fmt.Sprintf(`%s.exists(s, s.metadata.name == %s && %s)`, refStepsNodeID, strconv.Quote(stepK8sName), cond)
	}
	when := `bundle.status.phase != "Superseded" && ` + stepRef(`s.?status.?renderRequestedAt.hasValue()`)
	spec, err := renderRunSpec(pipeline, bundle, env)
	if err != nil {
		return GraphNode{}, err
	}
	spec["git"].(map[string]interface{})["pullRequest"] = "${" + renderPullRequestCond(refStepsNodeID, strconv.Quote(stepK8sName)) + "}"
	return GraphNode{
		ID: renderNodeID(env.Name),
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "RenderRun",
			"metadata": map[string]interface{}{
				"name": resolvableWhen(when, strconv.Quote(name)),
				"labels": map[string]interface{}{
					"kardinal.io/pipeline":    pipeline.Name,
					"kardinal.io/bundle":      bundle.Name,
					"kardinal.io/environment": env.Name,
					LabelBundleUID:            string(bundle.UID),
				},
			},
			"spec": spec,
		},
	}, nil
}

// liveRendersExpr is the spec.live.renders value of env's mirror node: the
// RenderRun this build rendered (name), applied by kro for this Bundle
// (genuineFilter), so a RenderRun created by hand is not a result.
func liveRendersExpr(env, name, bundleUID string) string {
	return fmt.Sprintf(`${%s.filter(r, %s && r.spec.environment == %s).map(r, %s)}`,
		refRenderRunsNodeID, genuineFilter("r", []string{name}, bundleUID), strconv.Quote(env), renderRecord())
}

// renderRecord is one spec.live.renders entry of RenderRun r.
func renderRecord() string {
	res := func(field, zero string) string {
		return fmt.Sprintf(`"%s": r.?status.?result.?%s.orValue(%s)`, field, field, zero)
	}
	result := "{" + strings.Join([]string{res("commitSHA", `""`), res("branch", `""`), res("dryCommit", `""`),
		res("renderer", `""`), res("objects", "0"), res("markerDigest", `""`), res("noChanges", "false"),
		res("driftOverwritten", `""`)}, ", ") + "}"
	return `{"name": r.metadata.name, "phase": r.?status.?phase.orValue("Pending"), "message": r.?status.?message.orValue(""), ` +
		`"knownMarkerDigests": r.?status.?knownMarkerDigests.orValue([]), "result": ` + result + `}`
}

// A layout: branch environment's RenderRun is a node of its own in the node
// shape (buildRenderRunNode) and an item of the RenderRuns collection in the
// compact shape (compact_render.go).
