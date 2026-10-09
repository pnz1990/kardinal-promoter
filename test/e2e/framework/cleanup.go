// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// drainTimeout bounds drain. While the repo exists, the controller closes a
// deleted step's PR in seconds.
const drainTimeout = 2 * time.Minute

// testNamespace is a namespace Namespace created for test t, and what the
// test's cleanups have done to it.
type testNamespace struct {
	t                  *testing.T
	name               string
	diagnosed, drained bool
}

// trackNamespace records that Namespace created name for t.
func (e *Env) trackNamespace(t *testing.T, name string) *testNamespace {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := &testNamespace{t: t, name: name}
	e.namespaces = append(e.namespaces, n)
	return n
}

// diagnoseOnce writes n's diagnostics (Diagnose) if the test has failed and
// no cleanup has written them yet.
func (e *Env) diagnoseOnce(t *testing.T, n *testNamespace) {
	t.Helper()
	if !t.Failed() {
		return
	}
	e.mu.Lock()
	done := n.diagnosed
	n.diagnosed = true
	e.mu.Unlock()
	if !done {
		e.Diagnose(t, n.name)
	}
}

// beforeRepoDelete runs in a repo's cleanup, before the repo is deleted. It
// drains the namespaces Namespace created for t while the repo still exists.
// Deleting a PromotionStep closes its open PR (the kardinal.io/close-pr
// finalizer). Once the repo is gone, the controller's PR lookup gets a 404,
// which it retries for up to five minutes, and the namespace stays
// Terminating all that time. The repo cleanup runs before the namespace's
// (t.Cleanup is last in, first out), so the namespace's delete comes too late.
//
// A failed test's namespaces are diagnosed first, so the diagnostics show
// the promotions as the test left them.
func (e *Env) beforeRepoDelete(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	var mine []*testNamespace
	for _, n := range e.namespaces {
		if n.t == t {
			mine = append(mine, n)
		}
	}
	e.mu.Unlock()
	for _, n := range mine {
		e.diagnoseOnce(t, n)
		e.mu.Lock()
		done := n.drained
		n.drained = true
		e.mu.Unlock()
		if !done {
			e.drain(t, n.name)
		}
	}
}

// drain deletes the Bundles in ns, waits until each of their Graphs is
// deleted or being deleted, deletes the PromotionSteps left (steps a test
// created without a Bundle), and waits until none is left. The steps are
// deleted only after the Graphs: kro recreates a step that a live Graph still
// has, and the controller then leaves that step's PR open.
//
// drain only logs when ns is not drained within drainTimeout, and the repo
// is deleted anyway.
func (e *Env) drain(t *testing.T, ns string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	start := time.Now()
	switch err := e.Client.DeleteAllOf(ctx, &v1alpha1.Bundle{}, client.InNamespace(ns)); {
	case apierrors.IsNotFound(err):
		return // the test deleted the namespace
	case err != nil:
		t.Logf("drain %s: delete Bundles: %v", ns, err)
		return
	}
	if !e.drained(ctx, t, ns, "Bundles and live Graphs", e.liveBundlesAndGraphs) {
		return
	}
	if err := e.Client.DeleteAllOf(ctx, &v1alpha1.PromotionStep{}, client.InNamespace(ns)); err != nil {
		t.Logf("drain %s: delete PromotionSteps: %v", ns, err)
		return
	}
	if !e.drained(ctx, t, ns, "PromotionSteps", e.promotionStepsLeft) {
		return
	}
	t.Logf("drained %s in %s", ns, time.Since(start).Round(100*time.Millisecond))
}

// drained polls left until it lists nothing in ns, and reports whether that
// happened before ctx ended.
func (e *Env) drained(ctx context.Context, t *testing.T, ns, what string, left func(context.Context, string) ([]string, error)) bool {
	t.Helper()
	var last string
	for {
		names, err := left(ctx, ns)
		switch {
		case err != nil:
			last = err.Error()
		case len(names) == 0:
			return true
		default:
			last = strings.Join(names, ", ")
		}
		select {
		case <-ctx.Done():
			t.Logf("drain %s: %s still left after %s: %s; deleting the repo anyway", ns, what, drainTimeout, last)
			return false
		case <-time.After(Poll):
		}
	}
}

// liveBundlesAndGraphs lists the Bundles in ns and the Graphs in ns that are
// not being deleted.
func (e *Env) liveBundlesAndGraphs(ctx context.Context, ns string) ([]string, error) {
	var bundles v1alpha1.BundleList
	if err := e.Client.List(ctx, &bundles, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	var left []string
	for _, b := range bundles.Items {
		left = append(left, "Bundle "+b.Name)
	}
	graphs := &unstructured.UnstructuredList{}
	graphs.SetGroupVersionKind(GraphGVR.GroupVersion().WithKind("GraphList"))
	if err := e.Client.List(ctx, graphs, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	for _, g := range graphs.Items {
		if g.GetDeletionTimestamp() == nil {
			left = append(left, "Graph "+g.GetName())
		}
	}
	return left, nil
}

// promotionStepsLeft lists the PromotionSteps in ns, with their finalizers.
func (e *Env) promotionStepsLeft(ctx context.Context, ns string) ([]string, error) {
	var steps v1alpha1.PromotionStepList
	if err := e.Client.List(ctx, &steps, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	left := make([]string, 0, len(steps.Items))
	for _, s := range steps.Items {
		left = append(left, fmt.Sprintf("%s (finalizers %v)", s.Name, s.Finalizers))
	}
	return left, nil
}

// DrainNamespaces drains the test's namespaces, as the cleanup of a repo
// framework.Env.Repo created does before it deletes the repo: for repos a
// test creates through Env.Git itself (the scale suite creates hundreds
// concurrently).
func (e *Env) DrainNamespaces(t *testing.T) {
	t.Helper()
	e.beforeRepoDelete(t)
}
