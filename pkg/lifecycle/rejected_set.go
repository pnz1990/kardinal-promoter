// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"sort"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// RejectedSetOf computes what rejecting b rejects (QA #1489): the artifacts
// of b that were not already Verified before it. The environments compared
// are the ones b reached (its change was deployed there: HealthChecking,
// Verified, AbortedByAlarm or RollingBack), or, when it reached none, every
// environment it has a status for. In each, the predecessor is the newest
// Bundle of the same Pipeline created before b (CompareCreation) that is
// Verified there. An artifact is rejected unless every compared environment
// has a predecessor carrying the same artifact (sameImage): an unchanged
// sidecar is not rejected, so rolling back to the predecessor stays possible,
// while the changed image is. With no environment to compare, or one without
// a predecessor, every artifact is rejected.
func RejectedSetOf(b *v1alpha1.Bundle, bundles []v1alpha1.Bundle) v1alpha1.RejectedArtifactSet {
	envs := reachedEnvironments(b)
	preds := make([]*v1alpha1.Bundle, 0, len(envs))
	var compared []string
	for _, env := range envs {
		p := verifiedBefore(b, bundles, env)
		if p == nil {
			preds = nil
			compared = nil
			break
		}
		preds = append(preds, p)
		compared = append(compared, env+"="+p.Name)
	}
	if len(envs) == 0 {
		preds = nil
	}
	set := v1alpha1.RejectedArtifactSet{ComparedWith: compared}
	for _, img := range b.Spec.Images {
		if !allCarryImage(preds, img) {
			set.Images = append(set.Images, img)
		}
	}
	if b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "" && !allCarryCommit(preds, b.Spec.ConfigRef.CommitSHA) {
		set.ConfigCommitSHA = b.Spec.ConfigRef.CommitSHA
	}
	return set
}

// reachedEnvironments are the environments b's change was deployed to, or,
// when none, every environment b has a status for. Sorted.
func reachedEnvironments(b *v1alpha1.Bundle) []string {
	var reached, all []string
	for _, e := range b.Status.Environments {
		all = append(all, e.Name)
		switch e.Phase {
		case "HealthChecking", "Verified", "AbortedByAlarm", "RollingBack":
			reached = append(reached, e.Name)
		}
	}
	if len(reached) == 0 {
		reached = all
	}
	sort.Strings(reached)
	return reached
}

// verifiedBefore is the newest Bundle of b's Pipeline created before b that
// is Verified in env, or nil.
func verifiedBefore(b *v1alpha1.Bundle, bundles []v1alpha1.Bundle, env string) *v1alpha1.Bundle {
	var best *v1alpha1.Bundle
	for i := range bundles {
		o := &bundles[i]
		if o.Name == b.Name || o.Namespace != b.Namespace || o.Spec.Pipeline != b.Spec.Pipeline ||
			CompareCreation(o, b) >= 0 || !verifiedIn(o, env) {
			continue
		}
		if best == nil || CompareCreation(o, best) > 0 {
			best = o
		}
	}
	return best
}

func verifiedIn(b *v1alpha1.Bundle, env string) bool {
	for _, e := range b.Status.Environments {
		if e.Name == env {
			return e.Phase == "Verified"
		}
	}
	return false
}

func allCarryImage(preds []*v1alpha1.Bundle, img v1alpha1.ImageRef) bool {
	if len(preds) == 0 {
		return false
	}
	for _, p := range preds {
		found := false
		for _, o := range p.Spec.Images {
			if sameImage(img, o) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func allCarryCommit(preds []*v1alpha1.Bundle, sha string) bool {
	if len(preds) == 0 {
		return false
	}
	for _, p := range preds {
		if p.Spec.ConfigRef == nil || p.Spec.ConfigRef.CommitSHA != sha {
			return false
		}
	}
	return true
}

// sameImage reports whether o is the very image img is: the same repository
// and digest, or, for img without a digest, the same tag.
func sameImage(img, o v1alpha1.ImageRef) bool {
	if img.Repository != o.Repository {
		return false
	}
	if img.Digest != "" {
		return img.Digest == o.Digest
	}
	return img.Tag != "" && img.Tag == o.Tag
}
