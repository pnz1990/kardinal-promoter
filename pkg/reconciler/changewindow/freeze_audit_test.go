// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

// TestFreezeAudit_WriteFailures covers the QA finding on #2001 (B2): a
// freeze audit write that fails never keeps the freeze from taking effect
// (its status is written), is retried and reported (a Warning Event), falls
// back to the controller's namespace when the frozen one refuses it, and
// never holds a deleted window past the grace. The FreezeAudited condition
// keeps a re-run from writing again, and FreezeStarted carries the time of
// the freeze.
func TestFreezeAudit_WriteFailures(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	team := freezeWindow("kardinal-freeze-everyone-gone", kardinalv1alpha1.FreezeEveryone, []string{"gone"})
	team.UID = "uid-team"
	team.CreationTimestamp = metav1.NewTime(at("2026-10-15T08:00:00Z"))
	broken := false
	creates := 0
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(team).WithStatusSubresource(team).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*kardinalv1alpha1.AuditEvent); ok {
				creates++
				if broken {
					return apierrors.NewServiceUnavailable("etcd timeout")
				}
				if obj.GetNamespace() == "gone" {
					return apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "gone")
				}
			}
			return cl.Create(ctx, obj, opts...)
		}}).Build()
	rec := events.NewFakeRecorder(20)
	now := at("2026-10-15T09:00:00Z")
	r := &changewindow.Reconciler{Client: c, Recorder: rec, AuditNamespace: "kardinal-system", NowFn: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: team.Name}}

	// The frozen namespace is gone: the record goes to the controller's.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	started := freezeAudits(t, c, "kardinal-system")[changewindow.AuditActionFreezeStarted]
	assert.Equal(t, at("2026-10-15T08:00:00Z"), started.Spec.Timestamp.UTC(), "when it froze, not when it was recorded")
	n := creates
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, n, creates, "recorded once (FreezeAudited)")

	// Every write fails: the status is still written, the reconcile retries.
	other := freezeWindow("kardinal-freeze-agents", kardinalv1alpha1.FreezeAgents, nil)
	other.UID = "uid-other"
	require.NoError(t, c.Create(ctx, other))
	broken = true
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: other.Name}})
	require.NoError(t, err, "an audit write never fails the reconcile")
	assert.Positive(t, res.RequeueAfter)
	var got kardinalv1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(other), &got))
	assert.Equal(t, got.Generation, got.Status.ObservedGeneration, "the freeze's status is written")
	assert.True(t, got.Status.Active)
	assert.Contains(t, <-rec.Events, changewindow.ReasonFreezeAuditFailed)

	// Deleted while writes fail: held for the grace, then let go.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(other), &got))
	require.Contains(t, got.Finalizers, changewindow.FreezeAuditFinalizer)
	require.NoError(t, c.Delete(ctx, &got))
	res, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: other.Name}})
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter, "retried within the grace")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(other), &got), "still there")
	now = got.DeletionTimestamp.Add(3 * time.Minute)
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: other.Name}})
	require.NoError(t, err)
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(other), &got)), "let go after the grace")
}

// TestFreezeAudit_DropsAStampOfSomeoneElse (#2001 QA): a freeze's stamp
// that names someone other than who froze or lifted it is not recorded.
func TestFreezeAudit_DropsAStampOfSomeoneElse(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	cw := freezeWindow("kardinal-freeze-everyone", kardinalv1alpha1.FreezeEveryone, nil) // createdBy alice
	cw.UID = "uid-x"
	cw.Spec.Freeze.Actor = &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "mallory"}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cw).WithStatusSubresource(cw).Build()
	r := &changewindow.Reconciler{Client: c, AuditNamespace: "kardinal-system",
		NowFn: func() time.Time { return at("2026-10-15T09:00:00Z") }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cw.Name}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	started := freezeAudits(t, c, "kardinal-system")[changewindow.AuditActionFreezeStarted]
	assert.Contains(t, started.Spec.Message, "alice froze")
	assert.Nil(t, started.Spec.Actor, "mallory's stamp on alice's freeze is dropped")

	var w kardinalv1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, req.NamespacedName, &w))
	w.Spec.Freeze.Lifted = &kardinalv1alpha1.FreezeLift{By: "bob",
		Actor: &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "mallory"}}
	require.NoError(t, c.Update(ctx, &w))
	require.NoError(t, c.Delete(ctx, &w))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	lifted := freezeAudits(t, c, "kardinal-system")[changewindow.AuditActionFreezeLifted]
	assert.Contains(t, lifted.Spec.Message, "bob lifted")
	assert.Nil(t, lifted.Spec.Actor)
}
