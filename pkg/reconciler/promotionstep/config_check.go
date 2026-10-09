// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
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
	if env.Shard != "" { //nolint:staticcheck // SA1019: read to reject it
		return graph.ShardNotSupported
	}
	if env.Health.Cluster != "" { //nolint:staticcheck // SA1019: read to reject it
		return graph.HealthClusterNotSupported
	}
	// Build rejects two or more regions, but a Graph built before the upgrade
	// is not rebuilt until the Pipeline spec changes, so its region steps can
	// still run.
	if ps.Spec.Region != "" { //nolint:staticcheck // SA1019: read to reject it
		return graph.RegionsNotSupported
	}
	if res := env.Health.Resource; res != nil && res.Kind != "" && res.Kind != "Deployment" &&
		health.EffectiveType(env) == health.DefaultType {
		return fmt.Sprintf("health.resource.kind %q is not supported: only Deployment is checked", res.Kind)
	}
	return ""
}

// repositoryNotAllowed returns why the step must not run because its
// Pipeline would have the controller's shared SCM token act on a repository
// --scm-allowed-repositories does not allow (#1332), or "". The Pipeline's
// own git.secretRef exempts it only when that Secret exists and no
// environment opens a PR (scm.RepositoryAllowlist.CheckPipeline). The
// Pipeline reconciler reports the same as Ready=False/RepositoryNotAllowed.
func (r *Reconciler) repositoryNotAllowed(ctx context.Context, pipeline *v1alpha1.Pipeline) (string, error) {
	if r.AllowedRepositories.Allows(pipeline.Spec.Git.URL) {
		return "", nil
	}
	own, err := scm.PipelineSecretExists(ctx, r.Client, pipeline)
	if err != nil {
		return "", err
	}
	if err := r.AllowedRepositories.CheckPipeline(pipeline, own); err != nil {
		return err.Error(), nil
	}
	return "", nil
}
