// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
//   - the PolicyGate reconciler passes the hold's Bundle through every gate
//     on its way, each pass with an EXEMPT reason, an AuditEvent and a
//     Warning Event (GateExemption).
// Writing a hold needs update on the Pipeline, like kardinal pause.

// HoldOf returns the hold of env in p, or nil.
func HoldOf(p *v1alpha1.Pipeline, env string) *v1alpha1.EnvironmentHold {
	if p == nil {
		return nil
	}
	for i := range p.Spec.Holds {
		if p.Spec.Holds[i].Environment == env {
			return &p.Spec.Holds[i]
		}
	}
	return nil
}

// HoldNaming returns the hold of p whose Bundle is bundle, or nil.
func HoldNaming(p *v1alpha1.Pipeline, bundle string) *v1alpha1.EnvironmentHold {
	if p == nil || bundle == "" {
		return nil
	}
	for i := range p.Spec.Holds {
		if p.Spec.Holds[i].Bundle == bundle {
			return &p.Spec.Holds[i]
		}
	}
	return nil
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
		Reason: req.HoldReason, CreatedBy: req.Actor, CreatedAt: &at}
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
			return fmt.Errorf("environment %s is already held on %s (%s); release it first with kardinal release-hold %s --env %s: %w",
				h.Environment, h.Bundle, h.Reason, pipeline, h.Environment, ErrConflict)
		}
		p.Spec.Holds = append(p.Spec.Holds, hold)
		return c.Update(ctx, &p)
	})
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
		h := HoldOf(&p, env)
		if h == nil {
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
