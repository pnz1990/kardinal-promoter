// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func TestPauseResume(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, pipeline("app", "test", "prod"))

	// Pause twice: idempotent.
	require.NoError(t, lifecycle.Pause(ctx, c, ns, "app"))
	require.NoError(t, lifecycle.Pause(ctx, c, ns, "app"))

	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app"}, &p))
	assert.True(t, p.Spec.Paused)

	var gate v1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "freeze-app"}, &gate))
	assert.Equal(t, "true", gate.Labels[lifecycle.LabelFreeze])
	assert.Equal(t, "app", gate.Labels[lifecycle.LabelPipeline])
	assert.Equal(t, "false", gate.Spec.Expression)
	require.Len(t, gate.OwnerReferences, 1, "the gate is owned by the Pipeline so it is deleted with it")
	assert.Equal(t, "Pipeline", gate.OwnerReferences[0].Kind)
	assert.Nil(t, gate.OwnerReferences[0].BlockOwnerDeletion, "blockOwnerDeletion would need finalizer rights on the Pipeline")

	paused, err := lifecycle.IsPaused(ctx, c, ns, "app")
	require.NoError(t, err)
	assert.True(t, paused)

	// Resume twice: idempotent.
	require.NoError(t, lifecycle.Resume(ctx, c, ns, "app"))
	require.NoError(t, lifecycle.Resume(ctx, c, ns, "app"))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app"}, &p))
	assert.False(t, p.Spec.Paused)
	err = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "freeze-app"}, &gate)
	assert.True(t, apierrors.IsNotFound(err), "resume deletes the freeze gate")

	paused, err = lifecycle.IsPaused(ctx, c, ns, "app")
	require.NoError(t, err)
	assert.False(t, paused)
}

func TestPauseResume_Errors(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	assert.ErrorIs(t, lifecycle.Pause(ctx, c, ns, "missing"), lifecycle.ErrNotFound)
	assert.ErrorIs(t, lifecycle.Resume(ctx, c, ns, "missing"), lifecycle.ErrNotFound)
}

func TestRemoveFreezeGate_LeavesUserGateAlone(t *testing.T) {
	ctx := context.Background()
	userGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "freeze-app", Namespace: ns},
		Spec:       v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	c := newClient(t, pipeline("app", "test"), userGate)

	paused, err := lifecycle.IsPaused(ctx, c, ns, "app")
	require.NoError(t, err)
	assert.False(t, paused, "a gate without the freeze label is not a pause")

	require.NoError(t, lifecycle.Resume(ctx, c, ns, "app"))
	var gate v1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "freeze-app"}, &gate),
		"resume must not delete a user gate that happens to share the name")
}

// TestSetPaused_WritesOnlyThePipeline: SetPaused, the UI's pause and resume,
// sets spec.paused and leaves the freeze gate to the Pipeline reconciler, so
// the caller needs only get and update on the Pipeline. A conflict with a
// concurrent write is retried.
func TestSetPaused_WritesOnlyThePipeline(t *testing.T) {
	ctx := context.Background()
	for _, paused := range []bool{true, false} {
		t.Run(fmt.Sprintf("paused=%t", paused), func(t *testing.T) {
			p := pipeline("app", "test")
			p.Spec.Paused = !paused
			var verbs []string
			conflicts := 1
			scheme := runtime.NewScheme()
			require.NoError(t, v1alpha1.AddToScheme(scheme))
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).
				WithInterceptorFuncs(interceptor.Funcs{
					Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						verbs = append(verbs, "update")
						if conflicts > 0 {
							conflicts--
							return apierrors.NewConflict(schema.GroupResource{Group: "kardinal.io", Resource: "pipelines"}, obj.GetName(), nil)
						}
						return cl.Update(ctx, obj, opts...)
					},
					Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						verbs = append(verbs, "patch")
						return cl.Patch(ctx, obj, patch, opts...)
					},
					Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						verbs = append(verbs, "create")
						return cl.Create(ctx, obj, opts...)
					},
					Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						verbs = append(verbs, "delete")
						return cl.Delete(ctx, obj, opts...)
					},
				}).Build()

			require.NoError(t, lifecycle.SetPaused(ctx, c, ns, "app", paused))
			require.NoError(t, lifecycle.SetPaused(ctx, c, ns, "app", paused), "idempotent")

			var got v1alpha1.Pipeline
			require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app"}, &got))
			assert.Equal(t, paused, got.Spec.Paused)
			assert.Equal(t, []string{"update", "update"}, verbs, "one conflict retried, then no write when already set")
			var gate v1alpha1.PolicyGate
			assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "freeze-app"}, &gate)),
				"the freeze gate is the Pipeline reconciler's to write")
		})
	}
	assert.ErrorIs(t, lifecycle.SetPaused(ctx, newClient(t), ns, "missing", true), lifecycle.ErrNotFound)
}
