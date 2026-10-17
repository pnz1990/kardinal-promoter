// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"fmt"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// previewPipeline is the Pipeline the CLI builds a Graph from when it
// previews one (policy simulate, policy list, validate, create bundle
// --dry-run): pipe with spec.environments[].verification cleared, and the
// analyses it names per environment.
//
// The controller reads the Argo Rollouts AnalysisTemplates verification
// names when it builds a Graph (translator.collectAnalyses); a preview does
// not. Verification adds no gate and does not change the promotion order,
// only the AnalysisRuns after an environment's health check, so a preview
// without it shows the gates and the order the controller builds. A
// template that does not exist still fails the version in the controller
// (#2011). What the builder checks without the templates (the timeout) is
// still checked, with the builder's message.
func previewPipeline(pipe *v1alpha1.Pipeline) (*v1alpha1.Pipeline, map[string][]string, error) {
	out := pipe.DeepCopy()
	analyses := map[string][]string{}
	for i := range out.Spec.Environments {
		env := &out.Spec.Environments[i]
		if env.Verification == nil {
			continue
		}
		if _, err := graph.AnalysisTimeout(env.Verification.Timeout); err != nil {
			return nil, nil, fmt.Errorf("build: environment %q verification: %w", env.Name, err)
		}
		for _, ref := range env.Verification.AnalysisTemplates {
			name := ref.Name
			if graph.AnalysisTemplateKind(ref) == graph.KindClusterAnalysisTemplate {
				name = "cluster/" + name
			}
			analyses[env.Name] = append(analyses[env.Name], name)
		}
		env.Verification = nil
	}
	return out, analyses, nil
}
