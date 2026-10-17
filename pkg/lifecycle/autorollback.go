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
	free, err := rollbackNameFree(ctx, c, ns, plain, pipeline, env, bundle)
	if err != nil || free {
		return plain, err
	}
	own := AutoRollbackName(bundle, source+"-"+env)
	free, err = rollbackNameFree(ctx, c, ns, own, pipeline, env, bundle)
	if err != nil {
		return "", err
	}
	if !free {
		return "", fmt.Errorf("bundle %s is at the automatic rollback's name and is not a rollback of %s in %s (nor is %s); "+
			"rename or delete them: %w", own, bundle, env, plain, ErrConflict)
	}
	return own, nil
}

// rollbackNameFree reports whether name is free for the automatic rollback
// of from in env: no Bundle has it, or the one that has it is that rollback.
func rollbackNameFree(ctx context.Context, c client.Reader, ns, name, pipeline, env, from string) (bool, error) {
	var b v1alpha1.Bundle
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b)
	switch {
	case apierrors.IsNotFound(err):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("get bundle %s: %w", name, err)
	}
	return IsRollbackOf(&b, pipeline, env, from), nil
}

// IsRollbackOf reports whether b is the rollback of Bundle from in env of
// pipeline that kardinal creates: the rollback label, the pipeline label,
// kardinal.io/rollback-from naming from, and intent.targetEnvironment env.
// A Bundle at a rollback's name that is not is someone else's (#2090).
func IsRollbackOf(b *v1alpha1.Bundle, pipeline, env, from string) bool {
	return b.Labels[LabelRollback] == "true" && b.Labels[LabelPipeline] == pipeline &&
		b.Annotations[AnnotationRollbackFrom] == from &&
		b.Spec.Intent != nil && b.Spec.Intent.TargetEnvironment == env
}

// FindRollback returns the name of an existing rollback Bundle that rolls the
// environment env of pipeline back from the Bundle from, or "" when there is
// none. Automatic rollback paths (onHealthFailure=rollback and RollbackPolicy)
// call it first, so one failing Bundle gets one rollback in an environment.
func FindRollback(ctx context.Context, c client.Reader, ns, pipeline, env, from string) (string, error) {
	var list v1alpha1.BundleList
	if err := c.List(ctx, &list, client.InNamespace(ns),
		client.MatchingLabels{LabelRollback: "true", LabelPipeline: pipeline}); err != nil {
		return "", fmt.Errorf("list rollback bundles of pipeline %s: %w", pipeline, err)
	}
	for i := range list.Items {
		b := &list.Items[i]
		if b.Annotations[AnnotationRollbackFrom] != from {
			continue
		}
		if b.Spec.Intent == nil || b.Spec.Intent.TargetEnvironment == env {
			return b.Name, nil
		}
	}
	return "", nil
}

// RollbackTaken reports whether rollback Bundle name, which a create found
// existing, is not the rollback of from in env of pipeline (IsRollbackOf):
// another environment's, created by a concurrent reconcile, or someone
// else's. The caller retries, and AutoRollbackNameIn then names env's own
// or stops for a human.
func RollbackTaken(ctx context.Context, c client.Reader, ns, name, pipeline, env, from string) (bool, error) {
	free, err := rollbackNameFree(ctx, c, ns, name, pipeline, env, from)
	return !free, err
}
