// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

const (
	promoted = "77fe8dd636ee0000000000000000000000000000"
	later    = "13012b285355000000000000000000000000000a"
)

// TestArgoCDAdapter_SyncedDescendant (#1575): an Application Healthy and
// Synced on a later commit of the branch is the promoted change deployed
// when the branch history says that commit contains it, also without Pods
// (status.summary.images empty). Not containing it, an unreadable history
// and no history reader all keep the step waiting, as before.
func TestArgoCDAdapter_SyncedDescendant(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]interface{}{"name": "fleet-env-008", "namespace": "argocd"},
		"status": map[string]interface{}{
			"health":         map[string]interface{}{"status": "Healthy"},
			"sync":           map[string]interface{}{"status": "Synced", "revision": later},
			"operationState": map[string]interface{}{"phase": "Succeeded", "syncResult": map[string]interface{}{"revision": later}},
			"summary":        map[string]interface{}{},
		},
	}}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	adapter := health.NewArgoCDAdapter(dynfake.NewSimpleDynamicClient(runtime.NewScheme(), app))
	var asked []string
	contains := func(answer bool, err error) func(context.Context, string) (bool, error) {
		return func(_ context.Context, rev string) (bool, error) {
			asked = append(asked, rev)
			return answer, err
		}
	}
	tests := []struct {
		name     string
		contains func(context.Context, string) (bool, error)
		healthy  bool
		reason   string
	}{
		{name: "the synced revision contains the promoted commit", contains: contains(true, nil), healthy: true,
			reason: "(synced revision 13012b285355 contains 77fe8dd636ee)"},
		{name: "it does not", contains: contains(false, nil), reason: "waiting for 77fe8dd636ee"},
		{name: "the history cannot be read", contains: contains(false, errors.New("ls-remote: timeout")),
			reason: "could not read whether 13012b285355 contains it: ls-remote: timeout"},
		{name: "no history reader", reason: "waiting for 77fe8dd636ee"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			asked = nil
			res, err := adapter.Check(context.Background(), health.CheckOptions{
				ArgoCD:           health.ArgoCDConfig{Name: "fleet-env-008", Namespace: "argocd"},
				ExpectedRevision: promoted,
				ExpectedImages:   []health.ImageExpectation{{Repository: "ghcr.io/pnz1990/kardinal-test-app", Tag: "main"}},
				RevisionContains: tc.contains,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.healthy, res.Healthy, res.Reason)
			assert.False(t, res.Terminal)
			assert.Contains(t, res.Reason, tc.reason)
			if tc.contains != nil {
				assert.Equal(t, []string{later}, asked, "the synced revision is the one asked about")
			}
		})
	}
}

// TestArgoCDAdapter_DescendantBeforeImages (#1575): when the Application also
// runs the Bundle images, the branch history decides first, and the images
// fallback still passes when the history says no or cannot be read.
func TestArgoCDAdapter_DescendantBeforeImages(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]interface{}{"name": "shared", "namespace": "argocd"},
		"status": map[string]interface{}{
			"health":         map[string]interface{}{"status": "Healthy"},
			"sync":           map[string]interface{}{"status": "Synced", "revision": later},
			"operationState": map[string]interface{}{"phase": "Succeeded", "syncResult": map[string]interface{}{"revision": later}},
			"summary":        map[string]interface{}{"images": []interface{}{"ghcr.io/pnz1990/kardinal-test-app:main"}},
		},
	}}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	adapter := health.NewArgoCDAdapter(dynfake.NewSimpleDynamicClient(runtime.NewScheme(), app))
	images := "(synced revision 13012b285355 is not 77fe8dd636ee, but the Application runs the Bundle images)"
	tests := []struct {
		name     string
		contains func(context.Context, string) (bool, error)
		reason   string
	}{
		{name: "the history says it contains it", reason: "(synced revision 13012b285355 contains 77fe8dd636ee)",
			contains: func(context.Context, string) (bool, error) { return true, nil }},
		{name: "the history says no: the images decide", reason: images,
			contains: func(context.Context, string) (bool, error) { return false, nil }},
		{name: "the history cannot be read: the images decide", reason: images,
			contains: func(context.Context, string) (bool, error) { return false, errors.New("timeout") }},
		{name: "no history reader", reason: images},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := adapter.Check(context.Background(), health.CheckOptions{
				ArgoCD:           health.ArgoCDConfig{Name: "shared", Namespace: "argocd"},
				ExpectedRevision: promoted,
				ExpectedImages:   []health.ImageExpectation{{Repository: "ghcr.io/pnz1990/kardinal-test-app", Tag: "main"}},
				RevisionContains: tc.contains,
			})
			require.NoError(t, err)
			assert.True(t, res.Healthy, res.Reason)
			assert.True(t, strings.HasSuffix(res.Reason, tc.reason), res.Reason)
		})
	}
}

// TestFluxAdapter_AppliedDescendant (#1575): a Kustomization Ready on a later
// commit that contains the promoted one is healthy without looking at its
// Deployments.
func TestFluxAdapter_AppliedDescendant(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]interface{}{"name": "fleet-env-008", "namespace": "flux-system", "generation": int64(1)},
		"status": map[string]interface{}{
			"observedGeneration":    int64(1),
			"lastAppliedRevision":   "main@sha1:" + later,
			"lastAttemptedRevision": "main@sha1:" + later,
			"conditions":            []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded"}},
		},
	}}
	ks.SetGroupVersionKind(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"})
	gvrs := map[schema.GroupVersionResource]string{{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}: "KustomizationList"}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, ks)
	adapter := health.NewFluxAdapter(dyn)
	res, err := adapter.Check(context.Background(), health.CheckOptions{
		Flux:             health.FluxConfig{Name: "fleet-env-008", Namespace: "flux-system"},
		ExpectedRevision: promoted,
		RevisionContains: func(_ context.Context, rev string) (bool, error) { return rev == later, nil },
	})
	require.NoError(t, err)
	assert.True(t, res.Healthy, res.Reason)
	assert.Contains(t, res.Reason, "lastAppliedRevision 13012b285355 contains 77fe8dd636ee")

	// A history that cannot be read waits, and says why (#1575 QA: as Argo CD).
	res, err = adapter.Check(context.Background(), health.CheckOptions{
		Flux:             health.FluxConfig{Name: "fleet-env-008", Namespace: "flux-system"},
		ExpectedRevision: promoted,
		RevisionContains: func(context.Context, string) (bool, error) { return false, errors.New("ls-remote: timeout") },
	})
	require.NoError(t, err)
	assert.False(t, res.Healthy, res.Reason)
	assert.Contains(t, res.Reason, "waiting for 77fe8dd636ee (could not read whether 13012b285355 contains it: ls-remote: timeout)")
}

// TestAdapters_AppliedAncestor (#1883): a tool Synced or Ready on an older
// commit of the branch than the promoted one says how far behind it is, so
// the step's message shows that the tool has not fetched the promoted commit
// yet; with no history reader, or one that does not know, it says nothing
// more. Covers HEALTH-ARGOCD-BEHIND-01.
func TestAdapters_AppliedAncestor(t *testing.T) {
	const earlier = "dc6706827cb70000000000000000000000000000"
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]interface{}{"name": "spike-prod-cd", "namespace": "argocd"},
		"status": map[string]interface{}{
			"health":         map[string]interface{}{"status": "Healthy"},
			"sync":           map[string]interface{}{"status": "Synced", "revision": earlier},
			"operationState": map[string]interface{}{"phase": "Succeeded", "syncResult": map[string]interface{}{"revision": earlier}},
		},
	}}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]interface{}{"name": "spike-prod", "namespace": "flux-system", "generation": int64(1)},
		"status": map[string]interface{}{
			"observedGeneration":    int64(1),
			"lastAppliedRevision":   "main@sha1:" + earlier,
			"lastAttemptedRevision": "main@sha1:" + earlier,
			"conditions":            []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded"}},
		},
	}}
	ks.SetGroupVersionKind(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"})
	argo := health.NewArgoCDAdapter(dynfake.NewSimpleDynamicClient(runtime.NewScheme(), app))
	flux := health.NewFluxAdapter(dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}: "KustomizationList"}, ks))
	behind := func(n int, err error) func(context.Context, string) (int, error) {
		return func(_ context.Context, rev string) (int, error) {
			assert.Equal(t, earlier, rev, "the applied revision is the one asked about")
			return n, err
		}
	}
	notContains := func(context.Context, string) (bool, error) { return false, nil }
	for _, tc := range []struct {
		name   string
		behind func(context.Context, string) (int, error)
		argo   string
		flux   string
	}{
		{name: "one commit behind", behind: behind(1, nil),
			argo: "revision=dc6706827cb7, waiting for 77fe8dd636ee (dc6706827cb7 is 1 commit behind it: Argo CD has not fetched the promoted commit yet)",
			flux: "lastAppliedRevision=dc6706827cb7, waiting for 77fe8dd636ee (dc6706827cb7 is 1 commit behind it: Flux has not fetched the promoted commit yet)"},
		{name: "three commits behind", behind: behind(3, nil),
			argo: "(dc6706827cb7 is 3 commits behind it: Argo CD", flux: "(dc6706827cb7 is 3 commits behind it: Flux"},
		{name: "not an ancestor", behind: behind(0, nil), argo: "revision=dc6706827cb7, waiting for 77fe8dd636ee",
			flux: "lastAppliedRevision=dc6706827cb7, waiting for 77fe8dd636ee"},
		{name: "the history cannot be read", behind: behind(0, errors.New("timeout")), argo: "waiting for 77fe8dd636ee",
			flux: "waiting for 77fe8dd636ee"},
		{name: "no history reader", argo: "waiting for 77fe8dd636ee", flux: "waiting for 77fe8dd636ee"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := argo.Check(context.Background(), health.CheckOptions{
				ArgoCD: health.ArgoCDConfig{Name: "spike-prod-cd", Namespace: "argocd"}, ExpectedRevision: promoted,
				RevisionContains: notContains, RevisionBehind: tc.behind})
			require.NoError(t, err)
			assert.True(t, res.Progressing, res.Reason)
			assert.Contains(t, res.Reason, tc.argo)
			if n, _ := callOr(tc.behind); n == 0 {
				assert.NotContains(t, res.Reason, "behind")
			}
			res, err = flux.Check(context.Background(), health.CheckOptions{
				Flux: health.FluxConfig{Name: "spike-prod", Namespace: "flux-system"}, ExpectedRevision: promoted,
				RevisionContains: notContains, RevisionBehind: tc.behind})
			require.NoError(t, err)
			assert.False(t, res.Healthy, res.Reason)
			assert.Contains(t, res.Reason, tc.flux)
		})
	}
}

// callOr calls f with the applied revision, or returns 0 for nil.
func callOr(f func(context.Context, string) (int, error)) (int, error) {
	if f == nil {
		return 0, nil
	}
	return f(context.Background(), "dc6706827cb70000000000000000000000000000")
}
