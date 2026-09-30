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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// PromoteRequest asks to promote the newest Bundle verified upstream of an
// environment into that environment.
type PromoteRequest struct {
	Namespace   string
	Pipeline    string
	Environment string
	// Actor is recorded in the kardinal.io/requested-by annotation.
	Actor string
	// Now stamps kardinal.io/created-at when not zero.
	Now time.Time
}

// PromotePlan is the result of PlanPromote.
type PromotePlan struct {
	// Source is the Bundle whose artifacts are promoted.
	Source *v1alpha1.Bundle
	// Upstreams are the environments Source is Verified in.
	Upstreams []string
	// Bundle is the Bundle to create.
	Bundle *v1alpha1.Bundle
}

// PlanPromote finds the newest Bundle of the pipeline that is Verified in every
// direct upstream of the environment and builds a Bundle that copies its
// images, config ref and provenance with intent.targetEnvironment set to the
// environment. It never builds a Bundle without artifacts.
//
// Errors wrap ErrNotFound (no pipeline), ErrInvalid (unknown environment, or
// an environment with no upstream) or ErrConflict (nothing verified upstream,
// the newest verified Bundle is already Verified or in flight in the
// environment, or the new Bundle would supersede one still promoting).
func PlanPromote(ctx context.Context, c client.Reader, req PromoteRequest) (*PromotePlan, error) {
	if req.Pipeline == "" || req.Environment == "" {
		return nil, fmt.Errorf("promote: pipeline and environment are required: %w", ErrInvalid)
	}
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Pipeline}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("promote: pipeline %s/%s: %w", req.Namespace, req.Pipeline, ErrNotFound)
		}
		return nil, fmt.Errorf("promote: get pipeline %s/%s: %w", req.Namespace, req.Pipeline, err)
	}
	if !hasEnvironment(&p, req.Environment) {
		return nil, fmt.Errorf("promote: pipeline %s has no environment %q: %w", req.Pipeline, req.Environment, ErrInvalid)
	}
	ups, err := graph.EnvironmentUpstreams(&p, req.Environment)
	if err != nil {
		return nil, fmt.Errorf("promote: %w: %w", err, ErrInvalid)
	}
	if len(ups) == 0 {
		return nil, fmt.Errorf("promote: %s is the first environment of pipeline %s, so there is nothing upstream to promote; create a Bundle with `kardinal create bundle`: %w",
			req.Environment, req.Pipeline, ErrInvalid)
	}

	histories := map[string]*envHistory{}
	for _, env := range append([]string{req.Environment}, ups...) {
		h, loadErr := loadEnvHistory(ctx, c, req.Namespace, req.Pipeline, env)
		if loadErr != nil {
			return nil, fmt.Errorf("promote: %w", loadErr)
		}
		histories[env] = h
	}

	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles, client.InNamespace(req.Namespace)); err != nil {
		return nil, fmt.Errorf("promote: list bundles: %w", err)
	}
	var candidates []*v1alpha1.Bundle
	for i := range bundles.Items {
		b := &bundles.Items[i]
		if b.Spec.Pipeline != req.Pipeline || !HasArtifacts(b) {
			continue
		}
		verified := true
		for _, up := range ups {
			if !histories[up].verifiedIn(b.Name) {
				verified = false
				break
			}
		}
		if verified {
			candidates = append(candidates, b)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("promote: no Bundle with artifacts is Verified in %s yet: %w",
			strings.Join(ups, ", "), ErrConflict)
	}
	slices.SortFunc(candidates, func(a, b *v1alpha1.Bundle) int { return CompareCreation(b, a) })
	src := candidates[0]
	target := histories[req.Environment]
	if target.verifiedIn(src.Name) {
		return nil, fmt.Errorf("promote: bundle %s, the newest Bundle Verified in %s, is already Verified in %s: %w",
			src.Name, strings.Join(ups, ", "), req.Environment, ErrConflict)
	}
	if target.inFlight(src.Name) {
		return nil, fmt.Errorf("promote: bundle %s is already being promoted to %s: %w",
			src.Name, req.Environment, ErrConflict)
	}
	// The new Bundle is the newest of its type, so it supersedes every Bundle
	// still promoting. Refuse rather than cancel one that is newer than the
	// source, or the source itself, which reaches the environment on its own.
	for i := range bundles.Items {
		o := &bundles.Items[i]
		if o.Spec.Pipeline != req.Pipeline || o.Spec.Type != src.Spec.Type || !InFlightPhase(o.Status.Phase) {
			continue
		}
		if o.Name == src.Name || CompareCreation(o, src) > 0 {
			return nil, fmt.Errorf("promote: bundle %s is still promoting and a new Bundle would supersede it; wait for it to finish: %w",
				o.Name, ErrConflict)
		}
	}

	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: req.Pipeline + "-",
			Namespace:    req.Namespace,
			Labels:       map[string]string{LabelPipeline: req.Pipeline},
			Annotations:  map[string]string{AnnotationPromotedFrom: src.Name},
		},
		Spec: v1alpha1.BundleSpec{
			Pipeline: req.Pipeline,
			Intent:   &v1alpha1.BundleIntent{TargetEnvironment: req.Environment},
		},
	}
	if req.Actor != "" {
		b.Annotations[AnnotationRequestedBy] = req.Actor
	}
	StampCreatedAt(b, req.Now)
	copyArtifacts(&b.Spec, src)
	if src.Spec.Provenance != nil {
		prov := *src.Spec.Provenance
		prov.RollbackOf = ""
		b.Spec.Provenance = &prov
	}
	return &PromotePlan{Source: src, Upstreams: ups, Bundle: b}, nil
}
