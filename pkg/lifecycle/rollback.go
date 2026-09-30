// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// RollbackRequest describes a rollback of one environment of a pipeline.
type RollbackRequest struct {
	Namespace   string
	Pipeline    string
	Environment string
	// ToBundle is the Bundle to go back to. Empty means the most recent Bundle,
	// other than the one deployed now, that was Verified in the environment.
	ToBundle string
	// FromBundle is the Bundle being rolled back. Empty means the Bundle whose
	// change was deployed last in the environment. Automatic rollbacks set it
	// to the failing Bundle.
	FromBundle string
	// Actor is recorded as spec.provenance.author of the rollback Bundle.
	Actor string
	// Name fixes the rollback Bundle name (used by automatic rollbacks for
	// idempotency). Empty means GenerateName "<pipeline>-rollback-".
	Name string
	// Reason is written to the kardinal.io/reason label when set.
	Reason string
	// Now stamps kardinal.io/created-at when not zero.
	Now time.Time
	// Automatic marks a rollback the controller starts on its own
	// (onHealthFailure=rollback, RollbackPolicy). An automatic rollback of a
	// Bundle that is itself a rollback is refused: a failing rollback must not
	// start another one, or the environment would ping-pong between two
	// Bundles. A human can still roll back with `kardinal rollback`.
	Automatic bool
}

// RollbackPlan is the result of PlanRollback.
type RollbackPlan struct {
	// Current is the Bundle deployed in the environment now. Nil when it is
	// unknown or has been deleted.
	Current *v1alpha1.Bundle
	// CurrentName is the name of the deployed Bundle, even when the object is
	// gone. Empty when nothing has been deployed.
	CurrentName string
	// Target is the Bundle whose artifacts the rollback restores.
	Target *v1alpha1.Bundle
	// Bundle is the rollback Bundle to create.
	Bundle *v1alpha1.Bundle
}

// PlanRollback picks the Bundle to roll back to and builds the rollback Bundle.
//
// Rules:
//   - The environment must exist in the pipeline.
//   - Automatic: the deployed Bundle must not itself be a rollback (label
//     kardinal.io/rollback=true).
//   - With ToBundle: the Bundle must exist, belong to the pipeline, carry
//     artifacts, differ from what is deployed now, and have been Verified in
//     the environment (every one of its PromotionSteps there is Verified).
//   - Without ToBundle: the target is the most recent Bundle, other than the
//     deployed one, whose PromotionSteps in the environment are all Verified,
//     that has the same type as the deployed one, carries artifacts, and deploys
//     different artifacts. A Bundle with the deployed (failing) images is never
//     chosen, and neither is a Bundle that an earlier rollback in the
//     environment rolled back from (named in the kardinal.io/rollback-from
//     annotation of a rollback Bundle): after V2 was rolled back to V1, rolling
//     back again does not return to V2. ToBundle can still name it.
//   - The rollback Bundle copies the target's images and config ref, and
//     puts back everything else the deployed Bundle changed (#1315): for each
//     image repository the deployed Bundle names and the target does not, the
//     version from the newest Bundle, other than the deployed one and the ones
//     rolled back from, that was Verified in the environment. The same for the
//     config commit of a config Bundle. When no such Bundle exists the
//     rollback is refused, naming the image or config repository, instead of
//     leaving the failing version in place. A target whose type cannot carry
//     what the deployed Bundle changed (a config Bundle for images, an image
//     or mixed Bundle for a config commit) is refused too. Without ToBundle a
//     target whose restored set equals what is deployed is skipped.
//   - It sets intent.targetEnvironment to this environment, and records the
//     target in spec.provenance.rollbackOf and the deployed Bundle in the
//     kardinal.io/rollback-from annotation. The Graph of a targetEnvironment
//     Bundle keeps every environment upstream of the target (graph
//     filterByIntent), so the rollback promotes the target's artifacts through
//     those environments first, with their gates, soak and health checks.
//
// Errors wrap ErrNotFound, ErrInvalid or ErrConflict.
func PlanRollback(ctx context.Context, c client.Reader, req RollbackRequest) (*RollbackPlan, error) {
	if req.Pipeline == "" || req.Environment == "" {
		return nil, fmt.Errorf("rollback: pipeline and environment are required: %w", ErrInvalid)
	}
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Pipeline}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("rollback: pipeline %s/%s: %w", req.Namespace, req.Pipeline, ErrNotFound)
		}
		return nil, fmt.Errorf("rollback: get pipeline %s/%s: %w", req.Namespace, req.Pipeline, err)
	}
	if !hasEnvironment(&p, req.Environment) {
		return nil, fmt.Errorf("rollback: pipeline %s has no environment %q: %w", req.Pipeline, req.Environment, ErrInvalid)
	}

	h, err := loadEnvHistory(ctx, c, req.Namespace, req.Pipeline, req.Environment)
	if err != nil {
		return nil, fmt.Errorf("rollback: %w", err)
	}

	plan := &RollbackPlan{CurrentName: req.FromBundle}
	if plan.CurrentName == "" {
		plan.CurrentName = h.deployed()
	}
	if plan.CurrentName != "" {
		cur, getErr := getBundle(ctx, c, req.Namespace, plan.CurrentName)
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			return nil, fmt.Errorf("rollback: get deployed bundle %s: %w", plan.CurrentName, getErr)
		}
		plan.Current = cur
	}
	if req.Automatic && plan.Current != nil && plan.Current.Labels[LabelRollback] == "true" {
		return nil, fmt.Errorf("rollback: %s is itself a rollback; a failing rollback is not rolled back automatically, roll back by hand with kardinal rollback: %w",
			plan.CurrentName, ErrConflict)
	}

	rolledBack, rbErr := rolledBackFrom(ctx, c, req.Namespace, req.Pipeline, req.Environment)
	if rbErr != nil {
		return nil, fmt.Errorf("rollback: %w", rbErr)
	}
	src := &restoreSources{
		c: c, ns: req.Namespace, pipeline: req.Pipeline, env: req.Environment,
		verified: h.verifiedNewestFirst(), deployed: plan.CurrentName, rolledBack: rolledBack,
		cache: map[string]*v1alpha1.Bundle{},
	}
	var restored *v1alpha1.Bundle

	if req.ToBundle != "" {
		target, getErr := getBundle(ctx, c, req.Namespace, req.ToBundle)
		if getErr != nil {
			if apierrors.IsNotFound(getErr) {
				return nil, fmt.Errorf("rollback: bundle %s/%s: %w", req.Namespace, req.ToBundle, ErrNotFound)
			}
			return nil, fmt.Errorf("rollback: get bundle %s: %w", req.ToBundle, getErr)
		}
		switch {
		case target.Spec.Pipeline != req.Pipeline:
			return nil, fmt.Errorf("rollback: bundle %s belongs to pipeline %q, not %s: %w",
				target.Name, target.Spec.Pipeline, req.Pipeline, ErrInvalid)
		case !HasArtifacts(target):
			return nil, fmt.Errorf("rollback: bundle %s has no images or config commit to restore: %w",
				target.Name, ErrInvalid)
		case target.Name == plan.CurrentName:
			return nil, fmt.Errorf("rollback: bundle %s is what %s runs now: %w",
				target.Name, req.Environment, ErrConflict)
		case plan.Current != nil && SameArtifacts(target, plan.Current):
			return nil, fmt.Errorf("rollback: bundle %s deploys the same artifacts as the deployed bundle %s: %w",
				target.Name, plan.CurrentName, ErrConflict)
		case !h.verifiedIn(target.Name):
			return nil, fmt.Errorf("rollback: bundle %s was never Verified in %s, so it is not known to work there; pick a Bundle that was: %w",
				target.Name, req.Environment, ErrInvalid)
		}
		r, restoreErr := src.restore(ctx, plan.Current, target)
		if restoreErr != nil {
			return nil, fmt.Errorf("rollback: %w", restoreErr)
		}
		if plan.Current != nil && SameArtifacts(r, plan.Current) {
			return nil, fmt.Errorf("rollback: rolling back to bundle %s restores the same artifacts as the deployed bundle %s: %w",
				target.Name, plan.CurrentName, ErrConflict)
		}
		plan.Target, restored = target, r
	} else {
		if plan.CurrentName == "" {
			return nil, fmt.Errorf("rollback: nothing has been deployed to %s in pipeline %s yet: %w",
				req.Environment, req.Pipeline, ErrConflict)
		}
		for _, name := range src.verified {
			if name == plan.CurrentName || rolledBack[name] {
				continue
			}
			cand, getErr := getBundle(ctx, c, req.Namespace, name)
			if getErr != nil {
				if apierrors.IsNotFound(getErr) {
					continue // pruned by historyLimit
				}
				return nil, fmt.Errorf("rollback: get bundle %s: %w", name, getErr)
			}
			if cand.Spec.Pipeline != req.Pipeline || !HasArtifacts(cand) {
				continue
			}
			if plan.Current != nil && (cand.Spec.Type != plan.Current.Spec.Type || SameArtifacts(cand, plan.Current)) {
				continue
			}
			// A refusal does not depend on the candidate: an image or config
			// commit missing from the history is missing for every one.
			r, restoreErr := src.restore(ctx, plan.Current, cand)
			if restoreErr != nil {
				return nil, fmt.Errorf("rollback: %w", restoreErr)
			}
			if plan.Current != nil && SameArtifacts(r, plan.Current) {
				continue
			}
			plan.Target, restored = cand, r
			break
		}
		if plan.Target == nil {
			return nil, fmt.Errorf("rollback: no earlier Bundle with artifacts, not already rolled back from, was Verified in %s (deployed now: %s); pick one with --to: %w",
				req.Environment, plan.CurrentName, ErrConflict)
		}
	}

	plan.Bundle = buildRollbackBundle(ctx, req, plan, restored)
	return plan, nil
}

// restoreSources is the environment history PlanRollback fills a rollback
// from: the Bundles Verified in the environment, newest first, without the
// deployed one and the ones a rollback in the environment rolled back from.
type restoreSources struct {
	c                 client.Reader
	ns, pipeline, env string
	verified          []string
	deployed          string
	rolledBack        map[string]bool
	cache             map[string]*v1alpha1.Bundle
}

// deploysConfig reports whether promoting b deploys its config commit rather
// than its images. Only config Bundles do: no step reads spec.configRef of an
// image or mixed Bundle, which are promoted like image Bundles
// (docs/design/09-config-only-promotions.md).
func deploysConfig(b *v1alpha1.Bundle) bool {
	return b.Spec.Type == "config"
}

// restore returns the artifacts of the rollback of cur to target: target's
// images and config ref, plus, from the history, the version of every image
// repository cur names that target does not, and, when cur is a config Bundle
// and target has no config commit, the newest earlier config commit.
// It fails with ErrConflict naming what the history has no other version of,
// and with ErrInvalid when target's type cannot carry what cur changed. With
// cur unknown (deleted), target's artifacts are returned as they are.
func (s *restoreSources) restore(ctx context.Context, cur, target *v1alpha1.Bundle) (*v1alpha1.Bundle, error) {
	out := &v1alpha1.Bundle{}
	copyArtifacts(&out.Spec, target)
	if cur == nil {
		return out, nil
	}

	if deploysConfig(cur) {
		ref := cur.Spec.ConfigRef
		if ref == nil || ref.CommitSHA == "" {
			return out, nil
		}
		if !deploysConfig(target) {
			return nil, fmt.Errorf("bundle %s is a %s Bundle and cannot restore the config commit of %s that the deployed config bundle %s changed; pick a config Bundle with --to: %w",
				target.Name, target.Spec.Type, ref.GitRepo, cur.Name, ErrInvalid)
		}
		if out.Spec.ConfigRef != nil && out.Spec.ConfigRef.CommitSHA != "" {
			return out, nil
		}
		from, err := s.newest(ctx, func(b *v1alpha1.Bundle) bool {
			return deploysConfig(b) && b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != ""
		})
		if err != nil {
			return nil, err
		}
		if from == nil {
			return nil, fmt.Errorf("no Bundle other than %s with a config commit of %s was Verified in %s, so a rollback to %s would leave the deployed commit in place: %w",
				cur.Name, ref.GitRepo, s.env, target.Name, ErrConflict)
		}
		fromRef := *from.Spec.ConfigRef
		out.Spec.ConfigRef = &fromRef
		return out, nil
	}

	if len(cur.Spec.Images) > 0 && deploysConfig(target) {
		return nil, fmt.Errorf("bundle %s is a config Bundle and cannot restore the images (%s) that the deployed %s bundle %s changed; pick an image Bundle with --to: %w",
			target.Name, repositories(cur.Spec.Images), cur.Spec.Type, cur.Name, ErrInvalid)
	}
	for _, img := range cur.Spec.Images {
		if hasRepository(out.Spec.Images, img.Repository) {
			continue
		}
		from, err := s.newest(ctx, func(b *v1alpha1.Bundle) bool {
			return !deploysConfig(b) && hasRepository(b.Spec.Images, img.Repository)
		})
		if err != nil {
			return nil, err
		}
		if from == nil {
			return nil, fmt.Errorf("no Bundle other than %s with image %s was Verified in %s, so a rollback to %s would leave %s at the deployed version; roll back with a Bundle that names it: %w",
				cur.Name, img.Repository, s.env, target.Name, img.Repository, ErrConflict)
		}
		for _, fromImg := range from.Spec.Images {
			if fromImg.Repository == img.Repository {
				out.Spec.Images = append(out.Spec.Images, fromImg)
			}
		}
	}
	return out, nil
}

// newest returns the newest history Bundle that match accepts, or nil.
// Bundles pruned by historyLimit and Bundles of another pipeline are skipped.
func (s *restoreSources) newest(ctx context.Context, match func(*v1alpha1.Bundle) bool) (*v1alpha1.Bundle, error) {
	for _, name := range s.verified {
		if name == s.deployed || s.rolledBack[name] {
			continue
		}
		b, ok := s.cache[name]
		if !ok {
			got, err := getBundle(ctx, s.c, s.ns, name)
			switch {
			case apierrors.IsNotFound(err):
			case err != nil:
				return nil, fmt.Errorf("get bundle %s: %w", name, err)
			default:
				b = got
			}
			s.cache[name] = b
		}
		if b != nil && b.Spec.Pipeline == s.pipeline && match(b) {
			return b, nil
		}
	}
	return nil, nil
}

func hasRepository(images []v1alpha1.ImageRef, repo string) bool {
	return slices.ContainsFunc(images, func(img v1alpha1.ImageRef) bool { return img.Repository == repo })
}

func repositories(images []v1alpha1.ImageRef) string {
	repos := make([]string, 0, len(images))
	for _, img := range images {
		repos = append(repos, img.Repository)
	}
	return strings.Join(repos, ", ")
}

// buildRollbackBundle builds the rollback Bundle of plan. restored holds the
// images and config ref to deploy (restoreSources.restore).
func buildRollbackBundle(ctx context.Context, req RollbackRequest, plan *RollbackPlan, restored *v1alpha1.Bundle) *v1alpha1.Bundle {
	labels := map[string]string{
		LabelRollback: "true",
		LabelPipeline: req.Pipeline,
	}
	if req.Reason != "" {
		labels[LabelReason] = req.Reason
	}
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: req.Namespace,
			Labels:    labels,
		},
		Spec: v1alpha1.BundleSpec{
			Pipeline: req.Pipeline,
			Intent:   &v1alpha1.BundleIntent{TargetEnvironment: req.Environment},
		},
	}
	if req.Name != "" {
		b.Name = req.Name
	} else {
		b.GenerateName = req.Pipeline + "-rollback-"
	}
	if plan.CurrentName != "" {
		b.Annotations = map[string]string{AnnotationRollbackFrom: plan.CurrentName}
	}
	StampCreatedAt(b, req.Now)

	copyArtifacts(&b.Spec, restored)
	prov := &v1alpha1.BundleProvenance{}
	if plan.Target.Spec.Provenance != nil {
		prov.CommitSHA = plan.Target.Spec.Provenance.CommitSHA
		prov.CIRunURL = copyableCIRunURL(ctx, plan.Target)
		prov.Author = plan.Target.Spec.Provenance.Author
		prov.Timestamp = plan.Target.Spec.Provenance.Timestamp
	}
	if req.Actor != "" {
		prov.Author = req.Actor
	}
	prov.RollbackOf = plan.Target.Name
	b.Spec.Provenance = prov
	return b
}

// rolledBackFrom returns the Bundles that a rollback in the environment has
// rolled back from: the kardinal.io/rollback-from annotation of every rollback
// Bundle of the pipeline that targets env. Rollback Bundles created before the
// annotation existed are not counted.
func rolledBackFrom(ctx context.Context, c client.Reader, ns, pipeline, env string) (map[string]bool, error) {
	var list v1alpha1.BundleList
	if err := c.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{LabelRollback: "true"}); err != nil {
		return nil, fmt.Errorf("list rollback bundles of pipeline %s: %w", pipeline, err)
	}
	out := map[string]bool{}
	for i := range list.Items {
		b := &list.Items[i]
		from := b.Annotations[AnnotationRollbackFrom]
		if from == "" || b.Spec.Pipeline != pipeline {
			continue
		}
		if b.Spec.Intent != nil && b.Spec.Intent.TargetEnvironment != "" && b.Spec.Intent.TargetEnvironment != env {
			continue
		}
		out[from] = true
	}
	return out, nil
}

func getBundle(ctx context.Context, c client.Reader, ns, name string) (*v1alpha1.Bundle, error) {
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// envHistory is the PromotionSteps of one pipeline environment, grouped by
// Bundle. A multi-region environment has one step per region.
type envHistory struct {
	byBundle map[string][]v1alpha1.PromotionStep
}

func loadEnvHistory(ctx context.Context, c client.Reader, ns, pipeline, env string) (*envHistory, error) {
	var steps v1alpha1.PromotionStepList
	if err := c.List(ctx, &steps, client.InNamespace(ns), client.MatchingLabels{LabelPipeline: pipeline}); err != nil {
		return nil, fmt.Errorf("list promotion steps of pipeline %s: %w", pipeline, err)
	}
	return historyOf(steps.Items, pipeline, env), nil
}

// deployed returns the Bundle whose change landed last in the environment: the
// newest step that is past its merge (health checking, verified, aborted or
// rolling back, or failed after the health check started).
func (h *envHistory) deployed() string {
	var newest *v1alpha1.PromotionStep
	for name := range h.byBundle {
		for i := range h.byBundle[name] {
			s := &h.byBundle[name][i]
			if !stepLanded(s) {
				continue
			}
			if newest == nil || newerStep(s, newest) {
				newest = s
			}
		}
	}
	if newest == nil {
		return ""
	}
	return newest.Spec.BundleName
}

// verifiedNewestFirst returns the Bundles whose steps in the environment are
// all Verified, most recently verified first.
func (h *envHistory) verifiedNewestFirst() []string {
	type entry struct {
		name string
		at   time.Time
	}
	var out []entry
	for name, steps := range h.byBundle {
		var at time.Time
		all := len(steps) > 0
		for i := range steps {
			if steps[i].Status.State != "Verified" {
				all = false
				break
			}
			if t := verifiedAt(&steps[i]); t.After(at) {
				at = t
			}
		}
		if all {
			out = append(out, entry{name: name, at: at})
		}
	}
	slices.SortFunc(out, func(a, b entry) int {
		if c := b.at.Compare(a.at); c != 0 {
			return c
		}
		return strings.Compare(b.name, a.name)
	})
	names := make([]string, len(out))
	for i := range out {
		names[i] = out[i].name
	}
	return names
}

// verifiedIn reports whether every step of bundle in the environment is
// Verified.
func (h *envHistory) verifiedIn(bundle string) bool {
	steps := h.byBundle[bundle]
	if len(steps) == 0 {
		return false
	}
	for i := range steps {
		if steps[i].Status.State != "Verified" {
			return false
		}
	}
	return true
}

// inFlight reports whether bundle has a step in the environment that has not
// finished yet.
func (h *envHistory) inFlight(bundle string) bool {
	for i := range h.byBundle[bundle] {
		switch h.byBundle[bundle][i].Status.State {
		case "", "Pending", "Promoting", "WaitingForMerge", "HealthChecking":
			return true
		}
	}
	return false
}

func stepLanded(s *v1alpha1.PromotionStep) bool {
	switch s.Status.State {
	case "HealthChecking", "Verified", "AbortedByAlarm", "RollingBack":
		return true
	case "Failed":
		return s.Status.HealthCheckExpiry != nil
	}
	return false
}

func newerStep(a, b *v1alpha1.PromotionStep) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.After(b.CreationTimestamp.Time)
	}
	return a.Name > b.Name
}

// verifiedAt is when the step became Verified: the Verified condition's
// transition time, else the step's creation time.
func verifiedAt(s *v1alpha1.PromotionStep) time.Time {
	if t, ok := VerifiedTime(s); ok {
		return t
	}
	return s.CreationTimestamp.Time
}
