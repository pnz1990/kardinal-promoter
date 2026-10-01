//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// pausedApp sets up a paused Pipeline in ns over a seeded GitOps repo, with
// no Argo CD Applications. Its Bundles get PromotionSteps that stay Pending,
// so a test can create Bundles that must not promote (for example images in
// the suite's plain-HTTP registry, which the kind node cannot pull). It
// returns once the freeze gate exists, so no step starts before the pause
// holds.
func pausedApp(t *testing.T, e *framework.Env, ns string, envs ...string) *app {
	t.Helper()
	a := &app{e: e, ns: ns, envs: envs,
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))}
	p := a.pipeline(nil)
	p.Spec.Paused = true
	a.apply(t, p)
	framework.Eventually(t, time.Minute, "the freeze gate of the paused Pipeline", func(ctx context.Context) (bool, string) {
		var g v1alpha1.PolicyGate
		err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: "freeze-" + pipelineName}, &g)
		return err == nil, fmt.Sprint(err)
	})
	return a
}

// bundles lists the Bundles in ns.
func bundles(t *testing.T, e *framework.Env, ns string, opts ...client.ListOption) []v1alpha1.Bundle {
	t.Helper()
	var list v1alpha1.BundleList
	if err := e.Client.List(context.Background(), &list, append(opts, client.InNamespace(ns))...); err != nil {
		t.Fatalf("list Bundles in %s: %v", ns, err)
	}
	return list.Items
}

// getBundle reads Bundle ns/name.
func getBundle(t *testing.T, e *framework.Env, ns, name string) *v1alpha1.Bundle {
	t.Helper()
	var b v1alpha1.Bundle
	if err := e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		t.Fatalf("get Bundle %s/%s: %v", ns, name, err)
	}
	return &b
}
