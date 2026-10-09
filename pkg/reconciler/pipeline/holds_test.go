// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

func holdAudits(t *testing.T, c client.Client) []kardinalv1alpha1.AuditEvent {
	t.Helper()
	var list kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items
}

// TestPipelineHolds (#1528 QA): the Pipeline reconciler writes a HoldCreated
// AuditEvent for a hold added by any client and a HoldReleased one when it
// goes, removes a hold at its expiresAt (HoldReleased says so) and requeues
// for the next expiry; a reconcile that runs again writes nothing new.
func TestPipelineHolds(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	now := t0
	at := metav1.NewTime(t0)
	exp := metav1.NewTime(t0.Add(time.Hour))
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{
		{Environment: "prod", Bundle: "app-rollback-1", Reason: "INC-42", CreatedBy: "alice", CreatedAt: &at, ExpiresAt: &exp},
	}
	c := newClientWithIndex(newPipelineScheme(), p)
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}
	get := func() *kardinalv1alpha1.Pipeline {
		var got kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		return &got
	}

	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, res.RequeueAfter, "back at the expiry")
	ae := holdAudits(t, c)
	require.Len(t, ae, 1)
	assert.Equal(t, "HoldCreated", ae[0].Spec.Action)
	assert.Equal(t, "app-rollback-1", ae[0].Spec.BundleName)
	assert.Equal(t, "prod", ae[0].Spec.Environment)
	assert.Equal(t, "alice held prod on rollback app-rollback-1: INC-42 (expires 2026-10-09T11:00:00Z)", ae[0].Spec.Message)
	assert.Len(t, get().Status.ObservedHolds, 1)

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Len(t, holdAudits(t, c), 1, "idempotent")

	// Expired: removed from the spec, then recorded as released.
	now = t0.Add(time.Hour)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, get().Spec.Holds, "the expired hold is removed")
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	ae = holdAudits(t, c)
	require.Len(t, ae, 2)
	assert.Equal(t, "HoldReleased", ae[1].Spec.Action)
	assert.Contains(t, ae[1].Spec.Message, "expired at 2026-10-09T11:00:00Z, removed by the controller")
	assert.Empty(t, get().Status.ObservedHolds)

	// Another hold, added and removed as kubectl would: both recorded.
	q := get()
	at2 := metav1.NewTime(now)
	q.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{{Environment: "test", Bundle: "app-rollback-2", Reason: "r", CreatedBy: "bob", CreatedAt: &at2}}
	require.NoError(t, c.Update(context.Background(), q))
	res, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "no expiry to wait for")
	q = get()
	q.Spec.Holds = nil
	require.NoError(t, c.Update(context.Background(), q))
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var actions []string
	for _, a := range holdAudits(t, c) {
		if a.Spec.BundleName == "app-rollback-2" {
			actions = append(actions, a.Spec.Action)
			if a.Spec.Action == "HoldReleased" {
				assert.Contains(t, a.Spec.Message, "released (spec.holds entry removed")
			}
		}
	}
	assert.ElementsMatch(t, []string{"HoldCreated", "HoldReleased"}, actions)
}

// failingAuditCreates is a client whose AuditEvent creates fail with an etcd
// timeout while fail is set.
type failingAuditCreates struct {
	client.Client
	fail bool
}

func (f *failingAuditCreates) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*kardinalv1alpha1.AuditEvent); ok && f.fail {
		return apierrors.NewInternalError(errors.New("etcdserver: request timed out"))
	}
	return f.Client.Create(ctx, obj, opts...)
}

// TestPipelineHolds_AuditOutbox (#1552): a HoldCreated AuditEvent whose
// create fails with an etcd timeout is kept in the Pipeline's
// status.pendingAuditEvents, stored with observedHolds, and the Pipeline is
// reconciled again within 5s. Once creates succeed the record is written,
// once, and the outbox is empty.
//
// Covers AUDIT-OUTBOX-01.
func TestPipelineHolds_AuditOutbox(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	at := metav1.NewTime(t0)
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{
		{Environment: "prod", Bundle: "app-rollback-1", Reason: "INC-42", CreatedBy: "alice", CreatedAt: &at},
	}
	c := &failingAuditCreates{Client: newClientWithIndex(newPipelineScheme(), p), fail: true}
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return t0 }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}
	get := func() *kardinalv1alpha1.Pipeline {
		var got kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		return &got
	}

	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, holdAudits(t, c))
	got := get()
	assert.Len(t, got.Status.ObservedHolds, 1, "the hold is recorded")
	require.Len(t, got.Status.PendingAuditEvents, 1, "with its AuditEvent in the outbox")
	assert.Equal(t, "HoldCreated", got.Status.PendingAuditEvents[0].Spec.Action)
	assert.Positive(t, res.RequeueAfter)
	assert.LessOrEqual(t, res.RequeueAfter, 5*time.Second)

	c.fail = false
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	ae := holdAudits(t, c)
	require.Len(t, ae, 1)
	assert.Equal(t, "HoldCreated", ae[0].Spec.Action)
	assert.Empty(t, get().Status.PendingAuditEvents)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Len(t, holdAudits(t, c), 1, "written once")
}

// staleGet serves the Pipeline from a stale copy while stale is set, as a
// lagging informer cache does.
type staleGet struct {
	client.Client
	stale   *kardinalv1alpha1.Pipeline
	creates int
}

func (s *staleGet) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*kardinalv1alpha1.AuditEvent); ok {
		s.creates++
	}
	return s.Client.Create(ctx, obj, opts...)
}

func (s *staleGet) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if p, ok := obj.(*kardinalv1alpha1.Pipeline); ok && s.stale != nil && key.Name == s.stale.Name {
		s.stale.DeepCopyInto(p)
		return nil
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

// TestPipelineHolds_StaleReadStoresNoSecondRecord (#1552 QA): a reconcile
// that reads the Pipeline from before a newer reconcile recorded a hold sees
// the hold as new. Its patch carries the resourceVersion it read, so it gets
// a Conflict, stores nothing in the outbox and runs again shortly: one
// HoldCreated record.
//
// Covers AUDIT-OUTBOX-01.
func TestPipelineHolds_StaleReadStoresNoSecondRecord(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	at := metav1.NewTime(t0)
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{
		{Environment: "prod", Bundle: "app-rollback-1", Reason: "INC-42", CreatedBy: "alice", CreatedAt: &at},
	}
	c := &staleGet{Client: newClientWithIndex(newPipelineScheme(), p)}
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return t0 }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}

	var before kardinalv1alpha1.Pipeline
	require.NoError(t, c.Client.Get(context.Background(), req.NamespacedName, &before))
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, holdAudits(t, c), 1)

	c.stale = before.DeepCopy()
	c.creates = 0
	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err, "a Conflict is retried, not returned")
	assert.Positive(t, res.RequeueAfter)
	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Client.Get(context.Background(), req.NamespacedName, &got))
	assert.Empty(t, got.Status.PendingAuditEvents, "the stale reconcile stored nothing")
	assert.Zero(t, c.creates, "and wrote no AuditEvent again")
	assert.Len(t, holdAudits(t, c), 1, "one HoldCreated")
}
