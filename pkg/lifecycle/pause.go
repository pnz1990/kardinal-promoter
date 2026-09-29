// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// How pause works
//
// spec.paused on the Pipeline is the request. The freeze PolicyGate
// freeze-<pipeline> (label kardinal.io/freeze=true, expression "false") is the
// signal the PromotionStep reconciler reads: while it exists, a step does not
// leave Pending and does not run its next git step. Steps waiting for a PR
// merge or running a health check carry on, because stopping them would leave
// a merged change unverified.
//
// Pause and Resume write both. The Pipeline reconciler converges the gate to
// spec.paused, so `kubectl patch pipeline --type merge -p '{"spec":{"paused":true}}'`
// pauses too. Deleting or creating the gate wakes every PromotionStep in the
// namespace (the PromotionStep reconciler watches PolicyGates), so resume takes
// effect at once.

// FreezeGateName returns the name of the freeze PolicyGate of a pipeline.
func FreezeGateName(pipeline string) string {
	return "freeze-" + pipeline
}

// PausedMessage is the step message shown while a pipeline is paused.
func PausedMessage(pipeline string) string {
	return fmt.Sprintf("pipeline %s is paused — resume with: kardinal resume %s", pipeline, pipeline)
}

// DesiredFreezeGate returns the freeze PolicyGate for p. When p has a UID the
// gate is owned by the Pipeline, so deleting the Pipeline deletes the gate.
func DesiredFreezeGate(p *v1alpha1.Pipeline) *v1alpha1.PolicyGate {
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      FreezeGateName(p.Name),
			Namespace: p.Namespace,
			Labels: map[string]string{
				LabelPipeline: p.Name,
				LabelScope:    "system",
				LabelFreeze:   "true",
			},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "false",
			Message:    fmt.Sprintf("Pipeline %s is paused — resume with: kardinal resume %s", p.Name, p.Name),
		},
	}
	if p.UID != "" {
		isController := true
		gate.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: v1alpha1.GroupVersion.String(),
			Kind:       "Pipeline",
			Name:       p.Name,
			UID:        p.UID,
			Controller: &isController,
		}}
	}
	return gate
}

// EnsureFreezeGate creates the freeze gate of p if it does not exist.
func EnsureFreezeGate(ctx context.Context, c client.Client, p *v1alpha1.Pipeline) error {
	if err := c.Create(ctx, DesiredFreezeGate(p)); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create freeze gate for pipeline %s: %w", p.Name, err)
	}
	return nil
}

// RemoveFreezeGate deletes the freeze gate of a pipeline. A PolicyGate with the
// same name that is not labelled kardinal.io/freeze=true is left alone.
func RemoveFreezeGate(ctx context.Context, c client.Client, ns, pipeline string) error {
	var gate v1alpha1.PolicyGate
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: FreezeGateName(pipeline)}, &gate); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get freeze gate for pipeline %s: %w", pipeline, err)
	}
	if gate.Labels[LabelFreeze] != "true" {
		return nil
	}
	if err := c.Delete(ctx, &gate); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete freeze gate for pipeline %s: %w", pipeline, err)
	}
	return nil
}

// IsPaused reports whether the freeze gate of a pipeline exists.
func IsPaused(ctx context.Context, c client.Reader, ns, pipeline string) (bool, error) {
	var gate v1alpha1.PolicyGate
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: FreezeGateName(pipeline)}, &gate); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get freeze gate for pipeline %s: %w", pipeline, err)
	}
	return gate.Labels[LabelFreeze] == "true", nil
}

// Pause sets spec.paused on the Pipeline and creates its freeze gate. It is
// idempotent. It returns ErrNotFound when the Pipeline does not exist.
func Pause(ctx context.Context, c client.Client, ns, pipeline string) error {
	p, err := setPaused(ctx, c, ns, pipeline, true)
	if err != nil {
		return err
	}
	return EnsureFreezeGate(ctx, c, p)
}

// Resume clears spec.paused on the Pipeline and deletes its freeze gate. It is
// idempotent. It returns ErrNotFound when the Pipeline does not exist.
func Resume(ctx context.Context, c client.Client, ns, pipeline string) error {
	if _, err := setPaused(ctx, c, ns, pipeline, false); err != nil {
		return err
	}
	return RemoveFreezeGate(ctx, c, ns, pipeline)
}

// setPaused patches spec.paused with a merge patch. The patch carries no
// resourceVersion, so a concurrent write to the Pipeline cannot make it fail
// with a conflict.
func setPaused(ctx context.Context, c client.Client, ns, pipeline string, paused bool) (*v1alpha1.Pipeline, error) {
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipeline}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("pipeline %s/%s: %w", ns, pipeline, ErrNotFound)
		}
		return nil, fmt.Errorf("get pipeline %s/%s: %w", ns, pipeline, err)
	}
	if p.Spec.Paused == paused {
		return &p, nil
	}
	patch := client.MergeFrom(p.DeepCopy())
	p.Spec.Paused = paused
	if err := c.Patch(ctx, &p, patch); err != nil {
		return nil, fmt.Errorf("patch pipeline %s/%s paused=%t: %w", ns, pipeline, paused, err)
	}
	return &p, nil
}
