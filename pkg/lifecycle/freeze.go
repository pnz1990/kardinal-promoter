// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
)

// How freezes work (docs/design/18-actor-policy.md section 9)
//
// A freeze is a cluster-scoped ChangeWindow of type blackout with
// spec.freeze. While it is active the PromotionStep reconciler holds the
// steps it selects (holdIfFrozen): every version (everyone, stop-the-line)
// or the versions an agent created (agents), in every namespace or the ones
// listed. Freeze and Unfreeze (kardinal freeze, the UI) write one window per
// scope, named by FreezeName; ExemptFromFreezes (kardinal override --freeze)
// adds a break-glass exemption to the freezes that hold a pipeline. The
// chart's freezes admission policy admits createdBy only as the requester.

// LabelFreezeWindow marks the ChangeWindows kardinal freeze writes.
const LabelFreezeWindow = "kardinal.io/freeze-window"

// DefaultExemption is how long an exemption lasts when no duration is given.
const DefaultExemption = time.Hour

// FreezeName is the ChangeWindow name of the freeze of actors (agents or
// everyone) in namespace ("" for every namespace).
func FreezeName(actors, namespace string) string {
	name := "kardinal-freeze-" + actors
	if namespace != "" {
		name += "-" + namespace
	}
	return name
}

// FreezeRequest is one kardinal freeze.
type FreezeRequest struct {
	// Actors is v1alpha1.FreezeAgents or v1alpha1.FreezeEveryone.
	Actors string
	// Namespace limits the freeze to one namespace; "" is every namespace.
	Namespace string
	// Reason is shown on every held step.
	Reason string
	// For ends the freeze after it; zero lasts until Unfreeze.
	For time.Duration
	// CreatedBy is who freezes: the authenticated identity.
	CreatedBy string
	// OnlyOwn lets the request change only a freeze CreatedBy made: an
	// existing freeze of the scope that someone else made is ErrForbidden.
	// The UI sets it for a caller whose rights are a namespace's
	// (docs/design/18-actor-policy.md section 9.5).
	OnlyOwn bool
	// Now is the current time.
	Now time.Time
}

// Freeze creates the freeze of req's scope, or updates the reason and end of
// the one that exists (its start and createdBy stay). It returns the window
// and whether it was created. A ChangeWindow of that name without
// spec.freeze is not kardinal's: ErrConflict.
func Freeze(ctx context.Context, c client.Client, req FreezeRequest) (*v1alpha1.ChangeWindow, bool, error) {
	if req.Actors != v1alpha1.FreezeAgents && req.Actors != v1alpha1.FreezeEveryone {
		return nil, false, fmt.Errorf("freeze agents or everyone, not %q: %w", req.Actors, ErrInvalid)
	}
	if req.Reason == "" {
		return nil, false, fmt.Errorf("a freeze needs a reason: %w", ErrInvalid)
	}
	if req.For < 0 {
		return nil, false, fmt.Errorf("a freeze cannot end before it starts: %w", ErrInvalid)
	}
	name := FreezeName(req.Actors, req.Namespace)
	var end metav1.Time
	if req.For > 0 {
		end = metav1.NewTime(req.Now.Add(req.For).UTC().Truncate(time.Second))
	}
	cw := &v1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{LabelFreezeWindow: "true"}},
		Spec: v1alpha1.ChangeWindowSpec{
			Type:   changewindow.TypeBlackout,
			Start:  metav1.NewTime(req.Now.UTC().Truncate(time.Second)),
			End:    end,
			Reason: req.Reason,
			Freeze: &v1alpha1.ChangeWindowFreeze{Actors: req.Actors, CreatedBy: req.CreatedBy},
		},
	}
	if req.Namespace != "" {
		cw.Spec.Freeze.Namespaces = []string{req.Namespace}
	}
	err := c.Create(ctx, cw)
	if err == nil {
		return cw, true, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, false, fmt.Errorf("create freeze %s: %w", name, err)
	}
	var out v1alpha1.ChangeWindow
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, types.NamespacedName{Name: name}, &out); err != nil {
			return err
		}
		if out.Spec.Freeze == nil {
			return fmt.Errorf("ChangeWindow %s exists and is not a freeze; rename or delete it: %w", name, ErrConflict)
		}
		if req.OnlyOwn && out.Spec.Freeze.CreatedBy != req.CreatedBy {
			return notYours(name, out.Spec.Freeze.CreatedBy, "change")
		}
		out.Spec.Reason, out.Spec.End = req.Reason, end
		return c.Update(ctx, &out)
	})
	if err != nil {
		return nil, false, fmt.Errorf("update freeze %s: %w", name, err)
	}
	return &out, false, nil
}

// Unfreeze deletes the freeze named name. A ChangeWindow without
// spec.freeze is never deleted (ErrConflict); a missing one is ErrNotFound.
// With onlyBy set, only a freeze onlyBy made is deleted (ErrForbidden
// otherwise): the UI sets it for a caller whose rights are a namespace's.
func Unfreeze(ctx context.Context, c client.Client, name, onlyBy string) error {
	var cw v1alpha1.ChangeWindow
	if err := c.Get(ctx, types.NamespacedName{Name: name}, &cw); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("no freeze %s: %w", name, ErrNotFound)
		}
		return fmt.Errorf("get freeze %s: %w", name, err)
	}
	if cw.Spec.Freeze == nil {
		return fmt.Errorf("ChangeWindow %s is not a freeze; delete it with kubectl if you mean to: %w", name, ErrConflict)
	}
	if onlyBy != "" && cw.Spec.Freeze.CreatedBy != onlyBy {
		return notYours(name, cw.Spec.Freeze.CreatedBy, "lift")
	}
	if err := c.Delete(ctx, &cw, client.Preconditions{UID: &cw.UID}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete freeze %s: %w", name, err)
	}
	return nil
}

// notYours is the ErrForbidden of a caller with a namespace's rights acting
// on a freeze someone else made.
func notYours(name, by, verb string) error {
	if by == "" {
		by = "(unknown)"
	}
	return fmt.Errorf("freeze %s was made by %s: to %s it you need update on changewindows/freeze cluster-wide "+
		"(the chart's freezer role), not a namespace's: %w", name, by, verb, ErrForbidden)
}

// ListFreezes returns the ChangeWindows with spec.freeze, sorted by name.
func ListFreezes(ctx context.Context, c client.Reader) ([]v1alpha1.ChangeWindow, error) {
	var list v1alpha1.ChangeWindowList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list ChangeWindows: %w", err)
	}
	var out []v1alpha1.ChangeWindow
	for _, cw := range list.Items {
		if cw.Spec.Freeze != nil {
			out = append(out, cw)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ExemptRequest is one kardinal override --freeze.
type ExemptRequest struct {
	Namespace, Pipeline string
	// Environment and Bundle narrow the exemption; "" is any.
	Environment, Bundle string
	Reason              string
	// ExpiresIn is how long it lasts; zero is DefaultExemption.
	ExpiresIn time.Duration
	// CreatedBy is who lets it through: the authenticated identity.
	CreatedBy string
	// Actor is CreatedBy's actor stamp (an agent's exemption never counts).
	Actor *v1alpha1.Actor
	Now   time.Time
}

// ExemptFromFreezes adds req's exemption to every freeze active now that
// selects req's namespace, and returns their names. With none it returns
// ErrNotFound: there is nothing to let through.
func ExemptFromFreezes(ctx context.Context, c client.Client, req ExemptRequest) ([]string, error) {
	if req.Reason == "" {
		return nil, fmt.Errorf("an exemption needs a reason: %w", ErrInvalid)
	}
	if req.Pipeline == "" || req.Namespace == "" {
		return nil, fmt.Errorf("an exemption names a pipeline and its namespace: %w", ErrInvalid)
	}
	d := req.ExpiresIn
	if d == 0 {
		d = DefaultExemption
	}
	if d < 0 {
		return nil, fmt.Errorf("an exemption cannot expire before it starts: %w", ErrInvalid)
	}
	freezes, err := ListFreezes(ctx, c)
	if err != nil {
		return nil, err
	}
	ex := v1alpha1.FreezeExemption{Namespace: req.Namespace, Pipeline: req.Pipeline, Environment: req.Environment,
		Bundle: req.Bundle, Reason: req.Reason, CreatedBy: req.CreatedBy, Actor: req.Actor,
		ExpiresAt: metav1.NewTime(req.Now.Add(d).UTC().Truncate(time.Second))}
	anyone := changewindow.FreezeTarget{Namespace: req.Namespace}
	var names []string
	for i := range freezes {
		f := &freezes[i]
		sel := *f.Spec.Freeze
		sel.Actors = v1alpha1.FreezeEveryone // whichever actor: the exemption is for the pipeline
		if !changewindow.FreezeActive(f) || !changewindow.FreezeSelects(&sel, anyone) {
			continue
		}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var cw v1alpha1.ChangeWindow
			if err := c.Get(ctx, types.NamespacedName{Name: f.Name}, &cw); err != nil {
				return err
			}
			if cw.Spec.Freeze == nil {
				return nil // unfrozen meanwhile
			}
			// Expired exemptions go (removing one is allowed; changing one is
			// not), so the list stays under its limit.
			kept := cw.Spec.Freeze.Exemptions[:0]
			for _, e := range cw.Spec.Freeze.Exemptions {
				if req.Now.Before(e.ExpiresAt.Time) {
					kept = append(kept, e)
				}
			}
			kept = append(kept, ex)
			cw.Spec.Freeze.Exemptions = kept
			return c.Update(ctx, &cw)
		})
		if apierrors.IsNotFound(err) {
			continue // unfrozen meanwhile
		}
		if err != nil {
			return names, fmt.Errorf("add exemption to freeze %s: %w", f.Name, err)
		}
		names = append(names, f.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no active freeze holds namespace %s: %w", req.Namespace, ErrNotFound)
	}
	return names, nil
}
