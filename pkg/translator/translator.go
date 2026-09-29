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
// policyNS is the list of namespaces to scan for org-level PolicyGates
// (typically []string{"platform-policies"}).
func New(
	graphClient *graph.GraphClient,
	builder *graph.Builder,
	k8s client.Reader,
	policyNS []string,
	log zerolog.Logger,
) *Translator {
	if len(policyNS) == 0 {
		policyNS = []string{"platform-policies"}
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

// Translate translates a Pipeline+Bundle pair to a Graph CR and creates it.
// Idempotent: if the Graph already exists, returns without error.
// Returns the name of the generated Graph.
func (t *Translator) Translate(ctx context.Context,
	pipeline *kardinalv1alpha1.Pipeline,
	bundle *kardinalv1alpha1.Bundle) (string, error) {
	log := zerolog.Ctx(ctx).With().
		Str("pipeline", pipeline.Name).
		Str("bundle", bundle.Name).
		Logger()

	// Collect PolicyGates from all policy namespaces + pipeline namespace
	gates, err := t.collectGates(ctx, pipeline)
	if err != nil {
		return "", fmt.Errorf("translator.Translate: collect gates: %w", err)
	}

	log.Debug().Int("gates", len(gates)).Msg("collected policy gates")

	// Inline PromotionTemplate steps before building the Graph.
	// Each environment that references a PromotionTemplate has its steps replaced
	// with those from the template (unless the environment overrides with local steps).
	// This keeps the Builder pure (no k8s client) — spec O4.
	pipeline, err = inlinePromotionTemplates(ctx, pipeline, t.k8s)
	if err != nil {
		return "", fmt.Errorf("translator.Translate: inline promotion templates: %w", err)
	}

	// Validate skip permissions before building the Graph.
	// The result of this check flows into Bundle.status via the Bundle reconciler
	// (which sets phase=Failed if Translate returns an error). This makes the
	// skip-permission decision observable via CRD status rather than invisible
	// inside graph.Builder. Eliminates GB-2 from 11-graph-purity-tech-debt.md.
	if err := graph.ValidateSkipPermissions(pipeline, bundle, gates); err != nil {
		return "", fmt.Errorf("translator.Translate: skip permission denied: %w", err)
	}

	// Build Graph spec
	result, err := t.builder.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: gates,
	})
	if err != nil {
		return "", fmt.Errorf("translator.Translate: build: %w", err)
	}

	log.Debug().
		Int("nodes", result.NodeCount).
		Str("graph", result.Graph.Name).
		Msg("graph spec built")

	// Inject health ref nodes for each environment that has health.type configured.
	// HE-1, HE-2, HE-3 from docs/design/11-graph-purity-tech-debt.md:
	// The translator emits read-only ref nodes for health verification so the
	// Graph can observe real K8s resource health. Each node's readyWhen feeds
	// the Graph's Ready condition and the UI.
	if injected, injErr := injectHealthNodes(pipeline, result.Graph, t.servedKind); injErr != nil {
		// Non-fatal: log and continue without Watch nodes rather than failing the promotion.
		// The PromotionStep reconciler's Go adapter path remains as a fallback.
		log.Warn().Err(injErr).Msg("health Watch node injection failed — continuing without Watch nodes")
	} else if injected > 0 {
		log.Debug().Int("healthNodes", injected).Msg("health Watch nodes injected into Graph")
	}

	// kro applies the Graph as spec.serviceAccountName; it must exist and be
	// bound before kro's first reconcile or every apply is forbidden.
	if err := t.identity.Ensure(ctx, result.Graph); err != nil {
		return "", fmt.Errorf("translator.Translate: graph identity: %w", err)
	}

	// Create the Graph CR
	if err := t.graphClient.Create(ctx, result.Graph); err != nil {
		return "", fmt.Errorf("translator.Translate: create graph: %w", err)
	}

	log.Info().
		Str("graph", result.Graph.Name).
		Int("nodes", result.NodeCount).
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

// injectHealthWatchNodes adds health ref nodes assuming every health kind is
// served by the cluster. See injectHealthNodes.
func injectHealthWatchNodes(pipeline *kardinalv1alpha1.Pipeline, g *graph.Graph) (int, error) {
	return injectHealthNodes(pipeline, g, nil)
}

// injectHealthNodes post-processes the Graph spec to add read-only ref nodes
// for each environment with a configured health.type.
//
// This is the translator-layer implementation of HE-1, HE-2, HE-3 from
// docs/design/11-graph-purity-tech-debt.md.
//
// For each environment env with health.type set:
//  1. Build a WatchNodeSpec from pkg/health.WatchNodeTemplate
//  2. Build the Graph node ID: "health<EnvSlug>" (camelCase via celSafeSlug)
//  3. Append a ref node: metadata.name for a single object, metadata.selector
//     for a collection
//
// The health condition is not added to the PromotionStep node's readyWhen:
// kro only lets readyWhen reference the node itself (ledger gap G3).
//
// served, when non-nil, filters out kinds the cluster does not serve (ledger
// gap G4). Returns the count of injected nodes.
func injectHealthNodes(
	pipeline *kardinalv1alpha1.Pipeline,
	g *graph.Graph,
	served func(apiVersion, kind string) bool,
) (int, error) {
	if pipeline == nil || g == nil {
		return 0, nil
	}

	injected := 0
	for _, env := range pipeline.Spec.Environments {
		if env.Health.Type == "" {
			continue // no health check configured for this env
		}

		// Build the resource name. We use the same convention as the PromotionStep
		// reconciler's handleHealthChecking: pipeline.Name + "-" + env.Name for
		// argocd/flux, and pipeline.Name for resource/argoRollouts/flagger.
		opts := healthOptsForEnv(pipeline.Name, env)

		spec, err := health.WatchNodeTemplate(env.Health.Type, opts)
		if err != nil {
			// Unknown health type — skip this env rather than failing the whole promotion.
			continue
		}
		if served != nil && !served(spec.APIVersion, spec.Kind) {
			continue
		}

		// Node ID: "health" + TitleCase(celSafeSlug(env.Name))
		// e.g. "prod-eu" → celSafeSlug → "prodEu" → "healthProdEu"
		// The "health" prefix + TitleCase keeps the ID a valid kro node ID
		// ([A-Za-z][A-Za-z0-9]*).
		envSlug := celSafeSlug(env.Name)
		if len(envSlug) > 0 {
			envSlug = strings.ToUpper(envSlug[:1]) + envSlug[1:]
		}
		nodeID := "health" + envSlug

		var healthNode graph.GraphNode
		if spec.UseWatchKind {
			// Collection ref: metadata.selector. kro evaluates readyWhen per
			// element with the element bound to "each".
			healthNode = graph.GraphNode{
				ID: nodeID,
				Ref: map[string]interface{}{
					"apiVersion": spec.APIVersion,
					"kind":       spec.Kind,
					"metadata": map[string]interface{}{
						"namespace": spec.Namespace,
						"selector": map[string]interface{}{
							"matchLabels": stringMapToInterface(spec.LabelSelector),
						},
					},
				},
				ReadyWhen: []string{"${" + strings.ReplaceAll(spec.ReadyWhen, "healthNode", "each") + "}"},
			}
		} else {
			// Single named ref.
			healthNode = graph.GraphNode{
				ID: nodeID,
				Ref: map[string]interface{}{
					"apiVersion": spec.APIVersion,
					"kind":       spec.Kind,
					"metadata": map[string]interface{}{
						"name":      spec.Name,
						"namespace": spec.Namespace,
					},
				},
				ReadyWhen: []string{"${" + strings.ReplaceAll(spec.ReadyWhen, "healthNode", nodeID) + "}"},
			}
		}
		g.Spec.Nodes = append(g.Spec.Nodes, healthNode)
		injected++
	}
	return injected, nil
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

// celSafeSlug produces an identifier safe for use as both a CEL variable name
// and as a kro Graph node ID. Mirrors graph.celSafeSlug exactly — must
// be kept in sync with pkg/graph/builder.go.
//
// See graph.celSafeSlug for full documentation. Summary: produces camelCase so
// the result matches kro's node ID pattern [A-Za-z][A-Za-z0-9]*.
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
				b.WriteRune(c - 'A' + 'a')
			case upperNext:
				b.WriteRune(c)
				upperNext = false
			default:
				b.WriteRune(c)
			}
		case c >= '0' && c <= '9':
			if b.Len() == 0 {
				b.WriteString("x")
			}
			b.WriteRune(c)
			upperNext = false
		default:
			upperNext = true
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

// collectGates lists PolicyGate templates from all policy namespaces and the
// pipeline's namespace. De-duplicates by name+namespace and skips gate
// instances (labelled kardinal.io/gate-template).
//
// Policy namespace resolution (TR-2 elimination — docs/design/11-graph-purity-tech-debt.md):
// If pipeline.spec.policyNamespaces is set, use those namespaces instead of the
// controller-wide default (t.policyNS). This makes the policy namespace list
// explicit in the Pipeline spec rather than hardcoded in the controller.
func (t *Translator) collectGates(ctx context.Context,
	pipeline *kardinalv1alpha1.Pipeline) ([]kardinalv1alpha1.PolicyGate, error) {
	seen := make(map[string]bool)
	var gates []kardinalv1alpha1.PolicyGate

	// Use Pipeline.spec.policyNamespaces when set; fall back to controller-wide default.
	baseNS := t.policyNS
	if len(pipeline.Spec.PolicyNamespaces) > 0 {
		baseNS = pipeline.Spec.PolicyNamespaces
	}

	namespaces := append([]string(nil), baseNS...)
	// Add pipeline namespace if not already included
	pipelineNS := pipeline.Namespace
	alreadyIncluded := false
	for _, ns := range namespaces {
		if ns == pipelineNS {
			alreadyIncluded = true
			break
		}
	}
	if !alreadyIncluded {
		namespaces = append(namespaces, pipelineNS)
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

// CollectGates returns the PolicyGate templates Translate passes to the Graph
// builder for pipeline. policyNS is the controller's --policy-namespaces list
// (nil means the controller default). The CLI uses it so `kardinal policy
// simulate` selects exactly the gates the controller would.
func CollectGates(ctx context.Context, k8s client.Reader, policyNS []string,
	pipeline *kardinalv1alpha1.Pipeline) ([]kardinalv1alpha1.PolicyGate, error) {
	return New(nil, nil, k8s, policyNS, zerolog.Nop()).collectGates(ctx, pipeline)
}
