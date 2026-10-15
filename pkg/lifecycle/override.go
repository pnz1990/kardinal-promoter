// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// OverrideTargets returns the gate instances an override of gateName is
// recorded on.
//
// A gate template (for example platform-policies/no-weekend-deploy) is never
// evaluated itself: each Bundle's Graph stamps an instance of it per
// environment in the Pipeline namespace, labelled with the template name, and
// the PolicyGate reconciler evaluates the instances. An instance name is
// usually longer than the 63 characters a label value may have, so a name
// that is an instance is read directly and never used in a label selector
// (E2E-R08). A template name is resolved to the instances of the Bundles
// still in progress. No promotion waits on the instances of a Verified,
// Failed, Superseded or Rejected Bundle, so they are left alone.
func OverrideTargets(ctx context.Context, c client.Client, ns, pipeline, stage, gateName string) (
	[]v1alpha1.PolicyGate, error) {
	var gate v1alpha1.PolicyGate
	getErr := c.Get(ctx, types.NamespacedName{Name: gateName, Namespace: ns}, &gate)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return nil, fmt.Errorf("get policygate %s: %w", gateName, getErr)
	}
	if getErr == nil {
		if _, isInstance := gate.Labels["kardinal.io/bundle"]; isInstance {
			if p := gate.Labels["kardinal.io/pipeline"]; p != "" && p != pipeline {
				return nil, kindError(ErrInvalid, fmt.Errorf("policygate %s/%s is an instance of pipeline %s, not %s",
					ns, gateName, p, pipeline))
			}
			if env := gate.Labels["kardinal.io/environment"]; stage != "" && env != "" && env != stage {
				return nil, kindError(ErrInvalid, fmt.Errorf("policygate %s/%s is the instance for stage %s, not %s",
					ns, gateName, env, stage))
			}
			return []v1alpha1.PolicyGate{gate}, nil
		}
	}

	// A template name is a valid label value (pkg/graph/validate.go checks
	// it); anything longer cannot label an instance.
	if len(validation.IsValidLabelValue(gateName)) > 0 {
		return nil, kindError(ErrNotFound, fmt.Errorf("get policygate %s: no gate instance of that name in namespace %s: %w",
			gateName, ns, getErr))
	}
	var instances v1alpha1.PolicyGateList
	selector := client.MatchingLabels{
		"kardinal.io/pipeline":      pipeline,
		"kardinal.io/gate-template": gateName,
	}
	if stage != "" {
		selector["kardinal.io/environment"] = stage
	}
	if err := c.List(ctx, &instances, client.InNamespace(ns), selector); err != nil {
		return nil, fmt.Errorf("list instances of policygate %s: %w", gateName, err)
	}
	var bundles v1alpha1.BundleList
	if len(instances.Items) > 0 {
		if err := c.List(ctx, &bundles, client.InNamespace(ns)); err != nil {
			return nil, fmt.Errorf("list bundles: %w", err)
		}
	}
	inProgress := make(map[string]bool, len(bundles.Items))
	for _, b := range bundles.Items {
		switch b.Status.Phase {
		case "Verified", "Failed", "Superseded", "Rejected":
		default:
			inProgress[b.Name] = true
		}
	}
	var targets []v1alpha1.PolicyGate
	for _, inst := range instances.Items {
		if inProgress[inst.Labels["kardinal.io/bundle"]] {
			targets = append(targets, inst)
		}
	}
	if len(targets) > 0 {
		sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
		return targets, nil
	}

	// An org gate's template is in a policy namespace, so the Get above does
	// not find it; its instances are still here.
	if getErr != nil && len(instances.Items) == 0 {
		return nil, kindError(ErrNotFound, fmt.Errorf("get policygate %s: no gate instance or template of that name for pipeline %s "+
			"in namespace %s: %w", gateName, pipeline, ns, getErr))
	}
	return nil, kindError(ErrConflict, fmt.Errorf("no in-progress Bundle of pipeline %s has an instance of gate %s in namespace %s "+
		"(stage %q; %d instance(s) of finished Bundles); an override applies to the instances a promoting "+
		"Bundle creates, so run it while the Bundle waits on the gate",
		pipeline, gateName, ns, stage, len(instances.Items)))
}

// classified is an error with a kind (ErrNotFound, ErrInvalid, ErrConflict)
// for errors.Is that keeps err's message as it is.
type classified struct{ err, kind error }

func (c classified) Error() string   { return c.err.Error() }
func (c classified) Unwrap() []error { return []error{c.err, c.kind} }

// kindError classifies err as kind without changing its message.
func kindError(kind, err error) error { return classified{err: err, kind: kind} }

// AppendOverride appends override to gate's spec.overrides. A merge patch
// replaces the whole list, so it carries the resourceVersion and is retried
// on conflict: a concurrent override is re-read, not overwritten.
func AppendOverride(ctx context.Context, c client.Client, gate *v1alpha1.PolicyGate,
	override v1alpha1.PolicyGateOverride) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		patch := client.MergeFromWithOptions(gate.DeepCopy(), client.MergeFromWithOptimisticLock{})
		gate.Spec.Overrides = append(gate.Spec.Overrides, override)
		err := c.Patch(ctx, gate, patch)
		if apierrors.IsConflict(err) {
			if getErr := c.Get(ctx, client.ObjectKeyFromObject(gate), gate); getErr != nil {
				return getErr
			}
		}
		return err
	})
}

// Reject sets spec.rejected on Bundle ns/name with reason and the verified
// requester by: the Bundle is never promoted again (kardinal reject, the
// UI). The patch carries the Bundle's resourceVersion, so a rejection
// someone else wrote in between is reported, not overwritten. Errors wrap
// ErrNotFound (no Bundle), ErrInvalid (no reason) and ErrConflict (already
// rejected, or changed meanwhile).
//
// stamp is by's actor stamp (nil records none): the BundleRejected
// AuditEvent names it.
func Reject(ctx context.Context, c client.Client, ns, name, reason, by string, stamp *v1alpha1.Actor,
	now time.Time) (*v1alpha1.Bundle, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, fmt.Errorf("reject: a reason is required: %w", ErrInvalid)
	}
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("reject: bundle %s not found in namespace %s: %w", name, ns, ErrNotFound)
		}
		return nil, fmt.Errorf("reject: get bundle %s: %w", name, err)
	}
	if r := b.Spec.Rejected; r != nil {
		return nil, fmt.Errorf("reject: bundle %s was already rejected by %s: %s: %w", name, r.By, r.Reason, ErrConflict)
	}
	patch := client.MergeFromWithOptions(b.DeepCopy(), client.MergeFromWithOptimisticLock{})
	at := metav1.NewTime(now.UTC().Truncate(time.Second))
	b.Spec.Rejected = &v1alpha1.BundleRejection{Reason: reason, By: by, At: &at, Actor: stamp}
	if err := c.Patch(ctx, &b, patch); err != nil {
		if apierrors.IsConflict(err) {
			return nil, fmt.Errorf("reject: bundle %s changed while rejecting it; try again: %w", name, ErrConflict)
		}
		return nil, fmt.Errorf("reject: patch bundle %s: %w", name, err)
	}
	return &b, nil
}
