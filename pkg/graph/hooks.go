// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Pre- and post-deploy hooks (docs/hooks.md, #1443).
//
// Each hook of an environment is a HookRun node (an owned kardinal CRD: the
// HookRun reconciler creates the Job and latches its result). A raw
// batch/v1 Job node does not work: kro deletes and prunes without a
// propagation policy, so a Job's Pods are orphaned; a deleted Job is
// re-created and runs again; and a changed Job template is an immutable-field
// error that stops prune and release for the whole Graph (ledger G12).
//
// Per environment with hooks the Graph gets:
//
//   - one HookRun node per hook. Its metadata.name resolves (resolvableWhen)
//     only when the hook may run: a pre hook when the step itself could be
//     created (upstreams Verified, gates ready, Bundle not Superseded), a
//     post hook when the step entered Verifying (status.verificationStartedAt,
//     read through refSteps). Hooks of one phase run in list order: each one
//     also waits for the previous one to succeed.
//   - spec.preHooks on the PromotionStep: the first entry references the
//     first pre HookRun node (a CEL edge, so the step is created after it),
//     the rest are literal names. spec.postHooks lists the post HookRun
//     names.
//   - a mirror patch node (live0<env>) that writes the environment's HookRun
//     results onto the step's spec.live.hooks. Its target is the step's
//     literal name, so it keeps updating after the step's own template stopped
//     resolving (a gate turned false later): ledger G14.
//
// Two selector refs per Graph, refHookRuns and refSteps, read the Bundle's
// HookRuns and PromotionSteps back. A selector that matches nothing is an
// empty list, not data-pending.

// Node IDs of the per-Graph read-back refs. ValidateNodeIDs rejects an
// environment whose node ID collides with them.
const (
	refHookRunsNodeID = "refHookRuns"
	refStepsNodeID    = "refSteps"
)

// Labels on HookRuns.
const (
	LabelHookPhase = "kardinal.io/hook-phase"
	LabelHook      = "kardinal.io/hook"
	// LabelKRONodeID is the label kro's Graph executor stamps on every object
	// it applies (kro.run/node-id, pkg/metadata/labels.go).
	LabelKRONodeID = "kro.run/node-id"
)

// DefaultHookTimeout is the timeout of a hook that sets none.
const DefaultHookTimeout = 30 * time.Minute

// HookRunName returns the HookRun (and Job) name of hook in phase of env for
// bundle: "<pipeline>-<bundle>-<env>-<phase>-<hook>", cut to fit 63
// characters (a Job name is a label value), then "-" and a hash of the
// parts. The hash is always there: the readable part alone is not
// injective ("prod" "post" "pre-smoke" and "prod-post" "pre" "smoke" both
// read prod-post-pre-smoke).
func HookRunName(pipeline, bundle, env, phase, hook string) string {
	preferred := pipeline + "-" + slugify(bundle) + "-" + slugify(env) + "-" + phase + "-" + hook
	return boundedName(preferred, false,
		nameKey("hookrun", pipeline, bundle, env, phase, hook), validation.DNS1123LabelMaxLength)
}

// hookNodeID is the node ID of a HookRun node: "hook0<phase>0<env>0<hook>".
func hookNodeID(env, phase, hook string) string {
	return "hook0" + phase + "0" + CELSafeSlug(env) + "0" + CELSafeSlug(hook)
}

// liveNodeID is the node ID of an environment's mirror patch node.
func liveNodeID(env string) string {
	return "live0" + CELSafeSlug(env)
}

// hooksOf returns env's hooks of phase, in list order.
func hooksOf(env kardinalv1alpha1.EnvironmentSpec, phase string) []kardinalv1alpha1.HookSpec {
	var out []kardinalv1alpha1.HookSpec
	for _, h := range env.Hooks {
		if h.Phase == phase {
			out = append(out, h)
		}
	}
	return out
}

// hasHooks reports whether any of envs has a hook.
func hasHooks(pipeline *kardinalv1alpha1.Pipeline, envs []string) bool {
	for _, name := range envs {
		if len(findEnvSpec(pipeline, name).Hooks) > 0 {
			return true
		}
	}
	return false
}

func findEnvSpec(pipeline *kardinalv1alpha1.Pipeline, name string) kardinalv1alpha1.EnvironmentSpec {
	for _, e := range pipeline.Spec.Environments {
		if e.Name == name {
			return e
		}
	}
	return kardinalv1alpha1.EnvironmentSpec{}
}

// ValidateHooks checks the hooks of every environment: unique names, a
// known phase, a timeout time.ParseDuration accepts, and a job that decodes
// as a batch/v1 JobSpec with at least one container. The CRD schema checks
// most of it; this also covers the job, which the schema cannot type.
func ValidateHooks(pipeline *kardinalv1alpha1.Pipeline) error {
	for _, env := range pipeline.Spec.Environments {
		seen := map[string]bool{}
		for _, h := range env.Hooks {
			where := fmt.Sprintf("environment %q hook %q", env.Name, h.Name)
			if h.Name == "" || slugify(h.Name) != h.Name {
				return fmt.Errorf("build: %s: the name must be a DNS label", where)
			}
			if seen[h.Name] {
				return fmt.Errorf("build: %s: two hooks have this name", where)
			}
			seen[h.Name] = true
			if h.Phase != kardinalv1alpha1.HookPhasePre && h.Phase != kardinalv1alpha1.HookPhasePost {
				return fmt.Errorf("build: %s: phase %q is not pre or post", where, h.Phase)
			}
			if _, err := HookTimeout(h.Timeout); err != nil {
				return fmt.Errorf("build: %s: %w", where, err)
			}
			if _, err := DecodeHookJob(h.Job.Raw); err != nil {
				return fmt.Errorf("build: %s: %w", where, err)
			}
		}
	}
	return nil
}

// HookTimeout parses a hook timeout; "" and "0" mean DefaultHookTimeout.
func HookTimeout(s string) (time.Duration, error) {
	if s == "" || s == "0" {
		return DefaultHookTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("timeout %q is not a positive duration", s)
	}
	return d, nil
}

// DecodeHookJob decodes raw as a batch/v1 JobSpec, rejecting unknown fields
// (the CRD keeps the field schemaless) and a Job without containers.
func DecodeHookJob(raw []byte) (*batchv1.JobSpec, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("job is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var spec batchv1.JobSpec
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("job is not a batch/v1 JobSpec: %w", err)
	}
	if len(spec.Template.Spec.Containers) == 0 {
		return nil, fmt.Errorf("job has no containers (job.template.spec.containers)")
	}
	return &spec, nil
}

// hookNodesInput is what buildHookNodes needs about one environment.
type hookNodesInput struct {
	pipeline, bundle, namespace string
	bundleUID                   string
	env                         kardinalv1alpha1.EnvironmentSpec
	stepK8sName                 string
	// conds are the conditions under which the environment's step may be
	// created (Bundle not Superseded, upstreams Verified, gates ready).
	conds []string
}

// hookNodes is the result of buildHookNodes for one environment.
type hookNodes struct {
	nodes     []GraphNode
	preHooks  []interface{} // spec.preHooks of the step
	postHooks []interface{} // spec.postHooks of the step
}

// buildHookNodes returns the HookRun nodes and the mirror patch node of one
// environment, and the step's spec.preHooks and spec.postHooks. It returns
// nothing for an environment without hooks.
func buildHookNodes(in hookNodesInput) (hookNodes, error) {
	var out hookNodes
	if len(in.env.Hooks) == 0 {
		return out, nil
	}
	var names []string
	for _, phase := range []string{kardinalv1alpha1.HookPhasePre, kardinalv1alpha1.HookPhasePost} {
		prevID := ""
		for i, h := range hooksOf(in.env, phase) {
			name := HookRunName(in.pipeline, in.bundle, in.env.Name, phase, h.Name)
			id := hookNodeID(in.env.Name, phase, h.Name)
			var conds []string
			if phase == kardinalv1alpha1.HookPhasePre {
				conds = append(conds, in.conds...)
			} else {
				conds = append(conds, `bundle.status.phase != "Superseded"`,
					fmt.Sprintf(`%s.exists(s, s.metadata.name == %s && s.?status.?verificationStartedAt.hasValue())`,
						refStepsNodeID, strconv.Quote(in.stepK8sName)))
			}
			if prevID != "" {
				conds = append(conds, fmt.Sprintf(`%s.?status.?phase.orValue("") == "Succeeded"`, prevID))
			}
			names = append(names, name)
			node, err := buildHookRunNode(id, name, in, phase, h, conds)
			if err != nil {
				return hookNodes{}, err
			}
			out.nodes = append(out.nodes, node)
			if phase == kardinalv1alpha1.HookPhasePre {
				if i == 0 {
					out.preHooks = append(out.preHooks, fmt.Sprintf("${%s.metadata.name}", id))
				} else {
					out.preHooks = append(out.preHooks, name)
				}
			} else {
				out.postHooks = append(out.postHooks, name)
			}
			prevID = id
		}
	}
	out.nodes = append(out.nodes, buildLiveMirrorNode(in.env.Name, in.stepK8sName, in.bundleUID, names))
	return out, nil
}

// buildHookRunNode builds the HookRun node of one hook. Its name resolves
// only when every condition in conds holds.
func buildHookRunNode(id, name string, in hookNodesInput, phase string,
	h kardinalv1alpha1.HookSpec, conds []string) (GraphNode, error) {
	var job interface{}
	dec := json.NewDecoder(bytes.NewReader(h.Job.Raw))
	dec.UseNumber()
	if err := dec.Decode(&job); err != nil {
		return GraphNode{}, fmt.Errorf("build: environment %q hook %q: job: %w", in.env.Name, h.Name, err)
	}
	spec := map[string]interface{}{
		"pipelineName": in.pipeline,
		"bundleName":   in.bundle,
		"environment":  in.env.Name,
		"hook":         h.Name,
		"phase":        phase,
		"job":          literalStrings(job),
		// A hook added to the Pipeline after its step passed the point it
		// runs at is Skipped by its reconciler, not run out of order.
		"stepAdvanced": stepAdvanced(in.stepK8sName, phase),
		// What the step recorded for this hook, when one ran before (a
		// HookRun deleted and applied again does not run its Job twice).
		"recorded": hookRecorded(in.stepK8sName, phase, h.Name),
	}
	if h.Timeout != "" {
		spec["timeout"] = h.Timeout
	}
	return GraphNode{
		ID: id,
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "HookRun",
			"metadata": map[string]interface{}{
				"name": resolvableWhen(strings.Join(conds, " && "), strconv.Quote(name)),
				"labels": map[string]interface{}{
					"kardinal.io/pipeline":    in.pipeline,
					"kardinal.io/bundle":      in.bundle,
					"kardinal.io/environment": in.env.Name,
					LabelHookPhase:            phase,
					LabelHook:                 h.Name,
					LabelBundleUID:            in.bundleUID,
				},
			},
			"spec": spec,
		},
		ReadyWhen: []string{fmt.Sprintf(`${%s.?status.?phase.orValue("") == "Succeeded"}`, id)},
	}, nil
}

// buildLiveMirrorNode builds the patch node that writes env's HookRun results
// onto its PromotionStep (spec.live.hooks). It copies only the HookRuns this
// build rendered (names, a literal list rebuilt at every translation) that
// kro applied (kro.run/node-id) for this Bundle (kardinal.io/bundle-uid), so
// a HookRun created by hand with a matching selector label is not a result.
func buildLiveMirrorNode(env, stepK8sName, bundleUID string, names []string) GraphNode {
	hooks := fmt.Sprintf(`${%s.filter(h, %s).map(h, {"name": h.metadata.name, "hook": h.spec.hook, `+
		`"phase": h.spec.phase, "result": h.?status.?phase.orValue("Pending"), "message": h.?status.?message.orValue(""), `+
		`"specHash": h.?status.?specHash.orValue("")})}`,
		refHookRunsNodeID, genuineFilter("h", names, bundleUID)+" && h.spec.environment == "+strconv.Quote(env))
	return GraphNode{
		ID: liveNodeID(env),
		Patch: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "PromotionStep",
			"metadata":   map[string]interface{}{"name": stepK8sName},
			"spec":       map[string]interface{}{"live": map[string]interface{}{"hooks": hooks}},
		},
	}
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

// hookRefNodes returns the selector refs that read the Bundle's HookRuns and
// PromotionSteps back into the Graph.
func hookRefNodes(pipeline, bundle, namespace string) []GraphNode {
	ref := func(id, kind string) GraphNode {
		return GraphNode{ID: id, Ref: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       kind,
			"metadata": map[string]interface{}{
				"namespace": namespace,
				"selector": map[string]interface{}{"matchLabels": map[string]interface{}{
					"kardinal.io/pipeline": pipeline,
					"kardinal.io/bundle":   bundle,
				}},
			},
		}}
	}
	return []GraphNode{ref(refHookRunsNodeID, "HookRun"), ref(refStepsNodeID, "PromotionStep")}
}

// literalStrings returns v with every string that contains "${" replaced by
// a kro expression that evaluates to the string itself. kro reads "${" in
// any template string as the start of an expression and has no escape, so
// a shell command such as `echo ${HOME}` in a hook's job would otherwise be
// compiled as CEL. Inside a CEL string literal kro does not look for "${".
func literalStrings(v interface{}) interface{} {
	switch t := v.(type) {
	case string:
		if strings.Contains(t, "${") {
			return "${" + strconv.Quote(t) + "}"
		}
		return t
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = literalStrings(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = literalStrings(val)
		}
		return out
	}
	return v
}

// stepConds returns the conditions under which a step with these upstream
// step node IDs and gate instance names may be created: the conditions the
// step's own bundleName, upstreamStates and requiredGates expressions gate
// on (gateReady is the gate collection's readiness condition).
func stepConds(held string, upstreams, gateNames []string, gateReady func(name string) string) []string {
	// The step's own resolvability: not Superseded, not waiting for a
	// maxConcurrentPromotions slot, and the held Bundle when the
	// environment is held (spec.holds).
	conds := []string{stepCond(held)}
	for _, up := range upstreams {
		conds = append(conds, verifiedCond(up))
	}
	for _, name := range gateNames {
		conds = append(conds, gateReady(name))
	}
	return conds
}

// attachHooks writes spec.preHooks and spec.postHooks onto a PromotionStep
// node.
func attachHooks(step GraphNode, hooks hookNodes) {
	spec, ok := step.Template["spec"].(map[string]interface{})
	if !ok {
		return
	}
	if len(hooks.preHooks) > 0 {
		spec["preHooks"] = hooks.preHooks
	}
	if len(hooks.postHooks) > 0 {
		spec["postHooks"] = hooks.postHooks
	}
}

// stepAdvanced is the HookRun's spec.stepAdvanced: whether the step already
// passed the point a hook of phase runs at (started, for a pre hook;
// finished, for a post hook), read through refSteps.
func stepAdvanced(stepK8sName, phase string) string {
	states := `!(s.?status.?state.orValue("") in ["", "Pending"])`
	if phase == kardinalv1alpha1.HookPhasePost {
		states = `s.?status.?state.orValue("") in ["Verified", "Failed", "AbortedByAlarm", "RollingBack"]`
	}
	return fmt.Sprintf(`${%s.exists(s, s.metadata.name == %s && %s)}`, refStepsNodeID, strconv.Quote(stepK8sName), states)
}

// hookRecorded is a HookRun's spec.recorded: the step's
// status.hookRecords entry for hook in phase, one string field at a time
// ("" when the step has none or does not exist yet). Each is a join over
// the (at most one) matching step and record, so every expression is a
// string whatever matches: kro type-checks a conditional's branches
// against the HookRun schema.
func hookRecorded(stepK8sName, phase, hook string) map[string]interface{} {
	field := func(f string) string {
		return fmt.Sprintf(`${%s.filter(s, s.metadata.name == %s).map(s, s.?status.?hookRecords.orValue([]).filter(r, `+
			`r.?hook.orValue("") == %s && r.?phase.orValue("") == %s).map(r, r.?%s.orValue(""))).map(l, l.join("")).join("")}`,
			refStepsNodeID, strconv.Quote(stepK8sName), strconv.Quote(hook), strconv.Quote(phase), f)
	}
	return map[string]interface{}{"specHash": field("specHash"), "result": field("result"), "message": field("message")}
}

// The compact shape does not build HookRun nodes or the mirror patch node:
// both are per environment and read the environment's step node, which the
// compact shape folds into the PromotionSteps collection. A Pipeline with
// hooks is built in the node shape, or refused (compactUnsupported).
func init() {
	RegisterCompactUnsupported(hooksUsed)
}

// hooksUsed returns the feature name when an environment of the Pipeline
// has hooks.
func hooksUsed(in BuildInput) string {
	if in.Pipeline == nil {
		return ""
	}
	for _, env := range in.Pipeline.Spec.Environments {
		if len(env.Hooks) > 0 {
			return "pre- and post-deploy hooks (spec.environments[].hooks)"
		}
	}
	return ""
}
