// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"crypto/sha1" //nolint:gosec // SHA-1 used for content addressing only, not cryptographic security
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// BuildInput contains everything needed to generate a Graph spec.
type BuildInput struct {
	// Pipeline is the user-authored promotion topology.
	Pipeline *kardinalv1alpha1.Pipeline

	// Bundle is the artifact to promote, with intent.
	Bundle *kardinalv1alpha1.Bundle

	// PolicyGates contains all gates from all policy namespaces + pipeline namespace.
	PolicyGates []kardinalv1alpha1.PolicyGate
}

// BuildResult is the output of the graph builder.
type BuildResult struct {
	// Graph is the generated Graph CR (not yet written to Kubernetes).
	Graph *Graph
	// NodeCount is the total number of nodes generated.
	NodeCount int
}

// Builder generates Graph specs from Pipeline + Bundle + PolicyGates.
// It implements the full translation algorithm from
// docs/design/02-pipeline-to-graph-translator.md.
type Builder struct{}

// NewBuilder creates a new Builder.
func NewBuilder() *Builder {
	return &Builder{}
}

// Build generates a Graph spec. Returns an error if the Pipeline is invalid
// (circular deps, unknown target env, skip denied, etc.).
// Does NOT write to Kubernetes.
func (b *Builder) Build(input BuildInput) (*BuildResult, error) {
	if input.Pipeline == nil {
		return nil, fmt.Errorf("build: pipeline is nil")
	}
	if input.Bundle == nil {
		return nil, fmt.Errorf("build: bundle is nil")
	}

	// Step 1: resolve environment ordering
	orderedEnvs, deps, err := resolveOrdering(input.Pipeline)
	if err != nil {
		return nil, err
	}

	// Step 2: filter environments by Bundle intent
	filteredEnvs, err := filterByIntent(orderedEnvs, deps, input.Bundle)
	if err != nil {
		return nil, err
	}
	if len(filteredEnvs) == 0 {
		return nil, fmt.Errorf("build: all environments skipped")
	}

	// Step 3: validate skip permissions — REMOVED from Build().
	// Skip-permission validation is now done by the caller (Translator.Translate)
	// before calling Build(), so the result is written to Bundle.status by the
	// Bundle reconciler (Graph-first: validation result flows through CRD status).
	// See docs/design/11-graph-purity-tech-debt.md GB-2.

	// Step 4: collect and match PolicyGates by environment
	gatesByEnv := matchGatesByEnv(filteredEnvs, input.PolicyGates)

	// Step 5 & 6: build nodes and wire edges
	nodes, err := buildNodes(input.Pipeline, input.Bundle, filteredEnvs, deps, gatesByEnv)
	if err != nil {
		return nil, err
	}

	// Step 7: assemble Graph
	g := assembleGraph(input.Pipeline, input.Bundle, nodes)

	return &BuildResult{
		Graph:     g,
		NodeCount: len(nodes),
	}, nil
}

// --- Step 1: resolve environment ordering ---

// resolveOrdering reads spec.environments, builds the dependency map,
// and returns the topologically sorted environment names.
func resolveOrdering(pipeline *kardinalv1alpha1.Pipeline) ([]string, map[string][]string, error) {
	envs := pipeline.Spec.Environments
	if len(envs) == 0 {
		return nil, nil, fmt.Errorf("build: pipeline has no environments")
	}

	// Build name set and dependency map
	nameSet := make(map[string]bool, len(envs))
	for _, e := range envs {
		nameSet[e.Name] = true
	}

	// Expand wave topology (K-06): if any environment has Wave > 0, build
	// wave-derived dependsOn edges before falling through to the default logic.
	// Wave N environments depend on ALL wave-(N-1) environments. Wave edges are
	// unioned with any explicit DependsOn entries on the same environment.
	waveDeps := expandWaveDeps(envs)

	deps := make(map[string][]string, len(envs)) // env → []dependsOn
	for i, e := range envs {
		// Start with any wave-derived edges.
		merged := append([]string(nil), waveDeps[e.Name]...)
		// Union with explicit DependsOn.
		for _, dep := range e.DependsOn {
			if !nameSet[dep] {
				return nil, nil, fmt.Errorf("build: environment %q dependsOn unknown environment %q",
					e.Name, dep)
			}
			if !containsStr(merged, dep) {
				merged = append(merged, dep)
			}
		}

		switch {
		case len(merged) > 0:
			deps[e.Name] = merged
		case i > 0 && e.Wave == 0:
			// Default: depends on previous in list (only when wave is not used)
			deps[e.Name] = []string{envs[i-1].Name}
		default:
			deps[e.Name] = nil
		}
	}

	// Topological sort (Kahn's algorithm) to detect cycles
	sorted, err := topoSort(nameSet, deps)
	if err != nil {
		return nil, nil, err
	}

	return sorted, deps, nil
}

// topoSort performs Kahn's topological sort on the dependency graph.
// Returns an error if a cycle is detected.
func topoSort(nodes map[string]bool, deps map[string][]string) ([]string, error) {
	// Compute in-degree
	inDegree := make(map[string]int, len(nodes))
	for n := range nodes {
		inDegree[n] = 0
	}
	for _, depsFor := range deps {
		_ = depsFor
	}
	// Build reverse map: node → dependents (nodes that depend on it)
	dependents := make(map[string][]string, len(nodes))
	for n, ds := range deps {
		for _, d := range ds {
			dependents[d] = append(dependents[d], n)
			inDegree[n]++
		}
	}

	// Start with nodes that have no prerequisites
	var queue []string
	for n := range nodes {
		if inDegree[n] == 0 {
			queue = append(queue, n)
		}
	}
	sortStrings(queue) // deterministic order

	var sorted []string
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		sorted = append(sorted, n)
		next := dependents[n]
		sortStrings(next)
		for _, d := range next {
			inDegree[d]--
			if inDegree[d] == 0 {
				queue = append(queue, d)
			}
		}
	}

	if len(sorted) != len(nodes) {
		return nil, fmt.Errorf("build: circular dependency detected in pipeline environments")
	}
	return sorted, nil
}

// --- Step 2: filter environments by Bundle intent ---

func filterByIntent(orderedEnvs []string, deps map[string][]string,
	bundle *kardinalv1alpha1.Bundle) ([]string, error) {
	if bundle.Spec.Intent == nil {
		return orderedEnvs, nil
	}

	result := make([]string, len(orderedEnvs))
	copy(result, orderedEnvs)

	// Apply targetEnvironment: keep only envs up to and including target
	if target := bundle.Spec.Intent.TargetEnvironment; target != "" {
		found := false
		for _, e := range orderedEnvs {
			if e == target {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("build: unknown target environment %q", target)
		}
		// Keep all envs that are on any path leading to target
		result = envPathTo(orderedEnvs, deps, target)
	}

	// Apply skipEnvironments
	for _, skip := range bundle.Spec.Intent.SkipEnvironments {
		result = removeEnv(result, skip)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("build: all environments skipped")
	}
	return result, nil
}

// envPathTo returns all envs on the path from the first env to target (inclusive).
// Uses a simple reachability walk on the deps graph.
func envPathTo(orderedEnvs []string, deps map[string][]string, target string) []string {
	// Find all ancestors of target (including target itself)
	ancestors := make(map[string]bool)
	var walk func(e string)
	walk = func(e string) {
		if ancestors[e] {
			return
		}
		ancestors[e] = true
		for _, dep := range deps[e] {
			walk(dep)
		}
	}
	walk(target)

	// Return envs in original order, keeping only ancestors
	var result []string
	for _, e := range orderedEnvs {
		if ancestors[e] {
			result = append(result, e)
		}
	}
	return result
}

// removeEnv removes an environment by name from the slice.
func removeEnv(envs []string, name string) []string {
	result := make([]string, 0, len(envs))
	for _, e := range envs {
		if e != name {
			result = append(result, e)
		}
	}
	return result
}

// --- Step 3: validate skip permissions ---

// ValidateSkipPermissions checks whether the Bundle's intent to skip environments
// is permitted by the org-level PolicyGates. Returns an error if any skip is denied.
//
// This function was previously called inside Build() which made the check invisible
// to the Graph. It is now exported so callers (Translator) can call it before Build(),
// allowing the Bundle reconciler to write the result to Bundle.status (Graph-first).
// See docs/design/11-graph-purity-tech-debt.md GB-2 (elimination in progress).
func ValidateSkipPermissions(pipeline *kardinalv1alpha1.Pipeline,
	bundle *kardinalv1alpha1.Bundle, allGates []kardinalv1alpha1.PolicyGate) error {
	if bundle.Spec.Intent == nil {
		return nil
	}
	for _, skip := range bundle.Spec.Intent.SkipEnvironments {
		// Check if any org gate applies to this environment
		hasOrgGate := false
		for _, g := range allGates {
			if g.Labels["kardinal.io/scope"] == "org" && appliesToEnv(g, skip) {
				hasOrgGate = true
				break
			}
		}
		if !hasOrgGate {
			// No org gate → skip is allowed without permission check
			continue
		}
		// Org gate exists — look for a SkipPermission gate
		permitted := false
		for _, g := range allGates {
			if g.Labels["kardinal.io/type"] == "skip-permission" &&
				appliesToEnv(g, skip) && g.Spec.SkipPermission {
				permitted = true
				break
			}
		}
		if !permitted {
			return fmt.Errorf("build: skip denied for environment %q: "+
				"org-level gate applies and no skip-permission gate allows it", skip)
		}
	}
	return nil
}

// appliesToEnv returns true if the gate's kardinal.io/applies-to label
// contains the given environment name.
func appliesToEnv(gate kardinalv1alpha1.PolicyGate, envName string) bool {
	appliesTo := gate.Labels["kardinal.io/applies-to"]
	if appliesTo == "" {
		return false
	}
	for _, e := range strings.Split(appliesTo, ",") {
		if strings.TrimSpace(e) == envName {
			return true
		}
	}
	return false
}

// --- Step 4: match PolicyGates by environment ---

// matchGatesByEnv returns a map of environmentName → []PolicyGate for gates
// that apply to each environment and have type "gate" (not skip-permission).
func matchGatesByEnv(filteredEnvs []string,
	allGates []kardinalv1alpha1.PolicyGate) map[string][]kardinalv1alpha1.PolicyGate {
	result := make(map[string][]kardinalv1alpha1.PolicyGate)
	envSet := make(map[string]bool, len(filteredEnvs))
	for _, e := range filteredEnvs {
		envSet[e] = true
	}
	for _, g := range allGates {
		// Skip skip-permission type gates
		if g.Labels["kardinal.io/type"] == "skip-permission" {
			continue
		}
		appliesTo := g.Labels["kardinal.io/applies-to"]
		for _, e := range strings.Split(appliesTo, ",") {
			e = strings.TrimSpace(e)
			if envSet[e] {
				result[e] = append(result[e], g)
			}
		}
	}
	return result
}

// --- Step 5 & 6: build nodes and wire edges ---

// buildNodes generates all PromotionStep and PolicyGate Graph nodes in
// dependency order, with correct readyWhen, propagateWhen, and edge fields.
func buildNodes(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	filteredEnvs []string, deps map[string][]string,
	gatesByEnv map[string][]kardinalv1alpha1.PolicyGate) ([]GraphNode, error) {
	// Build env spec map for quick lookup
	envSpecMap := make(map[string]kardinalv1alpha1.EnvironmentSpec)
	for _, e := range pipeline.Spec.Environments {
		envSpecMap[e.Name] = e
	}

	bundleSlug := bundleVersionSlug(bundle.Name) // CEL-safe (underscores) — used in node IDs + gateNodeName
	bundleSlugK8s := slugify(bundle.Name)        // K8s-safe (hyphens) — used in metadata.name only
	pipelineName := pipeline.Name

	// Filter deps to only include filtered envs
	filteredSet := make(map[string]bool, len(filteredEnvs))
	for _, e := range filteredEnvs {
		filteredSet[e] = true
	}

	var nodes []GraphNode

	for _, envName := range filteredEnvs {
		envSpec := envSpecMap[envName]

		// Compute upstream deps for this env (filtered to only include surviving envs)
		// Return as CEL-safe IDs (matching the step node IDs built with celSafeSlug).
		rawUpstreams := filteredDeps(envName, deps, filteredSet)
		upstreams := make([]string, len(rawUpstreams))
		for i, up := range rawUpstreams {
			upstreams[i] = celSafeSlug(up)
		}

		// PolicyGate nodes for this environment
		gates := gatesByEnv[envName]
		gateNodeIDs := make([]string, 0, len(gates))
		for _, gate := range gates {
			gateNodeID := gateNodeName(pipelineName, bundleSlug, gate.Name, gate.Namespace, envName)
			gateNodeK8s := gateNodeK8sName(bundleSlugK8s, gate.Name, gate.Namespace, envName)
			gateNodeIDs = append(gateNodeIDs, gateNodeID)

			gateNode := buildPolicyGateNode(gateNodeID, gateNodeK8s, gate, pipelineName, bundle.Name, envName, upstreams)
			nodes = append(nodes, gateNode)
		}

		// PRStatus Watch node — created alongside each PromotionStep.
		// The open-pr step writes this CRD; the PRStatusReconciler updates status.merged.
		// The PromotionStep spec carries the prStatusRef so it can watch it without polling.
		stepNodeID := celSafeSlug(envName)
		prStatusNodeID := prStatusNodeName(bundleSlug, envName)
		prStatusK8sName := prStatusNodeK8sName(bundleSlugK8s, envName)
		prStatusNode := buildPRStatusNode(prStatusNodeID, prStatusK8sName, pipelineName, bundle.Name, envName)
		nodes = append(nodes, prStatusNode)

		// PromotionStep node — node ID must be a valid CEL identifier
		stepNode := buildPromotionStepNode(
			pipelineName, bundleSlugK8s, envName, stepNodeID, envSpec, bundle, upstreams, gateNodeIDs, prStatusNodeID,
		)
		nodes = append(nodes, stepNode)
	}

	return nodes, nil
}

// filteredDeps returns the upstream dependencies of envName, filtered to only
// include environments that survived the intent filter.
func filteredDeps(envName string, deps map[string][]string, filteredSet map[string]bool) []string {
	// Collect all transitively reachable upstreams that are in filteredSet
	var result []string
	seen := make(map[string]bool)
	var walk func(e string)
	walk = func(e string) {
		for _, dep := range deps[e] {
			if seen[dep] {
				continue
			}
			seen[dep] = true
			if filteredSet[dep] {
				result = append(result, dep)
			} else {
				// dep was removed (skipped) — check its parents
				walk(dep)
			}
		}
	}
	walk(envName)
	return result
}

// buildPromotionStepNode builds a Graph node for a PromotionStep.
// nodeID is the CEL-safe identifier (underscores) used in readyWhen/propagateWhen.
// k8sName is the Kubernetes resource name (hyphens) for metadata.name.
// prStatusNodeID is the node ID of the companion PRStatus Watch node.
func buildPromotionStepNode(
	pipelineName, bundleSlugK8s, envName, nodeID string,
	envSpec kardinalv1alpha1.EnvironmentSpec,
	bundle *kardinalv1alpha1.Bundle,
	upstreams []string,
	gateNodeIDs []string,
	prStatusNodeID string,
) GraphNode {
	// Determine step type based on bundle type
	stepType := defaultStepType(bundle.Spec.Type)

	// Kubernetes resource name uses hyphens (RFC 1123 subdomain); envName may contain
	// hyphens which are allowed in K8s names.
	k8sResourceName := fmt.Sprintf("%s-%s-%s", pipelineName, bundleSlugK8s, envName)

	// Build the PromotionStep resource template
	templateMeta := map[string]interface{}{
		"name": k8sResourceName,
		"labels": map[string]interface{}{
			"kardinal.io/pipeline":    pipelineName,
			"kardinal.io/bundle":      bundle.Name,
			"kardinal.io/environment": envName,
		},
	}
	if envSpec.Shard != "" {
		labels := templateMeta["labels"].(map[string]interface{})
		labels["kardinal.io/shard"] = envSpec.Shard
	}

	templateSpec := map[string]interface{}{
		"pipelineName": pipelineName,
		"bundleName":   bundle.Name,
		"environment":  envName,
		"stepType":     stepType,
		// prStatusRef points to the companion PRStatus Watch node.
		// The PromotionStep reconciler reads spec.prStatusRef.name to find the
		// PRStatus CRD instead of polling GitHub directly (eliminates PS-4, SCM-2).
		"prStatusRef": fmt.Sprintf("${%s.metadata.name}", prStatusNodeID),
	}

	// Add upstream reference fields (creates CEL dependency edges)
	if len(upstreams) > 0 {
		// For simplicity, reference the first (primary) upstream
		// Fan-in: all upstreams must be Verified, so reference each
		for i, up := range upstreams {
			key := "upstreamVerified"
			if i > 0 {
				key = fmt.Sprintf("upstreamVerified%d", i+1)
			}
			templateSpec[key] = fmt.Sprintf("${%s.status.state}", up)
		}
	}

	// Add required gates reference (creates fan-in edges from gate nodes)
	if len(gateNodeIDs) > 0 {
		gateRefs := make([]interface{}, len(gateNodeIDs))
		for i, gid := range gateNodeIDs {
			gateRefs[i] = fmt.Sprintf("${%s.metadata.name}", gid)
		}
		templateSpec["requiredGates"] = gateRefs
	}

	template := map[string]interface{}{
		"apiVersion": "kardinal.io/v1alpha1",
		"kind":       "PromotionStep",
		"metadata":   templateMeta,
		"spec":       templateSpec,
	}

	stateRef := fmt.Sprintf("${%s.status.state}", nodeID)
	_ = stateRef // used in readyWhen/propagateWhen expressions

	return GraphNode{
		ID:       nodeID,
		Template: template,
		ReadyWhen: []string{
			fmt.Sprintf(`${%s.status.state == "Verified"}`, nodeID),
		},
		PropagateWhen: []string{
			fmt.Sprintf(`${%s.status.state == "Verified"}`, nodeID),
		},
	}
}

// buildPolicyGateNode builds a Graph node for a PolicyGate instance.
// nodeID is the CEL-safe identifier (underscores) used in readyWhen/propagateWhen.
// k8sName is the Kubernetes resource name (hyphens) for metadata.name.
func buildPolicyGateNode(
	nodeID, k8sName string,
	gate kardinalv1alpha1.PolicyGate,
	pipelineName, bundleName, envName string,
	upstreams []string,
) GraphNode {
	// Propagate scope and applies-to from the gate template so that
	// `kardinal policy list` can show the correct scope (org/team) and
	// applies-to value on the instantiated PolicyGate CRs (#249).
	scopeLabel := gate.Labels["kardinal.io/scope"]
	if scopeLabel == "" {
		scopeLabel = "team"
	}
	appliesToLabel := gate.Labels["kardinal.io/applies-to"]

	// kardinal.io/gate-name holds the user-defined gate name from the original
	// PolicyGate template (e.g. "no-weekend-deploys"). This is propagated through
	// cross-product instantiations so the UI can deduplicate by the human-readable
	// gate name rather than the long cross-product instance name.
	// If the input gate already has a gate-name label (cross-product case), inherit it;
	// otherwise use the gate's own name (direct template case).
	gateName := gate.Labels["kardinal.io/gate-name"]
	if gateName == "" {
		gateName = gate.Name
	}

	templateMeta := map[string]interface{}{
		"name": k8sName, // K8s resource name (RFC 1123 subdomain — hyphens allowed, no underscores)
		"labels": map[string]interface{}{
			// These labels allow `kardinal explain` and the PolicyGate reconciler
			// to query instances by pipeline, bundle, and environment.
			"kardinal.io/pipeline":      pipelineName,
			"kardinal.io/bundle":        bundleName,
			"kardinal.io/environment":   envName,
			"kardinal.io/gate-template": gate.Name,
			// gate-name: stable human-readable name, propagated through cross-product instances.
			"kardinal.io/gate-name": gateName,
			// Propagated from original PolicyGate template for CLI display.
			"kardinal.io/scope":      scopeLabel,
			"kardinal.io/applies-to": appliesToLabel,
		},
	}
	// Add upstream DAG edge annotation to maintain krocodile dependency ordering.
	// krocodile's collectStrings() scans all string values in the template recursively,
	// so placing the ${upstream.status.state} reference in an annotation still creates
	// the DAG edge from the upstream PromotionStep to this PolicyGate node (#618).
	// Using an annotation instead of spec.upstreamEnvironment avoids user confusion:
	// users reading PolicyGate spec no longer see controller-internal wiring.
	if len(upstreams) > 0 {
		templateAnnotations := map[string]interface{}{
			"kardinal.io/upstream-ref": fmt.Sprintf("${%s.status.state}", upstreams[0]),
		}
		templateMeta["annotations"] = templateAnnotations
	}

	templateSpec := map[string]interface{}{
		"expression":      gate.Spec.Expression,
		"message":         gate.Spec.Message,
		"recheckInterval": gate.Spec.RecheckInterval,
	}
	// Note: upstreamEnvironment removed from spec (#618).
	// The upstream DAG edge is maintained via the metadata annotation below.

	return GraphNode{
		ID: nodeID,
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "PolicyGate",
			"metadata":   templateMeta,
			"spec":       templateSpec,
		},
		ReadyWhen: []string{
			fmt.Sprintf(`${%s.status.ready == true}`, nodeID),
		},
		PropagateWhen: []string{
			fmt.Sprintf(`${%s.status.ready == true}`, nodeID),
		},
	}
}

// buildPRStatusNode builds a Graph Watch node for a PRStatus CRD.
//
// The PRStatus CRD is created as a placeholder by the Graph; the open-pr step
// populates spec.prURL, spec.prNumber, spec.repo after opening the PR.
// The PRStatus reconciler monitors GitHub and sets status.merged = true.
//
// This node uses readyWhen (not propagateWhen) so it does NOT block downstream
// PromotionSteps. The PromotionStep reconciler checks prStatusRef.status.merged
// in its own WaitingForMerge state. Using propagateWhen would create a circular
// dependency: PromotionStep references prstatus.metadata.name (creating a dep),
// but if prstatus.propagateWhen == false, the PromotionStep could never start.
//
// Graph-purity: this node provides observable PR merge state for the UI and
// for the PromotionStep reconciler (eliminates direct GitHub API polling PS-4, SCM-2).
func buildPRStatusNode(nodeID, k8sName, pipelineName, bundleName, envName string) GraphNode {
	templateMeta := map[string]interface{}{
		// The name is set at runtime by the open-pr step; the Graph creates a
		// placeholder. The PRStatus reconciler then updates its status.
		"name": k8sName,
		"labels": map[string]interface{}{
			"kardinal.io/pipeline":    pipelineName,
			"kardinal.io/bundle":      bundleName,
			"kardinal.io/environment": envName,
		},
	}

	templateSpec := map[string]interface{}{
		// prURL, prNumber, repo will be written by the open-pr step after the
		// PR is created. The Graph creates the CRD with empty spec; the step
		// patches spec once it has the GitHub response.
		"prURL":    "",
		"prNumber": 0,
		"repo":     "",
	}

	return GraphNode{
		ID: nodeID,
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "PRStatus",
			"metadata":   templateMeta,
			"spec":       templateSpec,
		},
		// ReadyWhen: health signal only — green in UI when PR is merged.
		// NOT propagateWhen: the PromotionStep references this node's metadata.name
		// which creates a dependency edge. Adding propagateWhen would block the
		// PromotionStep from starting (circular: PS can't start → PR never opened
		// → PRStatus never merged → propagateWhen never true → PS never starts).
		ReadyWhen: []string{
			fmt.Sprintf(`${%s.status.merged == true}`, nodeID),
		},
		// PropagateWhen is intentionally omitted — always propagates (unblocked).
		// Merge-gate is enforced by the PromotionStep WaitingForMerge state machine.
	}
}

// --- Step 7: assemble Graph ---

func assembleGraph(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	nodes []GraphNode) *Graph {
	graphName := graphNameFrom(pipeline.Name, bundle.Name)
	isController := true

	return &Graph{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "experimental.kro.run/v1alpha1",
			Kind:       "Graph",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      graphName,
			Namespace: pipeline.Namespace,
			Labels: map[string]string{
				"kardinal.io/pipeline": pipeline.Name,
				"kardinal.io/bundle":   bundle.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "kardinal.io/v1alpha1",
					Kind:       "Bundle",
					Name:       bundle.Name,
					UID:        bundle.UID,
					Controller: &isController,
				},
			},
		},
		Spec: GraphSpec{
			Nodes: nodes,
		},
	}
}

// --- naming helpers ---

// GraphNameFrom returns the deterministic Graph CR name for a (pipeline, bundle) pair.
// This is exported so the Bundle reconciler can compute the expected graph name
// without importing translator-layer code.
func GraphNameFrom(pipeline, bundle string) string {
	return graphNameFrom(pipeline, bundle)
}

// graphNameFrom generates a Graph name from the pipeline and bundle names.
// Both names are slugified (lowercased, non-alphanumeric → dash).
// Truncates to 63 chars (Kubernetes name limit).
func graphNameFrom(pipeline, bundle string) string {
	slug := slugify(pipeline) + "-" + slugify(bundle)
	if len(slug) > 63 {
		slug = slug[:59] + "-" + slug[len(slug)-3:]
	}
	if len(slug) > 63 {
		slug = slug[:63]
	}
	return slug
}

// gateNodeName generates a unique node ID for a PolicyGate instance.
// The ID is used as both the Kubernetes resource name (metadata.name) and as
// the CEL variable name in readyWhen/propagateWhen expressions. CEL identifiers
// must not contain hyphens, so we use camelCase (see celSafeSlug).
// Includes namespace to prevent collisions when same gate name exists in
// multiple namespaces.
//
// Components are slugged individually with celSafeSlug then joined with digit
// separators ("0" between parts, "00" before the bundle suffix). Digit separators
// survive camelCase without creating word boundaries (digits don't trigger
// capitalisation), preserving uniqueness across different (name, ns, env, bundle)
// combinations.
//
// krocodile (e082fe9+, PR #109) validates node IDs via IsDNS1123Label(strings.ToLower(id)),
// which enforces a 63-character limit per DNS label segment. If the full composed ID
// exceeds this limit, it is truncated to 54 chars and an 8-char SHA-1 hash suffix
// is appended (54 + "0" + 8 = 63 chars), preserving uniqueness.
func gateNodeName(pipeline, bundleSlug, gateName, gateNS, envName string) string {
	ns := gateNS
	if ns == "" {
		ns = "default"
	}
	// Slug each component individually, then join with digit separators so that
	// two components whose camelCase forms would otherwise collide remain distinct.
	// Example: gateName="a", ns="bC", env="d" → "a0bC0d00<bundle>"
	//          gateName="aB", ns="c", env="d" → "aB0c0d00<bundle>"  (no collision)
	full := celSafeSlug(gateName) + "0" +
		celSafeSlug(ns) + "0" +
		celSafeSlug(envName) + "00" +
		bundleSlug
	return truncateNodeID(full)
}

// gateNodeK8sName generates the Kubernetes resource name (hyphens, RFC 1123) for a
// PolicyGate instance. Separate from gateNodeName which returns the CEL-safe ID.
func gateNodeK8sName(bundleSlugK8s, gateName, gateNS, envName string) string {
	ns := gateNS
	if ns == "" {
		ns = "default"
	}
	return slugify(fmt.Sprintf("%s-%s-%s--%s", gateName, ns, envName, bundleSlugK8s))
}

// bundleVersionSlug returns a CEL-safe slug from the bundle name for use in node IDs.
//
// Dual-slug convention (GB-3/GB-4 in docs/design/11-graph-purity-tech-debt.md):
//   - celSafeSlug / bundleVersionSlug → camelCase, valid CEL identifiers AND DNS labels
//     Used in: node IDs, readyWhen/propagateWhen CEL expressions, gateNodeName
//   - slugify → hyphens, valid Kubernetes resource names
//     Used in: metadata.name fields only
//
// Always use celSafeSlug for IDs appearing in CEL expressions.
// Always use slugify for Kubernetes resource names.
func bundleVersionSlug(bundleName string) string {
	return celSafeSlug(bundleName)
}

// slugify replaces characters not valid in Kubernetes names with dashes.
// Produces kebab-case (hyphens) suitable for metadata.name fields.
// Do NOT use for CEL expression identifiers — use celSafeSlug instead.
// See dual-slug convention in bundleVersionSlug doc comment.
func slugify(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-':
			b.WriteRune(c)
		case c >= 'A' && c <= 'Z':
			b.WriteRune(c - 'A' + 'a')
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// truncateNodeID ensures a graph node ID fits within the 63-character DNS label
// limit enforced by krocodile (e082fe9+, PR #109) via IsDNS1123Label(strings.ToLower(id)).
//
// When the ID is short enough it is returned unchanged. When it exceeds 63 chars,
// the first 54 characters are kept and an 8-hex-char SHA-1 digest of the full ID
// is appended with a "0" separator: 54 + "0" + 8 = 63 chars exactly.
// The hash preserves uniqueness — two different long IDs that share the same 54-char
// prefix will have different hash suffixes.
func truncateNodeID(id string) string {
	const maxLen = 63
	const prefixLen = 54
	if len(id) <= maxLen {
		return id
	}
	h := sha1.New() //nolint:gosec
	h.Write([]byte(id))
	return id[:prefixLen] + "0" + fmt.Sprintf("%x", h.Sum(nil))[:8]
}

// celSafeSlug creates an identifier safe for use as both a CEL variable name
// and as a krocodile graph node ID.
//
// krocodile (e082fe9+) embeds node IDs into Kubernetes label key prefixes using
// the format "<nodeID>.<graphName>.<namespace>.internal.kro.run/reference".
// Kubernetes label key prefixes must be valid RFC 1123 DNS subdomains, which
// means each dot-separated segment may only contain [a-z0-9-] — no underscores.
// krocodile calls strings.ToLower(nodeID) when constructing the prefix, so the
// result of celSafeSlug must contain no underscores or hyphens after lowercasing.
//
// CEL identifier rules additionally forbid hyphens (only [a-zA-Z0-9_] allowed).
//
// Solution: camelCase. Non-alphanumeric characters (hyphens, underscores, dots,
// etc.) become word boundaries — the following letter is capitalised and the
// separator is dropped. This satisfies both constraints simultaneously:
//   - Valid CEL identifier: [a-zA-Z][a-zA-Z0-9]* ✓
//   - Valid DNS label after strings.ToLower(): [a-z0-9]+ ✓
//
// Examples:
//
//	"kardinal-test-app-uat"  → "kardinalTestAppUat"
//	"no_weekend_deploys"     → "noWeekendDeploys"
//	"prod-eu"                → "prodEu"
//	"0bad"                   → "x0bad"  (leading digit guarded by "x" prefix)
//	"MyApp"                  → "myApp"  (leading uppercase lowercased)
//
// IMPORTANT: This function exists in two places and must be kept identical:
//   - pkg/graph/builder.go
//   - pkg/translator/translator.go
func celSafeSlug(s string) string {
	var b strings.Builder
	upperNext := false
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
			if upperNext {
				b.WriteRune(c - 'a' + 'A')
				upperNext = false
			} else {
				b.WriteRune(c)
			}
		case c >= 'A' && c <= 'Z':
			switch {
			case b.Len() == 0:
				b.WriteRune(c - 'A' + 'a') // first char always lowercase
			case upperNext:
				b.WriteRune(c) // already upper — preserve
				upperNext = false
			default:
				b.WriteRune(c)
			}
		case c >= '0' && c <= '9':
			if b.Len() == 0 {
				b.WriteString("x") // guard leading digit with a safe prefix
			}
			b.WriteRune(c)
			upperNext = false
		default:
			// hyphens, underscores, spaces, dots → camelCase word boundary
			upperNext = true
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

// defaultStepType returns the primary step type for the given bundle type.
func defaultStepType(bundleType string) string {
	switch bundleType {
	case "config":
		return "config-merge"
	default:
		return "kustomize-set-image"
	}
}

// sortStrings is a simple in-place sort for small slices (avoids import of sort package).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// expandWaveDeps builds wave-derived dependency edges (K-06).
// For each environment with Wave > 0, it produces edges to ALL environments
// that have Wave == (this.Wave - 1). If no environments use Wave, returns an
// empty map and the caller falls through to the default sequential logic.
func expandWaveDeps(envs []kardinalv1alpha1.EnvironmentSpec) map[string][]string {
	waveDeps := make(map[string][]string, len(envs))

	// Index environments by wave number.
	byWave := make(map[int][]string)
	for _, e := range envs {
		if e.Wave > 0 {
			byWave[e.Wave] = append(byWave[e.Wave], e.Name)
		}
	}
	if len(byWave) == 0 {
		return waveDeps // no wave topology — caller uses default logic
	}

	for _, e := range envs {
		if e.Wave <= 1 {
			continue // wave 1 (or non-wave) has no predecessors via wave
		}
		prev := byWave[e.Wave-1]
		if len(prev) == 0 {
			continue
		}
		sorted := append([]string(nil), prev...)
		sortStrings(sorted)
		waveDeps[e.Name] = sorted
	}
	return waveDeps
}

// containsStr reports whether s contains target.
func containsStr(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}

// prStatusNodeName generates the CEL-safe node ID for a PRStatus Watch node.
// Format: prstatus0<bundleSlug>0<envSlug>
// Uses digit separator "0" (not underscore) because celSafeSlug now produces
// camelCase where underscores are invalid DNS label characters.
func prStatusNodeName(bundleSlug, envName string) string {
	return "prstatus0" + bundleSlug + "0" + celSafeSlug(envName)
}

// prStatusNodeK8sName generates the Kubernetes resource name (hyphens) for a
// PRStatus node. Truncated to 63 chars.
func prStatusNodeK8sName(bundleSlugK8s, envName string) string {
	raw := fmt.Sprintf("prstatus-%s-%s", bundleSlugK8s, slugify(envName))
	if len(raw) > 63 {
		raw = raw[:63]
	}
	return raw
}
