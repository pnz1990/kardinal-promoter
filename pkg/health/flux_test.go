// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// The Kustomization statuses below are trimmed from ones Flux v2.9.5
// kustomize-controller wrote on a kind cluster (the flux e2e suite of #1356).

const (
	fluxPushed   = "034ce92a1b2c3d4e5f60718293a4b5c6d7e8f901"
	fluxPrevious = "d7d4d8a000000000000000000000000000000000"
)

// kustomization is the Kustomization flux-system/web-prod at generation 3,
// observed by Flux, with a Ready condition of ready (reason, message) and
// status.lastAppliedRevision applied. mutate changes the object.
func kustomization(ready, reason, message, applied string, mutate func(obj map[string]interface{})) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]interface{}{"name": "web-prod", "namespace": "flux-system", "generation": int64(3)},
		"spec":       map[string]interface{}{"interval": "10m", "path": "./env/prod"},
		"status": map[string]interface{}{
			"observedGeneration":    int64(3),
			"lastAppliedRevision":   applied,
			"lastAttemptedRevision": applied,
			"conditions": []interface{}{map[string]interface{}{
				"type": "Ready", "status": ready, "reason": reason, "message": message,
			}},
		},
	}
	if mutate != nil {
		mutate(obj)
	}
	return &unstructured.Unstructured{Object: obj}
}

func checkFlux(t *testing.T, opts health.CheckOptions, objs ...runtime.Object) health.HealthStatus {
	t.Helper()
	opts.Flux = health.FluxConfig{Name: "web-prod", Namespace: "flux-system"}
	got, err := health.NewFluxAdapter(dynfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)).
		Check(context.Background(), opts)
	require.NoError(t, err)
	return got
}

// TestFluxAdapter_Suspended proves bug 11 of the health spike fixed: a
// promotion into a suspended Kustomization says the Kustomization is
// suspended while it waits. A suspended Kustomization that already applied
// the commit is healthy.
func TestFluxAdapter_Suspended(t *testing.T) {
	suspend := func(obj map[string]interface{}) {
		obj["spec"].(map[string]interface{})["suspend"] = true
	}
	tests := []struct {
		name   string
		obj    *unstructured.Unstructured
		want   wantKind
		reason string
	}{
		{name: "suspended on the previous commit",
			obj:  kustomization("True", "ReconciliationSucceeded", "Applied revision", "main@sha1:"+fluxPrevious, suspend),
			want: isProgressing,
			reason: "Kustomization flux-system/web-prod is suspended; Flux applies nothing until it is resumed " +
				"(Ready=True, observedGen=3, generation=3, lastAppliedRevision=d7d4d8a00000, waiting for 034ce92a1b2c)"},
		{name: "suspended on the pushed commit",
			obj:    kustomization("True", "ReconciliationSucceeded", "Applied revision", "main@sha1:"+fluxPushed, suspend),
			want:   isHealthy,
			reason: "Ready=True, generation=3 matches, lastAppliedRevision=034ce92a1b2c"},
		{name: "not suspended on the previous commit",
			obj:    kustomization("True", "ReconciliationSucceeded", "Applied revision", "main@sha1:"+fluxPrevious, nil),
			want:   isProgressing,
			reason: "Ready=True, observedGen=3, generation=3, lastAppliedRevision=d7d4d8a00000, waiting for 034ce92a1b2c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkFlux(t, health.CheckOptions{ExpectedRevision: fluxPushed}, tt.obj)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Equal(t, tt.reason, got.Reason)
		})
	}
}
