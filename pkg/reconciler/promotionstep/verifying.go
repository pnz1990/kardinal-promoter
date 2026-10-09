// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// Verifying: after the health check passed, a step with post-deploy hooks
// (docs/hooks.md) or Argo Rollouts analyses (docs/analysis.md) waits for
// them. Their results reach the step only through spec.live, which the
// Graph's mirror patch node writes.

// AnalysisRun phases (Argo Rollouts).
const (
	analysisSuccessful   = "Successful"
	analysisFailed       = "Failed"
	analysisError        = "Error"
	analysisInconclusive = "Inconclusive"
)

// verifies reports whether ps has anything to wait for after its health
// check.
func verifies(ps *v1alpha1.PromotionStep) bool {
	return len(ps.Spec.PostHooks) > 0 || len(ps.Spec.Analyses) > 0
}

// passHealth is called when the health check (and bake window) passed: a
// step with post-deploy hooks or analyses enters Verifying, every other step
// is Verified. reason and message are those of the Verified condition and
// state message.
func (r *Reconciler) passHealth(ctx context.Context, base, ps *v1alpha1.PromotionStep, reason, message string) error {
	if !verifies(ps) {
		return r.verify(ctx, base, ps, reason, message)
	}
	if ps.Status.VerificationStartedAt == nil {
		now := metav1.NewTime(r.now().UTC())
		ps.Status.VerificationStartedAt = &now
	}
	var running []string
	if n := len(ps.Spec.PostHooks); n > 0 {
		running = append(running, fmt.Sprintf("%d post-deploy hook(s): %s", n, strings.Join(ps.Spec.PostHooks, ", ")))
	}
	if n := len(ps.Spec.Analyses); n > 0 {
		running = append(running, fmt.Sprintf("%d analysis(es): %s", n, strings.Join(ps.Spec.Analyses, ", ")))
	}
	return r.transition(ctx, base, ps, StateVerifying, message+"; running "+strings.Join(running, " and "))
}

// analysisVerdict is what a step's AnalysisRuns say.
type analysisVerdict struct {
	failed, waiting *v1alpha1.LiveAnalysisRun
	// waitingName is the AnalysisRun name of waiting (the mirror may have no
	// entry for it yet).
	waitingName string
}

// analysisResults checks ps.Spec.Analyses, in order, against
// ps.Spec.Live.Analyses. inconclusivePasses counts Inconclusive as
// Successful.
func analysisResults(ps *v1alpha1.PromotionStep, inconclusivePasses bool) analysisVerdict {
	live := map[string]v1alpha1.LiveAnalysisRun{}
	if ps.Spec.Live != nil {
		for _, a := range ps.Spec.Live.Analyses {
			live[a.Name] = a
		}
	}
	var v analysisVerdict
	for _, name := range ps.Spec.Analyses {
		a, ok := live[name]
		switch {
		case ok && (a.Phase == analysisSuccessful || (a.Phase == analysisInconclusive && inconclusivePasses)):
		case ok && (a.Phase == analysisFailed || a.Phase == analysisError || a.Phase == analysisInconclusive):
			if v.failed == nil {
				a := a
				v.failed = &a
			}
		case v.waiting == nil:
			if !ok {
				a = v1alpha1.LiveAnalysisRun{Name: name}
			}
			v.waiting, v.waitingName = &a, name
		}
	}
	return v
}

func analysisLabel(a *v1alpha1.LiveAnalysisRun) string {
	if a.Template == "" {
		return a.Name
	}
	return a.Template + " (AnalysisRun " + a.Name + ")"
}

// handleVerifying waits for the post-deploy hooks and the analyses. All
// passed: Verified. One failed, or the analyses ran past
// spec.verification.timeout: onHealthFailure applies (none: Failed; abort:
// AbortedByAlarm; rollback: a rollback Bundle), as for a failed health
// check.
func (r *Reconciler) handleVerifying(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	base := ps.DeepCopy()
	hooks := hookResults(ps, ps.Spec.PostHooks)
	var env v1alpha1.EnvironmentSpec
	if len(ps.Spec.Analyses) > 0 || hooks.failed != nil {
		pipeline, err := r.loadPipeline(ctx, ps)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("load pipeline: %w", err)
		}
		env = findEnv(pipeline, ps.Spec.Environment)
	}
	if hooks.failed != nil {
		log.Info().Str("hook", hooks.failed.String()).Str("onHealthFailure", env.OnHealthFailure).Msg("post-deploy hook failed")
		return r.applyHealthFailurePolicy(ctx, log, base, ps, env, "post-deploy hooks", hooks.failMessage("post"))
	}
	inconclusivePasses := env.Verification != nil && env.Verification.Inconclusive == "pass"
	an := analysisResults(ps, inconclusivePasses)
	if an.failed != nil {
		msg := fmt.Sprintf("analysis %s %s", analysisLabel(an.failed), strings.ToLower(an.failed.Phase))
		if an.failed.Message != "" {
			msg += ": " + an.failed.Message
		}
		log.Info().Str("analysis", an.failed.Name).Str("phase", an.failed.Phase).Msg("analysis did not pass")
		return r.applyHealthFailurePolicy(ctx, log, base, ps, env, "analysis", msg)
	}
	if hooks.succeeded() && an.waiting == nil {
		reason, what := "PostHooksSucceeded", "post-deploy hooks succeeded: "+strings.Join(ps.Spec.PostHooks, ", ")
		if len(ps.Spec.Analyses) > 0 {
			reason, what = "VerificationSucceeded", "verification passed"
			if len(ps.Spec.PostHooks) > 0 {
				what += "; post-deploy hooks: " + strings.Join(ps.Spec.PostHooks, ", ")
			}
			what += "; analyses: " + strings.Join(ps.Spec.Analyses, ", ")
		}
		log.Info().Str("env", ps.Spec.Environment).Msg("verification passed, Verified")
		return ctrl.Result{}, r.verify(ctx, base, ps, reason, what)
	}

	requeue := requeueGateWait
	if an.waiting != nil {
		timeout := graph.DefaultAnalysisTimeout
		if env.Verification != nil {
			if d, err := graph.AnalysisTimeout(env.Verification.Timeout); err == nil {
				timeout = d
			}
		}
		if started := ps.Status.VerificationStartedAt; started != nil {
			left := started.Add(timeout).Sub(r.now())
			if left <= 0 {
				return r.applyHealthFailurePolicy(ctx, log, base, ps, env, "analysis", fmt.Sprintf(
					"analysis %s did not finish within %s (%s)", analysisLabel(an.waiting), timeout, phaseOrNotStarted(an.waiting.Phase)))
			}
			if left < requeue {
				requeue = left
			}
		}
	}
	var msg string
	switch {
	case !hooks.succeeded():
		msg = hooks.waitMessage("post")
	default:
		msg = fmt.Sprintf("waiting for analysis %s: %s", analysisLabel(an.waiting), phaseOrNotStarted(an.waiting.Phase))
	}
	if ps.Status.Message != msg {
		ps.Status.Message = msg
		if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("patch verification wait message: %w", err)
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func phaseOrNotStarted(phase string) string {
	if phase == "" {
		return "not started"
	}
	return phase
}
