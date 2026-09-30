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
// is the only honest behaviour.
func unsupportedConfig(pipeline *v1alpha1.Pipeline, env v1alpha1.EnvironmentSpec, ps *v1alpha1.PromotionStep) string {
	// Confused deputy: refused on purpose. The Pipeline reconciler reports the
	// same error as Ready=False/ValidationFailed.
	if err := graph.ValidateSecretRef(pipeline); err != nil {
		return err.Error()
	}
	// Distributed mode was removed. The Pipeline reconciler reports the same
	// field as Ready=False/NotImplemented; this fails a step left over from a
	// Graph built before the upgrade instead of leaving it Pending.
	if env.Shard != "" {
		return graph.ShardNotSupported
	}
	if env.Health.Cluster != "" {
		return graph.HealthClusterNotSupported
	}
	// Build rejects two or more regions, but a Graph built before the upgrade
	// is not rebuilt until the Pipeline spec changes, so its region steps can
	// still run.
	if ps.Spec.Region != "" {
		return graph.RegionsNotSupported
	}
	if res := env.Health.Resource; res != nil && res.Kind != "" && res.Kind != "Deployment" &&
		health.EffectiveType(env) == health.DefaultType {
		return fmt.Sprintf("health.resource.kind %q is not supported: only Deployment is checked", res.Kind)
	}
	return ""
}
