// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// conditionOverrideIgnored is True while spec.overrides has entries the
// controller does not count because their createdAt is after it first saw
// them.
const conditionOverrideIgnored = "OverrideIgnored"

// overrideClockSkew is how much later than firstSeen an override's
// createdAt may be (clock differences between the writer and the controller).
const overrideClockSkew = 5 * time.Minute

// overrideKey identifies one override in status.overridesSeen.
func overrideKey(o *kardinalv1alpha1.PolicyGateOverride) string {
	created := ""
	if o.CreatedAt != nil {
		created = o.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	h := sha256.Sum256([]byte(strings.Join([]string{o.Stage, o.Reason, o.CreatedBy, created,
		o.ExpiresAt.UTC().Format(time.RFC3339Nano)}, "\x00")))
	return hex.EncodeToString(h[:12])
}

func firstSeenOf(seen []kardinalv1alpha1.OverrideObservation) map[string]time.Time {
	m := make(map[string]time.Time, len(seen))
	for _, s := range seen {
		m[s.Key] = s.FirstSeen.Time
	}
	return m
}

// observeOverrides returns status.overridesSeen for overrides: the recorded
// firstSeen of each entry still present, and now for a new one, in spec
// order. Entries no longer in the spec are dropped.
func observeOverrides(overrides []kardinalv1alpha1.PolicyGateOverride, seen []kardinalv1alpha1.OverrideObservation,
	now time.Time) []kardinalv1alpha1.OverrideObservation {
	first := firstSeenOf(seen)
	out := make([]kardinalv1alpha1.OverrideObservation, 0, len(overrides))
	done := map[string]bool{}
	for i := range overrides {
		k := overrideKey(&overrides[i])
		if done[k] {
			continue
		}
		done[k] = true
		t, ok := first[k]
		if !ok {
			t = now
		}
		out = append(out, kardinalv1alpha1.OverrideObservation{Key: k, FirstSeen: metav1.NewTime(t)})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// futureDated reports whether o's createdAt is more than the clock skew after
// the controller first saw it: a writer dating an entry ahead to chain
// overrides past the cap.
func futureDated(o *kardinalv1alpha1.PolicyGateOverride, firstSeen time.Time) bool {
	return o.CreatedAt != nil && o.CreatedAt.After(firstSeen.Add(overrideClockSkew))
}

// findActiveOverride returns the first override matching envName (an
// override with Stage "" matches any) that is still in effect, and when it
// ends: the earlier of its expiresAt and its firstSeen plus maxOverride. An
// override the controller has not recorded counts from now; a future-dated
// one (futureDated) never counts.
func findActiveOverride(overrides []kardinalv1alpha1.PolicyGateOverride, envName string, now time.Time,
	firstSeen map[string]time.Time, maxOverride time.Duration) (*kardinalv1alpha1.PolicyGateOverride, time.Time) {
	for i := range overrides {
		o := &overrides[i]
		if o.ExpiresAt.IsZero() {
			continue
		}
		seen, ok := firstSeen[overrideKey(o)]
		if !ok {
			seen = now
		}
		if futureDated(o, seen) {
			continue
		}
		end := o.ExpiresAt.Time
		if capEnd := seen.Add(maxOverride); maxOverride > 0 && capEnd.Before(end) {
			end = capEnd
		}
		if !now.Before(end) {
			continue
		}
		if o.Stage == "" || o.Stage == envName {
			return o, end
		}
	}
	return nil, time.Time{}
}

// recordOverrides writes status.overridesSeen and the OverrideIgnored
// condition when they change. It writes only this reconciler's own status.
func (r *Reconciler) recordOverrides(ctx context.Context, gate *kardinalv1alpha1.PolicyGate, now time.Time) error {
	seen := observeOverrides(gate.Spec.Overrides, gate.Status.OverridesSeen, now)
	first := firstSeenOf(seen)
	var ignored []string
	for i := range gate.Spec.Overrides {
		o := &gate.Spec.Overrides[i]
		if futureDated(o, first[overrideKey(o)]) {
			ignored = append(ignored, fmt.Sprintf("%q by %s created %s", o.Reason, o.CreatedBy,
				o.CreatedAt.UTC().Format(time.RFC3339)))
		}
	}
	cond := metav1.Condition{Type: conditionOverrideIgnored, Status: metav1.ConditionFalse, Reason: "NoneIgnored",
		Message: "every override counts from when the controller first saw it", ObservedGeneration: gate.Generation}
	if len(ignored) > 0 {
		cond.Status, cond.Reason = metav1.ConditionTrue, "CreatedAfterFirstSeen"
		cond.Message = truncateMessage(fmt.Sprintf("%d override(s) ignored: createdAt is more than %s after the "+
			"controller first saw them, so they could extend an override past the cap: %s",
			len(ignored), overrideClockSkew, strings.Join(ignored, "; ")))
	}
	prev := meta.FindStatusCondition(gate.Status.Conditions, conditionOverrideIgnored)
	condChanged := len(gate.Spec.Overrides) > 0 || prev != nil
	if prev != nil && prev.Status == cond.Status && prev.Reason == cond.Reason && prev.Message == cond.Message {
		condChanged = false
	}
	if equalSeen(seen, gate.Status.OverridesSeen) && !condChanged {
		return nil
	}
	patch := client.MergeFromWithOptions(gate.DeepCopy(), client.MergeFromWithOptimisticLock{})
	gate.Status.OverridesSeen = seen
	if len(gate.Spec.Overrides) == 0 && len(ignored) == 0 {
		meta.RemoveStatusCondition(&gate.Status.Conditions, conditionOverrideIgnored)
	} else {
		cond.LastTransitionTime = metav1.NewTime(now)
		meta.SetStatusCondition(&gate.Status.Conditions, cond)
	}
	if err := r.Status().Patch(ctx, gate, patch); err != nil {
		return fmt.Errorf("record overrides seen: %w", err)
	}
	return nil
}

func equalSeen(a, b []kardinalv1alpha1.OverrideObservation) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || !a[i].FirstSeen.Equal(&b[i].FirstSeen) {
			return false
		}
	}
	return true
}
