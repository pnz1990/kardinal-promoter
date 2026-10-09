// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"sort"
	"strconv"
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

	// Shape, when set (GraphShapeNodes or GraphShapeCompact), is the shape of
	// the Bundle's existing Graph, which a re-translation keeps: switching
	// the shape of a Graph in flight would make kro prune the PromotionSteps
	// of the old shape's nodes. Empty chooses the shape (Builder.CompactAbove,
	// the Pipeline's AnnotationGraphShape).
	Shape string

	// PolicyNamespaces are the controller's org policy namespaces (not the
	// Pipeline's spec.policyNamespaces). Only skip-permission gates in these
	// namespaces can permit skipping an org-gated environment. Empty means
	// DefaultPolicyNamespace.
	PolicyNamespaces []string

	// Analyses are the Argo Rollouts analysis templates the environments'
	// spec.verification names, as the translator read them.
	Analyses AnalysisInput
	// MetricChecks are the MetricChecks of the Pipeline namespace. Each one
	// with spec.perPromotion that a gate of an environment reads gets an
	// instance node for that environment (buildMetricCheckNode).
	MetricChecks []kardinalv1alpha1.MetricCheck
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
	// GateInstances are the PolicyGate instances the Graph creates, in Graph
	// order (environment order, then gate order). They are rendered into the
	// PolicyGateData node; callers that need the gates (dry runs, policy
	// simulate) read them here instead of parsing the Graph.
	GateInstances []kardinalv1alpha1.PolicyGate
	// Upstreams are each environment's upstream environments in this Graph,
	// after skipped environments are bridged.
	Upstreams map[string][]string
	// Compact reports whether the Graph uses the compact shape: one
	// PromotionSteps collection instead of one node per environment.
	Compact bool
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
	// CompactAbove is the environment count above which a Graph uses the
	// compact shape when the Pipeline does not choose one
	// (AnnotationGraphShape). NewBuilder sets DefaultCompactAbove; zero makes
	// every Graph compact.
	CompactAbove int
}

// NewBuilder creates a new Builder.
func NewBuilder() *Builder {
	return &Builder{CompactAbove: DefaultCompactAbove}
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
	if err := ValidateHooks(input.Pipeline); err != nil {
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
	members, _, err := fleetMembers(input.Pipeline)
	if err != nil {
		return nil, err
	}
	gatesByEnv := matchGatesByEnv(filteredEnvs, input.PolicyGates, members)
	skipGates := skipPermissionGates(filteredEnvs, deps, input.Bundle, input.PolicyGates, input.PolicyNamespaces)
	if err := validateGateNames(filteredEnvs, gatesByEnv, skipGates); err != nil {
		return nil, err
	}

	// Step 5 & 6: build nodes and wire edges
	compact, err := b.compactShape(input.Pipeline, len(filteredEnvs), input.Shape, len(members) > 0)
	if err != nil {
		return nil, err
	}
	if compact {
		if err := checkCompactSupport(input); err != nil {
			return nil, err
		}
	}
	nodes, instances, upstreams, err := buildNodes(input.Pipeline, input.Bundle, filteredEnvs, deps, gatesByEnv, skipGates,
		input.MetricChecks, input.PolicyNamespaces, input.Analyses, compact, members)
	if err != nil {
		return nil, err
	}
	if err := ValidateNodeIDs(nodes); err != nil {
		return nil, err
	}

	// Step 7: assemble Graph
	g := assembleGraph(input.Pipeline, input.Bundle, nodes, b.serviceAccountName())
	g.Labels[LabelGraphShape] = GraphShapeNodes
	if compact {
		g.Labels[LabelGraphShape] = GraphShapeCompact
	}

	return &BuildResult{
		Graph:         g,
		NodeCount:     len(nodes),
		Environments:  filteredEnvs,
		GateInstances: instances,
		Upstreams:     upstreams,
		Compact:       compact,
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
// and returns the topologically sorted environment names, with every fleet
// environment replaced by its targets (expandFleets).
func resolveOrdering(pipeline *kardinalv1alpha1.Pipeline) ([]string, map[string][]string, error) {
	ordered, deps, err := resolveSpecOrdering(pipeline)
	if err != nil || !hasFleets(pipeline) {
		return ordered, deps, err
	}
	_, byFleet, err := fleetMembers(pipeline)
	if err != nil {
		return nil, nil, err
	}
	ordered, deps = expandFleets(ordered, deps, byFleet)
	return ordered, deps, nil
}

// resolveSpecOrdering is resolveOrdering over spec.environments as written:
// a fleet environment is one environment.
func resolveSpecOrdering(pipeline *kardinalv1alpha1.Pipeline) ([]string, map[string][]string, error) {
	sorted, deps, cycle, err := orderEnvironments(pipeline, nil)
	if cycle != nil {
		// Only a cycle needs to know where each edge came from: record it
		// on a second pass rather than on every call (the UI lists every
		// Pipeline's ordering on each poll).
		why := map[string]map[string]edgeSource{}
		_, _, cycle, _ = orderEnvironments(pipeline, why)
		return nil, nil, cycleError(cycle, why, pipeline.Spec.Environments)
	}
	return sorted, deps, err
}

// orderEnvironments is resolveSpecOrdering's work. With why non-nil it records
// where each edge came from. It returns the cycle, if there is one.
func orderEnvironments(pipeline *kardinalv1alpha1.Pipeline, why map[string]map[string]edgeSource) ([]string, map[string][]string, []string, error) {
	envs := pipeline.Spec.Environments
	if len(envs) == 0 {
		return nil, nil, nil, fmt.Errorf("build: pipeline has no environments")
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
	for i, e := range envs {
		var merged []string
		add := func(dep string, src edgeSource) {
			if !containsStr(merged, dep) {
				merged = append(merged, dep)
				if why != nil {
					if why[e.Name] == nil {
						why[e.Name] = map[string]edgeSource{}
					}
					why[e.Name][dep] = src
				}
			}
		}
		// Start with the edges to the previous wave, if there is one.
		for _, dep := range waves.waveDeps(e) {
			add(dep, fromWave)
		}
		// Union with explicit DependsOn.
		for _, dep := range e.DependsOn {
			if !nameSet[dep] {
				return nil, nil, nil, fmt.Errorf("build: environment %q dependsOn unknown environment %q",
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
		return nil, nil, cycle, nil
	}
	return sorted, deps, nil, nil
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
	// Index-based Kahn's algorithm: the names are sorted once, so a node's
	// index orders it as its name does, and the queue and each node's
	// dependents need only integer sorts.
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	idx := make(map[string]int, len(names))
	for i, n := range names {
		idx[n] = i
	}
	inDegree := make([]int, len(names))
	dependents := make([][]int, len(names))
	for n, ds := range deps {
		ni, ok := idx[n]
		if !ok {
			continue
		}
		for _, d := range ds {
			di, ok := idx[d]
			if !ok {
				continue
			}
			dependents[di] = append(dependents[di], ni)
			inDegree[ni]++
		}
	}
	queue := make([]int, 0, len(names))
	for i := range names {
		if inDegree[i] == 0 {
			queue = append(queue, i) // ascending: names are sorted
		}
	}
	sorted := make([]string, 0, len(names))
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		sorted = append(sorted, names[n])
		next := dependents[n]
		if len(next) > 1 {
			sort.Ints(next)
		}
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
		// A fleet environment is not in orderedEnvs, but deps lists its
		// targets (expandFleets), so the Graph stops after every target.
		found := isFleetName(orderedEnvs, deps, target)
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
	allGates []kardinalv1alpha1.PolicyGate, members map[string]fleetMember) map[string][]kardinalv1alpha1.PolicyGate {
	result := make(map[string][]kardinalv1alpha1.PolicyGate)
	envSet := make(map[string]bool, len(filteredEnvs))
	// A gate that applies to a fleet environment applies to each target.
	targetsOf := map[string][]string{}
	for _, e := range filteredEnvs {
		envSet[e] = true
		if m, ok := members[e]; ok {
			targetsOf[m.fleet] = append(targetsOf[m.fleet], e)
		}
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
			for _, env := range append([]string{e}, targetsOf[e]...) {
				if envSet[env] && !matched[env] {
					matched[env] = true
					result[env] = append(result[env], g)
				}
			}
		}
	}
	return result
}

// --- Step 5 & 6: build nodes and wire edges ---

// buildNodes generates the Graph nodes: the Bundle ref, one PromotionStep
// node per environment, and two collections, PolicyGates (every gate
// instance) and PRStatuses (one per environment).
//
// Gate instances and PRStatuses need no per-item gating (they are created
// with the Graph), so each kind is one forEach node over a def node that
// holds the rendered objects. That keeps the Graph small: one node per
// environment instead of one per object (docs/design/16-graph-capability-
// ledger.md gaps G9, G10). kro applies a collection's items in parallel.
// PromotionSteps stay one node each: a collection is all-or-nothing on
// pending data (G11), and each step is held back on its own upstreams and
// gates.
//
// A collection is also all-or-nothing on apply errors: when one item cannot
// be applied (a ResourceQuota, an admission policy that denies it,
// throttling) kro does not publish the collection, so nothing that
// references it resolves (G11). Steps reference the PolicyGates collection,
// because they must wait on their gates anyway: one gate instance that cannot
// be created holds every gated step, and the Bundle's GatesCreated condition
// names it. Steps name their PRStatus literally, so a PRStatus that cannot be
// created holds only its own environment.
func buildNodes(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	filteredEnvs []string, deps map[string][]string,
	gatesByEnv map[string][]kardinalv1alpha1.PolicyGate,
	skipGates map[string][]skipPermissionGate,
	metricChecks []kardinalv1alpha1.MetricCheck, policyNamespaces []string, analyses AnalysisInput,
	compact bool,
	members map[string]fleetMember) ([]GraphNode, []kardinalv1alpha1.PolicyGate, map[string][]string, error) {
	pipelineName := pipeline.Name
	bundleSlug := CELSafeSlug(bundle.Name) // camelCase — node IDs only

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
	nodes = append(nodes, readBackRefs(pipeline, filteredEnvs, bundle)...)

	gates := newGateCollections(pipelineName, bundle.Name)
	var prItems []interface{}
	var compactSteps []compactStep
	var compactMetrics []compactMetric
	upstreamEnvs := make(map[string][]string, len(filteredEnvs))

	for _, envName := range filteredEnvs {
		// Compute upstream deps for this env (filtered to only include surviving envs)
		// Return as CEL-safe IDs (matching the step node IDs built with CELSafeSlug).
		rawUpstreams := filteredDeps(envName, deps, filteredSet)
		upstreamEnvs[envName] = rawUpstreams
		upstreams := make([]string, len(rawUpstreams))
		for i, up := range rawUpstreams {
			upstreams[i] = CELSafeSlug(up)
		}

		// PolicyGate instances for this environment.
		var envGates []string
		for _, gate := range gatesByEnv[envName] {
			k8s := gateNodeK8sName(bundle.Name, gate.Name, gate.Namespace, envName)
			name, err := gates.add(gate, envName, k8s, nil)
			if err != nil {
				return nil, nil, nil, err
			}
			envGates = append(envGates, name)
		}

		// Skip-permission gate instances: this environment follows a skipped
		// org-gated environment, so it waits for the permission expressions.
		for _, sg := range skipGates[envName] {
			k8s := gateNodeK8sName(bundle.Name, sg.gate.Name, sg.gate.Namespace, envName)
			name, err := gates.add(sg.gate, envName, k8s, sg.skipped)
			if err != nil {
				return nil, nil, nil, err
			}
			envGates = append(envGates, name)
		}

		// Per-promotion MetricCheck instances the gates of this environment
		// read: a node each, or items of the compact shape's MetricChecks
		// collection.
		vars := MetricTemplateVars(pipeline, bundle, envName)
		for _, mc := range metricTemplatesFor(gatesByEnv[envName], metricChecks, pipeline.Namespace, policyNamespaces) {
			k8sName := metricNodeK8sName(bundle.Name, mc.Name, envName)
			if compact {
				spec, _, _, _, err := metricCheckSpec(mc, vars)
				if err != nil {
					return nil, nil, nil, err
				}
				compactMetrics = append(compactMetrics, compactMetric{name: k8sName, env: envName,
					template: mc.Name, upstreams: rawUpstreams, spec: spec})
				continue
			}
			node, err := buildMetricCheckNode(metricNodeName(bundleSlug, mc.Name, envName), k8sName,
				mc, vars, pipelineName, bundle.Name, envName, upstreams)
			if err != nil {
				return nil, nil, nil, err
			}
			nodes = append(nodes, node)
		}

		// PRStatus — created alongside each PromotionStep. The open-pr step
		// writes its spec; the PRStatusReconciler updates status.merged. The
		// PromotionStep spec carries the prStatusRef so it can watch it
		// without polling.
		prName := prStatusNodeK8sName(bundle.Name, envName)
		prItems = append(prItems, map[string]interface{}{"name": prName, "environment": envName})

		if compact {
			m := members[envName]
			compactSteps = append(compactSteps, compactStep{env: envName,
				name:           promotionStepK8sName(pipelineName, bundle.Name, envName),
				prStatus:       prName,
				upstreams:      rawUpstreams,
				gates:          envGates,
				fleet:          m.fleet,
				index:          m.index,
				maxConcurrent:  m.maxConcurrent,
				maxUnavailable: m.maxUnavailable,
			})
			continue
		}
		// PromotionStep node — node ID must be a valid CEL identifier.
		stepNode := buildPromotionStepNode(
			pipelineName, envName, CELSafeSlug(envName), bundle, upstreams, envGates, gates.readyCond, prName,
			heldCond(pipeline, envName),
		)
		extras, err := buildEnvExtras(hookNodesInput{
			pipeline: pipelineName, bundle: bundle.Name, namespace: bundle.Namespace,
			bundleUID:   string(bundle.UID),
			env:         findEnvSpec(pipeline, envName),
			stepK8sName: promotionStepK8sName(pipelineName, bundle.Name, envName),
			conds:       stepConds(heldCond(pipeline, envName), upstreams, envGates, gates.readyCond),
		}, analyses, bundle)
		if err != nil {
			return nil, nil, nil, err
		}
		attachExtras(stepNode, extras)
		nodes = append(nodes, stepNode)
		nodes = append(nodes, extras.nodes...)
	}

	nodes = append(nodes, gates.nodes()...)
	nodes = append(nodes, GraphNode{ID: NodePRStatusData, Def: map[string]interface{}{"items": prItems}},
		prStatusesNode(pipelineName, bundle.Name))
	if compact {
		nodes = append(nodes, compactNodes(pipeline, bundle, compactSteps, gates.collectionIDs())...)
		nodes = append(nodes, compactMetricNodes(pipelineName, bundle.Name, compactMetrics)...)
	}

	return nodes, gates.instances, upstreamEnvs, nil
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

// CondBundleWaitingForSlot is the Bundle condition that is True while a
// Failed Bundle of a Pipeline with spec.maxConcurrentPromotions waits for a
// slot (#1349). The Bundle reconciler writes it on its own Bundle; the Graph
// holds the Bundle's steps on it (bundleHeld) and the PromotionStep
// reconciler keeps the Bundle's Pending steps Pending. Without the hold, a
// failed environment that recovers (its step deleted and recreated) would
// promote while another Bundle has the slot.
const CondBundleWaitingForSlot = "WaitingForSlot"

// bundleHeld is the condition spec.bundleName of every PromotionStep node
// resolves under: the Bundle is neither Superseded nor Rejected (kardinal
// reject, #1451; both final) and does not wait for a
// maxConcurrentPromotions slot. has() keeps a Bundle without conditions
// resolvable (a missing key would be data-pending, see resolvableWhen).
const bundleHeld = `bundle.status.phase != "Superseded" && bundle.status.phase != "Rejected" && !(has(bundle.status.conditions) && ` +
	`bundle.status.conditions.exists(c_, c_.type == "` + CondBundleWaitingForSlot + `" && c_.status == "True"))`

// heldCond is the extra condition spec.bundleName of env's PromotionStep
// resolves under when the Pipeline holds env (spec.holds, kardinal rollback
// --hold, #1528): only the hold's Bundle may promote there. "" when env is
// not held. The hold is read when the Graph is built; adding or releasing a
// hold changes the Pipeline spec, which rebuilds every active Bundle's Graph
// in place (bundle reconciler ensurePipelineSpecCurrent), so the condition
// follows it. A Bundle the condition holds back gets no step in env; the
// steps that existed already are held by the PromotionStep reconciler.
func heldCond(pipeline *kardinalv1alpha1.Pipeline, env string) string {
	if h := heldBundle(pipeline, env, ""); h != "" {
		return "bundle.metadata.name == " + celString(h)
	}
	return ""
}

// heldBundle is the Bundle the Pipeline holds env on (spec.holds), or "".
func heldBundle(pipeline *kardinalv1alpha1.Pipeline, env, fleet string) string {
	for _, h := range pipeline.Spec.Holds {
		if h.Environment == env {
			return h.Bundle
		}
	}
	// A fleet target is also held by its fleet's hold (kardinal rollback
	// --env <fleet> --hold).
	if fleet != "" {
		for _, h := range pipeline.Spec.Holds {
			if h.Environment == fleet {
				return h.Bundle
			}
		}
	}
	return ""
}

// stepCond is the condition spec.bundleName resolves under: bundleHeld, and
// held when env is held (heldCond).
func stepCond(held string) string {
	if held == "" {
		return bundleHeld
	}
	return bundleHeld + " && " + held
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
// Superseded or Rejected.
//
// There is no per-region fan-out: Build rejects two or more
// spec.environments[].regions (see RegionsNotSupported) and ignores one.
func buildPromotionStepNode(
	pipelineName, envName, nodeID string,
	bundle *kardinalv1alpha1.Bundle,
	upstreams []string,
	gateNames []string,
	gateReady func(name string) string,
	prStatusName string,
	held string,
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
		// resolves while the Bundle is not Superseded or Rejected (kardinal
		// reject, #1451), so a Superseded or Rejected Bundle's
		// Graph creates no new PromotionStep when a gate or upstream later turns
		// ready (E2E-R20). kro re-reads the ref and watches it on every apply
		// (executor/simple.go applyRef), so the hold takes effect on the next
		// reconcile. Steps that already exist become Unresolved: kro neither
		// re-applies nor prunes them (executor/simple.go Apply, controller/graph/
		// tracking.go diffManagedResources), so they stay as history. includeWhen
		// is not used because an excluded node is pruned. Failed is not held:
		// a Failed Bundle can return to Promoting, unless it waits for a
		// maxConcurrentPromotions slot (bundleHeld, #1349).
		"bundleName":  resolvableWhen(stepCond(held), "bundle.metadata.name"),
		"environment": envName,
		"stepType":    stepType,
		// prStatusRef names the environment's PRStatus. The PromotionStep
		// reconciler reads it to find the PRStatus CRD instead of polling the
		// SCM (eliminates PS-4, SCM-2). It is a literal name, not a reference
		// to the PRStatuses collection: kro publishes a collection only when
		// every item applied (G11), so one PRStatus that cannot be created
		// would hold every step of the Bundle. The step waits in
		// WaitingForMerge until its PRStatus exists.
		"prStatusRef": prStatusName,
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

	// Required gates — an edge to the PolicyGates collection that holds this
	// step back until each of its gates reports status.ready == true. The
	// resolved value is the gate name, which the PromotionStep reconciler
	// reads.
	if len(gateNames) > 0 {
		gateRefs := make([]interface{}, len(gateNames))
		for i, name := range gateNames {
			gateRefs[i] = resolvableWhen(gateReady(name), celString(name))
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

// LabelGateTemplateNamespace is the namespace of the template a PolicyGate
// instance was made from. The instance lives in the Pipeline namespace; when
// the template is an org gate (from a --policy-namespaces namespace) the
// PolicyGate reconciler reads metrics.* from this namespace, not the
// instance's, so a team cannot decide an org gate with its own MetricChecks.
const LabelGateTemplateNamespace = "kardinal.io/gate-template-namespace"

// celString quotes s as a CEL string literal. strconv.Quote's escapes are a
// subset of CEL's.
func celString(s string) string {
	return strconv.Quote(s)
}

// prStatusesNode is the collection that creates one PRStatus per
// NodePRStatusData item.
//
// The Graph creates each PRStatus as a placeholder with no spec; the open-pr
// step populates spec.prURL, spec.prNumber, spec.repo after opening the PR.
// The PRStatus reconciler monitors the SCM and sets status.merged = true.
//
// The template deliberately has no spec: kro server-side applies templates
// and re-applies them on drift, so any spec field in the template would be
// owned by kro and reverted after the open-pr step writes it.
//
// The node has no readyWhen, so it is ready once applied. The PromotionStep
// references its PRStatus by name and enforces the merge in its own
// WaitingForMerge state, and its readyWhen (state Verified) already covers
// the merge: a step that opened a PR is Verified only after the merge. A
// readyWhen on status.merged would keep the Graph from ever being Ready when
// the step opens no PR, which the live approval cannot tell (B69): the step
// runs the step list recorded when it started, so an edit to pr-review does
// not add a PR to it, and a pr-review step with nothing to commit opens none.
//
// Graph-purity: this node provides observable PR merge state for the
// PromotionStep reconciler (eliminates direct GitHub API polling PS-4, SCM-2).
func prStatusesNode(pipelineName, bundleName string) GraphNode {
	return GraphNode{
		ID:      NodePRStatuses,
		ForEach: []map[string]string{{iterPR: "${" + NodePRStatusData + ".items}"}},
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "PRStatus",
			"metadata": map[string]interface{}{
				"name": "${" + iterPR + ".name}",
				"labels": map[string]interface{}{
					"kardinal.io/pipeline":    pipelineName,
					"kardinal.io/bundle":      bundleName,
					"kardinal.io/environment": "${" + iterPR + ".environment}",
				},
			},
		},
	}
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
	case "chart":
		return "helm-set-image"
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
