// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
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
		case ok && (h.Result == v1alpha1.HookRunSucceeded || h.Result == v1alpha1.HookRunSkipped):
			// Skipped: added to the Pipeline after the step passed the point it
			// runs at (recordSkippedHooks notes it on the step).
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

// holdForImageVerification keeps a Pending step that waits for the
// Bundle's ImageVerification (spec.imageVerification, a root step of a
// Pipeline with spec.imageVerification) Pending until the mirror shows it
// Verified, and fails it when it Failed (docs/image-verification.md).
func (r *Reconciler) holdForImageVerification(ctx context.Context, log zerolog.Logger, base, ps *v1alpha1.PromotionStep) (held bool, res ctrl.Result, err error) {
	if ps.Spec.ImageVerification == "" {
		return false, ctrl.Result{}, nil
	}
	var live v1alpha1.LiveImageVerification
	if ps.Spec.Live != nil && ps.Spec.Live.ImageVerification != nil {
		live = *ps.Spec.Live.ImageVerification
	}
	switch {
	case live.Name != "" && live.Name != ps.Spec.ImageVerification:
		// The mirror still shows the previous ImageVerification (a policy
		// change renamed it): wait for the new one's result.
		live = v1alpha1.LiveImageVerification{Message: fmt.Sprintf("the result shown is for %s", live.Name)}
	case live.Phase == v1alpha1.ImageVerificationVerified:
		bundle, err := r.loadBundle(ctx, ps)
		if err != nil {
			return true, ctrl.Result{}, err
		}
		if why := verifiedImagesDiffer(bundle, live.Images); why != "" {
			log.Warn().Str("imageVerification", ps.Spec.ImageVerification).Str("reason", why).Msg("the Bundle's images are not the verified ones")
			return true, ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, fmt.Sprintf(
				"refusing to promote: %s (image verification %s)", why, ps.Spec.ImageVerification))
		}
		return false, ctrl.Result{}, nil
	}
	if live.Phase == v1alpha1.ImageVerificationFailed {
		log.Info().Str("imageVerification", ps.Spec.ImageVerification).Msg("image verification failed — step failed before promoting")
		msg := fmt.Sprintf("image verification %s failed", ps.Spec.ImageVerification)
		if live.Message != "" {
			msg += ": " + live.Message
		}
		return true, ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
	}
	msg := fmt.Sprintf("waiting for image verification %s", ps.Spec.ImageVerification)
	if live.Message != "" {
		msg += ": " + live.Message
	}
	if ps.Status.Message != msg {
		ps.Status.Message = msg
		patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base))
		if apierrors.IsNotFound(patchErr) {
			return true, ctrl.Result{}, nil
		}
		if patchErr != nil {
			return true, ctrl.Result{}, fmt.Errorf("patch image verification wait message: %w", patchErr)
		}
	}
	return true, ctrl.Result{RequeueAfter: requeueGateWait}, nil
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

// ConditionHooksSkipped is True when hooks were added to the Pipeline after
// this step passed the point they run at; they were not run for this
// Bundle (their HookRuns are Skipped).
const ConditionHooksSkipped = "HooksSkipped"

// recordSkippedHooks sets ConditionHooksSkipped from spec.live.hooks. It
// reports whether the condition changed.
func recordSkippedHooks(ps *v1alpha1.PromotionStep, now time.Time) bool {
	if ps.Spec.Live == nil {
		return false
	}
	var skipped []string
	for _, h := range ps.Spec.Live.Hooks {
		if h.Result == v1alpha1.HookRunSkipped {
			name := h.Hook
			if name == "" {
				name = h.Name
			}
			skipped = append(skipped, fmt.Sprintf("%s-deploy hook %s", h.Phase, name))
		}
	}
	if len(skipped) == 0 {
		return false
	}
	msg := "added to the Pipeline after this step passed the point they run at, not run for this Bundle: " +
		strings.Join(skipped, ", ")
	if c := meta.FindStatusCondition(ps.Status.Conditions, ConditionHooksSkipped); c != nil && c.Message == msg {
		return false
	}
	meta.SetStatusCondition(&ps.Status.Conditions, metav1.Condition{
		Type: ConditionHooksSkipped, Status: metav1.ConditionTrue, Reason: "AddedTooLate", Message: msg,
		ObservedGeneration: ps.Generation, LastTransitionTime: metav1.NewTime(now),
	})
	return true
}

// verifiedImagesDiffer returns why bundle's images are not the ones an
// ImageVerification verified (verified: "repository@digest", repositories
// normalized), or "": every verified image is in the Bundle with that
// digest, and no Bundle image of a verified repository has another digest.
// A repository that does not parse fails closed.
func verifiedImagesDiffer(bundle *v1alpha1.Bundle, verified []string) string {
	want := map[string]string{}
	for _, v := range verified {
		repo, digest, ok := strings.Cut(v, "@")
		if !ok {
			return fmt.Sprintf("verified image %q has no digest", v)
		}
		want[repo] = digest
	}
	have := map[string]string{}
	for _, img := range bundle.Spec.Images {
		repo, err := graph.NormalizeRepository(img.Repository)
		if err != nil {
			return err.Error()
		}
		if d, ok := want[repo]; ok && img.Digest != d {
			return fmt.Sprintf("the Bundle's image %s has digest %q, not the verified %s", repo, img.Digest, d)
		}
		have[repo] = img.Digest
	}
	for repo, d := range want {
		if have[repo] != d {
			return fmt.Sprintf("the verified image %s@%s is not in the Bundle", repo, d)
		}
	}
	return ""
}
