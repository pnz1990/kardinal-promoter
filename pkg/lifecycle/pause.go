// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
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
// The Pipeline reconciler converges the gate to spec.paused, so setting the
// field is enough: SetPaused (the UI) and
// `kubectl patch pipeline --type merge -p '{"spec":{"paused":true}}'` write
// only the Pipeline. Pause and Resume (the CLI) also write the gate, so the
// hold starts without waiting for the Pipeline reconciler. A held step
// re-checks the gate every minute (requeuePaused in the PromotionStep
// reconciler); its PolicyGate watch wakes only the steps whose
// spec.requiredGates names a gate, and the freeze gate is never listed there.
// So resume takes effect within a minute, not at once.
//
// Only kardinal's own gate counts (IsFreezeGate). A PolicyGate a user named
// freeze-<pipeline> neither holds steps nor is deleted by resume; pausing
// while it exists fails with ErrConflict, and the Pipeline reconciler reports
// it in the Paused condition, so the pipeline is never silently left running.

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
		// Generated: the freeze gate is never a template, so its name may be
		// longer than 63 characters (a pipeline name can be 63).
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "false",
			Message:    fmt.Sprintf("Pipeline %s is paused — resume with: kardinal resume %s", p.Name, p.Name),
			Generated:  true,
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

// IsFreezeGate reports whether gate is kardinal's freeze gate of pipeline: it
// has the freeze gate's name and either the kardinal.io/freeze=true label or a
// controller owner reference to the Pipeline. Any other PolicyGate with that
// name belongs to a user and is not a pause.
func IsFreezeGate(gate *v1alpha1.PolicyGate, pipeline string) bool {
	if gate.Name != FreezeGateName(pipeline) {
		return false
	}
	if gate.Labels[LabelFreeze] == "true" {
		return true
	}
	owner := metav1.GetControllerOf(gate)
	return owner != nil && owner.Kind == "Pipeline" && owner.Name == pipeline &&
		owner.APIVersion == v1alpha1.GroupVersion.String()
}

// EnsureFreezeGate creates the freeze gate of p if it does not exist. When a
// PolicyGate that is not kardinal's freeze gate already has the name, nothing
// would hold the pipeline, so it returns ErrConflict naming that gate.
func EnsureFreezeGate(ctx context.Context, c client.Client, p *v1alpha1.Pipeline) error {
	err := c.Create(ctx, DesiredFreezeGate(p))
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create freeze gate for pipeline %s: %w", p.Name, err)
	}
	var gate v1alpha1.PolicyGate
	if err := c.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: FreezeGateName(p.Name)}, &gate); err != nil {
		return fmt.Errorf("get freeze gate for pipeline %s: %w", p.Name, err)
	}
	if !IsFreezeGate(&gate, p.Name) {
		return FreezeGateConflict(p.Name)
	}
	return nil
}

// FreezeGateConflict is the ErrConflict returned when a user PolicyGate has
// the freeze gate's name.
func FreezeGateConflict(pipeline string) error {
	return fmt.Errorf("PolicyGate %s already exists and is not kardinal's freeze gate (no %s=true label, not owned by the Pipeline), so pipeline %s is NOT paused; rename or delete that PolicyGate: %w",
		FreezeGateName(pipeline), LabelFreeze, pipeline, ErrConflict)
}

// RemoveFreezeGate deletes the freeze gate of a pipeline. A PolicyGate with the
// same name that is not kardinal's freeze gate (IsFreezeGate) is left alone.
func RemoveFreezeGate(ctx context.Context, c client.Client, ns, pipeline string) error {
	var gate v1alpha1.PolicyGate
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: FreezeGateName(pipeline)}, &gate); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get freeze gate for pipeline %s: %w", pipeline, err)
	}
	if !IsFreezeGate(&gate, pipeline) {
		return nil
	}
	if err := c.Delete(ctx, &gate); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete freeze gate for pipeline %s: %w", pipeline, err)
	}
	return nil
}

// IsPaused reports whether the freeze gate of a pipeline exists. A user
// PolicyGate with the same name is not a pause.
func IsPaused(ctx context.Context, c client.Reader, ns, pipeline string) (bool, error) {
	var gate v1alpha1.PolicyGate
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: FreezeGateName(pipeline)}, &gate); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get freeze gate for pipeline %s: %w", pipeline, err)
	}
	return IsFreezeGate(&gate, pipeline), nil
}

// Pause sets spec.paused on the Pipeline and creates its freeze gate. It is
// idempotent. It returns ErrNotFound when the Pipeline does not exist, and
// ErrConflict when a user PolicyGate has the freeze gate's name (spec.paused
// stays set, so the pause takes effect once that gate is renamed or deleted).
func Pause(ctx context.Context, c client.Client, ns, pipeline string) error {
	p, err := setPaused(ctx, c, ns, pipeline, true)
	if err != nil {
		return err
	}
	// A caller allowed to pause but not to write PolicyGates (the promoter
	// role) leaves the freeze gate to the Pipeline reconciler.
	if err := EnsureFreezeGate(ctx, c, p); err != nil && !apierrors.IsForbidden(err) {
		return err
	}
	return nil
}

// Resume clears spec.paused on the Pipeline and deletes its freeze gate. It is
// idempotent. It returns ErrNotFound when the Pipeline does not exist.
func Resume(ctx context.Context, c client.Client, ns, pipeline string) error {
	if _, err := setPaused(ctx, c, ns, pipeline, false); err != nil {
		return err
	}
	// As in Pause: without rights on PolicyGates the Pipeline reconciler
	// removes the gate.
	if err := RemoveFreezeGate(ctx, c, ns, pipeline); err != nil && !apierrors.IsForbidden(err) {
		return err
	}
	return nil
}

// SetPaused sets spec.paused on the Pipeline and leaves the freeze gate to
// the Pipeline reconciler. It needs only get and update on the Pipeline, so a
// UI user needs no rights on PolicyGates. A conflict with a concurrent write
// is retried. It is idempotent and returns ErrNotFound when the Pipeline does
// not exist.
func SetPaused(ctx context.Context, c client.Client, ns, pipeline string, paused bool) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var p v1alpha1.Pipeline
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipeline}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("pipeline %s/%s: %w", ns, pipeline, ErrNotFound)
			}
			return err
		}
		if p.Spec.Paused == paused {
			return nil
		}
		p.Spec.Paused = paused
		return c.Update(ctx, &p)
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("set pipeline %s/%s paused=%t: %w", ns, pipeline, paused, err)
	}
	return err
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
