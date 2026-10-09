// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Hooks and Argo Rollouts analyses in the compact shape.
//
// The node shape gives every HookRun and AnalysisRun its own node, gated by
// resolvableWhen on the conditions of its environment's step node, and
// mirrors their results onto the step through a patch node per environment.
// The compact shape has no step node per environment, so the runs follow the
// pattern of its per-promotion MetricChecks (compactMetricNodes): every run is
// an item of data, a def admits the items whose time has come, and a
// collection per MaxCollectionItems creates them. An item stays admitted once
// its object exists (read back through refHookRuns or refAnalysisRuns), so a
// later change never prunes a run in flight (G11: a collection's items leave
// only when the def drops them).
//
// A pre hook is admitted with its environment's step: its upstreams Verified,
// its gates ready, the Bundle not held (NodePromotionState), and the previous
// hook of its phase Succeeded. A post hook and an analysis are admitted once
// the step entered Verifying (status.verificationStartedAt, read back through
// NodeStepsObserved). The results reach the step through its own template:
// the PromotionSteps collection renders spec.live from the read-back refs,
// filtered to the runs this Graph rendered for the item's environment, so no
// mirror patch node is needed (a collection item is rendered whole, so the
// G14 workaround the node shape needs does not apply).

// Node IDs and iterators of the compact shape's hooks and analyses.
const (
	// NodeHookRunData holds every HookRun as data.
	NodeHookRunData = "HookRunData"
	// NodePromotionHooks is the HookRuns that may exist.
	NodePromotionHooks = "PromotionHooks"
	// NodeHookRuns creates one HookRun per NodePromotionHooks item.
	NodeHookRuns = "HookRuns"
	// NodeAnalysisRunData holds every AnalysisRun as data.
	NodeAnalysisRunData = "AnalysisRunData"
	// NodePromotionAnalyses is the AnalysisRuns that may exist.
	NodePromotionAnalyses = "PromotionAnalyses"
	// NodeAnalysisRuns creates one AnalysisRun per NodePromotionAnalyses item.
	NodeAnalysisRuns = "AnalysisRuns"
	// NodeRunState holds what the admissions read back: the HookRuns and
	// AnalysisRuns that exist, the HookRuns that succeeded and the steps in
	// Verifying.
	NodeRunState = "RunState"

	iterHook = "Hook"
	iterRun  = "Run"
)

// compactHook is one HookRun of the compact shape.
type compactHook struct {
	name, env, phase, hook, step, prev string
	upstreams, gates                   []string
	held                               bool
	job                                interface{}
	timeout                            string
}

// compactRun is one AnalysisRun of the compact shape.
type compactRun struct {
	name, env, template, step string
	spec                      map[string]interface{}
}

// compactEnvExtras is what one environment adds to the compact shape: its
// step's spec fields and its runs.
type compactEnvExtras struct {
	preHooks, postHooks, analyses []string
	policy                        map[string]interface{}
	hookRuns, analysisRuns        []string
	hooks                         []compactHook
	runs                          []compactRun
}

// buildCompactEnvExtras returns the hooks and analyses of one environment
// in the compact shape.
func buildCompactEnvExtras(in hookNodesInput, a AnalysisInput, bundle *kardinalv1alpha1.Bundle,
	upstreams, gates []string, held bool) (compactEnvExtras, error) {
	var out compactEnvExtras
	for _, phase := range []string{kardinalv1alpha1.HookPhasePre, kardinalv1alpha1.HookPhasePost} {
		prev := ""
		for _, h := range hooksOf(in.env, phase) {
			var job interface{}
			dec := json.NewDecoder(bytes.NewReader(h.Job.Raw))
			dec.UseNumber()
			if err := dec.Decode(&job); err != nil {
				return out, fmt.Errorf("build: environment %q hook %q: job: %w", in.env.Name, h.Name, err)
			}
			name := HookRunName(in.pipeline, in.bundle, in.env.Name, phase, h.Name)
			out.hooks = append(out.hooks, compactHook{name: name, env: in.env.Name, phase: phase, hook: h.Name,
				step: in.stepK8sName, prev: prev, upstreams: upstreams, gates: gates, held: held,
				job: literalStrings(job), timeout: h.Timeout})
			out.hookRuns = append(out.hookRuns, name)
			if phase == kardinalv1alpha1.HookPhasePre {
				out.preHooks = append(out.preHooks, name)
			} else {
				out.postHooks = append(out.postHooks, name)
			}
			prev = name
		}
	}
	runs, policy, err := analysisRunsOf(in, a, bundle)
	if err != nil {
		return out, err
	}
	for _, run := range runs {
		spec := map[string]interface{}{"args": []interface{}{}, "dryRun": []interface{}{}, "measurementRetention": []interface{}{}}
		for k, v := range run.spec {
			spec[k] = v
		}
		out.runs = append(out.runs, compactRun{name: run.name, env: in.env.Name, template: run.template,
			step: in.stepK8sName, spec: literalStrings(spec).(map[string]interface{})})
		out.analyses = append(out.analyses, run.template)
		out.analysisRuns = append(out.analysisRuns, run.name)
	}
	out.policy = map[string]interface{}{}
	if len(runs) > 0 {
		out.policy = policy
	}
	return out, nil
}

// compactRunNodes builds the compact shape's HookRun and AnalysisRun data,
// admissions and collections, and the read-back state they share.
func compactRunNodes(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	hooks []compactHook, runs []compactRun) []GraphNode {
	if len(hooks) == 0 && len(runs) == 0 {
		return nil
	}
	steps := NodeStepsObserved
	runState := map[string]interface{}{
		"verifying":  fmt.Sprintf(`${%s.filter(s, s.?status.?verificationStartedAt.hasValue()).map(s, s.metadata.name)}`, steps),
		"superseded": `${bundle.?status.?phase.orValue("") == "Superseded"}`,
	}
	if len(hooks) > 0 {
		runState["hookRuns"] = fmt.Sprintf(`${%s.map(r, r.metadata.name)}`, refHookRunsNodeID)
		runState["succeeded"] = fmt.Sprintf(`${%s.filter(r, r.?status.?phase.orValue("") == "Succeeded").map(r, r.metadata.name)}`,
			refHookRunsNodeID)
	}
	if len(runs) > 0 {
		runState["analysisRuns"] = fmt.Sprintf(`${%s.map(r, r.metadata.name)}`, refAnalysisRunsNodeID)
	}
	out := []GraphNode{{ID: NodeRunState, Def: runState}}
	out = append(out, compactHookNodes(pipeline, bundle, hooks)...)
	out = append(out, compactAnalysisNodes(pipeline, bundle, runs)...)
	return out
}

// compactHookNodes builds the HookRun data, admission and collections.
func compactHookNodes(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle, hooks []compactHook) []GraphNode {
	if len(hooks) == 0 {
		return nil
	}
	state, rs := NodePromotionState+".", NodeRunState+"."
	data := map[string]interface{}{}
	admit := map[string]interface{}{}
	var collections []GraphNode
	for i := 0; i*MaxCollectionItems < len(hooks); i++ {
		chunk := hooks[i*MaxCollectionItems : min(len(hooks), (i+1)*MaxCollectionItems)]
		items := make([]interface{}, len(chunk))
		for j, h := range chunk {
			items[j] = map[string]interface{}{
				"name": h.name, "environment": h.env, "phase": h.phase, "hook": h.hook, "step": h.step,
				"prev": h.prev, "upstreams": toInterfaces(h.upstreams), "gates": toInterfaces(h.gates),
				"held": h.held, "job": h.job, "timeout": h.timeout,
			}
		}
		field := chunkID("items", i)
		data[field] = items
		// Kept once it exists; otherwise after the previous hook of its
		// phase succeeded, and: a pre hook with its environment's step (as
		// NodePromotionWave admits it), a post hook once the step entered
		// Verifying and the Bundle is not Superseded.
		admit[field] = fmt.Sprintf(`${%[1]s.%[2]s.filter(h, h.name in %[3]shookRuns || `+
			`((h.prev == "" || h.prev in %[3]ssucceeded) && (h.phase == "pre" ? `+
			`(h.environment in %[4]sstarted || (%[4]shold == false && h.held == false && h.upstreams.all(u, u in %[4]sverified) && `+
			`h.gates.all(g, g in %[4]sreadyGates))) : `+
			`(h.step in %[3]sverifying && %[3]ssuperseded == false))))}`,
			NodeHookRunData, field, rs, state)
		item := func(f string) string { return "${" + iterHook + "." + f + "}" }
		collections = append(collections, GraphNode{
			ID:      chunkID(NodeHookRuns, i),
			ForEach: []map[string]string{{iterHook: fmt.Sprintf("${%s.%s}", NodePromotionHooks, field)}},
			Template: map[string]interface{}{
				"apiVersion": "kardinal.io/v1alpha1",
				"kind":       "HookRun",
				"metadata": map[string]interface{}{
					"name": item("name"),
					"labels": map[string]interface{}{
						"kardinal.io/pipeline":    pipeline.Name,
						"kardinal.io/bundle":      bundle.Name,
						"kardinal.io/environment": item("environment"),
						LabelHookPhase:            item("phase"),
						LabelHook:                 item("hook"),
						LabelBundleUID:            string(bundle.UID),
					},
				},
				"spec": map[string]interface{}{
					"pipelineName": pipeline.Name,
					"bundleName":   bundle.Name,
					"environment":  item("environment"),
					"hook":         item("hook"),
					"phase":        item("phase"),
					"job":          item("job"),
					"timeout":      item("timeout"),
					"stepAdvanced": stepAdvancedExprBy(iterHook+".step", iterHook+".phase"),
					"recorded":     hookRecordedExpr(iterHook+".step", iterHook+".phase", iterHook+".hook"),
				},
			},
		})
	}
	return append([]GraphNode{
		{ID: NodeHookRunData, Def: data},
		{ID: NodePromotionHooks, Def: admit},
	}, collections...)
}

// stepAdvancedExprBy is stepAdvancedExpr for a phase only known at run time
// (a collection item's field).
func stepAdvancedExprBy(step, phase string) string {
	return fmt.Sprintf(`${%s.exists(s, s.metadata.name == %s && (%s == "post" ? `+
		`s.?status.?state.orValue("") in ["Verified", "Failed", "AbortedByAlarm", "RollingBack"] : `+
		`!(s.?status.?state.orValue("") in ["", "Pending"])))}`, refStepsNodeID, step, phase)
}

// compactAnalysisNodes builds the AnalysisRun data, admission and
// collections. A run's spec.terminate is in its template (the node shape
// needs a patch node for it because a Superseded Bundle's run template stops
// resolving; an admitted item keeps resolving).
func compactAnalysisNodes(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle, runs []compactRun) []GraphNode {
	if len(runs) == 0 {
		return nil
	}
	rs := NodeRunState + "."
	data := map[string]interface{}{}
	admit := map[string]interface{}{}
	var collections []GraphNode
	for i := 0; i*MaxCollectionItems < len(runs); i++ {
		chunk := runs[i*MaxCollectionItems : min(len(runs), (i+1)*MaxCollectionItems)]
		items := make([]interface{}, len(chunk))
		for j, r := range chunk {
			items[j] = map[string]interface{}{"name": r.name, "environment": r.env, "template": r.template,
				"step": r.step, "spec": r.spec}
		}
		field := chunkID("items", i)
		data[field] = items
		admit[field] = fmt.Sprintf(`${%[1]s.%[2]s.filter(r, r.name in %[3]sanalysisRuns || `+
			`(r.step in %[3]sverifying && %[3]ssuperseded == false))}`, NodeAnalysisRunData, field, rs)
		item := func(f string) string { return "${" + iterRun + "." + f + "}" }
		collections = append(collections, GraphNode{
			ID:      chunkID(NodeAnalysisRuns, i),
			ForEach: []map[string]string{{iterRun: fmt.Sprintf("${%s.%s}", NodePromotionAnalyses, field)}},
			Template: map[string]interface{}{
				"apiVersion": AnalysisRunAPIVersion,
				"kind":       "AnalysisRun",
				"metadata": map[string]interface{}{
					"name": item("name"),
					"labels": map[string]interface{}{
						"kardinal.io/pipeline":    pipeline.Name,
						"kardinal.io/bundle":      bundle.Name,
						"kardinal.io/environment": item("environment"),
						LabelAnalysisTemplate:     item("template"),
						LabelBundleUID:            string(bundle.UID),
					},
				},
				"spec": map[string]interface{}{
					"metrics":              item("spec.metrics"),
					"args":                 item("spec.args"),
					"dryRun":               item("spec.dryRun"),
					"measurementRetention": item("spec.measurementRetention"),
					// Stop measuring once nothing waits for the run.
					"terminate": fmt.Sprintf(`${%s.superseded || %s.exists(s, s.metadata.name == %s.step && `+
						`s.?status.?state.orValue("") in ["Failed", "AbortedByAlarm", "RollingBack"])}`,
						NodeRunState, NodeStepsObserved, iterRun),
				},
			},
		})
	}
	return append([]GraphNode{
		{ID: NodeAnalysisRunData, Def: data},
		{ID: NodePromotionAnalyses, Def: admit},
	}, collections...)
}

// compactLive is the compact PromotionSteps template's spec.live: the
// item's HookRun and AnalysisRun results, from the read-back refs, filtered
// to the runs this Graph rendered for the item (its hookRuns and
// analysisRuns), applied by kro, for this Bundle.
func compactLive(bundle *kardinalv1alpha1.Bundle, hooks, analyses bool) map[string]interface{} {
	live := map[string]interface{}{}
	genuine := func(v, names string) string {
		return fmt.Sprintf(`%[1]s.metadata.name in %[2]s && %[1]s.metadata.?labels[?%[3]q].hasValue() && `+
			`%[1]s.metadata.?labels[?%[4]q].orValue("") == %[5]s`,
			v, names, LabelKRONodeID, LabelBundleUID, strconv.Quote(string(bundle.UID)))
	}
	if hooks {
		live["hooks"] = fmt.Sprintf(`${%s.filter(h, %s).map(h, {"name": h.metadata.name, "hook": h.spec.hook, `+
			`"phase": h.spec.phase, "result": h.?status.?phase.orValue("Pending"), "message": h.?status.?message.orValue(""), `+
			`"specHash": h.?status.?specHash.orValue("")})}`, refHookRunsNodeID, genuine("h", iterStep+".hookRuns"))
	}
	if analyses {
		live["analyses"] = fmt.Sprintf(`${%s.filter(r, %s).map(r, {"name": r.metadata.name, `+
			`"created": string(r.metadata.creationTimestamp), `+
			`"template": r.metadata.labels[%q], "phase": r.?status.?phase.orValue("Pending"), "message": r.?status.?message.orValue("")})}`,
			refAnalysisRunsNodeID, genuine("r", iterStep+".analysisRuns"), LabelAnalysisTemplate)
	}
	return live
}
