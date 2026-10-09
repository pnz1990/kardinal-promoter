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
// just as unwanted. Only what the rejection rejects counts: the artifacts
// that changed against the Bundle Verified before it (RejectedSetOf,
// status.rejectedArtifacts), so an unchanged sidecar stays promotable. Rollback, promote and Subscriptions skip any Bundle that
// carries one of them (Carries).
type RejectedArtifacts struct {
	// digests holds repository@digest keys of rejected images that have a
	// digest; tags holds repository:tag keys of rejected images without one.
	digests map[string]string
	tags    map[string]string
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
	return rejectedArtifactsOf(bundles, pipeline, false)
}

// RecordedRejectedArtifactsOf is RejectedArtifactsOf without the rejections
// whose set the Bundle reconciler has not recorded yet. Rejecting a Bundle
// because it carries a rejected artifact is final, so it waits for the
// recorded set rather than take every artifact of the rejected Bundle; the
// sibling's status write re-queues the Bundle.
func RecordedRejectedArtifactsOf(bundles []v1alpha1.Bundle, pipeline string) *RejectedArtifacts {
	return rejectedArtifactsOf(bundles, pipeline, true)
}

func rejectedArtifactsOf(bundles []v1alpha1.Bundle, pipeline string, recordedOnly bool) *RejectedArtifacts {
	r := &RejectedArtifacts{digests: map[string]string{}, tags: map[string]string{}, commits: map[string]string{}}
	for i := range bundles {
		b := &bundles[i]
		if b.Spec.Pipeline == pipeline && b.Spec.Rejected != nil && (!recordedOnly || b.Status.RejectedArtifacts != nil) {
			r.add(b)
		}
	}
	return r
}

// add records what b's rejection rejects: status.rejectedArtifacts once the
// Bundle reconciler wrote it (RejectedSetOf), else every artifact of b.
func (r *RejectedArtifacts) add(b *v1alpha1.Bundle) {
	images := b.Spec.Images
	commit := ""
	if b.Spec.ConfigRef != nil {
		commit = b.Spec.ConfigRef.CommitSHA
	}
	if set := b.Status.RejectedArtifacts; set != nil {
		images, commit = set.Images, set.ConfigCommitSHA
	}
	for _, img := range images {
		switch {
		case img.Digest != "":
			r.digests[img.Repository+"@"+img.Digest] = b.Name
		case img.Tag != "":
			r.tags[img.Repository+":"+img.Tag] = b.Name
		}
	}
	if commit != "" {
		r.commits[commit] = b.Name
	}
}

// carriesImage reports whether img is a rejected image. A rejected image
// with a digest matches only that digest: a tag moves (latest, a re-pushed
// fix), so r/a:latest@bad being rejected says nothing about r/a:latest@fixed.
// A rejected image without a digest is known only by its tag and matches any
// image with that repository and tag.
func (r *RejectedArtifacts) carriesImage(img v1alpha1.ImageRef) (string, bool) {
	if img.Digest != "" {
		if name, ok := r.digests[img.Repository+"@"+img.Digest]; ok {
			return name, true
		}
	}
	if img.Tag != "" {
		if name, ok := r.tags[img.Repository+":"+img.Tag]; ok {
			return name, true
		}
	}
	return "", false
}

// Carries reports whether b is rejected or deploys an image or config commit
// of a rejected Bundle (carriesImage), and names that Bundle. A nil set
// carries nothing.
func (r *RejectedArtifacts) Carries(b *v1alpha1.Bundle) (string, bool) {
	if Rejected(b) {
		return b.Name, true
	}
	if r == nil {
		return "", false
	}
	for _, img := range b.Spec.Images {
		if name, ok := r.carriesImage(img); ok {
			return name, true
		}
	}
	if b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "" {
		if name, ok := r.commits[b.Spec.ConfigRef.CommitSHA]; ok {
			return name, true
		}
	}
	return "", false
}
