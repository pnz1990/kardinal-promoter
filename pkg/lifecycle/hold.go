// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// An environment hold (Pipeline spec.holds, #1528) pins an environment to a
// rollback Bundle: kardinal rollback --hold restores the environment and keeps
// it there until kardinal release-hold. The hold is Pipeline spec, so:
//   - the Graph of every other Bundle creates no PromotionStep in the
//     environment (graph.Build reads spec.holds, and a spec change rebuilds
//     every active Bundle's Graph in place);
//   - the PromotionStep reconciler holds the other Bundles' steps that exist
//     there already before their next git step (HeldFrom);
//   - the Bundle reconciler never supersedes the hold's Bundle;
//   - the Bundle reconciler does not garbage-collect it (historyLimit);
//   - the PolicyGate reconciler passes the hold's Bundle through the gates
//     of the held environment, each pass with an EXEMPT reason, an
//     AuditEvent and a Warning Event (GateExemption), but only while
//     VerifyHeldRollback holds: the Bundle restores artifacts that were
//     Verified in the environment and still has the artifacts it had when
//     the hold was made;
//   - the Pipeline reconciler removes a hold at its expiresAt and writes the
//     HoldCreated and HoldReleased AuditEvents for every client alike.
// Writing a hold needs update on the virtual subresource pipelines/hold
// (the chart's hold-writes admission policy), which also pins createdBy to
// the requesting user.

// HoldNow is the clock HoldOf and HoldNaming compare expiresAt with. Tests
// replace it.
var HoldNow = time.Now

// HoldOf returns the hold of env in p, or nil. A hold whose expiresAt has
// passed counts as absent, also before the Pipeline reconciler removes it.
func HoldOf(p *v1alpha1.Pipeline, env string) *v1alpha1.EnvironmentHold {
	if h := exactHold(p, env); h != nil {
		return h
	}
	// A fleet target is held by its own hold, or by its fleet's (kardinal
	// rollback --env <fleet> --hold): the whole fleet stays on the rollback.
	if p != nil && len(p.Spec.Holds) > 0 && graph.HasFleets(p) {
		if fleet := graph.FleetOf(p, env); fleet != "" {
			return exactHold(p, fleet)
		}
	}
	return nil
}

// exactHold is the unexpired hold of p whose environment is env, without the
// fleet fallback of HoldOf.
func exactHold(p *v1alpha1.Pipeline, env string) *v1alpha1.EnvironmentHold {
	if p == nil {
		return nil
	}
	now := HoldNow()
	for i := range p.Spec.Holds {
		if p.Spec.Holds[i].Environment == env && !p.Spec.Holds[i].Expired(now) {
			return &p.Spec.Holds[i]
		}
	}
	return nil
}

// HoldNaming returns the hold of p whose Bundle is bundle, or nil. Expired
// holds count as absent.
func HoldNaming(p *v1alpha1.Pipeline, bundle string) *v1alpha1.EnvironmentHold {
	if p == nil || bundle == "" {
		return nil
	}
	now := HoldNow()
	for i := range p.Spec.Holds {
		if p.Spec.Holds[i].Bundle == bundle && !p.Spec.Holds[i].Expired(now) {
			return &p.Spec.Holds[i]
		}
	}
	return nil
}

// UntilExpiry is how long until h expires, or fallback when that is later
// or h has no expiresAt: the requeue of a reconciler that acts on h.
func UntilExpiry(h *v1alpha1.EnvironmentHold, fallback time.Duration) time.Duration {
	if h == nil || h.ExpiresAt == nil {
		return fallback
	}
	if d := h.ExpiresAt.Sub(HoldNow()); d < fallback {
		if d < time.Second {
			return time.Second
		}
		return d
	}
	return fallback
}

// HeldFrom returns the hold that keeps bundle out of env, or nil: env is held
// on another Bundle.
func HeldFrom(p *v1alpha1.Pipeline, env, bundle string) *v1alpha1.EnvironmentHold {
	if h := HoldOf(p, env); h != nil && h.Bundle != bundle {
		return h
	}
	return nil
}

// HeldMessage is what a step held by h says.
func HeldMessage(pipeline string, h *v1alpha1.EnvironmentHold) string {
	return fmt.Sprintf("environment %s is held on rollback %s (%s) — release with: kardinal release-hold %s --env %s",
		h.Environment, h.Bundle, h.Reason, pipeline, h.Environment)
}

// GateExemption is the reason a gate of the hold's Bundle passes when its
// expression does not: explicit, with who held the environment and why, and
// the gate's own result after it.
func GateExemption(h *v1alpha1.EnvironmentHold, gateReason string) string {
	by := h.CreatedBy
	if by == "" {
		by = "unknown"
	}
	return fmt.Sprintf("EXEMPT: rollback %s holds %s (by %s: %s); without the hold: %s",
		h.Bundle, h.Environment, by, h.Reason, gateReason)
}

// IsHoldExemption reports whether a gate reason is a GateExemption.
func IsHoldExemption(reason string) bool { return strings.HasPrefix(reason, "EXEMPT: rollback ") }

// HoldRequest is a rollback with a hold.
type HoldRequest struct {
	RollbackRequest
	// Reason is why the environment is held (required).
	HoldReason string
	// ExpiresIn, when positive, ends the hold that long after it is made.
	ExpiresIn time.Duration
}

// ArtifactDigest is the digest of what a Bundle deploys: its type, images
// (repository, tag, digest), configRef and chart, encoded as JSON and hashed
// with SHA-256. A hold records it, and the gate exemption applies only while
// the held Bundle still has the same digest.
func ArtifactDigest(spec v1alpha1.BundleSpec) string {
	raw, err := json.Marshal(struct {
		Type      string              `json:"type"`
		Images    []v1alpha1.ImageRef `json:"images"`
		ConfigRef *v1alpha1.ConfigRef `json:"configRef"`
		Chart     *v1alpha1.ChartRef  `json:"chart"`
	}{spec.Type, spec.Images, spec.ConfigRef, spec.Chart})
	if err != nil { // the fields are plain strings: Marshal cannot fail
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// VerifyHeldRollback decides whether the Bundle h names may pass the gates
// of h.Environment. It returns the Bundle, or "" and why not. The Bundle
// must be a rollback Bundle (label kardinal.io/rollback=true) of the
// Pipeline whose spec.provenance.rollbackOf names a Bundle Verified in the
// environment; each of its images must be one that a Bundle Verified in
// the environment deployed, and so must its configRef and chart; and its artifacts
// must still have the digest h recorded. A Bundle that someone edited, or
// that restores anything not already Verified there, is not exempt: it goes
// through the gates like any Bundle.
func VerifyHeldRollback(ctx context.Context, c client.Reader, p *v1alpha1.Pipeline, h *v1alpha1.EnvironmentHold) (*v1alpha1.Bundle, string) {
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: h.Bundle}, &b); err != nil {
		return nil, fmt.Sprintf("bundle %s: %v", h.Bundle, err)
	}
	if b.Labels[LabelRollback] != "true" || b.Spec.Pipeline != p.Name {
		return nil, fmt.Sprintf("bundle %s is not a rollback Bundle of pipeline %s", h.Bundle, p.Name)
	}
	if h.Artifacts == "" {
		return nil, "the hold records no artifact digest (holds made with kardinal rollback --hold or the UI do)"
	}
	if got := ArtifactDigest(b.Spec); got != h.Artifacts {
		return nil, fmt.Sprintf("bundle %s no longer has the artifacts it had when the hold was made", h.Bundle)
	}
	if b.Spec.Provenance == nil || b.Spec.Provenance.RollbackOf == "" {
		return nil, fmt.Sprintf("bundle %s names no rollbackOf", h.Bundle)
	}
	hist, err := loadEnvHistory(ctx, c, p.Namespace, p.Name, h.Environment)
	if err != nil {
		return nil, err.Error()
	}
	target := b.Spec.Provenance.RollbackOf
	if !hist.verifiedIn(target) {
		return nil, fmt.Sprintf("rollbackOf %s was never Verified in %s", target, h.Environment)
	}
	var t v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: target}, &t); err != nil {
		return nil, fmt.Sprintf("rollbackOf %s: %v", target, err)
	}
	// Per repository: every image repository, config repository and chart
	// rollbackOf deploys, the held Bundle deploys at rollbackOf's ref. Only
	// what rollbackOf does not name may come from other Verified Bundles
	// (PlanRollback fills those), so no combination is restored that the
	// rollback target did not run with.
	if why := sameRefsAs(&b, &t); why != "" {
		return nil, why
	}
	images := map[v1alpha1.ImageRef]bool{}
	configs := map[v1alpha1.ConfigRef]bool{}
	charts := map[v1alpha1.ChartRef]bool{}
	for _, name := range hist.verifiedNewestFirst() {
		if name == b.Name {
			continue
		}
		var v v1alpha1.Bundle
		if err := c.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: name}, &v); err != nil {
			continue
		}
		for _, img := range v.Spec.Images {
			images[img] = true
		}
		if v.Spec.ConfigRef != nil {
			configs[v1alpha1.ConfigRef{GitRepo: v.Spec.ConfigRef.GitRepo, CommitSHA: v.Spec.ConfigRef.CommitSHA}] = true
		}
		if v.Spec.Chart != nil {
			charts[*v.Spec.Chart] = true
		}
	}
	for _, img := range b.Spec.Images {
		if !images[img] {
			return nil, fmt.Sprintf("image %s was not deployed by a Bundle Verified in %s", imageString(img), h.Environment)
		}
	}
	if cr := b.Spec.ConfigRef; cr != nil && !configs[v1alpha1.ConfigRef{GitRepo: cr.GitRepo, CommitSHA: cr.CommitSHA}] {
		return nil, fmt.Sprintf("config commit %s was not deployed by a Bundle Verified in %s", cr.CommitSHA, h.Environment)
	}
	if ch := b.Spec.Chart; ch != nil && !charts[*ch] {
		return nil, fmt.Sprintf("chart %s %s was not deployed by a Bundle Verified in %s", ch.Name, ch.Version, h.Environment)
	}
	return &b, ""
}

// sameRefsAs says why b does not deploy target's ref of every repository
// target names, or "".
func sameRefsAs(b, target *v1alpha1.Bundle) string {
	held := map[string]v1alpha1.ImageRef{}
	for _, img := range b.Spec.Images {
		held[img.Repository] = img
	}
	for _, img := range target.Spec.Images {
		got, ok := held[img.Repository]
		if !ok || got != img {
			return fmt.Sprintf("image %s is not at rollbackOf %s's ref %s", img.Repository, target.Name, imageString(img))
		}
	}
	if tc := target.Spec.ConfigRef; tc != nil && tc.CommitSHA != "" {
		bc := b.Spec.ConfigRef
		if bc == nil || bc.GitRepo != tc.GitRepo || bc.CommitSHA != tc.CommitSHA {
			return fmt.Sprintf("config %s is not at rollbackOf %s's commit %s", tc.GitRepo, target.Name, tc.CommitSHA)
		}
	}
	if tch := target.Spec.Chart; tch != nil {
		bch := b.Spec.Chart
		if bch == nil || *bch != *tch {
			return fmt.Sprintf("chart %s is not at rollbackOf %s's version %s", tch.Name, target.Name, tch.Version)
		}
	}
	return ""
}

func imageString(img v1alpha1.ImageRef) string {
	s := img.Repository
	if img.Tag != "" {
		s += ":" + img.Tag
	}
	if img.Digest != "" {
		s += "@" + img.Digest
	}
	return s
}

// RollbackAndHold rolls req.Environment back (PlanRollback) and holds it on
// the rollback Bundle. The hold is written first, naming the Bundle about to
// be created, so the Bundle's gates are exempt from its first evaluation and
// no other Bundle slips into the environment in between. When the Bundle
// cannot be created the hold is removed again. An environment that is held
// already is refused (ErrConflict): release it first.
func RollbackAndHold(ctx context.Context, c client.Client, req HoldRequest) (*RollbackPlan, *v1alpha1.EnvironmentHold, error) {
	if strings.TrimSpace(req.HoldReason) == "" {
		return nil, nil, fmt.Errorf("a hold needs a reason: %w", ErrInvalid)
	}
	if req.Name == "" {
		suffix := make([]byte, 3)
		if _, err := rand.Read(suffix); err != nil {
			return nil, nil, fmt.Errorf("name the rollback bundle: %w", err)
		}
		req.Name = req.Pipeline + "-rollback-" + hex.EncodeToString(suffix)
	}
	// Refuse a held environment before planning: what is deployed there is
	// the held rollback, so the plan would fail for a less useful reason.
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Pipeline}, &p); err == nil {
		if h := HoldOf(&p, req.Environment); h != nil {
			return nil, nil, heldConflict(req.Pipeline, h)
		}
	}
	plan, err := PlanRollback(ctx, c, req.RollbackRequest)
	if err != nil {
		return nil, nil, err
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	at := metav1.NewTime(now.UTC())
	hold := v1alpha1.EnvironmentHold{Environment: req.Environment, Bundle: plan.Bundle.Name,
		Reason: req.HoldReason, CreatedBy: req.Actor, CreatedAt: &at, Artifacts: ArtifactDigest(plan.Bundle.Spec)}
	if req.ExpiresIn > 0 {
		exp := metav1.NewTime(now.UTC().Add(req.ExpiresIn))
		hold.ExpiresAt = &exp
	}
	if err := setHold(ctx, c, req.Namespace, req.Pipeline, hold); err != nil {
		return nil, nil, err
	}
	if err := c.Create(ctx, plan.Bundle); err != nil {
		if _, relErr := ReleaseHold(ctx, c, req.Namespace, req.Pipeline, req.Environment); relErr != nil {
			return nil, nil, fmt.Errorf("create rollback bundle: %w (and removing the hold failed: %v)", err, relErr)
		}
		return nil, nil, fmt.Errorf("create rollback bundle: %w", err)
	}
	return plan, &hold, nil
}

// setHold adds hold to the Pipeline. A hold of the same environment is a
// conflict.
func setHold(ctx context.Context, c client.Client, ns, pipeline string, hold v1alpha1.EnvironmentHold) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var p v1alpha1.Pipeline
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipeline}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("pipeline %s/%s: %w", ns, pipeline, ErrNotFound)
			}
			return fmt.Errorf("get pipeline %s/%s: %w", ns, pipeline, err)
		}
		if h := HoldOf(&p, hold.Environment); h != nil {
			return heldConflict(pipeline, h)
		}
		// An expired entry of the environment the controller has not
		// removed yet makes way (one entry per environment).
		kept := make([]v1alpha1.EnvironmentHold, 0, len(p.Spec.Holds)+1)
		for _, x := range p.Spec.Holds {
			if x.Environment != hold.Environment {
				kept = append(kept, x)
			}
		}
		kept = append(kept, hold)
		p.Spec.Holds = kept
		return c.Update(ctx, &p)
	})
}

// heldConflict is the refusal to hold an environment that is held already.
func heldConflict(pipeline string, h *v1alpha1.EnvironmentHold) error {
	return fmt.Errorf("environment %s is already held on %s (%s); release it first with kardinal release-hold %s --env %s: %w",
		h.Environment, h.Bundle, h.Reason, pipeline, h.Environment, ErrConflict)
}

// ReleaseHold removes the hold of env from the Pipeline and returns it. A
// Pipeline whose environment is not held is ErrNotFound.
func ReleaseHold(ctx context.Context, c client.Client, ns, pipeline, env string) (*v1alpha1.EnvironmentHold, error) {
	var released *v1alpha1.EnvironmentHold
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var p v1alpha1.Pipeline
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipeline}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("pipeline %s/%s: %w", ns, pipeline, ErrNotFound)
			}
			return fmt.Errorf("get pipeline %s/%s: %w", ns, pipeline, err)
		}
		h := exactHold(&p, env)
		if h == nil {
			if fh := HoldOf(&p, env); fh != nil {
				return fmt.Errorf("environment %s of pipeline %s is held through its fleet %s; release the fleet with --env %s: %w",
					env, pipeline, fh.Environment, fh.Environment, ErrNotFound)
			}
			return fmt.Errorf("environment %s of pipeline %s is not held: %w", env, pipeline, ErrNotFound)
		}
		cp := *h
		released = &cp
		kept := p.Spec.Holds[:0]
		for _, x := range p.Spec.Holds {
			if x.Environment != env {
				kept = append(kept, x)
			}
		}
		p.Spec.Holds = kept
		return c.Update(ctx, &p)
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) {
		return nil, fmt.Errorf("release hold of %s/%s %s: %w", ns, pipeline, env, err)
	}
	return released, err
}
