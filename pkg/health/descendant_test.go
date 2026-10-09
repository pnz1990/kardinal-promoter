// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"errors"
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
}
