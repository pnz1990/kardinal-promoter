// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package lifecycle holds the one implementation of the operator actions on a
// Pipeline: pause/resume, rollback and promote. The CLI, the UI API and the
// reconcilers (onHealthFailure=rollback, RollbackPolicy) call these functions
// so the rules are the same whichever surface triggers the action.
//
// The functions only read the cluster and return the objects to write. The
// caller creates them, so a reconciler keeps control of its own writes and a
// CLI can print what it created.
package lifecycle

import (
	"errors"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Labels and annotations written by lifecycle actions.
const (
	LabelPipeline    = "kardinal.io/pipeline"
	LabelEnvironment = "kardinal.io/environment"
	LabelRollback    = "kardinal.io/rollback"
	LabelEmergency   = "kardinal.io/emergency"
	LabelReason      = "kardinal.io/reason"
	LabelScope       = "kardinal.io/scope"
	LabelFreeze      = "kardinal.io/freeze"

	// AnnotationCreatedAt is the RFC 3339 creation time, with nanoseconds,
	// stamped by the component that creates a Bundle. The API server's
	// creationTimestamp has one-second precision, so two Bundles created in
	// the same second are ordered by this annotation instead of by name.
	AnnotationCreatedAt = "kardinal.io/created-at"
	// AnnotationRollbackFrom names the Bundle that was deployed in the
	// environment when the rollback was requested.
	AnnotationRollbackFrom = "kardinal.io/rollback-from"
	// AnnotationPromotedFrom names the Bundle whose artifacts a promote copied.
	AnnotationPromotedFrom = "kardinal.io/promoted-from"
	// AnnotationRequestedBy records who asked for a promote.
	AnnotationRequestedBy = "kardinal.io/requested-by"
)

// Sentinel errors. Callers map them to exit messages or HTTP status codes
// (ErrNotFound → 404, ErrInvalid → 400, ErrConflict → 409).
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid request")
	ErrConflict = errors.New("conflict")
)

// StampCreatedAt sets the kardinal.io/created-at annotation on obj to now,
// unless it is already set or now is zero.
func StampCreatedAt(obj metav1.Object, now time.Time) {
	if now.IsZero() {
		return
	}
	ann := obj.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	if _, ok := ann[AnnotationCreatedAt]; ok {
		return
	}
	ann[AnnotationCreatedAt] = now.UTC().Format(time.RFC3339Nano)
	obj.SetAnnotations(ann)
}

// CompareCreation orders two Bundles by creation: negative when a was created
// before b, positive when after, zero only for the same object.
//
// The API server's creationTimestamp decides when the seconds differ. Within
// the same second the kardinal.io/created-at annotation decides when both
// Bundles carry it. The name is the last resort, so the order is total and
// stable across reconciles.
func CompareCreation(a, b *v1alpha1.Bundle) int {
	if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
		return c
	}
	ta, okA := createdAt(a)
	tb, okB := createdAt(b)
	if okA && okB {
		if c := ta.Compare(tb); c != 0 {
			return c
		}
	}
	return strings.Compare(a.Name, b.Name)
}

// MoreCurrent reports whether Bundle a outranks Bundle b of the same
// Pipeline as the Pipeline's current Bundle. A Bundle that is not Superseded
// outranks a Superseded one whatever its phase, so a newer Failed Bundle is
// never hidden behind an older Verified or Promoting one (E2E-R15). Otherwise
// the newer one (CompareCreation) wins.
func MoreCurrent(a, b *v1alpha1.Bundle) bool {
	aSuperseded, bSuperseded := a.Status.Phase == "Superseded", b.Status.Phase == "Superseded"
	if aSuperseded != bSuperseded {
		return bSuperseded
	}
	return CompareCreation(a, b) > 0
}

// CurrentBundle returns the current Bundle of one Pipeline, given its
// Bundles: the newest Bundle that is not Superseded, whatever its phase, or
// the newest one when every Bundle is Superseded (MoreCurrent). It returns
// nil when bundles is empty. The UI API's activeBundleName, the pipeline
// table of kardinal get pipelines and web/src/bundleSelection.ts
// pickDefaultBundle all use this rule.
func CurrentBundle(bundles []v1alpha1.Bundle) *v1alpha1.Bundle {
	var current *v1alpha1.Bundle
	for i := range bundles {
		if current == nil || MoreCurrent(&bundles[i], current) {
			current = &bundles[i]
		}
	}
	return current
}

func createdAt(b *v1alpha1.Bundle) (time.Time, bool) {
	s, ok := b.Annotations[AnnotationCreatedAt]
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// HasArtifacts reports whether the Bundle carries something to deploy: at
// least one image, or a config commit.
func HasArtifacts(b *v1alpha1.Bundle) bool {
	return len(b.Spec.Images) > 0 || (b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "")
}

// SameArtifacts reports whether two Bundles deploy the same images and config
// commit, ignoring image order.
func SameArtifacts(a, b *v1alpha1.Bundle) bool {
	return slices.Equal(imageKeys(a), imageKeys(b)) && configKey(a) == configKey(b)
}

func imageKeys(b *v1alpha1.Bundle) []string {
	keys := make([]string, 0, len(b.Spec.Images))
	for _, img := range b.Spec.Images {
		keys = append(keys, img.Repository+":"+img.Tag+"@"+img.Digest)
	}
	slices.Sort(keys)
	return keys
}

func configKey(b *v1alpha1.Bundle) string {
	if b.Spec.ConfigRef == nil {
		return ""
	}
	return b.Spec.ConfigRef.GitRepo + "@" + b.Spec.ConfigRef.CommitSHA
}

// copyArtifacts deep-copies the images and config ref of src into dst.
func copyArtifacts(dst *v1alpha1.BundleSpec, src *v1alpha1.Bundle) {
	dst.Type = src.Spec.Type
	if len(src.Spec.Images) > 0 {
		dst.Images = append([]v1alpha1.ImageRef(nil), src.Spec.Images...)
	}
	if src.Spec.ConfigRef != nil {
		ref := *src.Spec.ConfigRef
		dst.ConfigRef = &ref
	}
}

func hasEnvironment(p *v1alpha1.Pipeline, env string) bool {
	for _, e := range p.Spec.Environments {
		if e.Name == env {
			return true
		}
	}
	return false
}

// InFlightPhase reports whether a Bundle phase is still on its way through the
// pipeline: new, Available or Promoting.
func InFlightPhase(phase string) bool {
	return phase == "" || phase == "Available" || phase == "Promoting"
}

// VerifiedTime returns when a PromotionStep became Verified: the transition
// time of its Verified=True condition. ok is false when the step has no such
// condition.
func VerifiedTime(s *v1alpha1.PromotionStep) (time.Time, bool) {
	cond := meta.FindStatusCondition(s.Status.Conditions, "Verified")
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.LastTransitionTime.IsZero() {
		return time.Time{}, false
	}
	return cond.LastTransitionTime.Time, true
}
