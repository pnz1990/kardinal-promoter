// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// RejectedArtifacts holds the artifacts of a Pipeline's rejected Bundles
// (kardinal reject, #1451). A rejection is about what a Bundle deploys, not
// the Bundle object: another Bundle with the same image or config commit is
// just as unwanted. Rollback, promote and Subscriptions skip any Bundle that
// carries one of them (Carries).
type RejectedArtifacts struct {
	// images holds repository@digest and repository:tag keys.
	images map[string]string
	// commits holds config commit SHAs.
	commits map[string]string
}

// LoadRejectedArtifacts lists the Bundles of pipeline in ns and collects the
// artifacts of the rejected ones. The Bundle reconciler never deletes a
// Rejected Bundle (historyLimit exempts it), so the set does not shrink.
func LoadRejectedArtifacts(ctx context.Context, c client.Reader, ns, pipeline string) (*RejectedArtifacts, error) {
	var list v1alpha1.BundleList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list bundles of pipeline %s: %w", pipeline, err)
	}
	return RejectedArtifactsOf(list.Items, pipeline), nil
}

// RejectedArtifactsOf collects the artifacts of the rejected Bundles of
// pipeline among bundles. Only Bundles rejected with spec.rejected count: a
// Bundle the controller rejected because it carries a rejected artifact adds
// nothing new.
func RejectedArtifactsOf(bundles []v1alpha1.Bundle, pipeline string) *RejectedArtifacts {
	r := &RejectedArtifacts{images: map[string]string{}, commits: map[string]string{}}
	for i := range bundles {
		b := &bundles[i]
		if b.Spec.Pipeline == pipeline && b.Spec.Rejected != nil {
			r.add(b)
		}
	}
	return r
}

func (r *RejectedArtifacts) add(b *v1alpha1.Bundle) {
	for _, img := range b.Spec.Images {
		for _, k := range imageRefKeys(img) {
			r.images[k] = b.Name
		}
	}
	if b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "" {
		r.commits[b.Spec.ConfigRef.CommitSHA] = b.Name
	}
}

// imageRefKeys are the keys an image is known by: its digest and its tag. A
// rejected image matches another one with the same repository and either the
// same digest or the same tag.
func imageRefKeys(img v1alpha1.ImageRef) []string {
	var keys []string
	if img.Digest != "" {
		keys = append(keys, img.Repository+"@"+img.Digest)
	}
	if img.Tag != "" {
		keys = append(keys, img.Repository+":"+img.Tag)
	}
	return keys
}

// Carries reports whether b is rejected or deploys an image or config commit
// of a rejected Bundle, and names that Bundle. A nil set carries nothing.
func (r *RejectedArtifacts) Carries(b *v1alpha1.Bundle) (string, bool) {
	if Rejected(b) {
		return b.Name, true
	}
	if r == nil {
		return "", false
	}
	for _, img := range b.Spec.Images {
		for _, k := range imageRefKeys(img) {
			if name, ok := r.images[k]; ok {
				return name, true
			}
		}
	}
	if b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "" {
		if name, ok := r.commits[b.Spec.ConfigRef.CommitSHA]; ok {
			return name, true
		}
	}
	return "", false
}
