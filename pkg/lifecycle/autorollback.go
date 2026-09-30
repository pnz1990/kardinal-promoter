// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// maxBundleNameLen keeps Bundle names within the 63-character limit of the
// kardinal.io/bundle label that the Graph puts on the Bundle's PromotionSteps.
const maxBundleNameLen = 63

// AutoRollbackName returns the fixed name of the automatic rollback Bundle
// that source (for example "alarm" or "policy") creates for the failing Bundle
// bundle. The name is deterministic, so a retried reconcile reuses the Bundle
// instead of creating another, and at most 63 characters long: a long Bundle
// name is shortened and suffixed with a hash so names stay distinct.
func AutoRollbackName(bundle, source string) string {
	name := bundle + "-rollback-" + source
	if len(name) <= maxBundleNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-rollback-" + source + "-" + hex.EncodeToString(sum[:])[:8]
	keep := maxBundleNameLen - len(suffix)
	if keep < 1 {
		return strings.TrimLeft(suffix, "-")[:maxBundleNameLen]
	}
	return strings.TrimRight(bundle[:keep], "-.") + suffix
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
