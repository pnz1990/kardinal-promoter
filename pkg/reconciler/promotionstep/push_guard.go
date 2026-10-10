// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// outputPushIntent is the step output (status.outputs.pushIntent) a git-push
// to the environment's base branch records, in a status write of its own,
// right before it pushes. A newer Bundle's step that has it may already have
// pushed, so an older promotion of the same environment must not push any
// more (#1603).
const outputPushIntent = "pushIntent"

// errPushIntent wraps the error of the push intent write, so the reconciler
// tells its NotFound and Conflict from those of other steps.
var errPushIntent = errors.New("record the push intent")

// pushGuard is StepState.BeforePush for ps: it refuses a stale push.
//
// It reads ps's Bundle from the API server, not the cache: the Bundle
// reconciler may have superseded it a moment ago. A push is stale when:
//
//   - the step's Bundle is Superseded or Rejected (spec.rejected), or gone;
//   - for a push to the base branch (direct), a Bundle that supersedes it
//     (lifecycle.SupersedingSiblings: the same type, not rejected, in flight
//     or Verified, newer, and the Bundle not held) has a step for this
//     environment that recorded its push intent, or completed git-push.
//
// Before the second check a direct push records its own intent. With both
// steps writing their intent before pushing and checking the other's before
// every push attempt, the older push cannot land after the newer one: if the
// newer push landed first, the older one is a fast-forward only from a base
// fetched after it, and its check, made after that fetch, sees the newer
// intent written before the newer push.
//
// base is the copy of ps the reconcile's own status write is computed from:
// the intent write moves the resourceVersion of both, so that write's
// optimistic lock still holds.
func (r *Reconciler) pushGuard(ps, base *v1alpha1.PromotionStep, state *steps.StepState) func(ctx context.Context, direct bool) error {
	return func(ctx context.Context, direct bool) error {
		reader := r.APIReader
		if reader == nil {
			reader = r.Client
		}
		var b v1alpha1.Bundle
		if err := reader.Get(ctx, client.ObjectKey{Namespace: ps.Namespace, Name: ps.Spec.BundleName}, &b); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("%w: bundle %s is gone", steps.ErrStalePush, ps.Spec.BundleName)
			}
			return fmt.Errorf("read bundle %s: %w", ps.Spec.BundleName, err)
		}
		switch {
		case b.Spec.Rejected != nil || b.Status.Phase == "Rejected":
			return fmt.Errorf("%w: bundle %s was rejected", steps.ErrStalePush, b.Name)
		case b.Status.Phase == "Superseded":
			return fmt.Errorf("%w: bundle %s was superseded — promotion cancelled", steps.ErrStalePush, b.Name)
		}
		if !direct {
			// A PR branch is kardinal's own: an old PR is closed when its
			// Bundle is superseded, before anyone merges it.
			return nil
		}
		if err := r.recordPushIntent(ctx, ps, base, state); err != nil {
			return err
		}
		if newer, err := r.newerPushed(ctx, reader, ps, &b); err != nil {
			return err
		} else if newer != "" {
			return fmt.Errorf("%w: newer bundle %s already pushed to %s; this promotion is superseded and does not push",
				steps.ErrNewerPushed, newer, ps.Spec.Environment)
		}
		return nil
	}
}

// recordPushIntent writes status.outputs.pushIntent of ps, and keeps it in
// state.Outputs so the reconcile's own status write keeps it too. It is
// written once per step.
func (r *Reconciler) recordPushIntent(ctx context.Context, ps, base *v1alpha1.PromotionStep, state *steps.StepState) error {
	if state.Outputs[outputPushIntent] != "" {
		return nil
	}
	at := r.now().UTC().Format(time.RFC3339Nano)
	// Locked on the resourceVersion this reconcile read: a stale copy fails
	// with a Conflict and the step is retried from a fresh read, so the
	// write never turns a stale copy into a current one.
	written := ps.DeepCopy()
	before := written.DeepCopy()
	written.Status.Outputs = cloneMap(written.Status.Outputs)
	if written.Status.Outputs == nil {
		written.Status.Outputs = map[string]string{}
	}
	written.Status.Outputs[outputPushIntent] = at
	if err := r.Status().Patch(ctx, written, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("%w of %s: %w", errPushIntent, ps.Name, err)
	}
	ps.ResourceVersion = written.ResourceVersion
	if base != nil {
		base.ResourceVersion = written.ResourceVersion
	}
	if state.Outputs == nil {
		state.Outputs = map[string]string{}
	}
	state.Outputs[outputPushIntent] = at
	return nil
}

// newerPushed returns the name of a Bundle that supersedes b
// (lifecycle.SupersedingSiblings, Verified ones included) and whose step for
// ps's environment recorded its push intent or completed git-push, or "".
//
// The API server decides: the environment's steps are listed from it, and
// the Bundle and the Pipeline (holds) of each step that pushed or is pushing
// are read from it, so a stale cache never lets a push through. Only the
// rejected artifacts come from the cache: a rejection is final, so the cache
// can miss one (and refuse a push the newer Bundle would no longer make) but
// never invent one.
func (r *Reconciler) newerPushed(ctx context.Context, reader client.Reader, ps *v1alpha1.PromotionStep,
	b *v1alpha1.Bundle) (string, error) {
	var list v1alpha1.PromotionStepList
	if err := reader.List(ctx, &list, client.InNamespace(ps.Namespace), client.MatchingLabels{
		"kardinal.io/pipeline":    ps.Spec.PipelineName,
		"kardinal.io/environment": ps.Spec.Environment,
	}); err != nil {
		return "", fmt.Errorf("list the steps of %s/%s: %w", ps.Spec.PipelineName, ps.Spec.Environment, err)
	}
	var candidates []*v1alpha1.PromotionStep
	for i := range list.Items {
		s := &list.Items[i]
		if s.Spec.BundleName != b.Name && s.Spec.Environment == ps.Spec.Environment && pushedOrPushing(s) {
			candidates = append(candidates, s)
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	var p *v1alpha1.Pipeline
	var pl v1alpha1.Pipeline
	if err := reader.Get(ctx, client.ObjectKey{Namespace: ps.Namespace, Name: b.Spec.Pipeline}, &pl); err == nil {
		p = &pl
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("read pipeline %s: %w", b.Spec.Pipeline, err)
	}
	var bundles v1alpha1.BundleList
	if err := r.List(ctx, &bundles, client.InNamespace(ps.Namespace)); err != nil {
		return "", fmt.Errorf("list the bundles of %s: %w", ps.Spec.PipelineName, err)
	}
	rejected := lifecycle.RejectedArtifactsOf(bundles.Items, b.Spec.Pipeline)
	for _, s := range candidates {
		var other v1alpha1.Bundle
		if err := reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.BundleName}, &other); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return "", fmt.Errorf("read bundle %s: %w", s.Spec.BundleName, err)
		}
		if len(lifecycle.SupersedingSiblings(p, b, []v1alpha1.Bundle{other}, rejected, true, ps.Spec.Environment)) > 0 {
			return other.Name, nil
		}
	}
	return "", nil
}

// pushedOrPushing reports whether s completed git-push, or recorded its push
// intent and has not stopped: a step that failed, was aborted or superseded
// with only an intent never pushed, and its intent is stale.
func pushedOrPushing(s *v1alpha1.PromotionStep) bool {
	for _, st := range s.Status.Steps {
		if st.Name == "git-push" && st.State == v1alpha1.StepExecutionCompleted {
			return true
		}
	}
	if s.Status.Outputs[outputPushIntent] == "" {
		return false
	}
	switch s.Status.State {
	case "Failed", "AbortedByAlarm", StateSuperseded:
		return false
	}
	return true
}
