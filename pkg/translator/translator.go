// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
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
