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
	// The controller's own token Secret is never a Pipeline's git Secret;
	// the Pipeline reconciler reports the same as Ready=False/ValidationFailed.
	if err := r.ControllerSecret.Refuses(pipeline); err != nil {
		return err.Error(), nil
	}
	if r.AllowedRepositories.Allows(pipeline.Spec.Git.URL) {
		return "", nil
	}
	state, err := scm.PipelineSecretState(ctx, r.Client, pipeline)
	if err != nil {
		return "", err
	}
	// An unlabelled git Secret counts as the Pipeline's own: when that is
	// what allows the repository, the step goes on to wait for the label
	// (waitForReferenceableSecret) instead of failing here.
	if err := r.AllowedRepositories.CheckPipeline(pipeline, state != scm.PipelineSecretNone); err != nil {
		return err.Error(), nil
	}
	return "", nil
}
