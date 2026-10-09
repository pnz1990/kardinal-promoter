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
)

// Pre- and post-deploy hooks (docs/hooks.md). The step reads hook results
// only from its own spec.live.hooks, which the Graph's mirror patch node
// writes from the HookRuns (pkg/graph/hooks.go). A mirror write changes the
// step's generation, which wakes it.

// StateVerifying — the health check passed; the post-deploy hooks run. The
// step is Verified when they all succeeded.
const StateVerifying = "Verifying"

// hookVerdict is what a step's hooks of one phase say.
type hookVerdict struct {
	// failed is the first hook, in order, that Failed (nil when none).
	failed *hookRef
	// waiting is the first hook, in order, that has not succeeded yet.
	waiting *hookRef
}

// hookRef is one hook as the mirror shows it.
type hookRef struct {
	run    string // HookRun name
	live   v1alpha1.LiveHookRun
	mirror bool // the mirror has an entry for it
}

// String names the hook: its Pipeline name and HookRun, or the HookRun name
// alone when the mirror has no entry for it yet.
func (h *hookRef) String() string {
	if h.live.Hook == "" {
		return h.run
	}
	return h.live.Hook + " (HookRun " + h.run + ")"
}

func (v hookVerdict) succeeded() bool { return v.failed == nil && v.waiting == nil }

// hookResults checks names, in order, against ps.Spec.Live.Hooks.
func hookResults(ps *v1alpha1.PromotionStep, names []string) hookVerdict {
	live := map[string]v1alpha1.LiveHookRun{}
	if ps.Spec.Live != nil {
		for _, h := range ps.Spec.Live.Hooks {
			live[h.Name] = h
		}
	}
	var v hookVerdict
	for _, name := range names {
		h, ok := live[name]
		ref := &hookRef{run: name, live: h, mirror: ok}
		switch {
		case ok && h.Result == v1alpha1.HookRunFailed:
			if v.failed == nil {
				v.failed = ref
			}
		case ok && h.Result == v1alpha1.HookRunSucceeded:
		case v.waiting == nil:
			v.waiting = ref
		}
	}
	return v
}

func (v hookVerdict) waitMessage(phase string) string {
	state := v.waiting.live.Result
	if !v.waiting.mirror || state == "" {
		state = "not started"
	}
	return fmt.Sprintf("waiting for %s-deploy hook %s: %s", phase, v.waiting, state)
}

func (v hookVerdict) failMessage(phase string) string {
	msg := fmt.Sprintf("%s-deploy hook %s failed", phase, v.failed)
	if v.failed.live.Message != "" {
		msg += ": " + v.failed.live.Message
	}
	return msg
}

// holdForPreHooks keeps a Pending step Pending until every pre-deploy hook
// succeeded, and fails it when one failed. held reports that the caller must
// return res and err.
func (r *Reconciler) holdForPreHooks(ctx context.Context, log zerolog.Logger, base, ps *v1alpha1.PromotionStep) (held bool, res ctrl.Result, err error) {
	if len(ps.Spec.PreHooks) == 0 {
		return false, ctrl.Result{}, nil
	}
	v := hookResults(ps, ps.Spec.PreHooks)
	if v.succeeded() {
		return false, ctrl.Result{}, nil
	}
	if v.failed != nil {
		log.Info().Str("hook", v.failed.String()).Msg("pre-deploy hook failed — step failed before promoting")
		return true, ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, v.failMessage("pre"))
	}
	msg := v.waitMessage("pre")
	if ps.Status.Message != msg {
		ps.Status.Message = msg
		patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base))
		if apierrors.IsNotFound(patchErr) {
			return true, ctrl.Result{}, nil
		}
		if patchErr != nil {
			return true, ctrl.Result{}, fmt.Errorf("patch pre-hook wait message: %w", patchErr)
		}
	}
	return true, ctrl.Result{RequeueAfter: requeueGateWait}, nil
}

// passHealth is called when the health check (and bake window) passed: a
// step with post-deploy hooks enters Verifying, every other step is
// Verified. reason and message are those of the Verified condition and state
// message.
func (r *Reconciler) passHealth(ctx context.Context, base, ps *v1alpha1.PromotionStep, reason, message string) error {
	if len(ps.Spec.PostHooks) == 0 {
		return r.verify(ctx, base, ps, reason, message)
	}
	if ps.Status.VerificationStartedAt == nil {
		now := metav1.NewTime(r.now().UTC())
		ps.Status.VerificationStartedAt = &now
	}
	return r.transition(ctx, base, ps, StateVerifying,
		fmt.Sprintf("%s; running %d post-deploy hook(s): %s", message, len(ps.Spec.PostHooks),
			strings.Join(ps.Spec.PostHooks, ", ")))
}

// handleVerifying waits for the post-deploy hooks. All succeeded: Verified.
// One failed: onHealthFailure applies (none: Failed; abort: AbortedByAlarm;
// rollback: a rollback Bundle), as for a failed health check.
func (r *Reconciler) handleVerifying(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	base := ps.DeepCopy()
	v := hookResults(ps, ps.Spec.PostHooks)
	switch {
	case v.succeeded():
		log.Info().Str("env", ps.Spec.Environment).Msg("post-deploy hooks succeeded, Verified")
		return ctrl.Result{}, r.verify(ctx, base, ps, "PostHooksSucceeded",
			fmt.Sprintf("post-deploy hooks succeeded: %s", strings.Join(ps.Spec.PostHooks, ", ")))
	case v.failed != nil:
		pipeline, err := r.loadPipeline(ctx, ps)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("load pipeline: %w", err)
		}
		env := findEnv(pipeline, ps.Spec.Environment)
		log.Info().Str("hook", v.failed.String()).Str("onHealthFailure", env.OnHealthFailure).Msg("post-deploy hook failed")
		return r.applyHealthFailurePolicy(ctx, log, base, ps, env, "post-deploy hooks", v.failMessage("post"))
	}
	if msg := v.waitMessage("post"); ps.Status.Message != msg {
		ps.Status.Message = msg
		if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("patch post-hook wait message: %w", err)
		}
	}
	return ctrl.Result{RequeueAfter: requeueGateWait}, nil
}
