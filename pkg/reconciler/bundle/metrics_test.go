// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

// TestBundleReconciler_VerifiedCounter checks the kardinal_bundles_total
// {phase="Verified"} counter the Grafana "Bundles Verified" stat reads: it goes up
// once on the Promoting -> Verified transition, not on later reconciles, and not
// while an environment is still promoting.
func TestBundleReconciler_VerifiedCounter(t *testing.T) {
	tests := []struct {
		name      string
		prodPhase string
		wantPhase string
		wantDelta float64
	}{
		{name: "all environments verified", prodPhase: "Verified", wantPhase: "Verified", wantDelta: 1},
		{name: "prod still promoting", prodPhase: "Promoting", wantPhase: "Promoting", wantDelta: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
				Spec: kardinalv1alpha1.PipelineSpec{
					Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}},
				},
			}
			b := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
				Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
				Status: kardinalv1alpha1.BundleStatus{
					Phase: "Promoting",
					Environments: []kardinalv1alpha1.EnvironmentStatus{
						{Name: "test", Phase: "Verified"},
						{Name: "prod", Phase: tt.prodPhase},
					},
				},
			}
			c := indexedBuilder(newScheme()).
				WithObjects(pipeline, b).
				WithStatusSubresource(&kardinalv1alpha1.Bundle{}).
				Build()
			rec := events.NewFakeRecorder(10)
			r := &bundle.Reconciler{Client: c, Recorder: rec}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"}}
			verified := observability.BundlesTotal.WithLabelValues("Verified")

			before := testutil.ToFloat64(verified)
			_, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, before+tt.wantDelta, testutil.ToFloat64(verified),
				"the Verified counter must go up once per Verified transition")

			var got kardinalv1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tt.wantPhase, got.Status.Phase)

			// A second reconcile of the same Bundle is not a new transition.
			_, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, before+tt.wantDelta, testutil.ToFloat64(verified),
				"a later reconcile must not count the Bundle again")
		})
	}
}
