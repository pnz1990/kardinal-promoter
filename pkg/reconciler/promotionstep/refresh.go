// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
)

// Event reasons of the GitOps refresh request.
const (
	ReasonRefreshRequested = "GitOpsRefreshRequested"
	ReasonRefreshFailed    = "GitOpsRefreshFailed"
)

// refreshWanted reports whether a HealthChecking step should ask the GitOps
// tool to sync now (health.refresh): refresh is on (controller and
// environment), the health type names an Argo CD Application or a Flux
// Kustomization, it has not asked yet, and the promotion wrote a commit that
// is in git now. rev and pending are expectedRevision's.
//
// A step with no changes wrote nothing: noChangesRefreshWanted decides for
// it once its health check ran. A step with neither a pushed commit nor a PR (update.strategy argocd patches the Application itself) has no
// commit to sync. While the PR merge commit may still be recorded (pending),
// the step waits for it, so the refresh is recorded against the commit; when
// the SCM never reports it, the refresh is requested without a revision.
func (r *Reconciler) refreshWanted(ps *v1alpha1.PromotionStep, env v1alpha1.EnvironmentSpec, healthType, rev string, pending bool) bool {
	switch {
	case !r.refreshAllowed(ps, env, healthType),
		ps.Status.Outputs["noChanges"] == "true":
		return false
	case rev != "":
		return true
	default:
		return opensPR(ps) && !pending
	}
}

// refreshAllowed reports whether the step may still ask the GitOps tool to
// sync: refresh is on (controller and environment), the health type names an
// Argo CD Application or a Flux Kustomization, and the step has not asked yet.
func (r *Reconciler) refreshAllowed(ps *v1alpha1.PromotionStep, env v1alpha1.EnvironmentSpec, healthType string) bool {
	return !r.RefreshDisabled &&
		(env.Health.Refresh == nil || *env.Health.Refresh) &&
		health.RefreshSupported(healthType) &&
		ps.Status.RefreshRequestedAt == nil
}

// noChangesRefreshWanted reports whether a step whose git-commit found
// nothing to commit should ask the GitOps tool to sync the branch head it
// waits for (rev, the head it cloned, #1669) after a health check that did
// not pass (#1910). Another writer pushed that head, so the tool may not have
// synced it yet, and its poll can take minutes. A check that passed means the
// tool applied the head (or a descendant), and there is nothing to ask for; a
// terminal result does not recover by syncing. A check that fails for another
// reason (the workload rolling out on the head) spends the one request
// harmlessly: the tool syncs what it already has.
func (r *Reconciler) noChangesRefreshWanted(ps *v1alpha1.PromotionStep, env v1alpha1.EnvironmentSpec, healthType, rev string,
	result health.HealthStatus) bool {
	return ps.Status.Outputs["noChanges"] == "true" && rev != "" && !result.Healthy && !result.Terminal &&
		r.refreshAllowed(ps, env, healthType)
}

// requestRefresh asks the GitOps object the health check names to sync the
// promoted commit now, instead of at the tool's next poll.
//
// The request is made at most once per step: status.refreshRequestedAt,
// status.refreshRevision and status.refreshMessage are patched before the
// annotations are, so a controller that crashes in between does not ask
// again (that step waits for the tool's poll, as before this existed). The
// Flux requestedAt value sent is health.FluxRequestValue of the recorded time
// and the step. A failed read of the Kustomization (for its source) spends
// the one request too: the step does not retry, the poll applies.
//
// A failure is not a health failure: the step records it in
// status.refreshMessage, emits a Warning Event and goes on checking health,
// which the tool's own poll then satisfies. Only the status patch can fail
// the reconcile. The patch of the annotations changes only
// metadata.annotations of the Application, Kustomization or GitRepository:
// no other object's status is written. Errors are written to status and the
// Event classified (health.ClassifyRefreshError): the API server's answer and
// address, which can be a remote cluster's, go to the log only.
func (r *Reconciler) requestRefresh(ctx context.Context, log zerolog.Logger, base, ps *v1alpha1.PromotionStep,
	detector *health.AutoDetector, opts health.CheckOptions, rev string) error {
	targets, targetsErr := detector.RefreshTargets(ctx, opts)
	at := metav1.NewTime(r.now().UTC().Truncate(time.Second))
	ps.Status.RefreshRequestedAt = &at
	ps.Status.RefreshRevision = rev
	if targetsErr == nil && len(targets) > 0 {
		ps.Status.RefreshMessage = "refresh requested: " + health.DescribeTargets(targets)
	} else {
		ps.Status.RefreshMessage = refreshFailedMessage(opts.Type, targetsErr)
	}
	if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch refresh request: %w", err)
	}
	if targetsErr != nil || len(targets) == 0 {
		log.Warn().Err(targetsErr).Str("adapter", opts.Type).Msg("GitOps refresh not requested")
		kubeevent.Emit(r.Recorder, ps, corev1.EventTypeWarning, ReasonRefreshFailed, "Verify", ps.Status.RefreshMessage)
		return nil
	}
	if err := detector.RequestRefresh(ctx, targets, health.FluxRequestValue(at.Time, ps.Namespace, ps.Name)); err != nil {
		log.Warn().Err(err).Str("adapter", opts.Type).Msg("GitOps refresh request failed")
		next := ps.DeepCopy()
		next.Status.RefreshMessage = refreshFailedMessage(opts.Type, err)
		if perr := r.Status().Patch(ctx, next, client.MergeFrom(ps)); perr != nil {
			return fmt.Errorf("patch refresh failure: %w", perr)
		}
		*ps = *next
		kubeevent.Emit(r.Recorder, ps, corev1.EventTypeWarning, ReasonRefreshFailed, "Verify", ps.Status.RefreshMessage)
		return nil
	}
	log.Info().Str("adapter", opts.Type).Str("revision", rev).Str("targets", health.DescribeTargets(targets)).
		Msg("GitOps refresh requested")
	kubeevent.Emit(r.Recorder, ps, corev1.EventTypeNormal, ReasonRefreshRequested, "Verify", ps.Status.RefreshMessage)
	return nil
}

// refreshFailedMessage is the status.refreshMessage of a failed request.
func refreshFailedMessage(healthType string, err error) string {
	tool := "Argo CD"
	if healthType == "flux" {
		tool = "Flux"
	}
	reason := "no object to refresh"
	if err != nil {
		reason = health.ClassifyRefreshError(err)
	}
	return fmt.Sprintf("refresh request failed (%s); waiting for %s to poll git", reason, tool)
}
