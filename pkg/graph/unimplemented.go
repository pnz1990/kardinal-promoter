// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// layoutBranchNotImplemented is the reason a layout: branch Pipeline cannot
// promote; the git-clone step fails its PromotionSteps with the same text.
const layoutBranchNotImplemented = "layout: branch is not implemented: kardinal does not write rendered " +
	"manifests to an env/<name> branch yet, so this promotion would change nothing; use layout: directory " +
	"(see docs/rendered-manifests.md)"

// UnimplementedFields returns one message per reserved Pipeline field that is
// set but not implemented, or nil. A Bundle fails where the field takes
// effect: Build rejects spec.environments[].steps and promotionTemplate, so
// every Bundle fails when its Graph is built; the PromotionStep reconciler
// fails regions fan-out (two or more regions), health.cluster and a
// health.resource.kind other than Deployment in that environment; the
// git-clone step fails layout: branch in every environment it applies to; and
// the API server rejects autoRollback (CRD CEL), which is also listed so a
// file checked offline gets the same answer.
//
// A git.secretRef in another namespace is refused on purpose, not
// unimplemented: see ValidateSecretRef.
//
// The Pipeline reconciler (Ready=False, reason NotImplemented), "kardinal
// validate" and the admission warnings all call it, so the Pipeline status,
// the CLI and the API server agree with what a Bundle does.
func UnimplementedFields(p *kardinalv1alpha1.Pipeline) []string {
	var msgs []string
	if p.Spec.Git.Layout == "branch" {
		msgs = append(msgs, "spec.git."+layoutBranchNotImplemented)
	}
	for i := range p.Spec.Environments {
		e := &p.Spec.Environments[i]
		if msg := customStepsUnimplemented(e); msg != "" {
			msgs = append(msgs, msg)
		}
		if e.AutoRollback != nil {
			msgs = append(msgs, fmt.Sprintf("environment %q: environments[].autoRollback is not implemented; "+
				"remove it (automatic rollback is configured with onHealthFailure, see docs/rollback.md)", e.Name))
		}
		if len(e.Regions) >= 2 {
			msgs = append(msgs, fmt.Sprintf("environment %q: environments[].regions fan-out is not implemented: "+
				"every region would push the same change to one branch; declare one environment per region "+
				"(e.g. prod-us, prod-eu)", e.Name))
		}
		if e.Layout == "branch" {
			msgs = append(msgs, fmt.Sprintf("environment %q: %s", e.Name, layoutBranchNotImplemented))
		}
		if e.Health.Cluster != "" {
			msgs = append(msgs, fmt.Sprintf("environment %q: health.cluster is not supported: remote-cluster "+
				"health checks are not implemented; for a workload in another cluster, check its Argo CD "+
				"Application in this cluster (health.type: argocd)", e.Name))
		}
		// As the PromotionStep reconciler checks it: only the resource adapter
		// reads health.resource, and it fails the step after the change merged.
		if res := e.Health.Resource; res != nil && res.Kind != "" && res.Kind != "Deployment" &&
			health.EffectiveType(*e) == health.DefaultType {
			msgs = append(msgs, fmt.Sprintf("environment %q: health.resource.kind %q is not supported: "+
				"only Deployment is checked", e.Name, res.Kind))
		}
	}
	return msgs
}

// customStepsUnimplemented returns why e's custom step sequence cannot run, or
// "". The PromotionStep reconciler always runs the default step sequence
// (steps.DefaultSequenceForBundle); PromotionStepSpec has no field to carry a
// custom one.
func customStepsUnimplemented(e *kardinalv1alpha1.EnvironmentSpec) string {
	if len(e.Steps) > 0 {
		return fmt.Sprintf("environment %q declares %d steps; spec.environments[].steps is "+
			"not implemented yet (the controller always runs the default step sequence), so "+
			"remove it; see docs/pipeline-reference.md#promotion-steps", e.Name, len(e.Steps))
	}
	if e.PromotionTemplate != nil {
		return fmt.Sprintf("environment %q references PromotionTemplate %q; "+
			"spec.environments[].promotionTemplate is not implemented yet (the controller always "+
			"runs the default step sequence), so remove it; see docs/pipeline-reference.md#promotion-steps",
			e.Name, e.PromotionTemplate.Name)
	}
	return ""
}
