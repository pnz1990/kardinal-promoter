// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"sort"
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

	// PolicyNamespaces are the controller's org policy namespaces (not the
	// Pipeline's spec.policyNamespaces). Only skip-permission gates in these
	// namespaces can permit skipping an org-gated environment. Empty means
	// DefaultPolicyNamespace.
	PolicyNamespaces []string
}

// BuildResult is the output of the graph builder.
type BuildResult struct {
	// Graph is the generated Graph CR (not yet written to Kubernetes).
	Graph *Graph
	// NodeCount is the total number of nodes generated.
	NodeCount int
	// Environments are the environments this Bundle promotes through, in
	// topological order, after targetEnvironment and skipEnvironments.
	Environments []string
}

// DefaultGraphServiceAccount is the ServiceAccount (in the Pipeline's
// namespace) that kro impersonates when it applies a kardinal Graph.
// See identity.go for how the controller provisions it.
const DefaultGraphServiceAccount = "kardinal-graph"

// Builder generates Graph specs from Pipeline + Bundle + PolicyGates.
// It implements the full translation algorithm from
// docs/design/02-pipeline-to-graph-translator.md.
type Builder struct {
	// ServiceAccountName is written to Graph.spec.serviceAccountName.
	// Empty means DefaultGraphServiceAccount.
	ServiceAccountName string
}

// NewBuilder creates a new Builder.
func NewBuilder() *Builder {
	return &Builder{}
}

// Build generates a Graph spec. Returns an error if the Pipeline is invalid
// (circular deps, unknown target or skipped env, skip denied, names that give
// invalid or colliding node IDs, env.steps, etc.). Every error wraps
// ErrInvalid: Build does NOT read or write Kubernetes, so the same input
// always fails the same way.
func (b *Builder) Build(input BuildInput) (*BuildResult, error) {
	res, err := b.build(input)
	if err != nil {
		return nil, asInvalid(err)
	}
	return res, nil
}

func (b *Builder) build(input BuildInput) (*BuildResult, error) {
	if input.Pipeline == nil {
		return nil, fmt.Errorf("build: pipeline is nil")
	}
	if input.Bundle == nil {
		return nil, fmt.Errorf("build: bundle is nil")
	}
	if err := validateInput(input.Pipeline, input.Bundle); err != nil {
		return nil, err
	}

	// Step 1: resolve environment ordering
	orderedEnvs, deps, err := resolveOrdering(input.Pipeline)
	if err != nil {
		return nil, err
	}

	// Step 2: filter environments by Bundle intent
	if err := validateSkipNames(input.Pipeline, input.Bundle); err != nil {
		return nil, err
	}
	filteredEnvs, err := filterByIntent(orderedEnvs, deps, input.Bundle)
	if err != nil {
		return nil, err
	}
	if len(filteredEnvs) == 0 {
		return nil, fmt.Errorf("build: all environments skipped")
	}
	if err := validateBundleStrategy(input.Pipeline, input.Bundle, filteredEnvs); err != nil {
		return nil, err
	}

	// Step 3: validate skip permissions. The error reaches Bundle.status
	// through the Translate error (phase Failed, reason TranslationError).
	// The permission expressions are evaluated on the Graph: see
	// skipPermissionGates.
	if err := ValidateSkipPermissions(input.Bundle, input.PolicyGates, input.PolicyNamespaces); err != nil {
		return nil, err
	}

	// Step 4: collect and match PolicyGates by environment
	gatesByEnv := matchGatesByEnv(filteredEnvs, input.PolicyGates)
	skipGates := skipPermissionGates(filteredEnvs, deps, input.Bundle, input.PolicyGates, input.PolicyNamespaces)
	if err := validateGateNames(filteredEnvs, gatesByEnv, skipGates); err != nil {
		return nil, err
	}

	// Step 5 & 6: build nodes and wire edges
	nodes := buildNodes(input.Pipeline, input.Bundle, filteredEnvs, deps, gatesByEnv, skipGates)
	if err := ValidateNodeIDs(nodes); err != nil {
		return nil, err
	}

	// Step 7: assemble Graph
	g := assembleGraph(input.Pipeline, input.Bundle, nodes, b.serviceAccountName())

	return &BuildResult{
		Graph:        g,
		NodeCount:    len(nodes),
		Environments: filteredEnvs,
	}, nil
}

func (b *Builder) serviceAccountName() string {
	if b == nil || b.ServiceAccountName == "" {
		return DefaultGraphServiceAccount
	}
	return b.ServiceAccountName
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

	// Expand wave topology (K-06). See defaultDeps for the rules; explicit
	// DependsOn entries are unioned with the wave-derived edges.
	waves := indexWaves(envs)

	deps := make(map[string][]string, len(envs)) // env → []dependsOn
	// why records where each edge came from, so a cycle error can say which
	// part of the spec made it.
	why := make(map[string]map[string]edgeSource, len(envs))
	for i, e := range envs {
		why[e.Name] = map[string]edgeSource{}
		var merged []string
		add := func(dep string, src edgeSource) {
			if !containsStr(merged, dep) {
				merged = append(merged, dep)
				why[e.Name][dep] = src
			}
		}
		// Start with the edges to the previous wave, if there is one.
		for _, dep := range waves.waveDeps(e) {
			add(dep, fromWave)
		}
		// Union with explicit DependsOn.
		for _, dep := range e.DependsOn {
			if !nameSet[dep] {
				return nil, nil, fmt.Errorf("build: environment %q dependsOn unknown environment %q",
					e.Name, dep)
			}
			add(dep, fromDependsOn)
		}
		if len(e.DependsOn) == 0 {
			for _, dep := range waves.defaultDeps(envs, i) {
				add(dep, fromListOrder)
			}
		}
		deps[e.Name] = merged
	}

	// Topological sort (Kahn's algorithm) to detect cycles
	sorted, cycle := topoSort(nameSet, deps)
	if cycle != nil {
		return nil, nil, cycleError(cycle, why, envs)
	}

	return sorted, deps, nil
}

// edgeSource is the part of a Pipeline spec an ordering edge comes from.
type edgeSource int

const (
	// fromDependsOn: the environment lists the upstream in dependsOn.
	fromDependsOn edgeSource = iota
	// fromWave: the environment's wave waits for the whole previous wave.
	fromWave
	// fromListOrder: the environment has no dependsOn and follows the
	// environment listed before it (see waveIndex.defaultDeps).
	fromListOrder
)

// cycleError describes a dependency cycle edge by edge. The fix hint names
// the wave ordering when no dependsOn entry is part of the cycle: then there
// is no dependsOn reference to remove.
func cycleError(cycle []string, why map[string]map[string]edgeSource,
	envs []kardinalv1alpha1.EnvironmentSpec) error {
	wave := make(map[string]int, len(envs))
	for _, e := range envs {
		wave[e.Name] = e.Wave
	}
	var reasons []string
	var reorder []string // environments that follow list order in the cycle
	hasDependsOn, hasWave := false, false
	for i := 0; i+1 < len(cycle); i++ {
		env, dep := cycle[i], cycle[i+1]
		switch why[env][dep] {
		case fromDependsOn:
			hasDependsOn = true
			reasons = append(reasons, fmt.Sprintf("%s dependsOn %s", env, dep))
		case fromWave:
			hasWave = true
			reasons = append(reasons, fmt.Sprintf("%s (wave %d) waits for all of wave %d, which includes %s",
				env, wave[env], wave[dep], dep))
		case fromListOrder:
			reorder = append(reorder, env)
			if wave[env] > 0 {
				reasons = append(reasons, fmt.Sprintf("wave %d starts after %s, the environment listed before it",
					wave[env], dep))
			} else {
				reasons = append(reasons, fmt.Sprintf("%s has no dependsOn, so it follows %s, listed before it",
					env, dep))
			}
		}
	}
	var fix string
	switch {
	case hasDependsOn:
		fix = "remove one of the dependsOn references to break the cycle"
	case hasWave:
		fix = fmt.Sprintf("list the waves in ascending order (%s), or set dependsOn on %s instead of relying on list order",
			waveOrder(cycle, wave), strings.Join(reorder, " or "))
	default:
		fix = fmt.Sprintf("set dependsOn on %s", strings.Join(reorder, " or "))
	}
	return fmt.Errorf("build: circular dependency in pipeline environments: %s (cycle!)\n  %s\n  Fix: %s",
		strings.Join(cycle, " → "), strings.Join(reasons, "; "), fix)
}

// waveOrder renders the waves on a cycle in ascending order, as in
// "wave 1 before wave 2".
func waveOrder(cycle []string, wave map[string]int) string {
	seen := map[int]bool{}
	var ns []int
	for _, env := range cycle {
		if n := wave[env]; n > 0 && !seen[n] {
			seen[n] = true
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprintf("wave %d", n)
	}
	return strings.Join(parts, " before ")
}

// topoSort performs Kahn's topological sort on the dependency graph. When
// the graph has a cycle it returns nil and the cycle as a path that starts
// and ends at the same environment.
func topoSort(nodes map[string]bool, deps map[string][]string) ([]string, []string) {
	// Compute in-degree
	inDegree := make(map[string]int, len(nodes))
	for n := range nodes {
		inDegree[n] = 0
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
	sort.Strings(queue) // deterministic order

	var sorted []string
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		sorted = append(sorted, n)
		next := dependents[n]
		sort.Strings(next)
		for _, d := range next {
			inDegree[d]--
			if inDegree[d] == 0 {
				queue = append(queue, d)
			}
		}
	}

	if len(sorted) != len(nodes) {
		return nil, findCycle(nodes, deps, sorted)
	}
	return sorted, nil
}

// findCycle finds a cycle in the dependency graph and returns it as a path
// such as [prod uat prod]. Uses the set of nodes that were NOT sorted (i.e.,
// those still in the cycle or downstream of it) to start the search.
func findCycle(nodes map[string]bool, deps map[string][]string, sorted []string) []string {
	// Identify nodes that are part of a cycle (not in sorted output).
	sortedSet := make(map[string]bool, len(sorted))
	for _, n := range sorted {
		sortedSet[n] = true
	}

	// Start from the first unsorted node by name, for a stable message.
	// Every unsorted node has an unsorted upstream, so following upstreams
	// from it must come back to a node already on the path.
	var unsorted []string
	for n := range nodes {
		if !sortedSet[n] {
			unsorted = append(unsorted, n)
		}
	}
	if len(unsorted) == 0 {
		return nil
	}
	sort.Strings(unsorted)

	pos := map[string]int{}
	path := []string{unsorted[0]}
	for {
		current := path[len(path)-1]
		pos[current] = len(path) - 1
		next := ""
		for _, dep := range deps[current] {
			if !sortedSet[dep] {
				next = dep
				break
			}
		}
		if next == "" {
			return append(path, path[0]) // unreachable: see above
		}
		if at, seen := pos[next]; seen {
			return append(path[at:], next)
		}
		path = append(path, next)
	}
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

	// skipEnvironments: filtered here, not with includeWhen on the
	// PromotionStep node. kro's includeWhen is contagious — a false
	// includeWhen also excludes every node that depends on the skipped node,
	// so a skipped "uat" would drop "prod" too. filteredDeps bridges the
	// skipped environment so "prod" depends on "test" directly.
	// See docs/design/16-graph-capability-ledger.md gap G2.
	if skips := bundle.Spec.Intent.SkipEnvironments; len(skips) > 0 {
		kept := result[:0]
		for _, e := range result {
			if !containsStr(skips, e) {
				kept = append(kept, e)
			}
		}
		result = kept
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
		// Skip-permission gates are placed by skipPermissionGates.
		if isSkipPermissionGate(g) {
			continue
		}
		appliesTo := g.Labels["kardinal.io/applies-to"]
		matched := make(map[string]bool)
		for _, e := range strings.Split(appliesTo, ",") {
			e = strings.TrimSpace(e)
			if envSet[e] && !matched[e] {
				matched[e] = true
				result[e] = append(result[e], g)
			}
		}
	}
	return result
}

// --- Step 5 & 6: build nodes and wire edges ---

// buildNodes generates all PromotionStep and PolicyGate Graph nodes in
// dependency order, with correct readyWhen and gating edges.
func buildNodes(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	filteredEnvs []string, deps map[string][]string,
	gatesByEnv map[string][]kardinalv1alpha1.PolicyGate,
	skipGates map[string][]skipPermissionGate) []GraphNode {
	// Build env spec map for quick lookup
	envSpecMap := make(map[string]kardinalv1alpha1.EnvironmentSpec)
	for _, e := range pipeline.Spec.Environments {
		envSpecMap[e.Name] = e
	}

	bundleSlug := bundleVersionSlug(bundle.Name) // camelCase — node IDs only
	pipelineName := pipeline.Name

	// Filter deps to only include filtered envs
	filteredSet := make(map[string]bool, len(filteredEnvs))
	for _, e := range filteredEnvs {
		filteredSet[e] = true
	}

	var nodes []GraphNode

	// Bundle ref node: brings bundle.spec.* into Graph CEL scope (#622).
	// A named ref — the translator knows the exact Bundle name at graph
	// generation time. ref: reads the object without owning it.
	bundleWatchNode := GraphNode{
		ID: "bundle",
		Ref: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "Bundle",
			"metadata": map[string]interface{}{
				"name":      bundle.Name,
				"namespace": bundle.Namespace,
			},
		},
		// ReadyWhen intentionally omitted — ref: is read-only.
	}
	nodes = append(nodes, bundleWatchNode)

	for _, envName := range filteredEnvs {
		envSpec := envSpecMap[envName]

		// Compute upstream deps for this env (filtered to only include surviving envs)
		// Return as CEL-safe IDs (matching the step node IDs built with CELSafeSlug).
		rawUpstreams := filteredDeps(envName, deps, filteredSet)
		upstreams := make([]string, len(rawUpstreams))
		for i, up := range rawUpstreams {
			upstreams[i] = CELSafeSlug(up)
		}

		// PolicyGate nodes for this environment
		gates := gatesByEnv[envName]
		gateNodeIDs := make([]string, 0, len(gates)+len(skipGates[envName]))
		for _, gate := range gates {
			gateNodeID := gateNodeName(bundleSlug, gate.Name, gate.Namespace, envName)
			gateNodeK8s := gateNodeK8sName(bundle.Name, gate.Name, gate.Namespace, envName)
			gateNodeIDs = append(gateNodeIDs, gateNodeID)
			nodes = append(nodes, buildPolicyGateNode(gateNodeID, gateNodeK8s, gate, pipelineName, bundle.Name, envName))
		}

		// Skip-permission gate instances: this environment follows a skipped
		// org-gated environment, so it waits for the permission expressions.
		for _, sg := range skipGates[envName] {
			gateNodeID := gateNodeName(bundleSlug, sg.gate.Name, sg.gate.Namespace, envName)
			gateNodeK8s := gateNodeK8sName(bundle.Name, sg.gate.Name, sg.gate.Namespace, envName)
			gateNodeIDs = append(gateNodeIDs, gateNodeID)
			nodes = append(nodes, buildSkipPermissionNode(gateNodeID, gateNodeK8s, sg, pipelineName, bundle.Name, envName))
		}

		// PRStatus node — created alongside each PromotionStep.
		// The open-pr step writes this CRD; the PRStatusReconciler updates status.merged.
		// The PromotionStep spec carries the prStatusRef so it can watch it without polling.
		stepNodeID := CELSafeSlug(envName)
		prStatusNodeID := prStatusNodeName(bundleSlug, envName)
		prStatusK8sName := prStatusNodeK8sName(bundle.Name, envName)
		prStatusNode := buildPRStatusNode(prStatusNodeID, prStatusK8sName, pipelineName, bundle.Name, envName,
			envSpec.Approval == "pr-review")
		nodes = append(nodes, prStatusNode)

		// PromotionStep node — node ID must be a valid CEL identifier.
		stepNode := buildPromotionStepNode(
			pipelineName, envName, stepNodeID, bundle, upstreams, gateNodeIDs, prStatusNodeID,
		)
		nodes = append(nodes, stepNode)
	}

	return nodes
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

// resolvableWhen returns a CEL template expression that evaluates to value
// when cond is true and fails with "index out of bounds" when cond is false.
//
// kro's standalone Graph does not hold dependents back on readyWhen (the
// executor's GateReadiness option is only enabled for RGD instances). It does
// treat "index out of bounds" as data-pending: the node is left Unresolved,
// nothing is created or pruned, and kro retries when a watched object changes.
// Embedding a gate condition in a field the dependent needs therefore holds
// the dependent back until cond holds. The iterator name carries an
// underscore so it can never collide with a node ID ([A-Za-z][A-Za-z0-9]*).
// See docs/design/16-graph-capability-ledger.md gap G1.
func resolvableWhen(cond, value string) string {
	return fmt.Sprintf("${[%s].filter(x_, %s)[0]}", value, cond)
}

// verifiedCond returns the CEL condition "upstream PromotionStep is Verified".
func verifiedCond(upstreamID string) string {
	return fmt.Sprintf(`%s.status.state == "Verified"`, upstreamID)
}

// buildPromotionStepNode builds a Graph node for a PromotionStep.
// nodeID is the CEL-safe identifier used in CEL expressions.
// k8sName is the Kubernetes resource name (hyphens) for metadata.name.
// prStatusNodeID is the node ID of the companion PRStatus node.
//
// Gating: spec.upstreamStates and spec.requiredGates only resolve once every
// upstream PromotionStep is Verified and every PolicyGate is ready (see
// resolvableWhen). Until then kro does not create this PromotionStep.
// spec.bundleName holds every step, roots included, once the Bundle is
// Superseded.
//
// There is no per-region fan-out: Build rejects two or more
// spec.environments[].regions (see RegionsNotSupported) and ignores one.
func buildPromotionStepNode(
	pipelineName, envName, nodeID string,
	bundle *kardinalv1alpha1.Bundle,
	upstreams []string,
	gateNodeIDs []string,
	prStatusNodeID string,
) GraphNode {
	// Determine step type based on bundle type
	stepType := defaultStepType(bundle.Spec.Type)

	// metadata.name is a DNS-1123 subdomain: "<pipeline>-<bundle>-<env>",
	// hash-suffixed when the Bundle or environment name is not a slug.
	k8sResourceName := promotionStepK8sName(pipelineName, bundle.Name, envName)

	// Build the PromotionStep resource template
	templateMeta := map[string]interface{}{
		"name": k8sResourceName,
		"labels": map[string]interface{}{
			"kardinal.io/pipeline":    pipelineName,
			"kardinal.io/bundle":      bundle.Name,
			"kardinal.io/environment": envName,
		},
	}
	templateSpec := map[string]interface{}{
		"pipelineName": pipelineName,
		// bundleName: live CEL reference to the Bundle ref node (#622). It only
		// resolves while the Bundle is not Superseded, so a Superseded Bundle's
		// Graph creates no new PromotionStep when a gate or upstream later turns
		// ready (E2E-R20). kro re-reads the ref and watches it on every apply
		// (executor/simple.go applyRef), so the hold takes effect on the next
		// reconcile. Steps that already exist become Unresolved: kro neither
		// re-applies nor prunes them (executor/simple.go Apply, controller/graph/
		// tracking.go diffManagedResources), so they stay as history. includeWhen
		// is not used because an excluded node is pruned. Failed is not held:
		// a Failed Bundle can return to Promoting.
		"bundleName":  resolvableWhen(`bundle.status.phase != "Superseded"`, "bundle.metadata.name"),
		"environment": envName,
		"stepType":    stepType,
		// prStatusRef points to the companion PRStatus node.
		// The PromotionStep reconciler reads spec.prStatusRef.name to find the
		// PRStatus CRD instead of polling GitHub directly (eliminates PS-4, SCM-2).
		"prStatusRef": fmt.Sprintf("${%s.metadata.name}", prStatusNodeID),
	}

	// Upstream states as a list — creates CEL dependency edges and gates this
	// step on every upstream being Verified. Using a single list avoids the
	// N-field upstreamVerified / upstreamVerified2 / ... anti-pattern (#625).
	if len(upstreams) > 0 {
		upstreamRefs := make([]interface{}, len(upstreams))
		for i, up := range upstreams {
			upstreamRefs[i] = resolvableWhen(verifiedCond(up), `"Verified"`)
		}
		templateSpec["upstreamStates"] = upstreamRefs
	}

	// Required gates — creates fan-in edges from gate nodes and holds this
	// step back until every gate reports status.ready == true. The resolved
	// value is the gate name, which the PromotionStep reconciler reads.
	if len(gateNodeIDs) > 0 {
		gateRefs := make([]interface{}, len(gateNodeIDs))
		for i, gid := range gateNodeIDs {
			gateRefs[i] = resolvableWhen(
				fmt.Sprintf("%s.status.ready == true", gid),
				fmt.Sprintf("%s.metadata.name", gid),
			)
		}
		templateSpec["requiredGates"] = gateRefs
	}

	template := map[string]interface{}{
		"apiVersion": "kardinal.io/v1alpha1",
		"kind":       "PromotionStep",
		"metadata":   templateMeta,
		"spec":       templateSpec,
	}

	node := GraphNode{
		ID:       nodeID,
		Template: template,
		ReadyWhen: []string{
			fmt.Sprintf(`${%s.status.state == "Verified"}`, nodeID),
		},
	}

	return node
}

// buildPolicyGateNode builds a Graph node for a PolicyGate instance.
// nodeID is the CEL-safe identifier used in CEL expressions.
// k8sName is the Kubernetes resource name (hyphens) for metadata.name.
//
// spec.overrides is deliberately not copied. kro server-side applies the
// template with force and re-applies it on drift, so a template-owned
// overrides list would revert every `kardinal override` patch on the live
// instance. The CLI records overrides on the instances instead.
func buildPolicyGateNode(
	nodeID, k8sName string,
	gate kardinalv1alpha1.PolicyGate,
	pipelineName, bundleName, envName string,
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
			// gate-template-namespace: the template's namespace, which can differ
			// from the Pipeline's (an org policy namespace or spec.policyNamespaces),
			// so `kardinal policy list` can match the instance to its template.
			"kardinal.io/gate-template-namespace": gate.Namespace,
			// gate-name: stable human-readable name, propagated through cross-product instances.
			"kardinal.io/gate-name": gateName,
			// Propagated from original PolicyGate template for CLI display.
			"kardinal.io/scope":      scopeLabel,
			"kardinal.io/applies-to": appliesToLabel,
		},
	}

	// generated marks the instance as kardinal's: it is never used as a
	// template, which is what lets its name be longer than 63 characters
	// (the PolicyGate CRD name rule).
	templateSpec := map[string]interface{}{
		"expression":      gate.Spec.Expression,
		"message":         gate.Spec.Message,
		"recheckInterval": gate.Spec.RecheckInterval,
		"generated":       true,
	}
	// when is copied only when set: the CRD defaults it to post-deploy, and an
	// empty string would fail the enum. It is deprecated and has no effect
	// (#1323); the copy keeps the instance a faithful copy of the template.
	if gate.Spec.When != "" { //nolint:staticcheck // SA1019: copied unchanged, no behaviour depends on it
		templateSpec["when"] = gate.Spec.When //nolint:staticcheck // SA1019: as above
	}

	return GraphNode{
		ID: nodeID,
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "PolicyGate",
			"metadata":   templateMeta,
			"spec":       templateSpec,
		},
		// ReadyWhen is the UI/Graph health signal. The blocking itself is done
		// by the dependent PromotionStep's spec.requiredGates expression.
		ReadyWhen: []string{
			fmt.Sprintf(`${%s.status.ready == true}`, nodeID),
		},
	}
}

// buildSkipPermissionNode builds the instance of a skip-permission gate that
// holds envName, the environment after one or more skipped org-gated
// environments. The instance is an ordinary PolicyGate: the reconciler
// evaluates the permission expression, and envName is promoted only once it
// is true. The labels say what the instance stands for.
func buildSkipPermissionNode(nodeID, k8sName string, sg skipPermissionGate,
	pipelineName, bundleName, envName string) GraphNode {
	node := buildPolicyGateNode(nodeID, k8sName, sg.gate, pipelineName, bundleName, envName)
	meta := node.Template["metadata"].(map[string]interface{})
	meta["labels"].(map[string]interface{})[LabelGateType] = GateTypeSkipPermission
	meta["annotations"] = map[string]interface{}{
		AnnotationSkippedEnvironments: strings.Join(sg.skipped, ","),
	}
	return node
}

// buildPRStatusNode builds a Graph node for a PRStatus CRD.
//
// The Graph creates the PRStatus as a placeholder with no spec; the open-pr
// step populates spec.prURL, spec.prNumber, spec.repo after opening the PR.
// The PRStatus reconciler monitors the SCM and sets status.merged = true.
//
// The template deliberately has no spec: kro server-side applies templates
// and re-applies them on drift, so any spec field in the template would be
// owned by kro and reverted after the open-pr step writes it.
//
// ReadyWhen is a health signal only (green in the UI once merged). It does
// not gate the PromotionStep, which references this node's metadata.name and
// enforces the merge gate in its own WaitingForMerge state. It is emitted only
// for pr-review environments: an auto environment never opens a PR, so a
// merged == true readyWhen would keep the Graph from ever reaching Ready.
//
// Graph-purity: this node provides observable PR merge state for the UI and
// for the PromotionStep reconciler (eliminates direct GitHub API polling PS-4, SCM-2).
func buildPRStatusNode(nodeID, k8sName, pipelineName, bundleName, envName string, prReview bool) GraphNode {
	templateMeta := map[string]interface{}{
		"name": k8sName,
		"labels": map[string]interface{}{
			"kardinal.io/pipeline":    pipelineName,
			"kardinal.io/bundle":      bundleName,
			"kardinal.io/environment": envName,
		},
	}

	node := GraphNode{
		ID: nodeID,
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "PRStatus",
			"metadata":   templateMeta,
		},
	}
	if prReview {
		node.ReadyWhen = []string{fmt.Sprintf(`${%s.status.merged == true}`, nodeID)}
	}
	return node
}

// --- Step 7: assemble Graph ---

func assembleGraph(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	nodes []GraphNode, serviceAccountName string) *Graph {
	graphName := graphNameFrom(pipeline.Name, bundle.Name)
	isController := true

	return &Graph{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GraphGVK.GroupVersion().String(),
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
			Nodes:              nodes,
			ServiceAccountName: serviceAccountName,
		},
	}
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

// waveIndex groups environments by wave number (K-06).
type waveIndex struct {
	byWave map[int][]string // wave number → env names, sorted
	sorted []int            // wave numbers present, ascending
	// firstPos is the list position of the first environment of each wave.
	firstPos map[int]int
}

func indexWaves(envs []kardinalv1alpha1.EnvironmentSpec) waveIndex {
	w := waveIndex{byWave: map[int][]string{}, firstPos: map[int]int{}}
	for i, e := range envs {
		if e.Wave <= 0 {
			continue
		}
		if _, ok := w.byWave[e.Wave]; !ok {
			w.sorted = append(w.sorted, e.Wave)
			w.firstPos[e.Wave] = i
		}
		w.byWave[e.Wave] = append(w.byWave[e.Wave], e.Name)
	}
	for _, names := range w.byWave {
		sort.Strings(names)
	}
	sort.Ints(w.sorted)
	return w
}

// lowerWave returns the number of the highest wave below e's wave, or 0 for
// environments without a wave and for the lowest wave. Gaps in the numbering
// (10, 20, 30) are skipped rather than turning a wave into a DAG root.
func (w waveIndex) lowerWave(e kardinalv1alpha1.EnvironmentSpec) int {
	if e.Wave <= 0 {
		return 0
	}
	prev := 0
	for _, n := range w.sorted {
		if n >= e.Wave {
			break
		}
		prev = n
	}
	return prev
}

// waveDeps returns the upstreams every wave environment has, whatever its
// dependsOn says: all environments of the previous wave, so a wave waits for
// the whole previous wave. Returns nil for environments without a wave and
// for the lowest wave.
func (w waveIndex) waveDeps(e kardinalv1alpha1.EnvironmentSpec) []string {
	prev := w.lowerWave(e)
	if prev == 0 {
		return nil
	}
	return append([]string(nil), w.byWave[prev]...)
}

// defaultDeps returns the implicit upstreams of envs[i] when it has no
// explicit dependsOn:
//
//   - An environment without a wave follows the environment listed before it,
//     or every environment of that environment's wave.
//   - A wave follows the last environment without a wave listed before the
//     wave's first environment ("prod-eu and prod-us start together after
//     staging"). For a wave above the lowest this is added only when that
//     environment is listed after the previous wave started, because
//     otherwise the previous wave already follows it.
//
// Only an environment with nothing before it is a root.
func (w waveIndex) defaultDeps(envs []kardinalv1alpha1.EnvironmentSpec, i int) []string {
	e := envs[i]
	if e.Wave <= 0 {
		if i == 0 {
			return nil
		}
		prev := envs[i-1]
		if prev.Wave > 0 {
			return append([]string(nil), w.byWave[prev.Wave]...)
		}
		return []string{prev.Name}
	}
	for j := w.firstPos[e.Wave] - 1; j >= 0; j-- {
		if envs[j].Wave > 0 {
			continue
		}
		if lower := w.lowerWave(e); lower != 0 && j < w.firstPos[lower] {
			return nil
		}
		return []string{envs[j].Name}
	}
	return nil
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
