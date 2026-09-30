// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"fmt"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// unsupportedConfig returns a message when the Pipeline asks for something the
// reconciler must refuse, or "" when the configuration is usable.
//
// Each case used to be accepted and then silently ignored or, for the Secret
// namespace, honoured in an unsafe way. Failing the step with a clear message
// is the only honest behaviour until the feature exists.
func unsupportedConfig(pipeline *v1alpha1.Pipeline, env v1alpha1.EnvironmentSpec, ps *v1alpha1.PromotionStep) string {
	// Confused deputy: refused on purpose. The Pipeline reconciler reports the
	// same error as Ready=False/ValidationFailed.
	if err := graph.ValidateSecretRef(pipeline); err != nil {
		return err.Error()
	}
	if env.Health.Cluster != "" {
		return "health.cluster is not supported: remote-cluster health checks are not implemented; " +
			"for a workload in another cluster, check its Argo CD Application in this cluster (health.type: argocd)"
	}
	if ps.Spec.Region != "" {
		return "environments[].regions fan-out is not implemented: every region would push the same change " +
			"to one branch; declare one environment per region (e.g. prod-us, prod-eu)"
	}
	if res := env.Health.Resource; res != nil && res.Kind != "" && res.Kind != "Deployment" &&
		health.EffectiveType(env) == health.DefaultType {
		return fmt.Sprintf("health.resource.kind %q is not supported: only Deployment is checked", res.Kind)
	}
	return ""
}
