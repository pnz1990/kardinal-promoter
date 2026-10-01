// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// canaryDeployment is a rolled-out Deployment name in namespace test that
// runs image, as the dynamic client of the flagger check reads it.
func canaryDeployment(name, image string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]interface{}{"name": name, "namespace": "test", "generation": int64(1)},
		"spec": map[string]interface{}{"replicas": int64(1), "template": map[string]interface{}{
			"spec": map[string]interface{}{"containers": []interface{}{
				map[string]interface{}{"name": "app", "image": image}}}}},
		"status": map[string]interface{}{
			"observedGeneration": int64(1), "replicas": int64(1), "updatedReplicas": int64(1),
			"readyReplicas": int64(1), "availableReplicas": int64(1),
			"conditions": []interface{}{map[string]interface{}{"type": "Available", "status": "True"}},
		},
	}}
}

// TestFlaggerRecordsWhenTheTargetRanTheBundle proves the reconciler half of
// the Flagger timing race fix: the first health check that finds the Canary
// target on the Bundle images records the time in status.targetUpdatedAt,
// once, and the checks after it pass that time to the flagger check. A Failed
// that Flagger set before it (the previous release, failing after the health
// check started but before the GitOps tool applied the Bundle) does not fail
// the step; a Failed set after it does.
func TestFlaggerRecordsWhenTheTargetRanTheBundle(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "flagger"}}
	v2 := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	started := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	steps := []v1alpha1.StepStatus{{Name: "health-check", State: v1alpha1.StepExecutionInProgress, StartedAt: &started}}
	after := func(d time.Duration) *metav1.Time { t := metav1.NewTime(started.Add(d)); return &t }
	// failed is the Canary after an analysis failed 20s after the health
	// check started; Flagger re-stamps status.lastTransitionTime at every tick.
	failed := func() *unstructured.Unstructured {
		failedAt := started.Add(20 * time.Second).UTC().Format(time.RFC3339)
		c := unstructuredObj("flagger.app/v1beta1", "Canary", "test", "p", map[string]interface{}{
			"phase": "Failed", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
			"conditions": []interface{}{map[string]interface{}{"type": "Promoted", "status": "False",
				"reason": "Failed", "lastUpdateTime": failedAt,
				"message": "Canary analysis failed, Deployment scaled to zero."}},
		})
		c.Object["spec"] = map[string]interface{}{"targetRef": map[string]interface{}{
			"apiVersion": "apps/v1", "kind": "Deployment", "name": "p"}}
		return c
	}
	tests := []struct {
		name         string
		targetImage  string
		seen         *metav1.Time
		wantState    string
		wantMsg      string
		wantFailures int
		// wantSeen is the expected status.targetUpdatedAt; recorded means
		// set by this reconcile (about now).
		wantSeen *metav1.Time
		recorded bool
	}{
		{name: "the first check to find the target on the Bundle records it and does not trust the Failed",
			targetImage: "ghcr.io/org/app:v2",
			wantState:   "HealthChecking", wantMsg: "this check is the first to find the target running the Bundle images",
			recorded: true},
		{name: "a Failed set before the target ran the Bundle is the previous release's",
			targetImage: "ghcr.io/org/app:v2", seen: after(30 * time.Second),
			wantState: "HealthChecking", wantMsg: "Failed is for an earlier release: its Promoted condition's lastUpdateTime",
			wantSeen: after(30 * time.Second)},
		{name: "a Failed set after the target ran the Bundle fails the step",
			targetImage: "ghcr.io/org/app:v2", seen: after(10 * time.Second),
			wantState: "Failed", wantMsg: "health alarm via flagger", wantFailures: 1,
			wantSeen: after(10 * time.Second)},
		{name: "a target not updated yet records nothing",
			targetImage: "ghcr.io/org/app:v1",
			wantState:   "HealthChecking", wantMsg: "not updated yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now().Truncate(time.Second)
			_, got, _ := healthCase{env: env, images: v2,
				status: v1alpha1.PromotionStepStatus{Steps: steps, TargetUpdatedAt: tt.seen},
				dynObjs: []runtime.Object{failed(),
					canaryDeployment("p", tt.targetImage), canaryDeployment("p-primary", "ghcr.io/org/app:v0")},
			}.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantFailures, got.Status.ConsecutiveHealthFailures)
			switch {
			case tt.recorded:
				require.NotNil(t, got.Status.TargetUpdatedAt)
				assert.False(t, got.Status.TargetUpdatedAt.Time.Before(before), "recorded at this check")
			case tt.wantSeen != nil:
				require.NotNil(t, got.Status.TargetUpdatedAt)
				assert.True(t, tt.wantSeen.Time.Equal(got.Status.TargetUpdatedAt.Time), "set once: %s", got.Status.TargetUpdatedAt)
			default:
				assert.Nil(t, got.Status.TargetUpdatedAt)
			}
		})
	}
}
