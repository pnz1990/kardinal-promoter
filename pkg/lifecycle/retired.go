// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

const (
	// LabelBundle is the label of a PromotionStep that names its Bundle.
	LabelBundle = "kardinal.io/bundle"

	// LabelRetired marks a PromotionStep that ListPromotionSteps rebuilt from
	// a retired Bundle's status.retiredSteps. Such a step exists only in
	// memory: it is never written to the cluster.
	LabelRetired = "kardinal.io/retired"

	// ConditionGraphRetired is the Bundle condition that is True once the
	// Bundle's Graph was deleted and its PromotionSteps are kept as
	// status.retiredSteps (#1492).
	ConditionGraphRetired = "GraphRetired"

	// retiredMessageMax is the most of a step message a RetiredStep keeps.
	retiredMessageMax = 512
	// retiredPRURLMax is the CRD's maxLength of RetiredStep.prURL.
	retiredPRURLMax = 2048
	// markerDigestLen is the length of a render marker digest (hex sha256).
	markerDigestLen = 64
)

// Retired reports whether b's Graph was retired: status.retiredAt is set.
// The GraphRetired condition is not read: another writer's merge patch of
// status.conditions from a stale copy can drop it, while it never touches
// status.retiredAt.
func Retired(b *v1alpha1.Bundle) bool {
	return b.Status.RetiredAt != nil
}

// RetiredStepOf returns the record a retired Bundle keeps of s.
func RetiredStepOf(s *v1alpha1.PromotionStep) v1alpha1.RetiredStep {
	r := v1alpha1.RetiredStep{
		Name:              s.Name,
		Environment:       s.Spec.Environment,
		StepType:          s.Spec.StepType,
		State:             s.Status.State,
		Message:           truncateUTF8(s.Status.Message, retiredMessageMax),
		PRURL:             s.Status.PRURL,
		CreatedAt:         s.CreationTimestamp,
		HealthCheckExpiry: s.Status.HealthCheckExpiry.DeepCopy(),
	}
	if r.PRURL == "" {
		r.PRURL = s.Status.Outputs["prURL"]
	}
	r.PRURL = truncateUTF8(r.PRURL, retiredPRURLMax)
	if d := s.Status.Outputs["markerDigest"]; len(d) == markerDigestLen {
		r.MarkerDigest = d
	}
	r.RenderRequested = s.Status.Outputs["renderRequested"] == "true"
	if t, ok := VerifiedTime(s); ok {
		at := metav1.NewTime(t)
		r.VerifiedAt = &at
	}
	return r
}

// StepFromRetired rebuilds, in memory, the PromotionStep that record r of
// Bundle b was taken from: its name, labels, spec.pipelineName,
// spec.bundleName, spec.environment, spec.stepType, creationTimestamp,
// status.state, status.message, status.prURL, status.healthCheckExpiry and
// the Verified condition. The step carries LabelRetired.
func StepFromRetired(b *v1alpha1.Bundle, r v1alpha1.RetiredStep) v1alpha1.PromotionStep {
	s := v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:              r.Name,
			Namespace:         b.Namespace,
			CreationTimestamp: r.CreatedAt,
			Labels: map[string]string{
				LabelPipeline:    b.Spec.Pipeline,
				LabelBundle:      b.Name,
				LabelEnvironment: r.Environment,
				LabelRetired:     "true",
			},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: b.Spec.Pipeline,
			BundleName:   b.Name,
			Environment:  r.Environment,
			StepType:     r.StepType,
		},
		Status: v1alpha1.PromotionStepStatus{
			State:             r.State,
			Message:           r.Message,
			PRURL:             r.PRURL,
			HealthCheckExpiry: r.HealthCheckExpiry.DeepCopy(),
		},
	}
	if r.VerifiedAt != nil {
		s.Status.Conditions = []metav1.Condition{{
			Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified",
			LastTransitionTime: *r.VerifiedAt,
		}}
	}
	return s
}

// ListPromotionSteps lists the PromotionSteps in ns whose labels match
// selector, as client.List would, and adds the steps of retired Bundles in ns
// that match it, rebuilt from status.retiredSteps (StepFromRetired). A record
// whose PromotionStep still exists (kro is deleting the retired Graph) is
// skipped, so a step is never listed twice. Use it wherever a Bundle's steps
// are read after the Bundle finished: rollback, promote, history, metrics,
// the Pipeline phase, the CLI and the UI.
func ListPromotionSteps(ctx context.Context, c client.Reader, ns string,
	selector client.MatchingLabels) ([]v1alpha1.PromotionStep, error) {
	var list v1alpha1.PromotionStepList
	if err := c.List(ctx, &list, client.InNamespace(ns), selector); err != nil {
		return nil, fmt.Errorf("list promotion steps: %w", err)
	}
	steps := list.Items

	var bundles []v1alpha1.Bundle
	if name, ok := selector[LabelBundle]; ok {
		var b v1alpha1.Bundle
		err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b)
		switch {
		case err == nil:
			bundles = []v1alpha1.Bundle{b}
		case !apierrors.IsNotFound(err):
			return nil, fmt.Errorf("get bundle %s/%s: %w", ns, name, err)
		}
	} else if pipeline, ok := selector[LabelPipeline]; ok {
		// One Pipeline's steps: its Bundles only (#1654).
		var err error
		if bundles, err = ListPipelineBundles(ctx, c, ns, pipeline); err != nil {
			return nil, err
		}
	} else {
		var bl v1alpha1.BundleList
		if err := c.List(ctx, &bl, client.InNamespace(ns)); err != nil {
			return nil, fmt.Errorf("list bundles: %w", err)
		}
		bundles = bl.Items
	}

	return AddRetiredSteps(steps, bundles, selector), nil
}

// AddRetiredSteps returns steps plus the steps of the retired Bundles among
// bundles whose labels match selector (nil matches every step), rebuilt from
// status.retiredSteps. A record whose PromotionStep is in steps (kro is still
// deleting the retired Graph) is skipped, so a step is never listed twice.
func AddRetiredSteps(steps []v1alpha1.PromotionStep, bundles []v1alpha1.Bundle,
	selector map[string]string) []v1alpha1.PromotionStep {
	live := make(map[types.NamespacedName]bool, len(steps))
	for i := range steps {
		live[types.NamespacedName{Namespace: steps[i].Namespace, Name: steps[i].Name}] = true
	}
	match := labels.SelectorFromSet(labels.Set(selector))
	for i := range bundles {
		b := &bundles[i]
		if len(b.Status.RetiredSteps) == 0 || !Retired(b) {
			continue
		}
		for _, r := range b.Status.RetiredSteps {
			if live[types.NamespacedName{Namespace: b.Namespace, Name: r.Name}] {
				continue
			}
			s := StepFromRetired(b, r)
			if match.Matches(labels.Set(s.Labels)) {
				steps = append(steps, s)
			}
		}
	}
	return steps
}

// truncateUTF8 cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
