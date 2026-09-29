// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"fmt"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// unsupportedConfig returns a message when the Pipeline asks for something the
// reconciler must refuse, or "" when the configuration is usable.
//
// Each case used to be accepted and then silently ignored or, for the Secret
// namespace, honoured in an unsafe way. Failing the step with a clear message
// is the only honest behaviour until the feature exists.
func unsupportedConfig(pipeline *v1alpha1.Pipeline, env v1alpha1.EnvironmentSpec, ps *v1alpha1.PromotionStep) string {
	if ref := pipeline.Spec.Git.SecretRef; ref != nil && ref.Namespace != "" && ref.Namespace != pipeline.Namespace {
		// Confused deputy: the controller can read Secrets in every namespace and
		// sends the token to spec.git.url, which the same author controls.
		return fmt.Sprintf(
			"git.secretRef.namespace %q is not allowed: the Secret must be in the Pipeline's namespace %q",
			ref.Namespace, pipeline.Namespace)
	}
	if env.Health.Cluster != "" {
		return "health.cluster is not supported: remote-cluster health checks are not implemented; " +
			"run a kardinal-agent in the workload cluster and set environments[].shard"
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
