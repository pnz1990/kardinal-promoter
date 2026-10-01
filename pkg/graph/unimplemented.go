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

// ShardNotSupported is the reason a Pipeline environment may not set shard:
// distributed mode (the kardinal-agent binary and --shard) was removed.
const ShardNotSupported = "shard is not supported: distributed mode was removed; remove shard from the " +
	"environment and the controller reconciles it (see docs/distributed-mode.md)"

// HealthClusterNotSupported is the reason a Pipeline environment may not set
// health.cluster: kardinal checks health only in the cluster it runs in.
const HealthClusterNotSupported = "health.cluster is not supported: kardinal checks health only in the " +
	"cluster it runs in; for a workload in another cluster, check its Argo CD Application or Flux " +
	"Kustomization in this cluster (health.type: argocd or flux, see docs/health-adapters.md#remote-clusters)"

// RegionsNotSupported is the reason Build rejects two or more
// spec.environments[].regions: every region would push the same change to
// the same branch.
const RegionsNotSupported = "regions is not supported; declare one environment per region (prod-us, prod-eu) " +
	"and use wave"

// UnimplementedFields returns one message per reserved or unsupported
// Pipeline field that is set, or nil. A Bundle fails where the field takes
// effect: Build rejects spec.environments[].steps and promotionTemplate
// (deprecated; the API server also rejects them) and two or more regions, so
// every Bundle fails when its Graph is built; the PromotionStep reconciler
// fails shard, health.cluster and a
// health.resource.kind other than Deployment in that environment; the
// git-clone step fails layout: branch in every environment it applies to; and
// the API server rejects autoRollback, steps and promotionTemplate (CRD CEL),
// which are also listed so a file checked offline, or a Pipeline stored before
// the CEL rules existed, gets the same answer.
//
// A git.secretRef in another namespace is refused on purpose, not
// unimplemented: see ValidateSecretRef.
//
// The Pipeline reconciler (Ready=False, reason NotImplemented) and "kardinal
// validate" both call it, so the Pipeline status and the CLI agree with what
// a Bundle does.
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
		if len(e.Regions) >= 2 { //nolint:staticcheck // SA1019: read to reject it
			msgs = append(msgs, fmt.Sprintf("environment %q: %s", e.Name, RegionsNotSupported))
		}
		if e.Shard != "" { //nolint:staticcheck // SA1019: read to reject it
			msgs = append(msgs, fmt.Sprintf("environment %q: %s", e.Name, ShardNotSupported))
		}
		if e.Layout == "branch" {
			msgs = append(msgs, fmt.Sprintf("environment %q: %s", e.Name, layoutBranchNotImplemented))
		}
		if e.Health.Cluster != "" { //nolint:staticcheck // SA1019: read to reject it
			msgs = append(msgs, fmt.Sprintf("environment %q: %s", e.Name, HealthClusterNotSupported))
		}
		// As the PromotionStep reconciler checks it: only the resource adapter
		// reads health.resource, and it fails the step before any git change.
		if res := e.Health.Resource; res != nil && res.Kind != "" && res.Kind != "Deployment" &&
			health.EffectiveType(*e) == health.DefaultType {
			msgs = append(msgs, fmt.Sprintf("environment %q: health.resource.kind %q is not supported: "+
				"only Deployment is checked", e.Name, res.Kind))
		}
	}
	return msgs
}

// customStepsUnimplemented returns why e's custom step sequence cannot run, or
// "". kardinal has no custom step engine: the PromotionStep reconciler always
// runs the default step sequence (steps.DefaultSequenceForBundle), and the
// PromotionTemplate CRD was removed. The CRD CEL rules reject both fields at
// admission; this check covers Pipelines stored before those rules existed.
func customStepsUnimplemented(e *kardinalv1alpha1.EnvironmentSpec) string {
	stepSpecs := e.Steps               //nolint:staticcheck // SA1019: read the deprecated field to reject it
	templateRef := e.PromotionTemplate //nolint:staticcheck // SA1019: read the deprecated field to reject it
	if len(stepSpecs) > 0 {
		return fmt.Sprintf("environment %q declares %d steps; spec.environments[].steps is "+
			"not supported (kardinal has no custom step engine and always runs the default step "+
			"sequence), so remove it; see docs/pipeline-reference.md#promotion-steps", e.Name, len(stepSpecs))
	}
	if templateRef != nil {
		return fmt.Sprintf("environment %q references PromotionTemplate %q; "+
			"spec.environments[].promotionTemplate is not supported (the PromotionTemplate CRD was "+
			"removed and the controller always runs the default step sequence), so remove it; "+
			"see docs/pipeline-reference.md#promotion-steps",
			e.Name, templateRef.Name)
	}
	return ""
}
