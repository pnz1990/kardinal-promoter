// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Argo Rollouts analysis (docs/analysis.md, #1444).
//
// An environment's spec.verification names AnalysisTemplates or
// ClusterAnalysisTemplates. The translator reads them when it builds the
// Graph (AnalysisInput); each becomes one AnalysisRun template node whose
// metrics, dryRun and measurementRetention are copied from the template and
// whose args are resolved here. Its metadata.name resolves once the step
// entered Verifying (verifyingCond), so the analysis runs after the health
// check. The run's name carries a hash of its rendered spec: a template edit
// picked up by a later translation gives a new name, so kro creates a new
// run (and prunes the old one) instead of changing the spec of a run in
// progress. The Argo Rollouts controller runs it; the mirror patch node
// copies status.phase onto the step (spec.live.analyses).
//
// Verification fails closed: when the cluster does not serve
// argoproj.io/v1alpha1 AnalysisRun (Argo Rollouts not installed), or a named
// template does not exist, the Graph is not built and the Bundle fails. It
// never drops the node and promotes without the analysis (unlike the health
// refs of ledger G4).

// AnalysisRunAPIVersion is the API version of Argo Rollouts' analysis kinds.
const AnalysisRunAPIVersion = "argoproj.io/v1alpha1"

// Analysis template kinds.
const (
	KindAnalysisTemplate        = "AnalysisTemplate"
	KindClusterAnalysisTemplate = "ClusterAnalysisTemplate"
)

// LabelAnalysisTemplate is set on every AnalysisRun the Graph creates: the
// template it runs.
const LabelAnalysisTemplate = "kardinal.io/analysis-template"

// DefaultAnalysisTimeout is the verification timeout when none is set.
const DefaultAnalysisTimeout = 30 * time.Minute

// AnalysisTemplate is an Argo Rollouts AnalysisTemplate or
// ClusterAnalysisTemplate as the translator read it.
type AnalysisTemplate struct {
	Kind string
	Name string
	// Spec is the template's spec: metrics, args, dryRun, measurementRetention.
	Spec map[string]interface{}
}

// AnalysisInput is what the translator read for the environments'
// spec.verification.
type AnalysisInput struct {
	// Templates by AnalysisTemplateKey. A template that does not exist has
	// no entry.
	Templates map[string]AnalysisTemplate
	// Unavailable, when set, says why no AnalysisRun can be created, for
	// example because the Argo Rollouts CRDs are not served. A Graph with
	// verification then fails to build.
	Unavailable string
}

// AnalysisTemplateKey is the AnalysisInput.Templates key of a template.
func AnalysisTemplateKey(kind, name string) string {
	return kind + "/" + name
}

// AnalysisTemplateKind returns ref's kind, AnalysisTemplate by default.
func AnalysisTemplateKind(ref kardinalv1alpha1.AnalysisTemplateRef) string {
	if ref.Kind == "" {
		return KindAnalysisTemplate
	}
	return ref.Kind
}

// AnalysisTimeout parses a verification timeout; "" and "0" mean
// DefaultAnalysisTimeout.
func AnalysisTimeout(s string) (time.Duration, error) {
	if s == "" || s == "0" {
		return DefaultAnalysisTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("timeout %q is not a positive duration", s)
	}
	return d, nil
}

func hasVerification(env kardinalv1alpha1.EnvironmentSpec) bool {
	return env.Verification != nil && len(env.Verification.AnalysisTemplates) > 0
}

// AnalysisRunName returns the AnalysisRun name of template in env for
// bundle: "<pipeline>-<bundle>-<env>-<template>-<hash>", where hash is a
// hash of the rendered run spec, cut and hash-suffixed to 63 characters.
func AnalysisRunName(pipeline, bundle, env, template, specHash string) string {
	preferred := pipeline + "-" + slugify(bundle) + "-" + slugify(env) + "-" + template + "-" + specHash
	return boundedName(preferred, isSlug(pipeline) && isSlug(bundle) && isSlug(env) && isSlug(template),
		nameKey("analysisrun", pipeline, bundle, env, template, specHash), validation.DNS1123LabelMaxLength)
}

// analysisNodeID is the node ID of an AnalysisRun node.
func analysisNodeID(env, template string) string {
	return "analysis0" + CELSafeSlug(env) + "0" + CELSafeSlug(template)
}

// analysisNodes is the result of buildAnalysisNodes for one environment.
type analysisNodes struct {
	nodes []GraphNode
	names []interface{} // spec.analyses of the step: the template names
	// policy is spec.analysisPolicy of the step.
	policy map[string]interface{}
}

// terminateNodeID is the node ID of the patch node that terminates an
// environment's AnalysisRun of template.
func terminateNodeID(env, template string) string {
	return "terminate0" + CELSafeSlug(env) + "0" + CELSafeSlug(template)
}

// buildAnalysisNodes returns the AnalysisRun nodes of one environment.
func buildAnalysisNodes(in hookNodesInput, a AnalysisInput, bundle *kardinalv1alpha1.Bundle) (analysisNodes, error) {
	var out analysisNodes
	if !hasVerification(in.env) {
		return out, nil
	}
	v := in.env.Verification
	where := fmt.Sprintf("build: environment %q verification", in.env.Name)
	if a.Unavailable != "" {
		return out, fmt.Errorf("%s: %s", where, a.Unavailable)
	}
	if _, err := AnalysisTimeout(v.Timeout); err != nil {
		return out, fmt.Errorf("%s: %w", where, err)
	}
	builtins, invalid := analysisBuiltinArgs(in.pipeline, in.env.Name, bundle)
	out.policy = map[string]interface{}{}
	if v.Inconclusive != "" {
		out.policy["inconclusive"] = v.Inconclusive
	}
	if v.Timeout != "" {
		out.policy["timeout"] = v.Timeout
	}
	for _, ref := range v.AnalysisTemplates {
		kind := AnalysisTemplateKind(ref)
		tmpl, ok := a.Templates[AnalysisTemplateKey(kind, ref.Name)]
		if !ok {
			if kind == KindClusterAnalysisTemplate {
				return out, fmt.Errorf("%s: ClusterAnalysisTemplate %q not found", where, ref.Name)
			}
			return out, fmt.Errorf("%s: AnalysisTemplate %q not found in namespace %q", where, ref.Name, in.namespace)
		}
		spec, err := analysisRunSpec(tmpl, v.Args, builtins, invalid)
		if err != nil {
			return out, fmt.Errorf("%s: %s %q: %w", where, kind, ref.Name, err)
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			return out, fmt.Errorf("%s: %s %q: %w", where, kind, ref.Name, err)
		}
		sum := sha256.Sum256(raw)
		name := AnalysisRunName(in.pipeline, in.bundle, in.env.Name, ref.Name, hex.EncodeToString(sum[:])[:8])
		id := analysisNodeID(in.env.Name, ref.Name)
		conds := []string{`bundle.status.phase != "Superseded"`, verifyingCond(in.stepK8sName)}
		out.nodes = append(out.nodes, GraphNode{
			ID: id,
			Template: map[string]interface{}{
				"apiVersion": AnalysisRunAPIVersion,
				"kind":       "AnalysisRun",
				"metadata": map[string]interface{}{
					"name": resolvableWhen(strings.Join(conds, " && "), strconv.Quote(name)),
					"labels": map[string]interface{}{
						"kardinal.io/pipeline":    in.pipeline,
						"kardinal.io/bundle":      in.bundle,
						"kardinal.io/environment": in.env.Name,
						LabelAnalysisTemplate:     ref.Name,
					},
				},
				"spec": literalStrings(spec),
			},
			ReadyWhen: []string{fmt.Sprintf(`${%s.?status.?phase.orValue("") == "Successful"}`, id)},
		})
		out.nodes = append(out.nodes, terminateNode(in, ref.Name, name))
		out.names = append(out.names, ref.Name)
	}
	return out, nil
}

// terminateNode is a patch node that sets spec.terminate on the
// AnalysisRun once nothing waits for it any more: the step failed (a failed
// analysis or hook, the verification timeout), was aborted or rolled back,
// or the Bundle was superseded. Argo Rollouts then stops its measurements.
// The target is the run's literal name, so the patch keeps working after the
// run's own template stopped resolving (a Superseded Bundle).
func terminateNode(in hookNodesInput, template, runName string) GraphNode {
	cond := fmt.Sprintf(`${bundle.?status.?phase.orValue("") == "Superseded" || %s.exists(s, s.metadata.name == %s && `+
		`s.?status.?state.orValue("") in ["Failed", "AbortedByAlarm", "RollingBack"])}`,
		refStepsNodeID, strconv.Quote(in.stepK8sName))
	return GraphNode{
		ID: terminateNodeID(in.env.Name, template),
		Patch: map[string]interface{}{
			"apiVersion": AnalysisRunAPIVersion,
			"kind":       "AnalysisRun",
			"metadata":   map[string]interface{}{"name": runName},
			"spec":       map[string]interface{}{"terminate": cond},
		},
	}
}

// Grammar of the Bundle values kardinal passes as AnalysisRun args. They
// come from the Bundle (CI, a Subscription, the Bundle API), which is less
// trusted than the AnalysisTemplate that interpolates them into queries,
// URLs and Job commands: a value outside its grammar is refused rather than
// passed on (docs/analysis.md#trust).
var (
	argTag    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	argDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	argCommit = regexp.MustCompile(`^[a-f0-9]{7,64}$`)
	// argRepo is the OCI distribution reference grammar for a repository:
	// an optional host[:port], then path components.
	argRepo = regexp.MustCompile(`^(?:(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)(?:\.(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?))*(?::[0-9]+)?/)?` +
		`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
)

// analysisBuiltinArgs are the arg values kardinal supplies to a template
// that declares them. invalid names each value that does not match its
// grammar and why; a template that declares one fails the Build.
func analysisBuiltinArgs(pipeline, env string, b *kardinalv1alpha1.Bundle) (map[string]string, map[string]string) {
	invalid := map[string]string{}
	args := map[string]string{"bundle": b.Name, "pipeline": pipeline, "environment": env}
	if len(b.Spec.Images) > 0 {
		img := b.Spec.Images[0]
		args["tag"] = img.Tag
		args["digest"] = img.Digest
		switch {
		case img.Digest != "":
			args["image"] = img.Repository + "@" + img.Digest
		case img.Tag != "":
			args["image"] = img.Repository + ":" + img.Tag
		default:
			args["image"] = img.Repository
		}
	}
	switch {
	case b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "":
		args["commit"] = b.Spec.ConfigRef.CommitSHA
	case b.Spec.Provenance != nil && b.Spec.Provenance.CommitSHA != "":
		args["commit"] = b.Spec.Provenance.CommitSHA
	}
	for k, v := range args {
		if v == "" {
			delete(args, k)
		}
	}
	check := func(arg string, re *regexp.Regexp, value string) {
		if value != "" && !re.MatchString(value) {
			invalid[arg] = fmt.Sprintf("the Bundle's %s %q does not match %s", arg, value, re)
			delete(args, arg)
		}
	}
	if len(b.Spec.Images) > 0 {
		img := b.Spec.Images[0]
		check("tag", argTag, img.Tag)
		check("digest", argDigest, img.Digest)
		switch {
		case !argRepo.MatchString(img.Repository):
			invalid["image"] = fmt.Sprintf("the Bundle's image repository %q is not a valid repository", img.Repository)
		case invalid["tag"] != "" || invalid["digest"] != "":
			invalid["image"] = "the Bundle's image has an invalid tag or digest"
		}
		if invalid["image"] != "" {
			delete(args, "image")
		}
	}
	check("commit", argCommit, args["commit"])
	return args, invalid
}

// analysisRunSpec returns the AnalysisRun spec for tmpl: its metrics,
// dryRun and measurementRetention, and one arg for each arg it declares,
// valued (in order of precedence) from the Pipeline's verification.args,
// kardinal's built-in args, or the template's own value or valueFrom. An
// arg left without a value is passed without one; Argo Rollouts then
// fails the run ("args.<name> was not resolved").
func analysisRunSpec(tmpl AnalysisTemplate, args []kardinalv1alpha1.AnalysisArg,
	builtins, invalid map[string]string) (map[string]interface{}, error) {
	metrics, _ := tmpl.Spec["metrics"].([]interface{})
	if len(metrics) == 0 {
		return nil, fmt.Errorf("has no metrics")
	}
	spec := map[string]interface{}{"metrics": metrics}
	for _, k := range []string{"dryRun", "measurementRetention"} {
		if v, ok := tmpl.Spec[k]; ok {
			spec[k] = v
		}
	}
	given := map[string]string{}
	for _, a := range args {
		given[a.Name] = a.Value
	}
	declared, _ := tmpl.Spec["args"].([]interface{})
	var out []interface{}
	for _, d := range declared {
		decl, ok := d.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("args: %v is not an object", d)
		}
		name, _ := decl["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("args: an arg has no name")
		}
		arg := map[string]interface{}{"name": name}
		if v, ok := given[name]; ok {
			arg["value"] = v
		} else if why, bad := invalid[name]; bad {
			return nil, fmt.Errorf("arg %s: %s", name, why)
		} else if v, ok := builtins[name]; ok {
			arg["value"] = v
		} else {
			for _, k := range []string{"value", "valueFrom"} {
				if v, ok := decl[k]; ok {
					arg[k] = v
				}
			}
		}
		out = append(out, arg)
	}
	if len(out) > 0 {
		spec["args"] = out
	}
	return spec, nil
}
