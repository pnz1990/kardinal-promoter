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
	case b.Spec.Intent != nil && b.Spec.Intent.TargetEnvironment != env:
		return AutoRollbackName(bundle, source+"-"+env), nil
	}
	return name, nil
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
// existing, rolls another environment than env back: created by a
// concurrent reconcile of another environment, it is not env's, and the
// caller retries (AutoRollbackNameIn then names env's own).
func RollbackTaken(ctx context.Context, c client.Reader, ns, name, env string) (bool, error) {
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		return false, fmt.Errorf("get bundle %s: %w", name, err)
	}
	return b.Spec.Intent != nil && b.Spec.Intent.TargetEnvironment != env, nil
}
