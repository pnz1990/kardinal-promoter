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
	"sort"
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
	// AnnotationCreatedBy is who created a Bundle: the Kubernetes username of
	// a person (the chart's admission policy pins it to the requester), or a
	// kardinal component ("subscription:<name>", "bundle-api",
	// "kardinal-controller") for the Bundles the controller creates. An
	// approval gate's excludeAuthor compares approvers with it.
	AnnotationCreatedBy = "kardinal.io/created-by"
)

// ControllerCreator is the kardinal.io/created-by of the Bundles the
// controller creates on its own (automatic rollbacks).
const ControllerCreator = "kardinal-controller"

// Creator names of kardinal components, not people: the Bundle API's static
// token and the UI without per-user authentication.
const (
	BundleAPICreator = "bundle-api"
	UICreator        = "kardinal-ui"
)

// ComponentCreator reports whether who, a kardinal.io/created-by value,
// names a kardinal component rather than a person: excludeAuthor cannot tell
// who asked for such a Bundle.
func ComponentCreator(who string) bool {
	switch who {
	case ControllerCreator, BundleAPICreator, UICreator:
		return true
	}
	return strings.HasPrefix(who, "subscription:")
}

// StampCreatedBy sets the kardinal.io/created-by annotation on obj to who,
// unless it is already set or who is empty.
func StampCreatedBy(obj metav1.Object, who string) {
	if who == "" {
		return
	}
	ann := obj.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	if _, ok := ann[AnnotationCreatedBy]; ok {
		return
	}
	ann[AnnotationCreatedBy] = who
	obj.SetAnnotations(ann)
}

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

// Halted reports whether b never promotes again: a newer Bundle superseded
// it, or it was rejected (Rejected). Both are history, so every view that
// picks a Pipeline's current Bundle skips them the same way.
func Halted(b *v1alpha1.Bundle) bool {
	return b.Status.Phase == "Superseded" || Rejected(b)
}

// RejectedLiveHint is what the views say about a Rejected Bundle whose change
// is live in an environment (RejectedLiveStep): rejecting stops promotions,
// it does not revert what was merged.
const RejectedLiveHint = "rejected change is live; roll back"

// LiveStepState reports whether a PromotionStep in state has its change in
// the environment: its PR merged or its push landed, and the health check
// runs (HealthChecking) or passed (Verified).
func LiveStepState(state string) bool {
	return state == "HealthChecking" || state == "Verified"
}

// RejectedLiveStep reports whether s, a step of b, is a rejected change that
// is live: b is Rejected and s is HealthChecking or Verified. Such a Bundle
// stays the current one in that environment, marked Rejected, so the views
// show what is deployed there instead of hiding it with the Halted Bundles.
func RejectedLiveStep(b *v1alpha1.Bundle, s *v1alpha1.PromotionStep) bool {
	return Rejected(b) && s.Spec.BundleName == b.Name && LiveStepState(s.Status.State)
}

// RejectedLiveEnvs returns, sorted, the environments where the Rejected
// Bundle b's change is live (RejectedLiveStep), or nil.
func RejectedLiveEnvs(b *v1alpha1.Bundle, steps []v1alpha1.PromotionStep) []string {
	if !Rejected(b) {
		return nil
	}
	seen := map[string]bool{}
	var envs []string
	for i := range steps {
		s := &steps[i]
		if s.Namespace == b.Namespace && RejectedLiveStep(b, s) && !seen[s.Spec.Environment] {
			seen[s.Spec.Environment] = true
			envs = append(envs, s.Spec.Environment)
		}
	}
	sort.Strings(envs)
	return envs
}

// moreCurrent reports whether Bundle a outranks Bundle b of the same
// Pipeline as the Pipeline's current Bundle. A Bundle that is not Halted
// (Superseded or Rejected), or is Rejected with its change live somewhere
// (live), outranks a Halted one whatever its phase, so a newer Failed Bundle
// is never hidden behind an older Verified or Promoting one (E2E-R15), and a
// rejected change that is deployed is not hidden behind an older Bundle.
// Otherwise the newer one (CompareCreation) wins.
func moreCurrent(a, b *v1alpha1.Bundle, live map[string]bool) bool {
	aHalted := Halted(a) && !live[a.Namespace+"/"+a.Name]
	bHalted := Halted(b) && !live[b.Namespace+"/"+b.Name]
	if aHalted != bHalted {
		return bHalted
	}
	return CompareCreation(a, b) > 0
}

// CurrentBundle returns the current Bundle of one Pipeline, given its
// Bundles and (any superset of) their PromotionSteps: the newest Bundle that
// is not Superseded or Rejected, whatever its phase, where a Rejected Bundle
// whose change is live in some environment (RejectedLiveStep) counts as not
// Rejected; or the newest one when every Bundle is Halted (moreCurrent). It
// returns nil when bundles is empty. The UI API's activeBundleName, the
// pipeline table of kardinal get pipelines and web/src/bundleSelection.ts
// pickDefaultBundle all use this rule.
func CurrentBundle(bundles []v1alpha1.Bundle, steps []v1alpha1.PromotionStep) *v1alpha1.Bundle {
	live := map[string]bool{}
	for i := range bundles {
		if len(RejectedLiveEnvs(&bundles[i], steps)) > 0 {
			live[bundles[i].Namespace+"/"+bundles[i].Name] = true
		}
	}
	var current *v1alpha1.Bundle
	for i := range bundles {
		if current == nil || moreCurrent(&bundles[i], current, live) {
			current = &bundles[i]
		}
	}
	return current
}

// CompareAuditEvents orders two AuditEvents by when they happened: negative
// when a happened before b. spec.timestamp has one-second resolution once
// stored, so within a second the writers' kardinal.io/created-at annotation
// (RFC3339Nano) decides, then the name. A gate that flips twice in one second
// thus lists its records in the order of the flips (#1484).
func CompareAuditEvents(a, b *v1alpha1.AuditEvent) int {
	if c := a.Spec.Timestamp.Compare(b.Spec.Timestamp.Time); c != 0 {
		return c
	}
	ta, okA := createdAtOf(a)
	tb, okB := createdAtOf(b)
	switch {
	case okA && okB:
		if c := ta.Compare(tb); c != 0 {
			return c
		}
	case okA != okB:
		// A record without the annotation was written by an older
		// controller, before the ones that carry it.
		if okA {
			return 1
		}
		return -1
	}
	return strings.Compare(a.Name, b.Name)
}

func createdAt(b *v1alpha1.Bundle) (time.Time, bool) { return createdAtOf(b) }

// createdAtOf parses obj's kardinal.io/created-at annotation.
func createdAtOf(obj metav1.Object) (time.Time, bool) {
	s, ok := obj.GetAnnotations()[AnnotationCreatedAt]
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

// copyArtifacts deep-copies the images, config ref and chart of src into dst. An
// image Bundle stored before the CRD refused a configRef on it may carry one
// it never deployed; it is not copied, so the copy passes the CRD (#1353).
func copyArtifacts(dst *v1alpha1.BundleSpec, src *v1alpha1.Bundle) {
	dst.Type = src.Spec.Type
	if len(src.Spec.Images) > 0 {
		dst.Images = append([]v1alpha1.ImageRef(nil), src.Spec.Images...)
	}
	if src.Spec.ConfigRef != nil && src.Spec.Type != "image" {
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

// SupersededMessage starts the message of every PromotionStep the
// supersession of its Bundle cancels (the PromotionStep reconciler writes
// it), so readers can tell such a step from one that failed on its own.
func SupersededMessage(bundle string) string {
	return "bundle " + bundle + " was superseded"
}

// CancelledBySupersession reports whether s ended because its Bundle was
// superseded, not because its promotion failed: it is Failed with
// SupersededMessage, or still in a state the supersession guard cancels
// (it has not run yet). A RollingBack or AbortedByAlarm step, or one that
// failed before the supersession, is not.
func CancelledBySupersession(s *v1alpha1.PromotionStep) bool {
	switch s.Status.State {
	case "Failed":
		return strings.HasPrefix(s.Status.Message, SupersededMessage(s.Spec.BundleName))
	case "", "Pending", "Promoting", "WaitingForMerge", "HealthChecking":
		return true
	}
	return false
}
