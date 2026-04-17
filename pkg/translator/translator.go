// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
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

	// Inject health Watch nodes for each environment that has health.type configured.
	// HE-1, HE-2, HE-3 from docs/design/11-graph-purity-tech-debt.md:
	// The translator emits krocodile Watch-reference nodes (identity-only template:
	// apiVersion+kind+metadata.name) for health verification so the Graph can
	// observe real K8s resource health without the PromotionStep reconciler
	// calling health adapters on the hot path.
	//
	// Each health Watch node:
	//   - Has an identity-only template → krocodile auto-detects it as a Watch reference
	//   - Has a readyWhen expression evaluating the K8s resource's health status
	//   - Updates the companion PromotionStep node's readyWhen to surface
	//     real-resource health in the Graph's UI signal
	if injected, injErr := injectHealthWatchNodes(pipeline, result.Graph); injErr != nil {
		// Non-fatal: log and continue without Watch nodes rather than failing the promotion.
		// The PromotionStep reconciler's Go adapter path remains as a fallback.
		log.Warn().Err(injErr).Msg("health Watch node injection failed — continuing without Watch nodes")
	} else if injected > 0 {
		log.Debug().Int("healthNodes", injected).Msg("health Watch nodes injected into Graph")
	}

	// Create the Graph CR
	if err := t.graphClient.Create(ctx, result.Graph); err != nil {
		return "", fmt.Errorf("translator.Translate: create graph: %w", err)
	}

	log.Info().
		Str("graph", result.Graph.Name).
		Int("nodes", result.NodeCount).
		Msg("translation complete: graph created")

	return result.Graph.Name, nil
}

// injectHealthWatchNodes post-processes the Graph spec to add krocodile Watch-reference
// nodes for each environment with a configured health.type.
//
// This is the translator-layer implementation of HE-1, HE-2, HE-3 from
// docs/design/11-graph-purity-tech-debt.md. Watch-reference nodes are auto-detected
// by krocodile (e082fe9+, Reference=Watch) when a node template contains only
// apiVersion, kind, and metadata.name/namespace.
//
// For each environment env with health.type set:
//  1. Build a WatchNodeSpec from pkg/health.WatchNodeTemplate
//  2. Build the Graph node ID: "health<EnvSlug>" (camelCase via celSafeSlug)
//  3. Append a Watch-reference node to the Graph spec
//  4. Update the PromotionStep node's readyWhen to include the health condition
//     (UI signal: the Graph shows the step as "ready" only when the K8s resource
//     is also healthy — not just when the reconciler set state=Verified)
//
// Returns the count of injected Watch nodes.
func injectHealthWatchNodes(
	pipeline *kardinalv1alpha1.Pipeline,
	g *graph.Graph,
) (int, error) {
	if pipeline == nil || g == nil {
		return 0, nil
	}

	// Build a name→spec index for fast PromotionStep node lookup.
	// Node IDs for PromotionStep nodes follow the celSafeSlug(envName) convention
	// used by builder.go. We match them by ID prefix.
	stepNodeByEnv := make(map[string]int, len(pipeline.Spec.Environments))
	for i, node := range g.Spec.Nodes {
		for _, env := range pipeline.Spec.Environments {
			if node.ID == celSafeSlug(env.Name) {
				stepNodeByEnv[env.Name] = i
				break
			}
		}
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

		// Node ID: "health" + TitleCase(celSafeSlug(env.Name))
		// e.g. "prod-eu" → celSafeSlug → "prodEu" → "healthProdEu"
		// The "health" prefix + TitleCase produces a camelCase ID with no underscores,
		// satisfying both CEL identifier rules and DNS label rules.
		envSlug := celSafeSlug(env.Name)
		if len(envSlug) > 0 {
			envSlug = strings.ToUpper(envSlug[:1]) + envSlug[1:]
		}
		nodeID := "health" + envSlug

		// Substitute the "healthNode" placeholder in ReadyWhen with the actual node ID.
		readyWhen := strings.ReplaceAll(spec.ReadyWhen, "healthNode", nodeID)

		// Build the Graph node template.
		// krocodile auto-detects the reference type from the template structure
		// (experimental/controller/types.go: DetectReference):
		//   - Watch:     apiVersion + kind + metadata.name  (single named resource)
		//   - WatchKind: apiVersion + kind, no metadata.name (collection by selector)
		var nodeTemplate map[string]interface{}
		if spec.UseWatchKind {
			// WatchKind: no metadata.name — krocodile watches all resources matching
			// the label selector.
			//
			// krocodile (node.go:reconcileWatchKind) extracts the selector from
			// tmpl["selector"] (flat top-level key) or tmpl["metadata"]["selector"].
			// We use the flat top-level form as it is simpler and matches the
			// krocodile source exactly.
			//
			// Namespace: krocodile ≥ 81c5a03 changed WatchKind namespace from
			// graph.GetNamespace() to tmpl["metadata"]["namespace"] (absent = cluster-wide).
			// We must include the namespace to scope the watch to the environment namespace.
			// Note: metadata.name is intentionally omitted — krocodile uses its absence to
			// classify this as ReferenceWatchKind rather than ReferenceWatch.
			nodeTemplate = map[string]interface{}{
				"apiVersion": spec.APIVersion,
				"kind":       spec.Kind,
				"selector":   spec.LabelSelector,
				"metadata": map[string]interface{}{
					"namespace": spec.Namespace,
				},
			}
		} else {
			// Watch: identity-only template (apiVersion + kind + metadata.name).
			// An identity-only template is classified ReferenceWatch by krocodile's
			// DetectReference (experimental/controller/types.go). Existing behavior — unchanged.
			nodeTemplate = map[string]interface{}{
				"apiVersion": spec.APIVersion,
				"kind":       spec.Kind,
				"metadata": map[string]interface{}{
					"name":      spec.Name,
					"namespace": spec.Namespace,
				},
			}
		}

		// Build the health observation node.
		// krocodile >= 05db829: explicit keyword required (#676).
		//   Single named Watch → Keyword: NodeKeywordRef ("ref:")
		//   Collection Watch (WatchKind) → Keyword: NodeKeywordWatch ("watch:")
		watchKeyword := graph.NodeKeywordRef
		if spec.UseWatchKind {
			watchKeyword = graph.NodeKeywordWatch
		}
		watchNode := graph.GraphNode{
			ID:       nodeID,
			Keyword:  watchKeyword,
			Template: nodeTemplate,
			// ReadyWhen: only set for WatchKind nodes. Ref nodes must NOT have ReadyWhen
			// (krocodile 81c5a03+: Watch+ReadyWhen would upgrade to Unresolved/Contribute).
			// Under 05db829 the explicit "watch:" keyword prevents this, but we keep
			// the same behavior for consistency: health condition on PromotionStep only.
			ReadyWhen: func() []string {
				if spec.UseWatchKind {
					return []string{readyWhen}
				}
				return nil
			}(),
		}
		g.Spec.Nodes = append(g.Spec.Nodes, watchNode)

		// Propagate the health readyWhen to the companion PromotionStep node.
		// The PromotionStep readyWhen provides the UI health signal.
		if idx, ok := stepNodeByEnv[env.Name]; ok {
			g.Spec.Nodes[idx].ReadyWhen = append(
				g.Spec.Nodes[idx].ReadyWhen,
				readyWhen,
			)
		}

		injected++
	}
	return injected, nil
}

// healthOptsForEnv builds the health.CheckOptions for a given environment using
// the same name conventions as the PromotionStep reconciler's handleHealthChecking.
//
// Convention (matches promotionstep/reconciler.go handleHealthChecking):
//   - resource: Deployment name = pipeline.Name, namespace = env.Name
//   - argocd:   Application name = pipeline.Name + "-" + env.Name, namespace = "argocd"
//   - flux:     Kustomization name = pipeline.Name + "-" + env.Name, namespace = "flux-system"
//   - argoRollouts: Rollout name = pipeline.Name, namespace = env.Name
//   - flagger:  Canary name = pipeline.Name, namespace = env.Name
func healthOptsForEnv(pipelineName string, env kardinalv1alpha1.EnvironmentSpec) health.CheckOptions {
	return health.CheckOptions{
		Type: env.Health.Type,
		Resource: health.ResourceConfig{
			Name:          pipelineName,
			Namespace:     env.Name,
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
// and as a krocodile graph node ID. Mirrors graph.celSafeSlug exactly — must
// be kept in sync with pkg/graph/builder.go.
//
// See graph.celSafeSlug for full documentation. Summary: produces camelCase so
// the result is valid as a CEL identifier AND as a DNS label after strings.ToLower().
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

// collectGates lists PolicyGates from all policy namespaces and the pipeline's namespace.
// De-duplicates by name+namespace.
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
			key := g.Namespace + "/" + g.Name
			if !seen[key] {
				seen[key] = true
				gates = append(gates, g)
			}
		}
	}

	return gates, nil
}
