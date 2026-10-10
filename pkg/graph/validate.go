// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// nodeIDPattern is kro's node ID grammar
// (kubernetes-sigs/kro pkg/graphengine/compiler/validation.go).
var nodeIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// reservedNodeIDs mirrors kro's reserved node IDs: Kubernetes manifest
// fields, kro's own vocabulary, and the CEL reserved words
// (kubernetes-sigs/kro pkg/graphengine/compiler/validation.go). kro rejects a
// Graph that uses one; Build rejects it first, with the environment or gate
// that produced it, instead of leaving a Graph that never compiles.
var reservedNodeIDs = map[string]bool{
	"apiVersion": true, "kind": true, "metadata": true, "namespace": true, "spec": true, "status": true,
	"graph": true, "graphengine": true, "kro": true,
	"each": true, "item": true, "items": true, "object": true, "self": true, "this": true, "context": true,
	// CEL reserved words.
	"true": true, "false": true, "null": true, "in": true,
	"as": true, "break": true, "const": true, "continue": true, "else": true,
	"for": true, "function": true, "if": true, "import": true, "let": true,
	"loop": true, "package": true, "return": true,
	"var": true, "void": true, "while": true,
	// "time" is proposed as reserved by kro#1434 (KREP-025 time.now()), not in
	// the pinned kro yet. Reserved now so a Pipeline with an environment
	// named "time" is refused before the kro upgrade would break it (ledger
	// Notes: node ID grammar).
	"time": true,
}

// reservedEnvironmentNames is the list the Pipeline CRD's reserved-name rule
// refuses (api/v1alpha1 EnvironmentSpec): the names whose node ID kro
// reserves, written as environment names (apiVersion becomes "api-version"),
// "bundle", the ID of the Bundle node every Graph has, and "time", which
// kro#1434 proposes to reserve.
// TestReservedEnvironmentNamesMatchCRD keeps the two in step.
var reservedEnvironmentNames = []string{
	"api-version", "kind", "metadata", "namespace", "spec", "status", "graph", "graphengine", "kro",
	"each", "item", "items", "object", "self", "this", "context", "true", "false", "null", "in",
	"as", "break", "const", "continue", "else", "for", "function", "if", "import", "let", "loop",
	"package", "return", "var", "void", "while", "bundle", "time",
}

// ReservedEnvironmentMessage is the message of the Pipeline CRD's
// reserved-name rule.
const ReservedEnvironmentMessage = "reserved environment name: the name becomes a kro Graph node ID; bundle, time, kro " +
	"reserved IDs (spec, status, metadata, graph, self, each, item, ...) and CEL keywords are not allowed; " +
	"rename the environment"

// ReservedEnvironmentName reports whether the API server refuses name as an
// environment name (ReservedEnvironmentMessage).
func ReservedEnvironmentName(name string) bool {
	return slices.Contains(reservedEnvironmentNames, name)
}

// ErrInvalid is in the chain of every error Build and ValidateNodeIDs
// return. Build reads nothing but its input, so such an error is permanent:
// a retry fails the same way until the Pipeline, the Bundle or a PolicyGate
// changes. Callers use errors.Is(err, ErrInvalid) to tell it from API errors
// that a retry can fix.
var ErrInvalid = errors.New("pipeline, bundle or policy gates cannot be built into a graph")

// invalidError keeps the message of err and adds ErrInvalid to its chain.
type invalidError struct{ err error }

func (e *invalidError) Error() string   { return e.err.Error() }
func (e *invalidError) Unwrap() []error { return []error{e.err, ErrInvalid} }

// asInvalid marks err as permanent; nil stays nil.
func asInvalid(err error) error {
	if err == nil || errors.Is(err, ErrInvalid) {
		return err
	}
	return &invalidError{err: err}
}

// maxPipelineNameHint is the longest Pipeline name that leaves room for the
// rollback Bundle name "<pipeline>-rollback-xxxxx" in a 63-character label.
const maxPipelineNameHint = validation.LabelValueMaxLength - len("-rollback-xxxxx")

// ValidateNodeIDs checks the node IDs of a Graph the way kro's compiler does,
// before the Graph is written: each ID matches ^[A-Za-z][A-Za-z0-9]*$, is not
// reserved, is unique, and does not equal a forEach iterator name (an
// iterator shadows a node of the same name inside the forEach node's
// expressions). Environment and gate names that differ only in case or
// punctuation ("prod-eu", "prod_eu", "prodEu") map to the same ID; the error
// names both sources so the user knows what to rename. Errors wrap
// ErrInvalid.
func ValidateNodeIDs(nodes []GraphNode) error {
	return asInvalid(validateNodeIDs(nodes))
}

func validateNodeIDs(nodes []GraphNode) error {
	iterators := map[string]string{}
	for _, n := range nodes {
		for _, dim := range n.ForEach {
			for name := range dim {
				iterators[name] = n.ID
			}
		}
	}
	seen := make(map[string]GraphNode, len(nodes))
	for _, n := range nodes {
		switch {
		case !nodeIDPattern.MatchString(n.ID):
			return fmt.Errorf("build: %s gets node id %q, which does not match %s",
				describeNode(n), n.ID, nodeIDPattern.String())
		case reservedNodeIDs[n.ID]:
			return fmt.Errorf("build: %s gets node id %q, which kro reserves; rename it", describeNode(n), n.ID)
		}
		if prev, dup := seen[n.ID]; dup {
			return fmt.Errorf("build: %s and %s get the same node id %q; names that differ only in "+
				"case or punctuation map to the same id, so rename one", describeNode(prev), describeNode(n), n.ID)
		}
		seen[n.ID] = n
		if owner, ok := iterators[n.ID]; ok {
			return fmt.Errorf("build: %s gets node id %q, which is the forEach iterator name of node %q; rename it",
				describeNode(n), n.ID, owner)
		}
	}
	return validateObjectNames(nodes)
}

// resolvableName matches a metadata.name built by resolvableWhen around a
// string literal: ${["<name>"].filter(x_, ...)[0]}.
var resolvableName = regexp.MustCompile(`^\$\{\[("(?:[^"\\]|\\.)*")\]\.filter\(x_, `)

// templateObjectName returns the metadata.name a template node renders, when
// the builder knows it: a literal, or a resolvableWhen around a literal.
func templateObjectName(n GraphNode) (string, bool) {
	if n.Template == nil || len(n.ForEach) > 0 {
		return "", false
	}
	md, _ := n.Template["metadata"].(map[string]interface{})
	name, _ := md["name"].(string)
	if name == "" {
		return "", false
	}
	if !strings.Contains(name, "${") {
		return name, true
	}
	m := resolvableName.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	lit, err := strconv.Unquote(m[1])
	if err != nil {
		return "", false
	}
	return lit, true
}

// validateObjectNames rejects two template nodes that render the same
// object (kind and name): kro rejects the Graph with a duplicate identity,
// or, for names that only resolve later, the second object would overwrite
// the first.
func validateObjectNames(nodes []GraphNode) error {
	seen := map[string]GraphNode{}
	for _, n := range nodes {
		name, ok := templateObjectName(n)
		if !ok {
			continue
		}
		key := fmt.Sprint(n.Template["apiVersion"], "/", n.Template["kind"], "/", name)
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("build: %s and %s both render %s %q; rename one",
				describeNode(prev), describeNode(n), n.Template["kind"], name)
		}
		seen[key] = n
	}
	return nil
}

// describeNode names the object a node stands for, for error messages.
func describeNode(n GraphNode) string {
	obj := n.Template
	if obj == nil {
		obj = n.Ref
	}
	if obj == nil {
		obj = n.Patch
	}
	kind, _ := obj["kind"].(string)
	meta, _ := obj["metadata"].(map[string]interface{})
	labels, _ := meta["labels"].(map[string]interface{})
	env, _ := labels["kardinal.io/environment"].(string)
	switch {
	case kind == "PolicyGate":
		gate, _ := labels["kardinal.io/gate-template"].(string)
		return fmt.Sprintf("PolicyGate %q for environment %q", gate, env)
	case env != "":
		return fmt.Sprintf("%s for environment %q", kind, env)
	case kind != "":
		name, _ := meta["name"].(string)
		return fmt.Sprintf("%s %q", kind, name)
	}
	return fmt.Sprintf("node %q", n.ID)
}

// validateInput rejects Pipelines and Bundles whose names the Graph cannot
// carry: every name ends up in a label value, and environment names end up
// in object names. Gate names are checked by validateGateNames,
// only for the gates this Graph uses.
func validateInput(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) error {
	if errs := validation.IsValidLabelValue(pipeline.Name); len(errs) > 0 {
		return fmt.Errorf("build: pipeline name %q cannot be used as a label value (%s); "+
			"use at most %d characters so rollback Bundle names fit too",
			pipeline.Name, strings.Join(errs, "; "), maxPipelineNameHint)
	}
	if errs := validation.IsValidLabelValue(bundle.Name); len(errs) > 0 {
		return fmt.Errorf("build: bundle name %q cannot be used as a label value: %s",
			bundle.Name, strings.Join(errs, "; "))
	}
	if err := ValidateBundleArtifacts(&bundle.Spec); err != nil {
		return fmt.Errorf("build: bundle %q: %w", bundle.Name, err)
	}
	return validateEnvironments(pipeline.Spec.Environments)
}

// ValidateBundleArtifacts checks that a Bundle carries what its type needs:
// at least one image for image and mixed, and configRef.commitSHA for config
// and mixed. An empty type counts as image, the default of the Bundle API and
// kardinal create bundle. The Bundle API, the CLI (lifecycle.ValidateNewBundle)
// and Build share this rule, so a Bundle with nothing to promote made with
// kubectl fails before the first environment instead of "succeeding" in every
// one with no images to update (#1285).
func ValidateBundleArtifacts(spec *kardinalv1alpha1.BundleSpec) error {
	typ := spec.Type
	if typ == "" {
		typ = "image"
	}
	needImages := typ == "image" || typ == "mixed"
	needConfig := typ == "config" || typ == "mixed"
	if needImages && len(spec.Images) == 0 {
		return fmt.Errorf("type %q requires at least one entry in images", typ)
	}
	if needConfig && (spec.ConfigRef == nil || spec.ConfigRef.CommitSHA == "") {
		return fmt.Errorf("type %q requires configRef.commitSHA", typ)
	}
	if typ == "chart" && (spec.Chart == nil || spec.Chart.Name == "" || spec.Chart.Version == "") {
		return fmt.Errorf("type %q requires chart.name and chart.version", typ)
	}
	return nil
}

// validateGateNames rejects a gate this Graph instantiates whose name cannot
// be copied into the kardinal.io/gate-template label of its instance. The
// PolicyGate CRD already refuses such names at creation; this is the backstop
// for gates created before that rule. Only the gates placed in this Graph are
// checked, so one bad gate fails only the Pipelines it applies to, not every
// Pipeline that reads its namespace.
func validateGateNames(envs []string, gatesByEnv map[string][]kardinalv1alpha1.PolicyGate,
	skipGates map[string][]skipPermissionGate) error {
	check := func(g kardinalv1alpha1.PolicyGate, env string) error {
		if errs := validation.IsValidLabelValue(g.Name); len(errs) > 0 {
			return fmt.Errorf("build: PolicyGate %q in namespace %q applies to environment %q, but its name "+
				"cannot be copied into the kardinal.io/gate-template label of the gate instance (%s); "+
				"recreate the gate with a name of at most %d characters",
				g.Name, g.Namespace, env, strings.Join(errs, "; "), validation.LabelValueMaxLength)
		}
		return nil
	}
	for _, env := range envs {
		for _, g := range gatesByEnv[env] {
			if err := check(g, env); err != nil {
				return err
			}
		}
		for _, sg := range skipGates[env] {
			if err := check(sg.gate, env); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateEnvironments checks the environment names, regions and steps.
func validateEnvironments(envs []kardinalv1alpha1.EnvironmentSpec) error {
	names := make(map[string]bool, len(envs))
	for _, e := range envs {
		if e.Name == "" {
			return fmt.Errorf("build: an environment has no name")
		}
		if errs := validation.IsValidLabelValue(e.Name); len(errs) > 0 {
			return fmt.Errorf("build: environment name %q cannot be used as a label value: %s",
				e.Name, strings.Join(errs, "; "))
		}
		if names[e.Name] {
			return fmt.Errorf("build: environment %q is declared twice", e.Name)
		}
		names[e.Name] = true
		// One region names nothing Build uses; two or more would push the same
		// change to the same branch once per region.
		if len(e.Regions) >= 2 { //nolint:staticcheck // SA1019: read to reject it
			return fmt.Errorf("build: environment %q: %s", e.Name, RegionsNotSupported)
		}
		// Refuse a custom step sequence instead of silently ignoring the steps
		// the author asked for.
		if msg := customStepsUnimplemented(&e); msg != "" {
			return fmt.Errorf("build: %s", msg)
		}
	}
	return nil
}

// validateSkipNames rejects intent.skipEnvironments entries that name no
// environment, so a typo does not silently promote through the environment
// the author meant to skip or pass a skip-permission check it would fail.
func validateSkipNames(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) error {
	if bundle.Spec.Intent == nil {
		return nil
	}
	known := make(map[string]bool, len(pipeline.Spec.Environments))
	fleets := map[string]bool{}
	for _, e := range pipeline.Spec.Environments {
		known[e.Name] = true
		if e.Fleet != nil {
			fleets[e.Name] = true
		}
	}
	var unknown []string
	for _, s := range bundle.Spec.Intent.SkipEnvironments {
		if fleets[s] {
			return fmt.Errorf("build: intent.skipEnvironments names fleet environment %q; skipping a fleet is not supported "+
				"(use intent.targetEnvironment to stop before it)", s)
		}
		if !known[s] {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("build: intent.skipEnvironments names unknown environments %v", unknown)
	}
	return nil
}

// ValidateSecretRef refuses a spec.git.secretRef in another namespace than the
// Pipeline's (C03-promotionstep-18). This is deliberate, not a missing
// feature: the controller can read Secrets in every namespace and sends the
// token to spec.git.url, which the same author controls, so a Pipeline could
// otherwise use another namespace's credentials. An empty Pipeline namespace
// fails closed. The PromotionStep reconciler refuses the step with this error,
// the Pipeline reconciler sets Ready=False/ValidationFailed and "kardinal
// validate" reports it.
func ValidateSecretRef(p *kardinalv1alpha1.Pipeline) error {
	if ref := p.Spec.Git.SecretRef; ref != nil && ref.Namespace != "" && ref.Namespace != p.Namespace {
		return fmt.Errorf("git.secretRef.namespace %q is not allowed: the Secret must be in the Pipeline's namespace %q",
			ref.Namespace, p.Namespace)
	}
	return nil
}

// ValidateUpdateStrategy refuses update.strategy argocd with approval
// pr-review (#1281): argocd patches the Argo CD Application directly, with no
// Git commit and so no PR to review. The Pipeline CRD rejects the combination
// at apply time; this check covers Pipelines stored before that rule. The
// Pipeline reconciler sets Ready=False/ValidationFailed and "kardinal
// validate" reports it. The argocd-set-image step refuses it too.
func ValidateUpdateStrategy(p *kardinalv1alpha1.Pipeline) error {
	for _, e := range p.Spec.Environments {
		if e.Update.Strategy == "argocd" && e.Approval == "pr-review" {
			return fmt.Errorf("environment %q: update.strategy argocd patches the Application directly and "+
				"cannot honour approval: pr-review; use approval: auto with a PolicyGate, or a git-based "+
				"strategy (kustomize or helm) for a reviewed promotion", e.Name)
		}
	}
	return nil
}

// validateBundleStrategy fails a config or mixed Bundle when an environment it
// promotes uses update.strategy argocd (#1281). argocd only sets the image in
// the Argo CD Application, so the Bundle's Git config change would be
// skipped. Failing at build stops the Bundle before its first environment,
// even when only a later one uses argocd.
//
// A chart Bundle needs update.strategy helm in every environment it promotes:
// only helm-set-image writes the chart version.
func validateBundleStrategy(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle, envs []string) error {
	promoted := make(map[string]bool, len(envs))
	for _, name := range envs {
		promoted[name] = true
	}
	// The promoted environments' specs: a fleet target has its fleet's.
	specs := make([]kardinalv1alpha1.EnvironmentSpec, 0, len(envs))
	for _, name := range envs {
		specs = append(specs, findEnvSpec(pipeline, name))
	}
	if bundle.Spec.Type == "chart" {
		for _, e := range specs {
			if promoted[e.Name] && e.Update.Strategy != "helm" {
				strategy := e.Update.Strategy
				if strategy == "" {
					strategy = "kustomize"
				}
				return fmt.Errorf("build: environment %q uses update.strategy %s, which cannot promote a chart "+
					"Bundle: only update.strategy helm writes the chart version (update.helm.chartVersionFile and "+
					"chartVersionPath); set it for that environment, or skip it with intent.skipEnvironments",
					e.Name, strategy)
			}
		}
		return nil
	}
	if bundle.Spec.Type != "config" && bundle.Spec.Type != "mixed" {
		return nil
	}
	for _, e := range specs {
		if promoted[e.Name] && e.Update.Strategy == "argocd" {
			return fmt.Errorf("build: environment %q uses update.strategy argocd, which does not support %s "+
				"Bundles: it sets only the image in the Argo CD Application and would skip the config change; "+
				"use a git-based strategy (kustomize or helm) for that environment, or skip it with "+
				"intent.skipEnvironments", e.Name, bundle.Spec.Type)
		}
	}
	return nil
}

// ValidateCIRunURL checks a Bundle's spec.provenance.ciRunURL, which CI sets
// and the PR body and the UI render as a link. It must be empty or an absolute
// http or https URL with a host, without user info (credentials would be
// published in every PR body) and without whitespace or control characters.
// The bundle API refuses to create a Bundle that fails it. Bundles created
// another way, or before this check, can still hold such a URL: promote and
// rollback drop it when they copy provenance, and the PR body and the UI do
// not render it. Errors do not echo the URL or any part of it.
func ValidateCIRunURL(raw string) error {
	if raw == "" {
		return nil
	}
	if strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("provenance.ciRunURL must not contain whitespace or control characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Not wrapped: url.Parse errors quote the URL or parts of it, such as
		// the "port" of https://user:token/x.
		return fmt.Errorf("provenance.ciRunURL is not a valid URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("provenance.ciRunURL must be an absolute http or https URL")
	}
	if u.User != nil {
		return fmt.Errorf("provenance.ciRunURL must not contain user info")
	}
	return nil
}
