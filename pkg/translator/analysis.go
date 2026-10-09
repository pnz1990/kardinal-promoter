// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// collectAnalyses reads the Argo Rollouts AnalysisTemplates (in the
// Pipeline namespace) and ClusterAnalysisTemplates that the Pipeline's
// environments name in spec.verification, for graph.Builder to copy into
// AnalysisRun nodes (pkg/graph/analysis.go).
//
// The templates are read here, not through Graph ref nodes, so a Graph
// renders one snapshot of each template, as it does for PolicyGate
// templates: a template edit reaches the Graph at its next translation
// (a Pipeline edit or a new Bundle), and a run in progress keeps its spec.
// A ref to a ClusterAnalysisTemplate would also need a cluster-scoped grant
// for every namespace's Graph identity (ledger G5).
//
// When the cluster does not serve AnalysisRun, Unavailable says so and the
// Build of a Graph with verification fails: verification fails closed.
// Templates that do not exist are left out (Build fails with their name).
// Other API errors are returned so the translation is retried.
func (t *Translator) collectAnalyses(ctx context.Context, pipeline *kardinalv1alpha1.Pipeline) (graph.AnalysisInput, error) {
	in := graph.AnalysisInput{Templates: map[string]graph.AnalysisTemplate{}}
	var refs []kardinalv1alpha1.AnalysisTemplateRef
	for _, env := range pipeline.Spec.Environments {
		if env.Verification != nil {
			refs = append(refs, env.Verification.AnalysisTemplates...)
		}
	}
	if len(refs) == 0 {
		return in, nil
	}
	if !t.servedKind(graph.AnalysisRunAPIVersion, "AnalysisRun") {
		in.Unavailable = fmt.Sprintf("%s AnalysisRun is not served by the cluster: install Argo Rollouts "+
			"(its CRDs) or remove spec.verification; kardinal does not promote without the analysis",
			graph.AnalysisRunAPIVersion)
		return in, nil
	}
	for _, ref := range refs {
		kind := graph.AnalysisTemplateKind(ref)
		key := graph.AnalysisTemplateKey(kind, ref.Name)
		if _, done := in.Templates[key]; done {
			continue
		}
		if !t.servedKind(graph.AnalysisRunAPIVersion, kind) {
			continue // Build reports it as not found
		}
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion(graph.AnalysisRunAPIVersion)
		obj.SetKind(kind)
		nn := types.NamespacedName{Name: ref.Name}
		if kind == graph.KindAnalysisTemplate {
			nn.Namespace = pipeline.Namespace
		}
		if err := t.k8s.Get(ctx, nn, obj); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return in, fmt.Errorf("get %s %s: %w", kind, nn, err)
		}
		spec, _, err := unstructured.NestedMap(obj.Object, "spec")
		if err != nil {
			return in, fmt.Errorf("%s %s: spec: %w", kind, nn, err)
		}
		in.Templates[key] = graph.AnalysisTemplate{Kind: kind, Name: ref.Name, Spec: spec}
	}
	return in, nil
}
