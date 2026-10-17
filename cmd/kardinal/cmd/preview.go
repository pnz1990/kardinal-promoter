// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"strings"

	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
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

// previewVerification is what a connected preview found out about a
// Pipeline's spec.verification: Fails is why the controller would fail the
// version (an AnalysisTemplate that does not exist, Argo Rollouts not
// installed, an arg it cannot pass), and NotChecked why the CLI could not
// tell (it may not read the templates). Both are empty when the Pipeline
// has no verification or it would build.
type previewVerification struct {
	Fails      string `json:"fails,omitempty"`
	NotChecked string `json:"notChecked,omitempty"`
}

// buildPreview builds in as the controller would, with the AnalysisTemplates
// its verification names read from the cluster through c (#2011). When the
// controller would fail the version for its verification, the result is the
// Graph without verification, and Fails says why; when the CLI cannot read
// the templates (RBAC), it is also the Graph without verification, and
// NotChecked says why. Other build errors are returned.
func buildPreview(ctx context.Context, c sigs_client.Client, b *graph.Builder, in graph.BuildInput) (*graph.BuildResult, previewVerification, error) {
	preview, analyses, err := previewPipeline(in.Pipeline)
	if err != nil {
		return nil, previewVerification{}, err
	}
	without := in
	without.Pipeline = preview
	if len(analyses) == 0 {
		res, err := b.Build(without)
		return res, previewVerification{}, err
	}
	read, readErr := translator.CollectAnalyses(ctx, c, c.RESTMapper(), in.Pipeline)
	if readErr != nil {
		res, err := b.Build(without)
		return res, previewVerification{NotChecked: "the AnalysisTemplates could not be read: " + readErr.Error()}, err
	}
	full := in
	full.Analyses = read
	res, fullErr := b.Build(full)
	if fullErr == nil {
		return res, previewVerification{}, nil
	}
	res, err = b.Build(without)
	if err != nil {
		return nil, previewVerification{}, err // not verification's: report the build error
	}
	return res, previewVerification{Fails: strings.TrimPrefix(fullErr.Error(), "build: ")}, nil
}
