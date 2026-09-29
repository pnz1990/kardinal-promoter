// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// Translator handles the full pipeline-to-graph creation flow:
// reads PolicyGates from the cluster, calls graph.Builder, creates the Graph CR.
type Translator struct {
	graphClient *graph.GraphClient
	builder     *graph.Builder
	k8s         client.Reader // for listing PolicyGates
	policyNS    []string      // namespaces to scan for org-level PolicyGates
	log         zerolog.Logger

	// identity provisions the ServiceAccount kro impersonates for the Graph.
	// Nil skips provisioning (unit tests, or an operator-managed identity).
	identity *graph.IdentityProvisioner
	// mapper, when set, drops health ref nodes whose kind is not served by
	// the cluster. kro resolves the CRD schema of every static-GVK node when
	// it compiles a Graph, so one missing CRD would reject the whole Graph.
	mapper meta.RESTMapper
}

// New creates a new Translator.
// policyNS is the list of org policy namespaces: they are always scanned for
// PolicyGates, and only gates in them may grant a skip permission. Empty
// means graph.DefaultPolicyNamespace.
func New(
	graphClient *graph.GraphClient,
	builder *graph.Builder,
	k8s client.Reader,
	policyNS []string,
	log zerolog.Logger,
) *Translator {
	if len(policyNS) == 0 {
		policyNS = []string{graph.DefaultPolicyNamespace}
	}
	return &Translator{
		graphClient: graphClient,
		builder:     builder,
		k8s:         k8s,
		policyNS:    policyNS,
		log:         log,
	}
}

// WithIdentity sets the provisioner that ensures the Graph ServiceAccount
// and its RoleBindings exist before each Graph is created.
func (t *Translator) WithIdentity(p *graph.IdentityProvisioner) *Translator {
	t.identity = p
	return t
}

// WithRESTMapper sets the mapper used to skip health ref nodes for kinds the
// cluster does not serve (for example Argo CD Applications without Argo CD).
func (t *Translator) WithRESTMapper(m meta.RESTMapper) *Translator {
	t.mapper = m
	return t
}

// Translate translates a Pipeline+Bundle pair to a Graph CR and applies it:
// it creates the Graph, or updates the spec of the Graph this Bundle already
// controls, and refuses a Graph of the same name controlled by another owner
// (graph.ErrGraphOwnedByOther). Returns the name of the Graph.
func (t *Translator) Translate(ctx context.Context,
	pipeline *kardinalv1alpha1.Pipeline,
	bundle *kardinalv1alpha1.Bundle) (string, error) {
	log := zerolog.Ctx(ctx).With().
		Str("pipeline", pipeline.Name).
		Str("bundle", bundle.Name).
		Logger()
	ctx = log.WithContext(ctx)

	// Collect PolicyGates from the org policy namespaces, the Pipeline's
	// spec.policyNamespaces, and the Pipeline namespace.
	gates, err := t.collectGates(ctx, pipeline)
	if err != nil {
		return "", fmt.Errorf("translator.Translate: collect gates: %w", err)
	}

	log.Debug().Int("gates", len(gates)).Msg("collected policy gates")

	// Build validates the input (names, skip permissions, node IDs) and
	// returns the Graph spec. Only the controller's org namespaces count as
	// org policy: a Pipeline's own spec.policyNamespaces adds gates but can
	// never grant a skip.
	result, err := t.builder.Build(graph.BuildInput{
		Pipeline:         pipeline,
		Bundle:           bundle,
		PolicyGates:      gates,
		PolicyNamespaces: t.policyNS,
	})
	if err != nil {
		return "", fmt.Errorf("translator.Translate: build: %w", err)
	}

	log.Debug().
		Int("nodes", result.NodeCount).
		Str("graph", result.Graph.Name).
		Msg("graph spec built")

	// Health ref nodes for the environments in this Graph (ledger gaps G3,
	// G4). Namespaces the Graph identity may not read get no ref.
	h := healthInjector{
		log:    log,
		served: t.servedKind,
		mayRead: func(ns string) bool {
			return t.identity.MayRead(result.Graph.Namespace, ns)
		},
	}
	injected := h.inject(pipeline, result.Graph, result.Environments)
	if err := graph.ValidateNodeIDs(result.Graph.Spec.Nodes); err != nil {
		return "", fmt.Errorf("translator.Translate: health nodes: %w", err)
	}

	// kro applies the Graph as spec.serviceAccountName; it must exist and be
	// bound before kro's first reconcile or every apply is forbidden. A ref
	// into a namespace the reader role could not be bound in would be a hard
	// kro error, so those health refs are dropped for this Graph.
	unbound, err := t.identity.Ensure(ctx, result.Graph)
	if err != nil {
		return "", fmt.Errorf("translator.Translate: graph identity: %w", err)
	}
	if dropped := dropHealthNodes(result.Graph, injected, unbound); len(dropped) > 0 {
		log.Warn().Strs("nodes", dropped).Strs("namespaces", unbound).
			Msg("health ref nodes dropped: the Graph identity cannot read their namespaces")
	}

	if err := t.graphClient.Create(ctx, result.Graph); err != nil {
		return "", fmt.Errorf("translator.Translate: create graph: %w", err)
	}

	// Remove reader RoleBindings no Graph in the namespace reads through any
	// more. Best effort: a failure leaves a binding for a later translation.
	if t.identity != nil {
		if graphs, err := t.graphClient.List(ctx, result.Graph.Namespace); err != nil {
			log.Warn().Err(err).Msg("graph identity: list graphs for prune")
		} else if err := t.identity.Prune(ctx, result.Graph.Namespace, graphs); err != nil {
			log.Warn().Err(err).Msg("graph identity: prune reader rolebindings")
		}
	}

	log.Info().
		Str("graph", result.Graph.Name).
		Int("nodes", len(result.Graph.Spec.Nodes)).
		Msg("translation complete: graph applied")

	return result.Graph.Name, nil
}

// servedKind reports whether the cluster serves apiVersion/kind. Without a
// mapper every kind is assumed served.
func (t *Translator) servedKind(apiVersion, kind string) bool {
	if t.mapper == nil {
		return true
	}
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return false
	}
	_, err = t.mapper.RESTMapping(gv.WithKind(kind).GroupKind(), gv.Version)
	return err == nil
}

// healthInjector adds read-only health ref nodes to a Graph.
//
// This is the translator-layer implementation of HE-1, HE-2, HE-3 from
// docs/design/11-graph-purity-tech-debt.md. The health condition is not added
// to the PromotionStep node's readyWhen: kro only lets readyWhen reference the
// node itself (ledger gap G3).
type healthInjector struct {
	log zerolog.Logger
	// served, when non-nil, filters out kinds the cluster does not serve
	// (ledger gap G4).
	served func(apiVersion, kind string) bool
	// mayRead, when non-nil, filters out refs into namespaces the Graph
	// identity may not read.
	mayRead func(namespace string) bool
}

// inject adds a ref node for each of envs (the environments the Graph
// promotes) whose health.type is set: metadata.name for a single object,
// metadata.selector for a collection. The node ID is "health" plus the
// camelCase environment name, with a number appended if another node already
// has that ID. It returns the namespace of each added node by node ID.
func (h healthInjector) inject(pipeline *kardinalv1alpha1.Pipeline, g *graph.Graph,
	envs []string) map[string]string {
	if pipeline == nil || g == nil {
		return nil
	}
	inGraph := make(map[string]bool, len(envs))
	for _, e := range envs {
		inGraph[e] = true
	}
	taken := make(map[string]bool, len(g.Spec.Nodes))
	for _, n := range g.Spec.Nodes {
		taken[n.ID] = true
	}

	injected := map[string]string{}
	for _, env := range pipeline.Spec.Environments {
		if env.Health.Type == "" || !inGraph[env.Name] {
			continue
		}
		log := h.log.With().Str("environment", env.Name).Str("healthType", env.Health.Type).Logger()

		spec, err := health.WatchNodeTemplate(env.Health.Type, healthOptsForEnv(pipeline.Name, env))
		if err != nil {
			log.Warn().Err(err).Msg("no health ref node: unknown health type")
			continue
		}
		if h.served != nil && !h.served(spec.APIVersion, spec.Kind) {
			log.Info().Str("kind", spec.Kind).Msg("no health ref node: kind not served by the cluster")
			continue
		}
		ns := spec.Namespace
		if ns == "" {
			// An empty namespace on a selector ref lists every namespace.
			ns = g.Namespace
		}
		if h.mayRead != nil && !h.mayRead(ns) {
			log.Warn().Str("namespace", ns).
				Msg("no health ref node: namespace not in --graph-reader-namespaces")
			continue
		}

		base := "health" + upperFirst(graph.CELSafeSlug(env.Name))
		nodeID := base
		for i := 2; taken[nodeID]; i++ {
			nodeID = fmt.Sprintf("%s%d", base, i)
		}
		taken[nodeID] = true

		metadata := map[string]interface{}{"namespace": ns}
		readyWhen := strings.ReplaceAll(spec.ReadyWhen, "healthNode", nodeID)
		if spec.UseWatchKind {
			// Collection ref: kro evaluates readyWhen per element with the
			// element bound to "each".
			metadata["selector"] = map[string]interface{}{
				"matchLabels": stringMapToInterface(spec.LabelSelector),
			}
			readyWhen = strings.ReplaceAll(spec.ReadyWhen, "healthNode", "each")
		} else {
			metadata["name"] = spec.Name
		}
		g.Spec.Nodes = append(g.Spec.Nodes, graph.GraphNode{
			ID: nodeID,
			Ref: map[string]interface{}{
				"apiVersion": spec.APIVersion,
				"kind":       spec.Kind,
				"metadata":   metadata,
			},
			ReadyWhen: []string{"${" + readyWhen + "}"},
		})
		injected[nodeID] = ns
	}
	if len(injected) > 0 {
		h.log.Debug().Int("healthNodes", len(injected)).Msg("health ref nodes injected into Graph")
	}
	return injected
}

// dropHealthNodes removes the injected health nodes whose namespace is in
// namespaces and returns their IDs. Nothing references a health node, so
// removing one leaves a valid Graph.
func dropHealthNodes(g *graph.Graph, injected map[string]string, namespaces []string) []string {
	if len(namespaces) == 0 {
		return nil
	}
	drop := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		drop[ns] = true
	}
	var dropped []string
	kept := g.Spec.Nodes[:0]
	for _, n := range g.Spec.Nodes {
		if ns, ok := injected[n.ID]; ok && drop[ns] {
			dropped = append(dropped, n.ID)
			continue
		}
		kept = append(kept, n)
	}
	g.Spec.Nodes = kept
	return dropped
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func stringMapToInterface(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// healthOptsForEnv builds the health.CheckOptions for a given environment using
// the same name conventions as the PromotionStep reconciler's handleHealthChecking.
//
// Convention (matches promotionstep/reconciler.go handleHealthChecking):
//   - resource: Deployment name = pipeline.Name, namespace = env.Name
//     (overridden by env.Health.Resource.Name/Namespace when set)
//   - argocd:   Application name = pipeline.Name + "-" + env.Name, namespace = "argocd"
//   - flux:     Kustomization name = pipeline.Name + "-" + env.Name, namespace = "flux-system"
//   - argoRollouts: Rollout name = pipeline.Name, namespace = env.Name
//   - flagger:  Canary name = pipeline.Name, namespace = env.Name
func healthOptsForEnv(pipelineName string, env kardinalv1alpha1.EnvironmentSpec) health.CheckOptions {
	// Resolve the resource name and namespace for type=resource.
	// When env.Health.Resource is set, it overrides the default pipeline/env convention
	// so operators can health-check a resource with a different name or in a different namespace.
	resourceName := pipelineName
	resourceNS := env.Name
	if env.Health.Resource != nil {
		if env.Health.Resource.Name != "" {
			resourceName = env.Health.Resource.Name
		}
		if env.Health.Resource.Namespace != "" {
			resourceNS = env.Health.Resource.Namespace
		}
	}
	return health.CheckOptions{
		Type: env.Health.Type,
		Resource: health.ResourceConfig{
			Name:          resourceName,
			Namespace:     resourceNS,
			Condition:     "Available",
			LabelSelector: env.Health.LabelSelector, // non-nil → WatchKind mode
		},
		ArgoCD: health.ArgoCDConfig{
			Name:      pipelineName + "-" + env.Name,
			Namespace: "argocd",
		},
		Flux: health.FluxConfig{
			Name:      pipelineName + "-" + env.Name,
			Namespace: "flux-system",
		},
		ArgoRollouts: health.ArgoRolloutsConfig{
			Name:      pipelineName,
			Namespace: env.Name,
		},
		Flagger: health.FlaggerConfig{
			Name:      pipelineName,
			Namespace: env.Name,
		},
	}
}

// collectGates lists PolicyGate templates from the org policy namespaces
// (t.policyNS), the Pipeline's spec.policyNamespaces, and the Pipeline's
// namespace. De-duplicates by name+namespace and skips gate instances
// (labelled kardinal.io/gate-template).
//
// spec.policyNamespaces only adds namespaces (TR-2,
// docs/design/11-graph-purity-tech-debt.md). The org namespaces are always
// scanned, so a Pipeline cannot opt out of org policy by listing other
// namespaces.
func (t *Translator) collectGates(ctx context.Context,
	pipeline *kardinalv1alpha1.Pipeline) ([]kardinalv1alpha1.PolicyGate, error) {
	seen := make(map[string]bool)
	var gates []kardinalv1alpha1.PolicyGate

	var namespaces []string
	scanned := map[string]bool{}
	for _, list := range [][]string{t.policyNS, pipeline.Spec.PolicyNamespaces, {pipeline.Namespace}} {
		for _, ns := range list {
			if ns != "" && !scanned[ns] {
				scanned[ns] = true
				namespaces = append(namespaces, ns)
			}
		}
	}

	for _, ns := range namespaces {
		var list kardinalv1alpha1.PolicyGateList
		if err := t.k8s.List(ctx, &list, client.InNamespace(ns)); err != nil {
			return nil, fmt.Errorf("list policy gates in %s: %w", ns, err)
		}
		for _, g := range list.Items {
			// Skip gate instances a Graph stamped from a template. They live in
			// the pipeline namespace and would otherwise be re-stamped as
			// templates for the next Bundle.
			if _, isInstance := g.Labels["kardinal.io/gate-template"]; isInstance {
				continue
			}
			key := g.Namespace + "/" + g.Name
			if !seen[key] {
				seen[key] = true
				gates = append(gates, g)
			}
		}
	}

	// The cached List comes back in map order. Sort so the rendered Graph is
	// identical between translations and a no-op Pipeline change does not
	// produce a spurious Graph update.
	sort.Slice(gates, func(i, j int) bool {
		if gates[i].Namespace != gates[j].Namespace {
			return gates[i].Namespace < gates[j].Namespace
		}
		return gates[i].Name < gates[j].Name
	})
	return gates, nil
}
