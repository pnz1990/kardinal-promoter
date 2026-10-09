// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// ConditionSCMUnavailable is True while the step waits for an open SCM
// circuit (#1476): the SCM host failed, so kardinal makes no call and waits,
// without spending the step's retries, for status.scmWaitSince plus the
// wait bound (scmWaitBound) at most.
const ConditionSCMUnavailable = "SCMUnavailable"

// DefaultSCMWaitTimeout bounds the wait for an SCM that stays down when the
// environment sets no stepTimeoutSeconds (--scm-wait-timeout).
const DefaultSCMWaitTimeout = 30 * time.Minute

// circuitJitter spreads the steps that wait for one circuit, so they do not
// all call the moment it lets one through: up to a fifth of the wait more.
var circuitJitter = func(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int64N(int64(d)/5+1)) //nolint:gosec // jitter, not security
}

// circuitWait reports whether err is an open SCM circuit (scm.ErrCircuitOpen)
// and how long to wait for it: until the circuit lets a call through, at
// least a second and at most retryMaxDelay, with jitter.
func circuitWait(err error, now time.Time) (time.Duration, bool) {
	var open *scm.ErrCircuitOpen
	if !errors.As(err, &open) {
		return 0, false
	}
	wait := open.RetryAfter.Sub(now)
	if wait < time.Second {
		wait = time.Second
	}
	if wait > retryMaxDelay {
		wait = retryMaxDelay
	}
	return circuitJitter(wait), true
}

// scmWaitBound is how long a step waits for the SCM in all: the
// environment's stepTimeoutSeconds when set, else the reconciler's
// SCMWaitTimeout (DefaultSCMWaitTimeout when zero).
func (r *Reconciler) scmWaitBound(stepTimeoutSeconds int) time.Duration {
	if stepTimeoutSeconds > 0 {
		return time.Duration(stepTimeoutSeconds) * time.Second
	}
	if r.SCMWaitTimeout > 0 {
		return r.SCMWaitTimeout
	}
	return DefaultSCMWaitTimeout
}

// startSCMWait records that ps waits for the SCM from now unless it already
// does, and reports how long it has waited and whether this is the start
// (when the caller emits the SCMUnavailable Event).
func (r *Reconciler) startSCMWait(ps *v1alpha1.PromotionStep, err error) (waited time.Duration, started bool) {
	now := r.now()
	if ps.Status.SCMWaitSince == nil {
		since := metav1.NewTime(now.UTC())
		ps.Status.SCMWaitSince = &since
		started = true
	}
	meta.SetStatusCondition(&ps.Status.Conditions, metav1.Condition{
		Type: ConditionSCMUnavailable, Status: metav1.ConditionTrue, Reason: "CircuitOpen",
		Message:            fmt.Sprintf("waiting for the SCM since %s: %v", ps.Status.SCMWaitSince.UTC().Format(time.RFC3339), err),
		ObservedGeneration: ps.Generation, LastTransitionTime: metav1.NewTime(now.UTC()),
	})
	return now.Sub(ps.Status.SCMWaitSince.Time), started
}

// clearSCMWait ends a wait for the SCM: the step made a call. The caller
// writes the status.
func clearSCMWait(ps *v1alpha1.PromotionStep, now time.Time) {
	if ps.Status.SCMWaitSince == nil {
		return
	}
	ps.Status.SCMWaitSince = nil
	meta.SetStatusCondition(&ps.Status.Conditions, metav1.Condition{
		Type: ConditionSCMUnavailable, Status: metav1.ConditionFalse, Reason: "Reachable",
		Message: "the SCM answered", ObservedGeneration: ps.Generation, LastTransitionTime: metav1.NewTime(now.UTC()),
	})
}

// endSCMWaitTimedOut ends a wait for the SCM that used up its bound: the
// step fails. SCMUnavailable turns False with reason TimedOut, so the
// condition does not claim the step still waits. The caller writes the status.
func endSCMWaitTimedOut(ps *v1alpha1.PromotionStep, now time.Time) {
	if ps.Status.SCMWaitSince == nil {
		return
	}
	since := ps.Status.SCMWaitSince.Time
	ps.Status.SCMWaitSince = nil
	meta.SetStatusCondition(&ps.Status.Conditions, metav1.Condition{
		Type: ConditionSCMUnavailable, Status: metav1.ConditionFalse, Reason: "TimedOut",
		Message: fmt.Sprintf("the SCM was unavailable for %s, the most this step waits; the step failed",
			now.Sub(since).Round(time.Second)),
		ObservedGeneration: ps.Generation, LastTransitionTime: metav1.NewTime(now.UTC()),
	})
}

// emitSCMUnavailable writes the one Warning Event of a wait.
func (r *Reconciler) emitSCMUnavailable(ps *v1alpha1.PromotionStep, bound time.Duration, err error) {
	kubeevent.Emit(r.Recorder, ps, corev1.EventTypeWarning, ConditionSCMUnavailable, "Promote",
		fmt.Sprintf("env %s: the SCM circuit is open; the step waits for it, for %s at most: %v",
			ps.Spec.Environment, bound, err))
}
