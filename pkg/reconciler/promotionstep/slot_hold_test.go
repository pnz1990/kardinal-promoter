// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// #1349: a Pending step of a Failed Bundle that waits for a
// maxConcurrentPromotions slot (condition WaitingForSlot True) does not
// start; once the Bundle reconciler lifts the hold it does.
func TestSlotHold_PendingStepWaits(t *testing.T) {
	for _, state := range []string{"", "Pending"} {
		t.Run("state="+state, func(t *testing.T) {
			ctx := context.Background()
			step := makeStep("step-test", "nginx-demo", "bundle-1", "test")
			step.Status.State = state
			b := makeBundle("bundle-1", "nginx-demo")
			b.Status.Phase = "Failed"
			meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{
				Type: graph.CondBundleWaitingForSlot, Status: metav1.ConditionTrue, Reason: "SlotTaken", Message: "held",
			})
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(step, makePipeline("nginx-demo"), b).Build()
			r := &promotionstep.Reconciler{
				Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() },
			}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"}}

			res, err := r.Reconcile(ctx, req)
			require.NoError(t, err)
			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
			assert.Equal(t, state, got.Status.State, "the step does not start")
			assert.Contains(t, got.Status.Message, "maxConcurrentPromotions")
			assert.Positive(t, res.RequeueAfter)
			assert.Empty(t, got.Status.Outputs, "no git step ran")

			rv := got.ResourceVersion
			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
			assert.Equal(t, rv, got.ResourceVersion, "a held step is not rewritten")

			var cur v1alpha1.Bundle
			require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "bundle-1", Namespace: "default"}, &cur))
			old := cur.DeepCopy()
			meta.RemoveStatusCondition(&cur.Status.Conditions, graph.CondBundleWaitingForSlot)
			require.NoError(t, c.Status().Update(ctx, &cur))
			assert.True(t, promotionstep.BundleWakesSteps.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: &cur}),
				"lifting the hold wakes the Bundle's steps")

			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
			assert.Equal(t, "Promoting", got.Status.State, "after the hold is lifted the step starts")
		})
	}
}
