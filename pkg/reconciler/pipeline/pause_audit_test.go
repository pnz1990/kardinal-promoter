// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// auditsOf are the AuditEvents with action, oldest first by name order of
// creation in the test.
func auditsOf(t *testing.T, c client.Client, action string) []kardinalv1alpha1.AuditEvent {
	t.Helper()
	var out []kardinalv1alpha1.AuditEvent
	for _, e := range holdAudits(t, c) {
		if e.Spec.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// TestPipelinePauseAudit covers #1939: a pause and a resume of a Pipeline or
// of one fleet are AuditEvents, whichever client made them. A change written
// with spec.lastChange (kardinal pause, resume, the UI) names who, why and
// their actor stamp; one made without it (kubectl) names no actor and says
// where to look. A re-run writes nothing new, and the first record (an
// upgrade, a Pipeline created paused) is not audited.
func TestPipelinePauseAudit(t *testing.T) {
	ctx := context.Background()
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Environments[1].Fleet = &kardinalv1alpha1.FleetSpec{}
	c := newClientWithIndex(newPipelineScheme(), p)
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC) }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}
	reconcile := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
	}
	bump := func() { // the API server's generation, which the fake client does not keep
		var cur kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(ctx, req.NamespacedName, &cur))
		cur.Generation++
		require.NoError(t, c.Update(ctx, &cur))
	}
	reconcile()
	assert.Empty(t, auditsOf(t, c, pipeline.AuditActionPipelinePaused), "the first record is not audited")

	alice := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "alice"}
	require.NoError(t, lifecycle.Pause(ctx, c, "default", "app",
		&kardinalv1alpha1.PipelineChange{By: "alice", Reason: "INC-7: bad config in prod", Actor: alice}))
	bump()
	reconcile()
	reconcile()
	paused := auditsOf(t, c, pipeline.AuditActionPipelinePaused)
	require.Len(t, paused, 1, "once")
	assert.Equal(t, "alice paused pipeline app: INC-7: bad config in prod", paused[0].Spec.Message)
	assert.Equal(t, alice, paused[0].Spec.Actor)
	assert.Equal(t, "app", paused[0].Spec.PipelineName)

	// kubectl patch spec.paused=false: no record of who.
	var cur kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, req.NamespacedName, &cur))
	cur.Spec.Paused = false
	cur.Generation++
	require.NoError(t, c.Update(ctx, &cur))
	reconcile()
	resumed := auditsOf(t, c, pipeline.AuditActionPipelineResumed)
	require.Len(t, resumed, 1)
	assert.Nil(t, resumed[0].Spec.Actor)
	assert.Contains(t, resumed[0].Spec.Message, "pipeline app resumed (spec changed without kardinal; the Kubernetes audit log names who)")

	// A fleet.
	bob := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob"}
	require.NoError(t, lifecycle.SetFleetPaused(ctx, c, "default", "app", "prod", true,
		&kardinalv1alpha1.PipelineChange{By: "bob", Actor: bob}))
	bump()
	reconcile()
	paused = auditsOf(t, c, pipeline.AuditActionPipelinePaused)
	require.Len(t, paused, 2)
	var fleet kardinalv1alpha1.AuditEvent
	for _, e := range paused {
		if e.Spec.Environment == "prod" {
			fleet = e
		}
	}
	assert.Equal(t, "bob paused fleet prod of pipeline app", fleet.Spec.Message)
	assert.Equal(t, bob, fleet.Spec.Actor)
}

// TestPipelinePauseAudit_UpgradeRecordsNothing: a Pipeline already paused
// when the controller first records it (an upgrade) writes no
// PipelinePaused: nobody paused it now.
func TestPipelinePauseAudit_UpgradeRecordsNothing(t *testing.T) {
	p := makePipelineWithEnvs("app", "default", "test")
	p.Spec.Paused = true
	c := newClientWithIndex(newPipelineScheme(), p)
	r := &pipeline.Reconciler{Client: c, Now: time.Now}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}})
	require.NoError(t, err)
	assert.Empty(t, auditsOf(t, c, pipeline.AuditActionPipelinePaused))
	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(p), &got))
	require.NotNil(t, got.Status.ObservedPaused)
	assert.True(t, *got.Status.ObservedPaused)
}

// TestPipelineHolds_ReleaseNamesWho (#1939): a hold's HoldCreated names its
// actor stamp, and its HoldReleased the person kardinal unhold recorded in
// spec.lastChange, with the reason.
func TestPipelineHolds_ReleaseNamesWho(t *testing.T) {
	ctx := context.Background()
	at := metav1.NewTime(time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC))
	alice := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "alice"}
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "app-rollback-1", Reason: "INC-42",
		CreatedBy: "alice", CreatedAt: &at, Actor: alice}}
	c := newClientWithIndex(newPipelineScheme(), p, holdBundle("app-rollback-1"))
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return at.Time }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	created := auditsOf(t, c, pipeline.AuditActionHoldCreated)
	require.Len(t, created, 1)
	assert.Equal(t, alice, created[0].Spec.Actor)

	carol := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "carol"}
	_, err = lifecycle.ReleaseHold(ctx, c, "default", "app", "prod",
		&kardinalv1alpha1.PipelineChange{By: "carol", Reason: "fix verified", Actor: carol})
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	released := auditsOf(t, c, pipeline.AuditActionHoldReleased)
	require.Len(t, released, 1)
	assert.Contains(t, released[0].Spec.Message, "released by carol: fix verified")
	assert.Equal(t, carol, released[0].Spec.Actor)
}

// TestPipelinePauseAudit_LastChangeIsConsumedOnce covers the QA finding on
// #2001 (B1): spec.lastChange names who made the change it was written with,
// once. A later kubectl change that leaves it in place is recorded with no
// actor, not with the earlier record's; a record for another fleet names no
// one; and a stamp naming someone other than by is dropped.
func TestPipelinePauseAudit_LastChangeIsConsumedOnce(t *testing.T) {
	ctx := context.Background()
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Environments[1].Fleet = &kardinalv1alpha1.FleetSpec{}
	c := newClientWithIndex(newPipelineScheme(), p)
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC) }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}
	reconcile := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
	}
	kubectl := func(edit func(*kardinalv1alpha1.Pipeline)) {
		t.Helper()
		var cur kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(ctx, req.NamespacedName, &cur))
		edit(&cur)
		cur.Generation++
		require.NoError(t, c.Update(ctx, &cur))
	}
	reconcile()
	alice := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "alice"}
	require.NoError(t, lifecycle.Pause(ctx, c, "default", "app", &kardinalv1alpha1.PipelineChange{By: "alice", Actor: alice}))
	kubectl(func(*kardinalv1alpha1.Pipeline) {})
	reconcile()
	kubectl(func(cur *kardinalv1alpha1.Pipeline) { cur.Spec.Paused = false })
	reconcile()
	kubectl(func(cur *kardinalv1alpha1.Pipeline) { cur.Spec.Paused = true })
	reconcile()
	paused := auditsOf(t, c, pipeline.AuditActionPipelinePaused)
	require.Len(t, paused, 2)
	var byAlice, byNobody int
	for _, e := range paused {
		if e.Spec.Actor != nil && e.Spec.Actor.Principal == "alice" {
			byAlice++
		} else if e.Spec.Actor == nil {
			byNobody++
		}
	}
	assert.Equal(t, 1, byAlice, "alice's pause")
	assert.Equal(t, 1, byNobody, "the later kubectl pause names no one")

	// A record for another fleet does not name the pause of this one.
	kubectl(func(cur *kardinalv1alpha1.Pipeline) {
		cur.Spec.PausedFleets = []string{"prod"}
		cur.Spec.LastChange = &kardinalv1alpha1.PipelineChange{Action: kardinalv1alpha1.PipelineChangePauseFleet,
			Target: "staging", By: "bob", Actor: &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob"}}
	})
	reconcile()
	for _, e := range auditsOf(t, c, pipeline.AuditActionPipelinePaused) {
		if e.Spec.Environment == "prod" {
			assert.Nil(t, e.Spec.Actor, "a record for staging names no one for prod")
		}
	}

	// A stamp of someone else than by is not recorded.
	kubectl(func(cur *kardinalv1alpha1.Pipeline) {
		cur.Spec.PausedFleets = nil
		cur.Spec.LastChange = &kardinalv1alpha1.PipelineChange{Action: kardinalv1alpha1.PipelineChangeResumeFleet,
			Target: "prod", By: "carol", Actor: &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "mallory"}}
	})
	reconcile()
	resumed := auditsOf(t, c, pipeline.AuditActionPipelineResumed)
	require.NotEmpty(t, resumed)
	for _, e := range resumed {
		if e.Spec.Environment == "prod" {
			assert.Contains(t, e.Spec.Message, "carol resumed fleet prod")
			assert.Nil(t, e.Spec.Actor, "mallory's stamp on carol's record is dropped")
		}
	}
}

// TestPipelineHolds_StaleReleaseRecord (B1): after alice's unhold of prod, a
// later hold of prod removed with kubectl is not attributed to her.
func TestPipelineHolds_StaleReleaseRecord(t *testing.T) {
	ctx := context.Background()
	at := metav1.NewTime(time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC))
	hold := func(created metav1.Time) kardinalv1alpha1.EnvironmentHold {
		return kardinalv1alpha1.EnvironmentHold{Environment: "prod", Bundle: "app-rollback-1", Reason: "INC-42",
			CreatedBy: "alice", CreatedAt: &created}
	}
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{hold(at)}
	c := newClientWithIndex(newPipelineScheme(), p, holdBundle("app-rollback-1"))
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return at.Time }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}
	reconcile := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
	}
	reconcile()
	_, err := lifecycle.ReleaseHold(ctx, c, "default", "app", "prod", &kardinalv1alpha1.PipelineChange{By: "alice"})
	require.NoError(t, err)
	reconcile()
	var cur kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, req.NamespacedName, &cur))
	cur.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{hold(metav1.NewTime(at.Add(time.Hour)))}
	require.NoError(t, c.Update(ctx, &cur))
	reconcile()
	require.NoError(t, c.Get(ctx, req.NamespacedName, &cur))
	cur.Spec.Holds = nil // kubectl
	require.NoError(t, c.Update(ctx, &cur))
	reconcile()
	released := auditsOf(t, c, pipeline.AuditActionHoldReleased)
	require.Len(t, released, 2)
	n := 0
	for _, e := range released {
		if strings.Contains(e.Spec.Message, "released by alice") {
			n++
		}
	}
	assert.Equal(t, 1, n, "only the first release is alice's: %v", released)
}
