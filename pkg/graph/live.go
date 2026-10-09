// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"strconv"
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Per-environment nodes that run around a PromotionStep and report back to
// it: hooks (hooks.go) and Argo Rollouts analyses (analysis.go). Their
// results reach the step through one mirror patch node per environment
// (live0<env>) whose target is the step's literal name, so they keep
// arriving after the step's own template stopped resolving (ledger G14).
// Selector refs read the Bundle's HookRuns, AnalysisRuns and PromotionSteps
// back; a selector that matches nothing is an empty list, not data-pending.

// envExtras is what buildEnvExtras returns for one environment.
type envExtras struct {
	nodes     []GraphNode
	preHooks  []interface{} // spec.preHooks of the step
	postHooks []interface{} // spec.postHooks of the step
	analyses  []interface{} // spec.analyses of the step
	// analysisPolicy is spec.analysisPolicy of the step.
	analysisPolicy map[string]interface{}
}

// buildEnvExtras returns the hook and analysis nodes of one environment and
// its mirror patch node; nothing for an environment with neither.
func buildEnvExtras(in hookNodesInput, analyses AnalysisInput, bundle *kardinalv1alpha1.Bundle) (envExtras, error) {
	hooks, err := buildHookNodes(in)
	if err != nil {
		return envExtras{}, err
	}
	runs, err := buildAnalysisNodes(in, analyses, bundle)
	if err != nil {
		return envExtras{}, err
	}
	out := envExtras{preHooks: hooks.preHooks, postHooks: hooks.postHooks}
	out.nodes = append(out.nodes, hooks.nodes...)
	out.nodes = append(out.nodes, runs.nodes...)
	out.analyses = runs.names
	if len(runs.names) > 0 {
		out.analysisPolicy = runs.policy
	}
	if len(hooks.nodes) > 0 || len(runs.nodes) > 0 {
		out.nodes = append(out.nodes, buildLiveMirrorNode(in.env.Name, in.stepK8sName, in.bundleUID, hooks.names, len(runs.nodes) > 0))
	}
	return out, nil
}

// attachExtras writes spec.preHooks, spec.postHooks and spec.analyses onto a
// PromotionStep node.
func attachExtras(step GraphNode, x envExtras) {
	spec, ok := step.Template["spec"].(map[string]interface{})
	if !ok {
		return
	}
	for field, v := range map[string][]interface{}{"preHooks": x.preHooks, "postHooks": x.postHooks, "analyses": x.analyses} {
		if len(v) > 0 {
			spec[field] = v
		}
	}
	if x.analysisPolicy != nil {
		spec["analysisPolicy"] = x.analysisPolicy
	}
}

// buildLiveMirrorNode builds the patch node that writes env's HookRun and
// AnalysisRun results onto its PromotionStep (spec.live). Hook results come
// only from the HookRuns this build rendered (hookNames, a literal list
// rebuilt at every translation) that kro applied for this Bundle
// (genuineFilter), so a HookRun created by hand is not a result.
func buildLiveMirrorNode(env, stepK8sName, bundleUID string, hookNames []string, analyses bool) GraphNode {
	live := map[string]interface{}{}
	if len(hookNames) > 0 {
		live["hooks"] = fmt.Sprintf(`${%s.filter(h, %s).map(h, {"name": h.metadata.name, "hook": h.spec.hook, `+
			`"phase": h.spec.phase, "result": h.?status.?phase.orValue("Pending"), "message": h.?status.?message.orValue("")})}`,
			refHookRunsNodeID, genuineFilter("h", hookNames, bundleUID)+" && h.spec.environment == "+strconv.Quote(env))
	}
	if analyses {
		live["analyses"] = fmt.Sprintf(`${%s.filter(r, r.metadata.labels[%q] == %s).map(r, {"name": r.metadata.name, `+
			`"created": string(r.metadata.creationTimestamp), `+
			`"template": r.metadata.labels[%q], "phase": r.?status.?phase.orValue("Pending"), "message": r.?status.?message.orValue("")})}`,
			refAnalysisRunsNodeID, "kardinal.io/environment", strconv.Quote(env), LabelAnalysisTemplate)
	}
	return GraphNode{
		ID: liveNodeID(env),
		Patch: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "PromotionStep",
			"metadata":   map[string]interface{}{"name": stepK8sName},
			"spec":       map[string]interface{}{"live": live},
		},
	}
}

// readBackRefs returns the selector refs the Graph's hooks and analyses
// need: the Bundle's PromotionSteps (post hooks and analyses start once the
// step entered Verifying), HookRuns and AnalysisRuns.
func readBackRefs(pipeline *kardinalv1alpha1.Pipeline, envs []string, bundle *kardinalv1alpha1.Bundle) []GraphNode {
	var hooks, analyses bool
	for _, name := range envs {
		env := findEnvSpec(pipeline, name)
		hooks = hooks || len(env.Hooks) > 0
		analyses = analyses || hasVerification(env)
	}
	ref := func(id, apiVersion, kind string) GraphNode {
		return GraphNode{ID: id, Ref: map[string]interface{}{
			"apiVersion": apiVersion,
			"kind":       kind,
			"metadata": map[string]interface{}{
				// A selector ref without a namespace lists every namespace.
				"namespace": bundle.Namespace,
				"selector": map[string]interface{}{"matchLabels": map[string]interface{}{
					"kardinal.io/pipeline": pipeline.Name,
					"kardinal.io/bundle":   bundle.Name,
				}},
			},
		}}
	}
	var out []GraphNode
	if hooks || analyses {
		out = append(out, ref(refStepsNodeID, "kardinal.io/v1alpha1", "PromotionStep"))
	}
	if hooks {
		out = append(out, ref(refHookRunsNodeID, "kardinal.io/v1alpha1", "HookRun"))
	}
	if analyses {
		out = append(out, ref(refAnalysisRunsNodeID, AnalysisRunAPIVersion, "AnalysisRun"))
	}
	return out
}

// verifyingCond is the condition "the step entered Verifying", read through
// refSteps. It holds from then on (status.verificationStartedAt is set once).
func verifyingCond(stepK8sName string) string {
	return fmt.Sprintf(`%s.exists(s, s.metadata.name == %s && s.?status.?verificationStartedAt.hasValue())`,
		refStepsNodeID, strconv.Quote(stepK8sName))
}

// genuineFilter is the CEL condition on v (an object read through a
// selector ref) that it is one of names, applied by kro, for the Bundle
// whose UID is bundleUID.
func genuineFilter(v string, names []string, bundleUID string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return fmt.Sprintf(`%[1]s.metadata.name in [%[2]s] && %[1]s.metadata.?labels[?%[3]q].hasValue() && `+
		`%[1]s.metadata.?labels[?%[4]q].orValue("") == %[5]s`,
		v, strings.Join(quoted, ", "), LabelKRONodeID, LabelBundleUID, strconv.Quote(bundleUID))
}
