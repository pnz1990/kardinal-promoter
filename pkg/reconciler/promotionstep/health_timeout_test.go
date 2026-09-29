// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

const badImage = "ghcr.io/org/app:v2-bad"

// crashLoopDeployment returns the status a 1-replica Deployment keeps while
// the pod of its new image crash-loops. The new ReplicaSet exists
// (Progressing=True, reason ReplicaSetUpdated) but its pod never becomes
// available, and Kubernetes reports nothing else until progressDeadlineSeconds.
// With RollingUpdate the old pod keeps serving (2 replicas, 1 updated,
// Available=True); with Recreate the old pod is gone (1 replica, 0 available,
// Available=False).
func crashLoopDeployment(name, ns string, strategy appsv1.DeploymentStrategyType) *appsv1.Deployment {
	one := int32(1)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: &one, Strategy: appsv1.DeploymentStrategy{Type: strategy}},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 2, UpdatedReplicas: 1, UnavailableReplicas: 1},
	}
	progressing := appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue,
		Reason: "ReplicaSetUpdated", Message: `ReplicaSet "` + name + `-5d4f8" is progressing.`}
	if strategy == appsv1.RecreateDeploymentStrategyType {
		d.Status.Replicas = 1
		d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable,
			Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable",
			Message: "Deployment does not have minimum availability."}, progressing}
	} else {
		d.Status.Replicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas = 2, 1, 1
		d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable,
			Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable",
			Message: "Deployment has minimum availability."}, progressing}
	}
	return withImage(d, badImage)
}

func getBundle(c client.Client, name string) (*v1alpha1.Bundle, error) {
	var b v1alpha1.Bundle
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// TestCrashLoopRollsBackAtHealthTimeout proves that a crash-looping new image
// is rolled back within health.timeout. Kubernetes reports such a Deployment
// as still rolling out, which is not a health failure, so no check fails
// before the timeout; reaching health.timeout without a Healthy result counts
// as a health failure and applies onHealthFailure.
func TestCrashLoopRollsBackAtHealthTimeout(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"},
		OnHealthFailure: "rollback"}
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2-bad"}}
	for _, strategy := range []appsv1.DeploymentStrategyType{appsv1.RollingUpdateDeploymentStrategyType, appsv1.RecreateDeploymentStrategyType} {
		t.Run(string(strategy), func(t *testing.T) {
			deploy := crashLoopDeployment("p", "test", strategy)

			// Within the timeout: rolling out, not a failure, no rollback.
			c, got, _ := healthCase{env: env, images: images, objs: []client.Object{deploy.DeepCopy()}}.run(t)
			require.Equal(t, "HealthChecking", got.Status.State, got.Status.Message)
			assert.Equal(t, 0, got.Status.ConsecutiveHealthFailures)
			assert.Contains(t, got.Status.Message, "waiting for resource")
			require.NotNil(t, got.Status.HealthCheckExpiry)
			assert.WithinDuration(t, time.Now().Add(time.Minute), got.Status.HealthCheckExpiry.Time, 10*time.Second)
			_, err := getBundle(c, "b1-rollback-alarm")
			assert.True(t, apierrors.IsNotFound(err), "no rollback Bundle before the timeout")

			// health.timeout passed with the same status: rolled back.
			expired := metav1.NewTime(time.Now().Add(-time.Second))
			c, got, _ = healthCase{env: env, images: images, objs: []client.Object{deploy.DeepCopy()},
				status: v1alpha1.PromotionStepStatus{HealthCheckExpiry: &expired, Message: got.Status.Message}}.run(t)
			assert.Equal(t, "RollingBack", got.Status.State, got.Status.Message)
			assert.Equal(t, 1, got.Status.ConsecutiveHealthFailures)
			assert.Contains(t, got.Status.Message, "health check timeout after 1m0s; last result: waiting for resource")
			assert.Contains(t, got.Status.Message, "rollback Bundle b1-rollback-alarm created")
			rb, err := getBundle(c, "b1-rollback-alarm")
			require.NoError(t, err)
			assert.Equal(t, "true", rb.Labels["kardinal.io/rollback"])
			require.NotNil(t, rb.Spec.Provenance)
			assert.Equal(t, "b1", rb.Spec.Provenance.RollbackOf)
		})
	}
}

// TestHealthTimeoutAppliesOnHealthFailure proves that reaching health.timeout
// is counted in status.consecutiveHealthFailures (which the RollbackPolicy
// reconciler reads) and applies each onHealthFailure action, and that a
// rollback Bundle is not rolled back again.
func TestHealthTimeoutAppliesOnHealthFailure(t *testing.T) {
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2-bad"}}
	expired := metav1.NewTime(time.Now().Add(-time.Second))
	tests := []struct {
		name            string
		onHealthFailure string
		rollbackBundle  bool
		wantState       string
		wantMsg         string
		wantRollback    bool
		wantAudit       []string
	}{
		{onHealthFailure: "", wantState: "Failed",
			wantMsg: "health alarm via resource (onHealthFailure=none): health check timeout after 1m0s", wantAudit: []string{"PromotionFailed"}},
		{onHealthFailure: "none", wantState: "Failed",
			wantMsg: "health alarm via resource (onHealthFailure=none): health check timeout after 1m0s", wantAudit: []string{"PromotionFailed"}},
		{onHealthFailure: "abort", wantState: "AbortedByAlarm",
			wantMsg: "health alarm via resource (onHealthFailure=abort): health check timeout after 1m0s"},
		{onHealthFailure: "rollback", wantState: "RollingBack",
			wantMsg: "health alarm via resource (onHealthFailure=rollback): health check timeout after 1m0s", wantRollback: true},
		// A rollback Bundle is not rolled back again, so rollbacks do not chain.
		{name: "rollback Bundle is not rolled back again", onHealthFailure: "rollback", rollbackBundle: true,
			wantState: "AbortedByAlarm", wantMsg: "Bundle b1 is a rollback and is not rolled back again"},
	}
	for _, tt := range tests {
		name := tt.name
		if name == "" {
			name = "onHealthFailure=" + tt.onHealthFailure
		}
		t.Run(name, func(t *testing.T) {
			env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"},
				OnHealthFailure: tt.onHealthFailure}
			hc := healthCase{env: env, images: images,
				objs:   []client.Object{crashLoopDeployment("p", "test", appsv1.RecreateDeploymentStrategyType)},
				status: v1alpha1.PromotionStepStatus{HealthCheckExpiry: &expired}}
			if tt.rollbackBundle {
				hc.bundle = func(b *v1alpha1.Bundle) {
					b.Labels = map[string]string{"kardinal.io/rollback": "true"}
					b.Spec.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "b0"}
				}
			}
			c, got, _ := hc.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Equal(t, 1, got.Status.ConsecutiveHealthFailures)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			_, err := getBundle(c, "b1-rollback-alarm")
			assert.Equal(t, tt.wantRollback, err == nil, "rollback Bundle created")
			if tt.wantAudit != nil {
				assert.Equal(t, tt.wantAudit, auditActions(t, c))
			}
		})
	}
}
