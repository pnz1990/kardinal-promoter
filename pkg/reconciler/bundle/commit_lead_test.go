// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
)

// TestBundleReconciler_CommitToProductionFromCommitTime (#1942): a
// version's status.metrics.commitToProductionMinutes starts at
// spec.provenance.commitTime when CI set it, so the build counts; without
// it, or with a commitTime after the Bundle's creation (no commit is made
// after its Bundle), it starts at the Bundle's creation. The Bundle was
// created 2h ago and its last environment passed its health check now.
func TestBundleReconciler_CommitToProductionFromCommitTime(t *testing.T) {
	created := time.Now().UTC().Add(-2 * time.Hour)
	tests := []struct {
		name     string
		commit   *metav1.Time
		min, max int64
	}{
		{"no commit time: from the Bundle's creation", nil, 119, 121},
		{"commit an hour before the Bundle", &metav1.Time{Time: created.Add(-time.Hour)}, 179, 181},
		{"a commit time after the Bundle is ignored", &metav1.Time{Time: created.Add(30 * time.Minute)}, 119, 121},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default", CreationTimestamp: metav1.NewTime(created)},
				Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app",
					Provenance: &kardinalv1alpha1.BundleProvenance{CommitSHA: "abc1234", CommitTime: tc.commit}},
				Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
			}
			step := &kardinalv1alpha1.PromotionStep{
				ObjectMeta: metav1.ObjectMeta{Name: "step-prod", Namespace: "default",
					Labels: map[string]string{"kardinal.io/bundle": "app-v1"}},
				Spec:   kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: "prod", StepType: "auto"},
				Status: kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
			}
			c := indexedBuilder(newScheme()).WithObjects(&kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
				Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "prod"}}},
			}, b, step).WithStatusSubresource(b).Build()
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}}
			_, err := (&bundle.Reconciler{Client: c}).Reconcile(context.Background(), req)
			require.NoError(t, err)
			var got kardinalv1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			require.NotNil(t, got.Status.Metrics)
			m := got.Status.Metrics.CommitToProductionMinutes
			assert.True(t, m >= tc.min && m <= tc.max, "commitToProductionMinutes %d, want %d..%d", m, tc.min, tc.max)
		})
	}
}
