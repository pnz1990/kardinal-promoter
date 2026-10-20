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

// AutoRollbackNameIn is AutoRollbackName for the automatic rollback of
// bundle (of pipeline) in env: AutoRollbackName(bundle, source), unless a
// Bundle of that name exists that is not that rollback (IsRollbackOf):
// another environment's (fan-out, waves, a fleet's targets in flight at once,
// #2082), or one someone else created there (#2090). Then it is
// AutoRollbackName(bundle, source+"-"+env), env's own. When that name holds
// a Bundle that is not the rollback either, it returns an error wrapping
// ErrConflict that names it: the callers stop for a human, as when nothing
// is safe to roll back to, instead of adopting it or retrying forever.
func AutoRollbackNameIn(ctx context.Context, c client.Reader, ns, pipeline, bundle, source, env string) (string, error) {
	plain := AutoRollbackName(bundle, source)
	free, err := rollbackNameFree(ctx, c, ns, plain, pipeline, env)
	if err != nil || free {
		return plain, err
	}
	own := AutoRollbackName(bundle, source+"-"+env)
	free, err = rollbackNameFree(ctx, c, ns, own, pipeline, env)
	if err != nil {
		return "", err
	}
	if !free {
		return "", fmt.Errorf("bundle %s is at the automatic rollback's name and is not a rollback of %s in %s (nor is %s); "+
			"rename or delete them: %w", own, bundle, env, plain, ErrConflict)
	}
	return own, nil
}

// rollbackNameFree reports whether name, one of the names of the automatic
// rollback of a failing Bundle in env, is free for it: no Bundle has it, or
// the one that has it is that rollback (IsRollbackOf).
func rollbackNameFree(ctx context.Context, c client.Reader, ns, name, pipeline, env string) (bool, error) {
	var b v1alpha1.Bundle
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b)
	switch {
	case apierrors.IsNotFound(err):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("get bundle %s: %w", name, err)
	}
	return IsRollbackOf(&b, pipeline, env), nil
}

// IsRollbackOf reports whether b, a Bundle at one of the names the
// controller creates the automatic rollback of a failing Bundle in env of
// pipeline at (AutoRollbackNameIn), is that rollback: the controller's
// stamp (IsAutoRollback, which only the controller can write, #2187),
// spec.pipeline the pipeline, and intent.targetEnvironment env. The name
// binds it to the failing Bundle; the labels and kardinal.io/rollback-from
// do not decide it, since anyone who may patch Bundles can rewrite them
// (#2223). A Bundle at the name that is not is someone else's, or another
// environment's (#2090).
func IsRollbackOf(b *v1alpha1.Bundle, pipeline, env string) bool {
	return IsAutoRollback(b) && b.Spec.Pipeline == pipeline &&
		b.Spec.Intent != nil && b.Spec.Intent.TargetEnvironment == env
}

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

// IsAutoRollback reports whether b is an automatic rollback the controller
// created: its actor stamp is the controller's, via auto-rollback, and its
// kardinal.io/created-by is the controller. Only the controller may write
// that stamp (the chart's actor admission refuses a via or a controller
// kind from anyone else) and that creator (bundle-creator admission). The
// rollback labels, kardinal.io/rollback-from and the intent are not proof:
// anyone who may create Bundles can set them, and a Bundle adopted as the
// automatic rollback on them would stand in for it (#2090 QA).
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
// existing, is not the automatic rollback in env of pipeline whose name it
// is (IsRollbackOf):
// another environment's, created by a concurrent reconcile, or someone
// else's. The caller retries, and AutoRollbackNameIn then names env's own
// or stops for a human.
func RollbackTaken(ctx context.Context, c client.Reader, ns, name, pipeline, env string) (bool, error) {
	free, err := rollbackNameFree(ctx, c, ns, name, pipeline, env)
	return !free, err
}
