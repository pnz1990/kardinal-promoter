// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
)

func freezeAudits(t *testing.T, c client.Client, ns string) map[string]kardinalv1alpha1.AuditEvent {
	t.Helper()
	var list kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace(ns)))
	out := map[string]kardinalv1alpha1.AuditEvent{}
	for _, e := range list.Items {
		out[e.Spec.Action] = e
	}
	return out
}

// TestFreezeAudit covers #1939: a freeze's start and its lift are
// AuditEvents naming who did it and their actor stamp. A finalizer keeps
// the window until FreezeLifted is written, so a delete with kubectl is
// recorded too (with an unknown actor); a re-run writes nothing new.
func TestFreezeAudit(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	alice := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "alice"}
	bob := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob"}
	cluster := freezeWindow("kardinal-freeze-everyone", kardinalv1alpha1.FreezeEveryone, nil)
	cluster.UID = "uid-cluster"
	cluster.Spec.Freeze.Actor = alice
	team := freezeWindow("kardinal-freeze-agents-payments", kardinalv1alpha1.FreezeAgents, []string{"payments"})
	team.UID = "uid-team"
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cluster, team).WithStatusSubresource(cluster, team).Build()
	r := &changewindow.Reconciler{Client: c, AuditNamespace: "kardinal-system",
		NowFn: func() time.Time { return at("2026-10-15T09:00:00Z") }}
	reconcile := func(name string) {
		t.Helper()
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
		require.NoError(t, err)
	}
	reconcile(cluster.Name)
	reconcile(cluster.Name)
	reconcile(team.Name)

	got := freezeAudits(t, c, "kardinal-system")
	require.Contains(t, got, changewindow.AuditActionFreezeStarted)
	started := got[changewindow.AuditActionFreezeStarted]
	assert.Equal(t, "alice froze kardinal-freeze-everyone (everyone, every namespace): INC-4211", started.Spec.Message)
	assert.Equal(t, alice, started.Spec.Actor)
	assert.Contains(t, freezeAudits(t, c, "payments"), changewindow.AuditActionFreezeStarted,
		"a freeze of one namespace is audited there")

	var w kardinalv1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), &w))
	assert.Contains(t, w.Finalizers, changewindow.FreezeAuditFinalizer)

	// kardinal unfreeze: lifted, then deleted.
	w.Spec.Freeze.Lifted = &kardinalv1alpha1.FreezeLift{By: "bob", Actor: bob}
	require.NoError(t, c.Update(ctx, &w))
	require.NoError(t, c.Delete(ctx, &w))
	reconcile(cluster.Name)
	lifted := freezeAudits(t, c, "kardinal-system")[changewindow.AuditActionFreezeLifted]
	assert.Equal(t, "bob lifted kardinal-freeze-everyone (everyone, every namespace)", lifted.Spec.Message)
	assert.Equal(t, bob, lifted.Spec.Actor)
	assert.Error(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), &w), "deleted once audited")

	// kubectl delete: no record of who.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(team), &w))
	require.NoError(t, c.Delete(ctx, &w))
	reconcile(team.Name)
	lifted = freezeAudits(t, c, "payments")[changewindow.AuditActionFreezeLifted]
	assert.Nil(t, lifted.Spec.Actor)
	assert.Contains(t, lifted.Spec.Message, "deleted without kardinal unfreeze; the Kubernetes audit log names who")
}
