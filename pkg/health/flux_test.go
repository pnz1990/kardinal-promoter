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

// stalledMessage is the Ready=False message Flux writes when it gives up on a
// Deployment past its progress deadline.
const stalledMessage = "health check failed after 1m5.017502721s: failed early due to stalled resources: " +
	"[Deployment/prod/web status: 'Failed']"

// TestFluxAdapter_Stalled proves bug 12 of the health spike fixed: Flux
// giving up on the promoted commit because its Deployment stalled is
// terminal, so the step fails at once instead of at health.timeout. Other
// Ready=False results stay unhealthy.
func TestFluxAdapter_Stalled(t *testing.T) {
	attempted := func(rev string) func(obj map[string]interface{}) {
		return func(obj map[string]interface{}) {
			obj["status"].(map[string]interface{})["lastAttemptedRevision"] = rev
		}
	}
	tests := []struct {
		name     string
		obj      *unstructured.Unstructured
		expected string
		want     wantKind
		reason   string
	}{
		{name: "the promoted commit stalled",
			obj: kustomization("False", "HealthCheckFailed", stalledMessage, "main@sha1:"+fluxPrevious,
				attempted("main@sha1:"+fluxPushed)),
			expected: fluxPushed, want: isTerminal,
			reason: "Ready=False, observedGen=3, generation=3: " + stalledMessage + " (lastAttemptedRevision=034ce92a1b2c)"},
		{name: "a stall with no expected commit",
			obj:  kustomization("False", "HealthCheckFailed", stalledMessage, "main@sha1:"+fluxPrevious, nil),
			want: isTerminal, reason: "stalled resources"},
		{name: "another commit stalled",
			obj: kustomization("False", "HealthCheckFailed", stalledMessage, "main@sha1:"+fluxPrevious,
				attempted("main@sha1:"+fluxPrevious)),
			expected: fluxPushed, want: isUnhealthy, reason: "stalled resources"},
		{name: "a health check timeout",
			obj: kustomization("False", "HealthCheckFailed", "health check failed after 3m0s: timeout waiting for: "+
				"[Deployment/prod/web status: 'InProgress']", "main@sha1:"+fluxPrevious, attempted("main@sha1:"+fluxPushed)),
			expected: fluxPushed, want: isUnhealthy, reason: "timeout waiting for"},
		{name: "a build failure",
			obj: kustomization("False", "BuildFailed", "kustomize build failed: accumulating resources", "main@sha1:"+fluxPrevious,
				attempted("main@sha1:"+fluxPushed)),
			expected: fluxPushed, want: isUnhealthy, reason: "kustomize build failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkFlux(t, health.CheckOptions{ExpectedRevision: tt.expected}, tt.obj)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}
