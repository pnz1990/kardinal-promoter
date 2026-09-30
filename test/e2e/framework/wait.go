// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Poll is the interval every wait in the harness uses.
const Poll = 2 * time.Second

// Eventually calls check every Poll until it returns true or timeout passes.
// check returns a description of what it saw; on timeout the test fails with
// the last one, so the failure says what state the object was stuck in.
func Eventually(t *testing.T, timeout time.Duration, what string, check func(ctx context.Context) (bool, string)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	last := "never checked"
	for {
		ok, seen := check(ctx)
		if seen != "" {
			last = seen
		}
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out after %s waiting for %s; last seen: %s", timeout, what, last)
		case <-time.After(Poll):
		}
	}
}

// Consistently fails the test if check returns false at any poll during d.
// Use it for "must not happen" assertions (a gate keeps blocking, no PR opens).
func Consistently(t *testing.T, d time.Duration, what string, check func(ctx context.Context) (bool, string)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for {
		if ok, seen := check(ctx); !ok {
			t.Fatalf("%s stopped holding: %s", what, seen)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(Poll):
		}
	}
}

// Step returns the Bundle's PromotionStep for env, found by the labels the
// Graph builder sets (the name is hash-suffixed when long). ok is false when
// the step does not exist yet.
func (e *Env) Step(ctx context.Context, ns, pipeline, bundle, env string) (*v1alpha1.PromotionStep, bool, error) {
	var list v1alpha1.PromotionStepList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{
		"kardinal.io/pipeline":    pipeline,
		"kardinal.io/bundle":      bundle,
		"kardinal.io/environment": env,
	}); err != nil {
		return nil, false, err
	}
	switch len(list.Items) {
	case 0:
		return nil, false, nil
	case 1:
		return &list.Items[0], true, nil
	default:
		return nil, false, fmt.Errorf("%d PromotionSteps for %s/%s/%s", len(list.Items), pipeline, bundle, env)
	}
}

// WaitStepState waits until the Bundle's PromotionStep for env reaches state.
// It fails early when the step reaches a different terminal state.
func (e *Env) WaitStepState(t *testing.T, ns, pipeline, bundle, env, state string, timeout time.Duration) *v1alpha1.PromotionStep {
	t.Helper()
	var got *v1alpha1.PromotionStep
	what := fmt.Sprintf("step %s/%s/%s to be %s", pipeline, bundle, env, state)
	Eventually(t, timeout, what, func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil {
			return false, err.Error()
		}
		if !ok {
			return false, "no PromotionStep yet"
		}
		got = ps
		s := ps.Status.State
		if s != state && isTerminalStep(s) {
			t.Fatalf("step %s reached %s, want %s: %s", ps.Name, s, state, ps.Status.Message)
		}
		return s == state, fmt.Sprintf("state=%q message=%q", s, ps.Status.Message)
	})
	return got
}

// WaitBundlePhase waits until the Bundle reaches phase.
func (e *Env) WaitBundlePhase(t *testing.T, ns, bundle, phase string, timeout time.Duration) *v1alpha1.Bundle {
	t.Helper()
	var b v1alpha1.Bundle
	key := types.NamespacedName{Namespace: ns, Name: bundle}
	Eventually(t, timeout, fmt.Sprintf("bundle %s to be %s", bundle, phase), func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, key, &b); err != nil {
			return false, err.Error()
		}
		return b.Status.Phase == phase, fmt.Sprintf("phase=%q", b.Status.Phase)
	})
	return &b
}

func isTerminalStep(state string) bool {
	switch state {
	case "Verified", "Failed", "AbortedByAlarm":
		return true
	}
	return false
}
