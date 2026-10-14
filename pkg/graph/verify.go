// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"slices"
	"sort"
	"strconv"

	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// Verify gates and AgentRuns (docs/design/19-verdicts.md).
//
// A verify gate waits for Verdicts: pass, fail or inconclusive results that
// verifiers (agents, people, CI) wrote for the Bundle in an upstream
// environment. The Graph reads the Bundle's Verdicts with a selector ref
// (VerdictsNodeID), the pattern approval gates use for Approvals, and the
// gate collection copies the eligible ones into each verify gate instance's
// spec.verdicts. The PolicyGate reconciler counts them.
//
// A verify gate with verify.run also gets in-cluster verifiers: AgentRuns,
// one per run and attempt, as items of data (NodeAgentRunData), admitted by
// a def (NodeAgentRunsAdmitted) and created by a collection (NodeAgentRuns),
// the compact shape's pattern for HookRuns (compact_extras.go), in both
// shapes. An attempt is admitted once its verified environment's step is
// Verified and the Bundle is not Superseded or Rejected; a later attempt
// only after the previous one finished without a pass or a fail (its
// Verdict is missing or inconclusive). The retry is decided here, from the
// AgentRuns' status and the Verdicts, never by a reconciler. An item stays
// admitted once its AgentRun exists (G11).

// Node IDs of the verify nodes. They start with an uppercase letter, which
// CELSafeSlug never produces, so no environment can take them.
const (
	// VerdictsNodeID is the selector ref on the Bundle's Verdicts.
	VerdictsNodeID = "Verdicts"
	// NodeAgentRunState is what the AgentRun admission reads back: the
	// AgentRuns that exist and that finished, the runs a pass or fail
	// decided, and the steps that are Verified.
	NodeAgentRunState = "AgentRunState"
	// NodeAgentRunData holds every AgentRun attempt as data.
	NodeAgentRunData = "AgentRunData"
	// NodeAgentRunsAdmitted is the AgentRuns that may exist.
	NodeAgentRunsAdmitted = "AgentRunsAdmitted"
	// NodeAgentRuns creates one AgentRun per NodeAgentRunsAdmitted item.
	NodeAgentRuns = "AgentRuns"
	// refAgentRunsNodeID reads the Bundle's AgentRuns back.
	refAgentRunsNodeID = "refAgentRuns"
	// refVerifyStepsNodeID reads the Bundle's PromotionSteps back for the
	// AgentRun admission in the node shape (the compact shape reads
	// NodeStepsObserved).
	refVerifyStepsNodeID = "refVerifySteps"

	iterAgentRun = "AgentRun"
)

// Labels on AgentRuns and Verdicts.
const (
	LabelCheck       = "kardinal.io/check"
	LabelAgentRunRun = "kardinal.io/agentrun-run"
)

// maxCopiedVerdicts caps spec.verdicts (the CRD's maxItems).
const maxCopiedVerdicts = 101

// MaxAgentRunNameLength keeps an AgentRun's Job name whole in its Pods'
// names: the Job controller cuts generateName to 58 characters before the
// 5-character suffix, and the Verdict admission checks the Pod name starts
// with the run's name.
const MaxAgentRunNameLength = 52

// needsVerdicts reports whether a gate instance made from gate reads the
// Verdicts: it has spec.verify.
func needsVerdicts(gate kardinalv1alpha1.PolicyGate) bool {
	return gate.Spec.Verify != nil
}

// anyNeedsVerdicts reports whether any gate of the Graph reads the Verdicts.
func anyNeedsVerdicts(gatesByEnv map[string][]kardinalv1alpha1.PolicyGate) bool {
	for _, gates := range gatesByEnv {
		if slices.ContainsFunc(gates, needsVerdicts) {
			return true
		}
	}
	return false
}

// verdictsRefNode is a selector ref on the Verdicts labelled with the
// Bundle, in the Bundle's namespace (approvalsRefNode's pattern). The Graph
// ServiceAccount needs list and watch on verdicts (chart graph-rbac.yaml).
func verdictsRefNode(bundle *kardinalv1alpha1.Bundle) GraphNode {
	return GraphNode{
		ID: VerdictsNodeID,
		Ref: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "Verdict",
			"metadata": map[string]interface{}{
				"namespace": bundle.Namespace,
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"kardinal.io/bundle": bundle.Name},
				},
			},
		},
	}
}

// VerifiedEnvironment is the environment gate's verify policy verifies
// when the gate applies to env: verify.environment, or env's only direct
// upstream. upstreams are env's direct upstreams in this Bundle's Graph
// and ancestors every environment upstream of env. It fails when the
// environment cannot be verified before env promotes.
func VerifiedEnvironment(gate kardinalv1alpha1.PolicyGate, env string, upstreams []string, ancestors map[string]bool) (string, error) {
	p := gate.Spec.Verify
	what := fmt.Sprintf("PolicyGate %q in namespace %q for environment %q", gate.Name, gate.Namespace, env)
	if p.Environment == "" {
		if len(upstreams) != 1 {
			return "", fmt.Errorf("build: %s has verify without verify.environment, and %s has %d direct upstreams in "+
				"this Bundle's promotion; set verify.environment", what, env, len(upstreams))
		}
		return upstreams[0], nil
	}
	if p.Environment == env {
		return "", fmt.Errorf("build: %s verifies %s itself, which can never pass: the version is not in %s "+
			"when its gates are evaluated; verify an upstream environment", what, env, env)
	}
	if !ancestors[p.Environment] {
		return "", fmt.Errorf("build: %s verifies %s, which is not upstream of %s in this Bundle's promotion "+
			"(or the Bundle skips it), so no verdict could exist in time", what, p.Environment, env)
	}
	return p.Environment, nil
}

// ancestorsOf is every environment upstream of env, through deps, that the
// Bundle promotes (filtered).
func ancestorsOf(env string, deps map[string][]string, filtered map[string]bool) map[string]bool {
	out := map[string]bool{}
	var walk func(e string)
	walk = func(e string) {
		for _, d := range deps[e] {
			if out[d] {
				continue
			}
			if filtered[d] {
				out[d] = true
			}
			walk(d)
		}
	}
	walk(env)
	return out
}

// verifyPolicyData is gate's verify policy as Graph data for the instance
// of environment env, which verifies verified. Every field is always set,
// so a template reading one never goes data-pending (G11).
func verifyPolicyData(gate kardinalv1alpha1.PolicyGate, verified string) map[string]interface{} {
	p := gate.Spec.Verify
	exclude := p.ExcludeAuthor == nil || *p.ExcludeAuthor
	data := map[string]interface{}{
		"check": p.Check, "environment": verified, "required": int64(max(p.Required, 1)),
		"maxFailures": int64(p.MaxFailures), "allowedAgents": toInterfaces(p.AllowedAgents),
		"allowedUsers": toInterfaces(p.AllowedUsers), "allowedGroups": toInterfaces(p.AllowedGroups),
		"excludeAuthor": exclude, "onInconclusive": orDefault(p.OnInconclusive, kardinalv1alpha1.VerifyHold),
		"retries": int64(p.Retries), "timeout": p.Timeout, "onTimeout": orDefault(p.OnTimeout, kardinalv1alpha1.VerifyFail),
	}
	if p.Run != nil {
		run := map[string]interface{}{"template": p.Run.Template, "count": int64(max(p.Run.Count, p.Required, 1))}
		if p.Run.Namespace != "" {
			run["namespace"] = p.Run.Namespace
		}
		data["run"] = run
	}
	return data
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// verifyPolicyFromData is the inverse of verifyPolicyData.
func verifyPolicyFromData(d map[string]interface{}) *kardinalv1alpha1.GateVerifyPolicy {
	str := func(k string) string { s, _ := d[k].(string); return s }
	num := func(k string) int { n, _ := d[k].(int64); return int(n) }
	list := func(k string) []string {
		var out []string
		for _, v := range d[k].([]interface{}) {
			out = append(out, v.(string))
		}
		return out
	}
	exclude, _ := d["excludeAuthor"].(bool)
	p := &kardinalv1alpha1.GateVerifyPolicy{Check: str("check"), Environment: str("environment"),
		Required: num("required"), MaxFailures: num("maxFailures"), AllowedAgents: list("allowedAgents"),
		AllowedUsers: list("allowedUsers"), AllowedGroups: list("allowedGroups"), ExcludeAuthor: &exclude,
		OnInconclusive: str("onInconclusive"), Retries: num("retries"), Timeout: str("timeout"), OnTimeout: str("onTimeout")}
	if run, ok := d["run"].(map[string]interface{}); ok {
		p.Run = &kardinalv1alpha1.GateVerifyRun{Template: run["template"].(string), Count: int(run["count"].(int64))}
		p.Run.Namespace, _ = run["namespace"].(string)
	}
	return p
}

// verdictsOfItem is the spec.verdicts of a verify gate instance: the
// Verdicts of this very Bundle (spec.bundleUID == the Bundle's UID) and
// Pipeline, for the item's verified environment and check, from writers the
// policy allows (an agent only when it is in allowedAgents; anyone else when
// listed, or when both people lists are empty), sorted by name, at most
// maxCopiedVerdicts of them. Filtering before the cap means Verdicts that
// cannot count cannot crowd out the ones that do.
func verdictsOfItem(pipeline string) string {
	p := iterGate + ".verify"
	eligible := fmt.Sprintf(`(v.spec.?actor.?kind.orValue("") == "agent" ? v.spec.actor.agent in %[1]s.allowedAgents : `+
		`((size(%[1]s.allowedUsers) == 0 && size(%[1]s.allowedGroups) == 0) || v.spec.by in %[1]s.allowedUsers || `+
		`v.spec.?groups.orValue([]).exists(g, g in %[1]s.allowedGroups)))`, p)
	list := fmt.Sprintf(`%[1]s.filter(v, v.spec.bundleUID == bundle.metadata.uid && v.spec.pipeline == %[2]s && `+
		`v.spec.environment == %[3]s.environment && v.spec.check == %[3]s.check && %[4]s)`+
		`.sortBy(v, v.metadata.name).map(v, {"name": v.metadata.name, "createdAt": string(v.metadata.creationTimestamp), `+
		`"bundle": v.spec.bundle, "bundleUID": v.spec.bundleUID, "environment": v.spec.environment, "check": v.spec.check, `+
		`"result": v.spec.result, "summary": v.spec.?summary.orValue(""), ?"evidenceURL": v.spec.?evidenceURL, `+
		`"findings": size(v.spec.?findings.orValue([])), `+
		`"highFindings": size(v.spec.?findings.orValue([]).filter(f, f.severity in ["high", "critical"])), `+
		`"by": v.spec.by, ?"groups": v.spec.?groups, ?"actor": v.spec.?actor, ?"run": v.spec.?run})`,
		VerdictsNodeID, strconv.Quote(pipeline), p, eligible)
	return fmt.Sprintf(`${size(%[1]s) > %[2]d ? %[1]s.slice(0, %[2]d) : %[1]s}`, list, maxCopiedVerdicts)
}

// agentRunAttempt is one AgentRun of the Graph.
type agentRunAttempt struct {
	name, env, check, template, step, prev, targetNamespace string
	run, attempt                                            int
}

// agentRunKey identifies the runs of one (verified environment, check,
// template): two gates that ask for the same share them.
type agentRunKey struct{ env, check, template string }

// agentRunPlan collects the AgentRuns the verify gates of a Graph ask for.
type agentRunPlan struct {
	count, attempts map[agentRunKey]int
	namespace       map[agentRunKey]string
}

func newAgentRunPlan() *agentRunPlan {
	return &agentRunPlan{count: map[agentRunKey]int{}, attempts: map[agentRunKey]int{}, namespace: map[agentRunKey]string{}}
}

// add records the runs gate (verifying verified) asks for, if any.
func (p *agentRunPlan) add(gate kardinalv1alpha1.PolicyGate, verified string) {
	v := gate.Spec.Verify
	if v == nil || v.Run == nil {
		return
	}
	k := agentRunKey{env: verified, check: v.Check, template: v.Run.Template}
	p.count[k] = max(p.count[k], v.Run.Count, v.Required, 1)
	attempts := 1
	if v.OnInconclusive == kardinalv1alpha1.VerifyRetry {
		attempts += v.Retries
	}
	p.attempts[k] = max(p.attempts[k], attempts)
	if v.Run.Namespace != "" {
		p.namespace[k] = v.Run.Namespace
	}
}

// AgentRunName is the AgentRun (Job and Verdict) name of attempt of run of
// check in env for bundle, at most MaxAgentRunNameLength characters.
func AgentRunName(pipeline, bundle, env, check string, run, attempt int) string {
	preferred := fmt.Sprintf("%s-%s-%s-%s-r%d-a%d", pipeline, slugify(bundle), slugify(env), check, run, attempt)
	return boundedName(preferred, false,
		nameKey("agentrun", pipeline, bundle, env, check, strconv.Itoa(run), strconv.Itoa(attempt)),
		min(MaxAgentRunNameLength, validation.DNS1123LabelMaxLength))
}

// TargetNamespace is the namespace an AgentRun of env tests: the workload
// namespace of env's health check (resource, argoRollouts, flagger), else
// the environment's name, the namespace those checks default to.
func TargetNamespace(pipeline *kardinalv1alpha1.Pipeline, env string) string {
	opts := health.OptionsForEnv(pipeline.Name, findEnvSpec(pipeline, env))
	switch opts.Type {
	case "argoRollouts":
		return opts.ArgoRollouts.Namespace
	case "flagger":
		return opts.Flagger.Namespace
	case "resource":
		return opts.Resource.Namespace
	}
	return env
}

// attempts lists the plan's AgentRuns in a stable order.
func (p *agentRunPlan) list(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) []agentRunAttempt {
	keys := make([]agentRunKey, 0, len(p.count))
	for k := range p.count {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.env != b.env {
			return a.env < b.env
		}
		if a.check != b.check {
			return a.check < b.check
		}
		return a.template < b.template
	})
	var out []agentRunAttempt
	for _, k := range keys {
		ns := p.namespace[k]
		if ns == "" {
			ns = TargetNamespace(pipeline, k.env)
		}
		for run := 1; run <= p.count[k]; run++ {
			prev := ""
			for attempt := 1; attempt <= p.attempts[k]; attempt++ {
				name := AgentRunName(pipeline.Name, bundle.Name, k.env, k.check, run, attempt)
				out = append(out, agentRunAttempt{name: name, env: k.env, check: k.check, template: k.template,
					step: promotionStepK8sName(pipeline.Name, bundle.Name, k.env), prev: prev,
					targetNamespace: ns, run: run, attempt: attempt})
				prev = name
			}
		}
	}
	return out
}

// agentRunNodes returns the AgentRun data, read-back, admission and
// collection nodes for runs (nil when there are none). steps is the node
// that reads the Bundle's PromotionSteps back: NodeStepsObserved in the
// compact shape; in the node shape this adds refVerifySteps.
func agentRunNodes(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle, runs []agentRunAttempt, compact bool) []GraphNode {
	if len(runs) == 0 {
		return nil
	}
	ref := func(id, kind string, labels map[string]interface{}) GraphNode {
		return GraphNode{ID: id, Ref: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1", "kind": kind,
			"metadata": map[string]interface{}{"namespace": bundle.Namespace,
				"selector": map[string]interface{}{"matchLabels": labels}},
		}}
	}
	var out []GraphNode
	steps := NodeStepsObserved
	if !compact {
		steps = refVerifyStepsNodeID
		out = append(out, ref(refVerifyStepsNodeID, "PromotionStep",
			map[string]interface{}{"kardinal.io/pipeline": pipeline.Name, "kardinal.io/bundle": bundle.Name}))
	}
	out = append(out, ref(refAgentRunsNodeID, "AgentRun",
		map[string]interface{}{"kardinal.io/bundle": bundle.Name, LabelBundleUID: string(bundle.UID)}))

	// Only AgentRuns kro applied for this Bundle count: one someone else
	// created under one of the names keeps no item admitted (G17).
	applied := fmt.Sprintf(`r.metadata.?labels[?%q].hasValue() && r.metadata.?labels[?%q].orValue("") == %s`,
		LabelKRONodeID, LabelBundleUID, strconv.Quote(string(bundle.UID)))
	runVerdict := `v.spec.?run.hasValue() && v.spec.bundleUID == bundle.metadata.uid`
	state := map[string]interface{}{
		"existing": fmt.Sprintf(`${%s.filter(r, %s).map(r, r.metadata.name)}`, refAgentRunsNodeID, applied),
		"finished": fmt.Sprintf(`${%s.filter(r, %s && r.?status.?phase.orValue("") in ["Succeeded", "Failed"]).map(r, r.metadata.name)}`,
			refAgentRunsNodeID, applied),
		// A pass or a fail ends a run: no further attempt.
		"decided": fmt.Sprintf(`${%s.filter(v, %s && v.spec.result in ["pass", "fail"]).map(v, v.metadata.name)}`,
			VerdictsNodeID, runVerdict),
		"verified": fmt.Sprintf(`${%s.filter(s, s.?status.?state.orValue("") == "Verified").map(s, s.metadata.name)}`, steps),
	}
	data := map[string]interface{}{}
	admit := map[string]interface{}{}
	var collections []GraphNode
	for i := 0; i*MaxCollectionItems < len(runs); i++ {
		chunk := runs[i*MaxCollectionItems : min(len(runs), (i+1)*MaxCollectionItems)]
		items := make([]interface{}, len(chunk))
		for j, r := range chunk {
			items[j] = map[string]interface{}{"name": r.name, "environment": r.env, "check": r.check,
				"template": r.template, "step": r.step, "prev": r.prev, "targetNamespace": r.targetNamespace,
				"run": int64(r.run), "attempt": int64(r.attempt)}
		}
		field := chunkID("items", i)
		data[field] = items
		// Kept once it exists. Otherwise: the Bundle neither Superseded nor
		// Rejected, the verified environment's step Verified, and for a
		// retry the previous attempt finished with no pass or fail.
		admit[field] = fmt.Sprintf(`${%[1]s.%[2]s.filter(a, a.name in %[3]s.existing || (%[4]s && a.step in %[3]s.verified && `+
			`(a.prev == "" || (a.prev in %[3]s.finished && !(a.prev in %[3]s.decided)))))}`,
			NodeAgentRunData, field, NodeAgentRunState, bundleHeld)
		collections = append(collections, agentRunCollection(chunkID(NodeAgentRuns, i),
			fmt.Sprintf("${%s.%s}", NodeAgentRunsAdmitted, field), pipeline, bundle))
	}
	out = append(out, GraphNode{ID: NodeAgentRunState, Def: state},
		GraphNode{ID: NodeAgentRunData, Def: data}, GraphNode{ID: NodeAgentRunsAdmitted, Def: admit})
	return append(out, collections...)
}

// agentRunCollection creates one AgentRun per item of list.
func agentRunCollection(id, list string, pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) GraphNode {
	it := func(f string) string { return "${" + iterAgentRun + "." + f + "}" }
	return GraphNode{
		ID:      id,
		ForEach: []map[string]string{{iterAgentRun: list}},
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "AgentRun",
			"metadata": map[string]interface{}{
				"name": it("name"),
				"labels": map[string]interface{}{
					"kardinal.io/pipeline":    pipeline.Name,
					"kardinal.io/bundle":      bundle.Name,
					LabelBundleUID:            string(bundle.UID),
					"kardinal.io/environment": it("environment"),
					LabelCheck:                it("check"),
					LabelAgentRunRun:          "${string(" + iterAgentRun + ".run)}",
				},
			},
			"spec": map[string]interface{}{
				"pipelineName":    pipeline.Name,
				"bundleName":      bundle.Name,
				"bundleUID":       string(bundle.UID),
				"version":         BundleVersion(bundle),
				"environment":     it("environment"),
				"targetNamespace": it("targetNamespace"),
				"check":           it("check"),
				"template":        it("template"),
				"run":             it("run"),
				"attempt":         it("attempt"),
				// The result of the Verdict named after this run, when one
				// exists: a re-created AgentRun does not run again.
				"recorded": fmt.Sprintf(`${%s.filter(v, v.metadata.name == %s.name && v.spec.?run.hasValue() && `+
					`v.spec.bundleUID == bundle.metadata.uid).map(v, v.spec.result).join("")}`, VerdictsNodeID, iterAgentRun),
			},
		},
		ReadyWhen: []string{`${each.?status.?phase.orValue("") in ["Succeeded", "Failed", "Skipped"]}`},
	}
}
