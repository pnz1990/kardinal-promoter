// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// AutoRollbackName returns the fixed name of the automatic rollback Bundle
// that source (for example "alarm", "policy", or "alarm-<environment>")
// creates for the failing Bundle bundle: bundle-rollback-source when that is
// a valid object name and kardinal.io/bundle label value (63 characters).
// Otherwise it is the start of bundle, "-rollback-" and the first 10 hex
// characters of the SHA-256 of the whole name, so it still reads as a
// rollback of bundle and different names stay different. It is
// deterministic, so a retried reconcile or a restarted controller finds the
// Bundle again.
func AutoRollbackName(bundle, source string) string {
	name := bundle + "-rollback-" + source
	if len(validation.IsValidLabelValue(name)) == 0 && len(validation.IsDNS1123Subdomain(name)) == 0 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-rollback-" + hex.EncodeToString(sum[:])[:10]
	head := strings.TrimRight(bundle[:min(len(bundle), validation.LabelValueMaxLength-len(suffix))], "-._")
	if head == "" {
		return strings.TrimPrefix(suffix, "-")
	}
	return head + suffix
}

// AutoRollbackNameIn is AutoRollbackName for the rollback of bundle in env:
// AutoRollbackName(bundle, source), unless a Bundle of that name exists and
// rolls another environment back (another environment of the Bundle failed
// first: fan-out, waves, a fleet's targets in flight at once), then
// AutoRollbackName(bundle, source+"-"+env), so each environment gets its own
// (#2082).
func AutoRollbackNameIn(ctx context.Context, c client.Reader, ns, bundle, source, env string) (string, error) {
	name := AutoRollbackName(bundle, source)
	var b v1alpha1.Bundle
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b)
	switch {
	case apierrors.IsNotFound(err):
		return name, nil
	case err != nil:
		return "", fmt.Errorf("get bundle %s: %w", name, err)
	case !IsAutoRollback(&b) || (b.Spec.Intent != nil && b.Spec.Intent.TargetEnvironment != env):
		// Someone else's Bundle at the name, or another environment's
		// rollback: env's own name.
		return AutoRollbackName(bundle, source+"-"+env), nil
	}
	return name, nil
}

// IsAutoRollback reports whether b is an automatic rollback the controller
// created: its actor stamp is the controller's, via auto-rollback, and its
// kardinal.io/created-by is the controller. Only the controller may write
// that stamp (the chart's actor admission refuses a via or a controller
// kind from anyone else) and that creator (bundle-creator admission). The
// rollback labels, kardinal.io/rollback-from and the intent are not proof:
// anyone who may create Bundles can set them, and a Bundle adopted as the
// automatic rollback on them would stand in for it (#2090 QA).
// IsControllerRollback reports whether b is a rollback the controller wrote:
// an automatic rollback (IsAutoRollback), or a rollback the controller
// created for a UI or API caller (kardinal.io/rollback, and an actor stamp
// via ui or api, which only the controller may write). A rollback a client
// created itself (kubectl, kardinal rollback) carries only markers its
// creator writes, so no decision counts it as one (#2187).
func IsControllerRollback(b *v1alpha1.Bundle) bool {
	if IsAutoRollback(b) {
		return true
	}
	a := b.Spec.Actor
	return b.Labels[LabelRollback] == "true" && a != nil && (a.Via == v1alpha1.ViaUI || a.Via == v1alpha1.ViaAPI)
}

func IsAutoRollback(b *v1alpha1.Bundle) bool {
	a := b.Spec.Actor
	return a != nil && a.Kind == v1alpha1.ActorController && a.Via == v1alpha1.ViaAutoRollback &&
		b.Annotations[AnnotationCreatedBy] == ControllerCreator
}

// The sources of the automatic rollbacks (AutoRollbackName): the step's
// onHealthFailure=rollback and a RollbackPolicy.
const (
	RollbackSourceAlarm  = "alarm"
	RollbackSourcePolicy = "policy"
)

// FindRollback returns the name of an existing automatic rollback Bundle
// (IsAutoRollback) of pipeline that rolls the environment env back from the
// Bundle from, or "" when there is none. Automatic rollback paths
// (onHealthFailure=rollback and RollbackPolicy) call it first, so one
// failing Bundle gets one rollback in an environment.
//
// It is bound to from by its name: the controller creates the automatic
// rollback of from only at AutoRollbackNameIn's names for the alarm and
// policy sources (the plain name, or the environment's own), and a name
// never changes. The kardinal.io/rollback-from annotation and the labels do
// not decide it: anyone who may patch Bundles can rewrite them, also on a
// stamped rollback of an earlier failure, which would then pass for this
// one's and leave the environment on the failing version (#2187 QA). The
// pipeline is the immutable spec.pipeline.
func FindRollback(ctx context.Context, c client.Reader, ns, pipeline, env, from string) (string, error) {
	for _, src := range []string{RollbackSourceAlarm, RollbackSourcePolicy} {
		for _, name := range []string{AutoRollbackName(from, src), AutoRollbackName(from, src+"-"+env)} {
			var b v1alpha1.Bundle
			err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b)
			switch {
			case apierrors.IsNotFound(err):
				continue
			case err != nil:
				return "", fmt.Errorf("get bundle %s: %w", name, err)
			}
			if !IsAutoRollback(&b) || b.Spec.Pipeline != pipeline {
				continue
			}
			if b.Spec.Intent == nil || b.Spec.Intent.TargetEnvironment == env {
				return name, nil
			}
		}
	}
	return "", nil
}

// RollbackTaken reports whether rollback Bundle name, which a create found
// existing, rolls another environment than env back: created by a
// concurrent reconcile of another environment, it is not env's, and the
// caller retries (AutoRollbackNameIn then names env's own).
func RollbackTaken(ctx context.Context, c client.Reader, ns, name, env string) (bool, error) {
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		return false, fmt.Errorf("get bundle %s: %w", name, err)
	}
	return !IsAutoRollback(&b) || (b.Spec.Intent != nil && b.Spec.Intent.TargetEnvironment != env), nil
}
