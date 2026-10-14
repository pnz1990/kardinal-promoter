// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestFreeze_CreateUpdateUnfreeze: kardinal freeze writes one window per
// scope, a repeat updates its reason and end (start and createdBy stay),
// and unfreeze deletes only freezes.
func TestFreeze_CreateUpdateUnfreeze(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	plain := &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-freeze-everyone"},
		Spec: v1alpha1.ChangeWindowSpec{Type: "blackout"}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(plain).Build()
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

	cw, created, err := Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeAgents, Reason: "INC-1", CreatedBy: "alice", Now: now})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "kardinal-freeze-agents", cw.Name)
	assert.True(t, cw.Spec.End.IsZero(), "until unfreeze")
	assert.Equal(t, "alice", cw.Spec.Freeze.CreatedBy)
	assert.Empty(t, cw.Spec.Freeze.Namespaces)

	cw, created, err = Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeAgents, Reason: "INC-1, wider", For: 2 * time.Hour,
		CreatedBy: "bob", Now: now.Add(time.Hour)})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "INC-1, wider", cw.Spec.Reason)
	assert.Equal(t, now.Add(3*time.Hour), cw.Spec.End.UTC())
	assert.Equal(t, now, cw.Spec.Start.UTC(), "start stays")
	assert.Equal(t, "alice", cw.Spec.Freeze.CreatedBy, "who froze stays")

	ns, _, err := Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeAgents, Namespace: "payments", Reason: "x", CreatedBy: "a", Now: now})
	require.NoError(t, err)
	assert.Equal(t, "kardinal-freeze-agents-payments", ns.Name)
	assert.Equal(t, []string{"payments"}, ns.Spec.Freeze.Namespaces)

	_, _, err = Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeEveryone, Reason: "x", CreatedBy: "a", Now: now})
	assert.True(t, errors.Is(err, ErrConflict), "a same-named window that is not a freeze: %v", err)
	_, _, err = Freeze(ctx, c, FreezeRequest{Actors: "robots", Reason: "x", Now: now})
	assert.True(t, errors.Is(err, ErrInvalid))
	_, _, err = Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeAgents, Now: now})
	assert.True(t, errors.Is(err, ErrInvalid), "a reason is required")

	list, err := ListFreezes(ctx, c)
	require.NoError(t, err)
	require.Len(t, list, 2)

	assert.True(t, errors.Is(Unfreeze(ctx, c, "kardinal-freeze-everyone"), ErrConflict), "never deletes a plain window")
	require.NoError(t, Unfreeze(ctx, c, "kardinal-freeze-agents"))
	assert.True(t, errors.Is(Unfreeze(ctx, c, "kardinal-freeze-agents"), ErrNotFound))
	var still v1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-everyone"}, &still))
}

// TestExemptFromFreezes: an exemption is added to every active freeze that
// selects the namespace, expired ones are dropped, and with no active freeze
// there is nothing to let through.
func TestExemptFromFreezes(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	old := v1alpha1.FreezeExemption{Namespace: "payments", Pipeline: "x", Reason: "old", CreatedBy: "z",
		ExpiresAt: metav1.NewTime(now.Add(-time.Minute))}
	window := func(name string, active bool, ns ...string) *v1alpha1.ChangeWindow {
		return &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
			Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.NewTime(now.Add(-time.Hour)),
				Freeze: &v1alpha1.ChangeWindowFreeze{Actors: v1alpha1.FreezeAgents, Namespaces: ns, CreatedBy: "alice",
					Exemptions: []v1alpha1.FreezeExemption{old}}},
			Status: v1alpha1.ChangeWindowStatus{Active: active, ObservedGeneration: 1}}
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(window("a-cluster", true), window("b-billing", true, "billing"),
		window("c-ended", false)).Build()

	names, err := ExemptFromFreezes(ctx, c, ExemptRequest{Namespace: "payments", Pipeline: "checkout", Environment: "prod",
		Reason: "hotfix", CreatedBy: "bob", Now: now})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-cluster"}, names)
	var got v1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "a-cluster"}, &got))
	require.Len(t, got.Spec.Freeze.Exemptions, 1, "the expired one is dropped")
	e := got.Spec.Freeze.Exemptions[0]
	assert.Equal(t, "bob", e.CreatedBy)
	assert.Equal(t, "prod", e.Environment)
	assert.Equal(t, now.Add(DefaultExemption), e.ExpiresAt.UTC())

	_, err = ExemptFromFreezes(ctx, c, ExemptRequest{Namespace: "shop", Pipeline: "p", Reason: "r", Now: now})
	require.NoError(t, err, "a cluster freeze selects every namespace")
	_, err = ExemptFromFreezes(ctx, c, ExemptRequest{Namespace: "shop", Pipeline: "p", Now: now})
	assert.True(t, errors.Is(err, ErrInvalid), "a reason is required")

	empty := fake.NewClientBuilder().WithScheme(s).WithObjects(window("c-ended", false)).Build()
	_, err = ExemptFromFreezes(ctx, empty, ExemptRequest{Namespace: "payments", Pipeline: "checkout", Reason: "r", Now: now})
	assert.True(t, errors.Is(err, ErrNotFound))
}
