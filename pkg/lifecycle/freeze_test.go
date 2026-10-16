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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

	// OnlyOwn (a caller with a namespace's rights in the UI): someone else's
	// freeze is neither changed nor lifted.
	_, _, err = Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeAgents, Namespace: "payments", Reason: "mine",
		For: time.Minute, CreatedBy: "mallory", OnlyOwn: true, Now: now})
	assert.True(t, errors.Is(err, ErrForbidden), "someone else's freeze: %v", err)
	var kept v1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-agents-payments"}, &kept))
	assert.Equal(t, "x", kept.Spec.Reason, "unchanged")
	assert.True(t, kept.Spec.End.IsZero(), "unchanged")
	_, _, err = Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeAgents, Namespace: "payments", Reason: "wider",
		CreatedBy: "a", OnlyOwn: true, Now: now})
	require.NoError(t, err, "its own freeze")
	assert.True(t, errors.Is(Unfreeze(ctx, c, "kardinal-freeze-agents-payments", "mallory", nil), ErrForbidden))
	require.NoError(t, Unfreeze(ctx, c, "kardinal-freeze-agents-payments", "a", nil), "its own freeze")

	assert.True(t, errors.Is(Unfreeze(ctx, c, "kardinal-freeze-everyone", "", nil), ErrConflict), "never deletes a plain window")
	require.NoError(t, Unfreeze(ctx, c, "kardinal-freeze-agents", "", nil))
	assert.True(t, errors.Is(Unfreeze(ctx, c, "kardinal-freeze-agents", "", nil), ErrNotFound))
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
	// Unfrozen: kardinal unfreeze wrote spec.freeze.lifted (its status is a
	// generation behind) and deleted it; the finalizer keeps it until
	// FreezeLifted is recorded. It is not exempted from: it no longer holds.
	lifted := window("d-lifted", false)
	lifted.Generation = 2
	lifted.Spec.Freeze.Lifted = &v1alpha1.FreezeLift{By: "carol"}
	lifted.Finalizers = []string{"kardinal.io/freeze-audit"}
	lifted.DeletionTimestamp = &metav1.Time{Time: now.Add(-time.Second)}
	// Deleted with kubectl: no lift mark, the finalizer keeps it (#2034).
	kubectlDeleted := window("f-kubectl", true)
	kubectlDeleted.Finalizers = []string{"kardinal.io/freeze-audit"}
	kubectlDeleted.DeletionTimestamp = &metav1.Time{Time: now.Add(-time.Second)}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(window("a-cluster", true), window("b-billing", true, "billing"),
		window("c-ended", false), lifted, kubectlDeleted).Build()

	// The CLI, UI and MCP list freezes through ListFreezes: no freeze being
	// deleted is listed (#2034).
	listed, err := ListFreezes(ctx, c)
	require.NoError(t, err)
	var listedNames []string
	for _, f := range listed {
		listedNames = append(listedNames, f.Name)
	}
	assert.Equal(t, []string{"a-cluster", "b-billing", "c-ended"}, listedNames)

	stamp := &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}
	names, err := ExemptFromFreezes(ctx, c, ExemptRequest{Namespace: "payments", Pipeline: "checkout", Environment: "prod",
		Reason: "hotfix", CreatedBy: "bob", Actor: stamp, Now: now})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-cluster"}, names)
	var got v1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "a-cluster"}, &got))
	require.Len(t, got.Spec.Freeze.Exemptions, 1, "the expired one is dropped")
	e := got.Spec.Freeze.Exemptions[0]
	assert.Equal(t, "bob", e.CreatedBy)
	assert.Equal(t, "prod", e.Environment)
	assert.Equal(t, stamp, e.Actor, "the stamp is recorded")
	assert.Equal(t, now.Add(DefaultExemption), e.ExpiresAt.UTC())

	_, err = ExemptFromFreezes(ctx, c, ExemptRequest{Namespace: "shop", Pipeline: "p", Reason: "r", Now: now})
	require.NoError(t, err, "a cluster freeze selects every namespace")
	_, err = ExemptFromFreezes(ctx, c, ExemptRequest{Namespace: "shop", Pipeline: "p", Now: now})
	assert.True(t, errors.Is(err, ErrInvalid), "a reason is required")

	empty := fake.NewClientBuilder().WithScheme(s).WithObjects(window("c-ended", false)).Build()
	_, err = ExemptFromFreezes(ctx, empty, ExemptRequest{Namespace: "payments", Pipeline: "checkout", Reason: "r", Now: now})
	assert.True(t, errors.Is(err, ErrNotFound))
}

// TestUnfreeze_RecordsWhoLifted (#1939): kardinal unfreeze writes who lifts
// the freeze (spec.freeze.lifted) before it deletes the window, for the
// FreezeLifted AuditEvent the ChangeWindow reconciler writes.
func TestUnfreeze_RecordsWhoLifted(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	var lifted *v1alpha1.FreezeLift
	c := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			var cw v1alpha1.ChangeWindow
			require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(obj), &cw))
			lifted = cw.Spec.Freeze.Lifted
			return cl.Delete(ctx, obj, opts...)
		}}).Build()
	_, _, err := Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeEveryone, Reason: "INC-1", CreatedBy: "alice",
		Stamp: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "alice"}, Now: time.Now()})
	require.NoError(t, err)
	var cw v1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-everyone"}, &cw))
	assert.Equal(t, "alice", cw.Spec.Freeze.Actor.Principal, "who froze is stamped")

	bob := &v1alpha1.FreezeLift{By: "bob", Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}}
	require.NoError(t, Unfreeze(ctx, c, "kardinal-freeze-everyone", "", bob))
	assert.Equal(t, bob, lifted, "written before the delete")
}

// TestUnfreeze_FailedDeleteKeepsTheFreeze (#2034 QA): an unfreeze whose
// delete fails (a role with update but not delete, a timeout) returns the
// error and takes its lift mark back: the freeze stays in force, listed and
// unmarked, and kardinal freeze of the same scope works.
func TestUnfreeze_FailedDeleteKeepsTheFreeze(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	gr := schema.GroupResource{Group: "kardinal.io", Resource: "changewindows"}
	c := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return apierrors.NewForbidden(gr, "kardinal-freeze-everyone", errors.New("no delete"))
		}}).Build()
	_, _, err := Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeEveryone, Reason: "INC-1", CreatedBy: "alice", Now: time.Now()})
	require.NoError(t, err)

	err = Unfreeze(ctx, c, "kardinal-freeze-everyone", "", &v1alpha1.FreezeLift{By: "bob"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete freeze kardinal-freeze-everyone")
	var cw v1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-everyone"}, &cw))
	assert.Nil(t, cw.Spec.Freeze.Lifted, "the mark is taken back")
	listed, err := ListFreezes(ctx, c)
	require.NoError(t, err)
	assert.Len(t, listed, 1, "still listed")

	// A stale mark (an older controller, or a failed take-back) is cleared
	// by freezing again.
	cw.Spec.Freeze.Lifted = &v1alpha1.FreezeLift{By: "bob"}
	require.NoError(t, c.Update(ctx, &cw))
	_, created, err := Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeEveryone, Reason: "INC-2", CreatedBy: "alice", Now: time.Now()})
	require.NoError(t, err)
	assert.False(t, created)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-everyone"}, &cw))
	assert.Nil(t, cw.Spec.Freeze.Lifted)
	assert.Equal(t, "INC-2", cw.Spec.Reason)
}

// TestFreeze_RefusesAFreezeBeingLifted (#2034 QA): freezing a scope whose
// freeze is being deleted (kept by its audit finalizer) is a conflict that
// says so, not an update of a window about to vanish.
func TestFreeze_RefusesAFreezeBeingLifted(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).Build()
	_, _, err := Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeAgents, Namespace: "payments", Reason: "INC-1", CreatedBy: "alice", Now: time.Now()})
	require.NoError(t, err)
	var cw v1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-agents-payments"}, &cw))
	cw.Finalizers = []string{"kardinal.io/freeze-audit"}
	require.NoError(t, c.Update(ctx, &cw))
	require.NoError(t, c.Delete(ctx, &cw))

	_, _, err = Freeze(ctx, c, FreezeRequest{Actors: v1alpha1.FreezeAgents, Namespace: "payments", Reason: "INC-2", CreatedBy: "alice", Now: time.Now()})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflict), "%v", err)
	assert.Contains(t, err.Error(), "freeze kardinal-freeze-agents-payments is being lifted; try again in a few seconds")
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-agents-payments"}, &cw))
	assert.Equal(t, "INC-1", cw.Spec.Reason, "not updated")
}
