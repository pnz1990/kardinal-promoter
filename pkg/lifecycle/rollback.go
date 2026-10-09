// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
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
	// Actor is who asked for the rollback. It is recorded in the
	// kardinal.io/requested-by annotation of the rollback Bundle;
	// spec.provenance keeps the target's, so its author is the author of the
	// restored build.
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

// Rejected reports whether b was rejected (kardinal reject, #1451): its
// spec.rejected is set, or its phase is Rejected. A rejected Bundle is never
// promoted again, so rollback and promote never pick it, also when it was
// Verified somewhere before it was rejected.
func Rejected(b *v1alpha1.Bundle) bool {
	return b.Spec.Rejected != nil || b.Status.Phase == "Rejected"
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
//   - A rejected Bundle (Rejected), or any Bundle that carries an image or
//     config commit of a rejected one (RejectedArtifacts), is never the
//     target, and never the source of an image or config commit the rollback
//     restores; ToBundle naming one is refused with ErrInvalid. Rolling back from a rejected Bundle that
//     reached the environment is what rollback is for.
//   - With ToBundle: the Bundle must exist, belong to the pipeline, carry
//     artifacts, differ from what is deployed now, and have been Verified in
//     the environment (every one of its PromotionSteps there is Verified).
//   - Without ToBundle: the target is the most recent Bundle, other than the
//     deployed one, whose PromotionSteps in the environment are all Verified,
//     that deploys what the deployed one's type deploys, carries artifacts,
//     and deploys different artifacts: an image or mixed Bundle when an image
//     Bundle is deployed, a config or mixed Bundle when a config Bundle is,
//     a Bundle of any type when a mixed Bundle is. A deployed image or config
//     Bundle so goes back to the newest earlier images or config commit, also
//     when a mixed Bundle deployed them (B66): the rollback Bundle then has
//     the deployed type and only the mixed target's images, or only its
//     config commit, and the rest stays as deployed. A deployed mixed Bundle
//     goes back to the newest earlier images and config commit, whichever
//     Bundles deployed them: when the target's type cannot carry everything
//     the mixed Bundle changed, the rollback Bundle is mixed, with the
//     target's artifacts and the newest earlier version of the rest. A Bundle
//     with the deployed (failing) images is never
//     chosen, and neither is a Bundle that an earlier rollback in the
//     environment rolled back from (named in the kardinal.io/rollback-from
//     annotation of a rollback Bundle): after V2 was rolled back to V1, rolling
//     back again does not return to V2. ToBundle can still name it.
//   - The rollback Bundle copies the target's images and config ref, and
//     puts back everything else the deployed Bundle changed (#1315): for each
//     image repository the deployed Bundle names and the target does not, the
//     version from the newest Bundle, other than the deployed one and the ones
//     rolled back from, that was Verified in the environment and deploys
//     images (an image or mixed Bundle). The same for the config commit of a
//     deployed config or mixed Bundle when the target has none, from the
//     newest such Bundle that deploys a config commit (a config or mixed
//     Bundle). When no such Bundle exists the rollback is refused, naming the
//     image or config repository, instead of leaving the failing version in
//     place. A target whose type cannot carry what the deployed Bundle changed
//     is refused too: a config Bundle for the images of an image Bundle, an
//     image Bundle for the commit of a config Bundle. A deployed mixed Bundle
//     can go to a config Bundle only when its images are the newest earlier
//     versions, and to an image Bundle only when its config commit is the
//     newest earlier one. Without ToBundle a target whose restored set
//     deploys nothing new is skipped.
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
	if targets := fleetTargets(&p, req.Environment); targets != nil {
		return planFleetRollback(ctx, c, req, targets)
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

	fleet := ""
	if graph.HasFleets(&p) {
		fleet = graph.FleetOf(&p, req.Environment)
	}
	rolledBack, rbErr := rolledBackFrom(ctx, c, req.Namespace, req.Pipeline, req.Environment, fleet)
	if rbErr != nil {
		return nil, fmt.Errorf("rollback: %w", rbErr)
	}
	rejected, rejErr := LoadRejectedArtifacts(ctx, c, req.Namespace, req.Pipeline)
	if rejErr != nil {
		return nil, fmt.Errorf("rollback: %w", rejErr)
	}
	src := &restoreSources{
		c: c, ns: req.Namespace, pipeline: req.Pipeline, env: req.Environment,
		verified: h.verifiedNewestFirst(), deployed: plan.CurrentName, rolledBack: rolledBack,
		rejected: rejected, cache: map[string]*v1alpha1.Bundle{},
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
		case Rejected(target):
			return nil, fmt.Errorf("rollback: bundle %s was rejected, so it is never promoted again; pick another Bundle: %w",
				target.Name, ErrInvalid)
		case rejectedArtifact(rejected, target) != "":
			return nil, fmt.Errorf("rollback: bundle %s deploys an artifact of the rejected bundle %s, so it is never promoted again; pick another Bundle: %w",
				target.Name, rejectedArtifact(rejected, target), ErrInvalid)
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
		if plan.Current != nil {
			same, sameErr := src.sameDeployed(ctx, r, plan.Current)
			if sameErr != nil {
				return nil, fmt.Errorf("rollback: %w", sameErr)
			}
			if same {
				return nil, fmt.Errorf("rollback: rolling back to bundle %s restores the same artifacts as the deployed bundle %s: %w",
					target.Name, plan.CurrentName, ErrConflict)
			}
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
			if cand.Spec.Pipeline != req.Pipeline || !HasArtifacts(cand) || rejectedArtifact(rejected, cand) != "" {
				continue
			}
			if plan.Current != nil && SameArtifacts(cand, plan.Current) {
				continue
			}
			mixed := plan.Current != nil && plan.Current.Spec.Type == "mixed"
			from := cand
			if plan.Current != nil && !mixed && cand.Spec.Type != plan.Current.Spec.Type {
				// A deployed image or config Bundle goes back to the newest
				// earlier images or config commit, which a mixed Bundle may
				// have deployed: the rollback takes only that part of it.
				if from = onlyTypeOf(cand, plan.Current); from == nil {
					continue
				}
			}
			r, restoreErr := src.restore(ctx, plan.Current, from)
			if mixed && errors.Is(restoreErr, ErrInvalid) {
				// cand's type cannot carry everything the mixed Bundle changed
				// (an image Bundle its config commit, a config Bundle its
				// images; restore refuses a target with ErrInvalid only for
				// its type): the rollback is a mixed Bundle with cand's
				// artifacts and the newest earlier version of the rest.
				r, restoreErr = src.restore(ctx, plan.Current, asMixed(cand))
			}
			// Any other refusal does not depend on the candidate: an image or
			// config commit missing from the history is missing for every one.
			if restoreErr != nil {
				return nil, fmt.Errorf("rollback: %w", restoreErr)
			}
			if plan.Current != nil {
				same, sameErr := src.sameDeployed(ctx, r, plan.Current)
				if sameErr != nil {
					return nil, fmt.Errorf("rollback: %w", sameErr)
				}
				if same {
					continue
				}
			}
			plan.Target, restored = cand, r
			break
		}
		if plan.Target == nil {
			// The --to hint is for a person; an automatic rollback reports
			// the refusal in a status condition or Event.
			hint := "; pick one with --to"
			if req.Automatic {
				hint = ""
			}
			return nil, fmt.Errorf("rollback: no earlier Bundle with artifacts, not already rolled back from, was Verified in %s (deployed now: %s)%s: %w",
				req.Environment, plan.CurrentName, hint, ErrConflict)
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
	rejected          *RejectedArtifacts
	cache             map[string]*v1alpha1.Bundle
}

// rejectedArtifact names the rejected Bundle whose artifact b carries, or ""
// (RejectedArtifacts.Carries).
func rejectedArtifact(r *RejectedArtifacts, b *v1alpha1.Bundle) string {
	name, ok := r.Carries(b)
	if !ok {
		return ""
	}
	return name
}

// asMixed returns a copy of b of type mixed, so that a rollback to it deploys
// a config commit as well as images.
func asMixed(b *v1alpha1.Bundle) *v1alpha1.Bundle {
	m := b.DeepCopy()
	m.Spec.Type = "mixed"
	return m
}

// onlyTypeOf returns a copy of the mixed Bundle b with only what the deployed
// image or config Bundle cur deploys, and cur's type: b's images for an image
// Bundle, b's config commit for a config Bundle. A rollback of cur to it
// leaves the rest as deployed. It returns nil when b is not mixed, cur is
// neither an image nor a config Bundle, or b carries none of it.
func onlyTypeOf(b, cur *v1alpha1.Bundle) *v1alpha1.Bundle {
	if b.Spec.Type != "mixed" {
		return nil
	}
	n := b.DeepCopy()
	n.Spec.Type = cur.Spec.Type
	switch cur.Spec.Type {
	case "image":
		n.Spec.ConfigRef = nil
	case "config":
		n.Spec.Images = nil
	default:
		return nil
	}
	if !HasArtifacts(n) {
		return nil
	}
	return n
}

// deploysConfig reports whether promoting b deploys its config commit: config
// Bundles do, and so do mixed Bundles, before their images (the config-merge
// step, steps.DefaultSequenceForBundle).
func deploysConfig(b *v1alpha1.Bundle) bool {
	return b.Spec.Type == "config" || b.Spec.Type == "mixed"
}

// deploysImages reports whether promoting b deploys its images: every Bundle
// but a config or chart Bundle does.
func deploysImages(b *v1alpha1.Bundle) bool {
	return b.Spec.Type != "config" && b.Spec.Type != "chart"
}

// sameDeployed reports whether deploying the rollback artifacts r changes
// nothing in the environment, where cur is deployed: each image r deploys,
// and r's config commit when r deploys one, is what the environment runs.
// What r does not deploy stays as it is, so it is not compared. The
// environment runs what cur deployed and, for what cur did not deploy (an
// image repository cur does not name, the config commit under an image
// Bundle), the newest earlier version in the history, the one restore fills
// from. When the history has none, the version is not known and counts as a
// change.
func (s *restoreSources) sameDeployed(ctx context.Context, r, cur *v1alpha1.Bundle) (bool, error) {
	if r.Spec.Type == "chart" {
		// A chart Bundle deploys only its chart version; cur is the
		// deployed chart Bundle (a rollback stays within its type).
		return cur.Spec.Type == "chart" && chartKey(r) == chartKey(cur), nil
	}
	if deploysImages(r) {
		for _, img := range r.Spec.Images {
			at, err := s.deployedImage(ctx, cur, img.Repository)
			if err != nil {
				return false, err
			}
			if at == nil || imageKey(*at) != imageKey(img) {
				return false, nil
			}
		}
	}
	if !deploysConfig(r) {
		return true, nil
	}
	at, err := s.deployedConfig(ctx, cur)
	if err != nil {
		return false, err
	}
	return at != nil && configKey(at) == configKey(r), nil
}

// deployedImage returns the image of repo the environment runs with cur
// deployed: cur's when cur deploys an image of repo, otherwise the newest
// earlier version in the history, or nil when there is none.
func (s *restoreSources) deployedImage(ctx context.Context, cur *v1alpha1.Bundle, repo string) (*v1alpha1.ImageRef, error) {
	from := cur
	if !deploysImages(cur) || !hasRepository(cur.Spec.Images, repo) {
		var err error
		if from, err = s.newestImage(ctx, repo); err != nil || from == nil {
			return nil, err
		}
	}
	for i := range from.Spec.Images {
		if from.Spec.Images[i].Repository == repo {
			return &from.Spec.Images[i], nil
		}
	}
	return nil, nil
}

// deployedConfig returns the Bundle whose config commit the environment runs
// with cur deployed: cur when cur deploys one, otherwise the newest earlier
// one in the history, or nil when there is none.
func (s *restoreSources) deployedConfig(ctx context.Context, cur *v1alpha1.Bundle) (*v1alpha1.Bundle, error) {
	if deploysConfig(cur) && cur.Spec.ConfigRef != nil && cur.Spec.ConfigRef.CommitSHA != "" {
		return cur, nil
	}
	return s.newestConfig(ctx)
}

// restore returns the artifacts of the rollback of cur to target: target's
// images and config ref, plus, from the history, the version of every image
// repository cur deployed that target does not name, and, when cur deployed a
// config commit (a config or mixed Bundle) and target has none, the newest
// earlier config commit.
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
		if err := s.restoreConfig(ctx, cur, target, out); err != nil {
			return nil, err
		}
	}
	if deploysImages(cur) {
		if err := s.restoreImages(ctx, cur, target, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// restoreConfig puts back the config commit cur deployed: out keeps target's
// commit or, when target has none, gets the newest earlier one. An image
// target deploys no config commit, so it is refused unless cur is a mixed
// Bundle whose commit is the newest earlier one, i.e. cur changed only images.
func (s *restoreSources) restoreConfig(ctx context.Context, cur, target, out *v1alpha1.Bundle) error {
	ref := cur.Spec.ConfigRef
	if ref == nil || ref.CommitSHA == "" {
		return nil
	}
	if !deploysConfig(target) {
		why := ""
		if cur.Spec.Type == "mixed" {
			prev, err := s.newestConfig(ctx)
			if err != nil {
				return err
			}
			if prev != nil && configKey(prev) == configKey(cur) {
				return nil
			}
			why = fmt.Sprintf(" to %s, and no earlier config commit was Verified in %s", shortCommit(ref.CommitSHA), s.env)
			if prev != nil {
				why = fmt.Sprintf(" from %s to %s", shortCommit(prev.Spec.ConfigRef.CommitSHA), shortCommit(ref.CommitSHA))
			}
		}
		return fmt.Errorf("bundle %s is an image Bundle and cannot restore the config commit of %s that the deployed %s bundle %s changed%s; pick a config or mixed Bundle with --to: %w",
			target.Name, ref.GitRepo, cur.Spec.Type, cur.Name, why, ErrInvalid)
	}
	if out.Spec.ConfigRef != nil && out.Spec.ConfigRef.CommitSHA != "" {
		return nil
	}
	from, err := s.newestConfig(ctx)
	if err != nil {
		return err
	}
	if from == nil {
		return fmt.Errorf("no Bundle other than %s with a config commit of %s was Verified in %s, so a rollback to %s would leave the deployed commit in place: %w",
			cur.Name, ref.GitRepo, s.env, target.Name, ErrConflict)
	}
	fromRef := *from.Spec.ConfigRef
	out.Spec.ConfigRef = &fromRef
	return nil
}

// restoreImages puts back the images cur deployed: out keeps target's images
// and gets, for each repository of cur that target does not name, the version
// of the newest earlier Bundle that deployed it. A config target deploys no
// images, so it is refused unless cur is a mixed Bundle whose images are all
// the newest earlier versions, i.e. cur changed only its config commit.
func (s *restoreSources) restoreImages(ctx context.Context, cur, target, out *v1alpha1.Bundle) error {
	if len(cur.Spec.Images) == 0 {
		return nil
	}
	if !deploysImages(target) {
		changed := cur.Spec.Images
		if cur.Spec.Type == "mixed" {
			var err error
			if changed, err = s.changedImages(ctx, cur); err != nil {
				return err
			}
			if len(changed) == 0 {
				return nil
			}
		}
		return fmt.Errorf("bundle %s is a config Bundle and cannot restore the images (%s) that the deployed %s bundle %s changed; pick an image or mixed Bundle with --to: %w",
			target.Name, repositories(changed), cur.Spec.Type, cur.Name, ErrInvalid)
	}
	for _, img := range cur.Spec.Images {
		if hasRepository(out.Spec.Images, img.Repository) {
			continue
		}
		from, err := s.newestImage(ctx, img.Repository)
		if err != nil {
			return err
		}
		if from == nil {
			return fmt.Errorf("no Bundle other than %s with image %s was Verified in %s, so a rollback to %s would leave %s at the deployed version; roll back with a Bundle that names it: %w",
				cur.Name, img.Repository, s.env, target.Name, img.Repository, ErrConflict)
		}
		for _, fromImg := range from.Spec.Images {
			if fromImg.Repository == img.Repository {
				out.Spec.Images = append(out.Spec.Images, fromImg)
			}
		}
	}
	return nil
}

// changedImages returns the images of cur that differ from the newest earlier
// version of their repository, or have none.
func (s *restoreSources) changedImages(ctx context.Context, cur *v1alpha1.Bundle) ([]v1alpha1.ImageRef, error) {
	var changed []v1alpha1.ImageRef
	for _, img := range cur.Spec.Images {
		prev, err := s.newestImage(ctx, img.Repository)
		if err != nil {
			return nil, err
		}
		if prev == nil || !slices.Contains(prev.Spec.Images, img) {
			changed = append(changed, img)
		}
	}
	return changed, nil
}

// newestConfig returns the newest history Bundle that deployed a config
// commit, or nil.
func (s *restoreSources) newestConfig(ctx context.Context) (*v1alpha1.Bundle, error) {
	return s.newest(ctx, func(b *v1alpha1.Bundle) bool {
		return deploysConfig(b) && b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != ""
	})
}

// newestImage returns the newest history Bundle that deployed an image of
// repo, or nil.
func (s *restoreSources) newestImage(ctx context.Context, repo string) (*v1alpha1.Bundle, error) {
	return s.newest(ctx, func(b *v1alpha1.Bundle) bool {
		return deploysImages(b) && hasRepository(b.Spec.Images, repo)
	})
}

// shortCommit is the first 7 characters of a commit SHA, for messages.
func shortCommit(sha string) string {
	return sha[:min(7, len(sha))]
}

// newest returns the newest history Bundle that match accepts, or nil.
// Bundles pruned by historyLimit, rejected Bundles and Bundles of another
// pipeline are skipped.
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
		if b != nil && b.Spec.Pipeline == s.pipeline && rejectedArtifact(s.rejected, b) == "" && match(b) {
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

// fleetTargets returns the target environments of env when env is a fleet
// environment of p, else nil.
func fleetTargets(p *v1alpha1.Pipeline, env string) []string {
	for _, e := range p.Spec.Environments {
		if e.Name == env && e.Fleet != nil {
			byFleet, _ := graph.FleetTargetEnvironments(p)
			if t := byFleet[env]; len(t) > 0 {
				return t
			}
			return []string{}
		}
	}
	return nil
}

// planFleetRollback plans the rollback of a whole fleet (kardinal rollback
// --env <fleet>): the rollback of the fleet's first target that has
// something deployed, by PlanRollback's rules, with intent.targetEnvironment
// the fleet, so its Graph promotes the restored artifacts to every target.
// Targets that already run them commit nothing and are Verified.
func planFleetRollback(ctx context.Context, c client.Reader, req RollbackRequest, targets []string) (*RollbackPlan, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("rollback: fleet environment %s has no targets: %w", req.Environment, ErrInvalid)
	}
	for _, t := range targets {
		if req.FromBundle == "" {
			h, err := loadEnvHistory(ctx, c, req.Namespace, req.Pipeline, t)
			if err != nil {
				return nil, fmt.Errorf("rollback of fleet %s: %w", req.Environment, err)
			}
			if h.deployed() == "" {
				continue // this target never ran anything: look at the next one
			}
		}
		sub := req
		sub.Environment = t
		plan, err := PlanRollback(ctx, c, sub)
		if err != nil {
			return nil, fmt.Errorf("rollback of fleet %s (planned from target %s): %w", req.Environment, t, err)
		}
		plan.Bundle.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: req.Environment}
		return plan, nil
	}
	return nil, fmt.Errorf("rollback: nothing has been deployed to any target of fleet %s in pipeline %s yet: %w",
		req.Environment, req.Pipeline, ErrConflict)
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
	ann := map[string]string{}
	if plan.CurrentName != "" {
		ann[AnnotationRollbackFrom] = plan.CurrentName
	}
	if req.Actor != "" {
		ann[AnnotationRequestedBy] = req.Actor
	}
	if len(ann) > 0 {
		b.Annotations = ann
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
	prov.RollbackOf = plan.Target.Name
	b.Spec.Provenance = prov
	return b
}

// rolledBackFrom returns the Bundles that a rollback in the environment has
// rolled back from: the kardinal.io/rollback-from annotation of every rollback
// Bundle of the pipeline that targets env, or fleet (the fleet env is a
// target of; "" when none). Rollback Bundles created before the
// annotation existed are not counted.
func rolledBackFrom(ctx context.Context, c client.Reader, ns, pipeline, env, fleet string) (map[string]bool, error) {
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
		// A rollback of env, or of the whole fleet env is a target of.
		if t := b.Spec.Intent; t != nil && t.TargetEnvironment != "" && t.TargetEnvironment != env &&
			(fleet == "" || t.TargetEnvironment != fleet) {
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
	// Retired Bundles (#1492) keep their steps in status.retiredSteps.
	steps, err := ListPromotionSteps(ctx, c, ns, client.MatchingLabels{LabelPipeline: pipeline})
	if err != nil {
		return nil, fmt.Errorf("steps of pipeline %s: %w", pipeline, err)
	}
	return historyOf(steps, pipeline, env), nil
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
		case "", "Pending", "Promoting", "WaitingForMerge", "HealthChecking", "Verifying":
			return true
		}
	}
	return false
}

func stepLanded(s *v1alpha1.PromotionStep) bool {
	switch s.Status.State {
	case "HealthChecking", "Verifying", "Verified", "AbortedByAlarm", "RollingBack":
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
