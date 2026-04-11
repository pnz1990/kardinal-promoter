// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
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

	// Step 3: validate skip permissions
	if err := validateSkipPermissions(input.Pipeline, input.Bundle, input.PolicyGates); err != nil {
		return nil, err
	}

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

	deps := make(map[string][]string, len(envs)) // env → []dependsOn
	for i, e := range envs {
		switch {
		case len(e.DependsOn) > 0:
			// Validate dependsOn references
			for _, dep := range e.DependsOn {
				if !nameSet[dep] {
					return nil, nil, fmt.Errorf("build: environment %q dependsOn unknown environment %q",
						e.Name, dep)
				}
			}
			deps[e.Name] = e.DependsOn
		case i > 0:
			// Default: depends on previous in list
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

func validateSkipPermissions(pipeline *kardinalv1alpha1.Pipeline,
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

		// PromotionStep node — node ID must be a valid CEL identifier
		stepNodeID := celSafeSlug(envName)
		stepNode := buildPromotionStepNode(
			pipelineName, bundleSlugK8s, envName, stepNodeID, envSpec, bundle, upstreams, gateNodeIDs,
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
func buildPromotionStepNode(
	pipelineName, bundleSlugK8s, envName, nodeID string,
	envSpec kardinalv1alpha1.EnvironmentSpec,
	bundle *kardinalv1alpha1.Bundle,
	upstreams []string,
	gateNodeIDs []string,
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
	templateMeta := map[string]interface{}{
		"name": k8sName, // K8s resource name (RFC 1123 subdomain — hyphens allowed, no underscores)
		"labels": map[string]interface{}{
			// These labels allow `kardinal explain` and the PolicyGate reconciler
			// to query instances by pipeline, bundle, and environment.
			"kardinal.io/pipeline":      pipelineName,
			"kardinal.io/bundle":        bundleName,
			"kardinal.io/environment":   envName,
			"kardinal.io/gate-template": gate.Name,
		},
	}

	templateSpec := map[string]interface{}{
		"expression":      gate.Spec.Expression,
		"message":         gate.Spec.Message,
		"recheckInterval": gate.Spec.RecheckInterval,
	}

	// Add upstream reference to create dependency edge
	if len(upstreams) > 0 {
		templateSpec["upstreamEnvironment"] = fmt.Sprintf("${%s.status.state}", upstreams[0])
	}

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
// must not contain hyphens, so we use underscores.
// Includes namespace to prevent collisions when same gate name exists in
// multiple namespaces.
func gateNodeName(pipeline, bundleSlug, gateName, gateNS, envName string) string {
	ns := gateNS
	if ns == "" {
		ns = "default"
	}
	// Use celSafeSlug so the ID is valid as a CEL identifier (underscores).
	raw := fmt.Sprintf("%s_%s_%s__%s", gateName, ns, envName, bundleSlug)
	return celSafeSlug(raw)
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

// bundleVersionSlug returns a slug from the bundle name suitable for node naming.
func bundleVersionSlug(bundleName string) string {
	return celSafeSlug(bundleName)
}

// slugify replaces characters not valid in Kubernetes names with dashes.
// Use celSafeSlug for node IDs that appear in CEL expressions.
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

// celSafeSlug creates an identifier safe for use as a CEL variable name.
// CEL identifiers follow Go rules: letters, digits, underscores; must not
// start with a digit. Hyphens and other special chars are replaced with '_'.
func celSafeSlug(s string) string {
	var b strings.Builder
	for i, c := range s {
		switch {
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9' && i > 0) || c == '_':
			b.WriteRune(c)
		case c >= 'A' && c <= 'Z':
			b.WriteRune(c - 'A' + 'a')
		case c == '0' && i == 0:
			// Leading digit — prefix with underscore
			b.WriteRune('_')
			b.WriteRune(c)
		default:
			b.WriteRune('_')
		}
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
