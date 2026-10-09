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
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// Labels and annotations written by lifecycle actions.
const (
	LabelPipeline    = "kardinal.io/pipeline"
	LabelEnvironment = "kardinal.io/environment"
	LabelRollback    = "kardinal.io/rollback"
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
	// AnnotationRequestedBy records who asked for a promote, a rollback or a
	// Bundle created from the UI.
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
// It compares the tuple (creationTimestamp, has created-at, created-at, name).
// The API server's creationTimestamp decides when the seconds differ. Within
// the same second a Bundle without a valid kardinal.io/created-at annotation
// (created with kubectl, GitOps or an older controller) sorts before every
// Bundle that has one, and two stamped Bundles are ordered by the stamp. The
// name is the last resort. That is a total order, so sorting gives the same
// result whatever the List order (#1316).
func CompareCreation(a, b *v1alpha1.Bundle) int {
	if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
		return c
	}
	ta, okA := createdAt(a)
	tb, okB := createdAt(b)
	switch {
	case okA != okB:
		if okA {
			return 1
		}
		return -1
	case okA:
		if c := ta.Compare(tb); c != 0 {
			return c
		}
	}
	return strings.Compare(a.Name, b.Name)
}

// moreCurrent reports whether Bundle a outranks Bundle b of the same
// Pipeline as the Pipeline's current Bundle. A Bundle that is not Superseded
// outranks a Superseded one whatever its phase, so a newer Failed Bundle is
// never hidden behind an older Verified or Promoting one (E2E-R15). Otherwise
// the newer one (CompareCreation) wins.
func moreCurrent(a, b *v1alpha1.Bundle) bool {
	aSuperseded, bSuperseded := a.Status.Phase == "Superseded", b.Status.Phase == "Superseded"
	if aSuperseded != bSuperseded {
		return bSuperseded
	}
	return CompareCreation(a, b) > 0
}

// CurrentBundle returns the current Bundle of one Pipeline, given its
// Bundles: the newest Bundle that is not Superseded, whatever its phase, or
// the newest one when every Bundle is Superseded (moreCurrent). It returns
// nil when bundles is empty. The UI API's activeBundleName, the pipeline
// table of kardinal get pipelines and web/src/bundleSelection.ts
// pickDefaultBundle all use this rule.
func CurrentBundle(bundles []v1alpha1.Bundle) *v1alpha1.Bundle {
	var current *v1alpha1.Bundle
	for i := range bundles {
		if current == nil || moreCurrent(&bundles[i], current) {
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
// least one image, a config commit, or a chart version.
func HasArtifacts(b *v1alpha1.Bundle) bool {
	return len(b.Spec.Images) > 0 || (b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "") ||
		(b.Spec.Chart != nil && b.Spec.Chart.Version != "")
}

// SameArtifacts reports whether two Bundles deploy the same images and config
// commit, ignoring image order.
func SameArtifacts(a, b *v1alpha1.Bundle) bool {
	return slices.Equal(imageKeys(a), imageKeys(b)) && configKey(a) == configKey(b) && chartKey(a) == chartKey(b)
}

func imageKeys(b *v1alpha1.Bundle) []string {
	keys := make([]string, 0, len(b.Spec.Images))
	for _, img := range b.Spec.Images {
		keys = append(keys, imageKey(img))
	}
	slices.Sort(keys)
	return keys
}

// imageKey identifies an image by repository, tag and digest.
func imageKey(img v1alpha1.ImageRef) string {
	return img.Repository + ":" + img.Tag + "@" + img.Digest
}

func configKey(b *v1alpha1.Bundle) string {
	if b.Spec.ConfigRef == nil {
		return ""
	}
	return b.Spec.ConfigRef.GitRepo + "@" + b.Spec.ConfigRef.CommitSHA
}

// chartKey identifies a chart Bundle's chart version.
func chartKey(b *v1alpha1.Bundle) string {
	if b.Spec.Chart == nil {
		return ""
	}
	return b.Spec.Chart.RepoURL + "/" + b.Spec.Chart.Name + ":" + b.Spec.Chart.Version + "@" + b.Spec.Chart.Digest
}

// copyArtifacts deep-copies the images, config ref and chart of src into dst.
func copyArtifacts(dst *v1alpha1.BundleSpec, src *v1alpha1.Bundle) {
	dst.Type = src.Spec.Type
	if len(src.Spec.Images) > 0 {
		dst.Images = append([]v1alpha1.ImageRef(nil), src.Spec.Images...)
	}
	if src.Spec.ConfigRef != nil {
		ref := *src.Spec.ConfigRef
		dst.ConfigRef = &ref
	}
	if src.Spec.Chart != nil {
		chart := *src.Spec.Chart
		dst.Chart = &chart
	}
}

// copyableCIRunURL is the ciRunURL a Bundle copied from src may carry: src's,
// unless it fails graph.ValidateCIRunURL, the bundle API's check (E2E-R22).
// src may have been created another way or before that check. The dropped URL
// is not logged: it can hold credentials.
func copyableCIRunURL(ctx context.Context, src *v1alpha1.Bundle) string {
	raw := src.Spec.Provenance.CIRunURL
	if err := graph.ValidateCIRunURL(raw); err != nil {
		zerolog.Ctx(ctx).Info().
			Str("bundle", src.Name).
			Str("namespace", src.Namespace).
			Str("reason", err.Error()).
			Msg("not copying provenance.ciRunURL to the new Bundle")
		return ""
	}
	return raw
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
