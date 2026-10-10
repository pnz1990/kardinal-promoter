// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"fmt"
	"strings"
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

// TestPause_LongPipelineNameMarksFreezeGateGenerated (GATE-REJECT-02): the
// PolicyGate CRD limits names to 63 characters except on the gates kardinal
// creates, which carry spec.generated. A pipeline name of 57 characters or
// more gives a freeze gate name over 63, so the freeze gate is marked
// generated and the API server accepts it.
func TestPause_LongPipelineNameMarksFreezeGateGenerated(t *testing.T) {
	ctx := context.Background()
	name := strings.Repeat("p", 100)
	c := newClient(t, pipeline(name, "test", "prod"))
	require.NoError(t, lifecycle.Pause(ctx, c, ns, name))

	gateName := lifecycle.FreezeGateName(name)
	assert.Greater(t, len(gateName), 63)
	var gate v1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: gateName}, &gate))
	assert.True(t, gate.Spec.Generated, "the CRD name rule exempts only generated gates")
	paused, err := lifecycle.IsPaused(ctx, c, ns, name)
	require.NoError(t, err)
	assert.True(t, paused)
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

// TestIsFreezeGate: only kardinal's own gate (the freeze label, or a
// controller owner reference to the Pipeline) is a freeze gate.
func TestIsFreezeGate(t *testing.T) {
	isController := true
	owner := func(kind, name string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: kind, Name: name, UID: "u", Controller: &isController}}
	}
	tests := []struct {
		name string
		gate v1alpha1.PolicyGate
		want bool
	}{
		{name: "freeze label", want: true, gate: v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{
			Name: "freeze-app", Labels: map[string]string{lifecycle.LabelFreeze: "true"}}}},
		{name: "owned by the pipeline", want: true, gate: v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{
			Name: "freeze-app", OwnerReferences: owner("Pipeline", "app")}}},
		{name: "user gate with the name", gate: v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "freeze-app"}}},
		{name: "owned by another pipeline", gate: v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{
			Name: "freeze-app", OwnerReferences: owner("Pipeline", "other")}}},
		{name: "owned by something else", gate: v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{
			Name: "freeze-app", OwnerReferences: owner("Graph", "app")}}},
		{name: "another name", gate: v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{
			Name: "freeze-other", Labels: map[string]string{lifecycle.LabelFreeze: "true"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, lifecycle.IsFreezeGate(&tc.gate, "app"))
		})
	}
}

// TestPause_UserGateWithTheFreezeNameIsAConflict: a user PolicyGate named
// freeze-<pipeline> used to make pause a silent no-op (EnsureFreezeGate
// ignored AlreadyExists and IsPaused saw no label). Pause now fails with
// ErrConflict naming the gate, leaves spec.paused set so the pause applies once
// the gate is gone, and never changes the user's gate.
func TestPause_UserGateWithTheFreezeNameIsAConflict(t *testing.T) {
	ctx := context.Background()
	userGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "freeze-app", Namespace: ns},
		Spec:       v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	c := newClient(t, pipeline("app", "test"), userGate)

	err := lifecycle.Pause(ctx, c, ns, "app")
	require.ErrorIs(t, err, lifecycle.ErrConflict)
	assert.Contains(t, err.Error(), "freeze-app")
	assert.Contains(t, err.Error(), "NOT paused")

	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app"}, &p))
	assert.True(t, p.Spec.Paused)
	var gate v1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "freeze-app"}, &gate))
	assert.Equal(t, "!schedule.isWeekend", gate.Spec.Expression, "the user's gate is not changed")
	paused, err := lifecycle.IsPaused(ctx, c, ns, "app")
	require.NoError(t, err)
	assert.False(t, paused)
}

// TestFreezeGate_OwnedWithoutLabel: a gate owned by the Pipeline counts as
// its freeze gate even when the label was removed, so it still holds and
// resume still deletes it.
func TestFreezeGate_OwnedWithoutLabel(t *testing.T) {
	ctx := context.Background()
	p := pipeline("app", "test")
	p.UID = "uid-app"
	gate := lifecycle.DesiredFreezeGate(p)
	delete(gate.Labels, lifecycle.LabelFreeze)
	c := newClient(t, p, gate)

	paused, err := lifecycle.IsPaused(ctx, c, ns, "app")
	require.NoError(t, err)
	assert.True(t, paused)
	require.NoError(t, lifecycle.EnsureFreezeGate(ctx, c, p))
	require.NoError(t, lifecycle.RemoveFreezeGate(ctx, c, ns, "app"))
	var got v1alpha1.PolicyGate
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "freeze-app"}, &got)))
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

// TestPauseResume_WithoutGateRights: a caller allowed to update the Pipeline
// but not to write PolicyGates (the promoter role with
// rbac.userRoles.directWrites) pauses and resumes; the Pipeline reconciler
// creates and removes the freeze gate. Any other gate error still fails.
func TestPauseResume_WithoutGateRights(t *testing.T) {
	ctx := context.Background()
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "kardinal.io", Resource: "policygates"}, "freeze-app", fmt.Errorf("no"))
	tests := []struct {
		name    string
		gateErr error
		wantErr bool
	}{
		{"forbidden is left to the reconciler", forbidden, false},
		{"other errors fail", fmt.Errorf("boom"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, v1alpha1.AddToScheme(scheme))
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pipeline("app", "test")).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						return tt.gateErr
					},
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*v1alpha1.PolicyGate); ok {
							return tt.gateErr
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				}).Build()
			var p v1alpha1.Pipeline
			err := lifecycle.Pause(ctx, c, ns, "app")
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app"}, &p))
			assert.True(t, p.Spec.Paused, "spec.paused is set either way")
			err = lifecycle.Resume(ctx, c, ns, "app")
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app"}, &p))
			assert.False(t, p.Spec.Paused)
		})
	}
}
