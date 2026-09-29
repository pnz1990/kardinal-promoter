// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

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
