// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
)

// #1428: with onHealthFailure: rollback the step turns RollingBack and the
// rollback Bundle is created. When the Bundle reconciler sees the RollingBack
// step before its cache has the rollback Bundle, the Bundle turns Failed.
// Once the rollback Bundle is seen, the failing Bundle ends Superseded, the
// same as when the rollback Bundle is seen first: the rollback Bundle's
// creation re-queues it, and a Failed Bundle whose failing environments are
// all RollingBack yields to the newer Bundle.
//
// Covers ONFAIL-ROLLBACK-04.
func TestLifecycle_RollingBackBundleSupersededWhenRollbackSeenLate(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-time.Hour)
	v1 := lcBundle("app-v1", "image", "Promoting", t0)
	v1.Status.GraphRef = "app-app-v1"
	c := lcClient(lcPipeline("app", lcEnvs("test", "prod")...), v1,
		lcStep("app-v1", "test", "s-test", "Verified"),
		lcStep("app-v1", "prod", "s-prod", "RollingBack"))
	r := &bundle.Reconciler{Client: c}

	// The race: the step is RollingBack, the rollback Bundle is not cached yet.
	lcReconcile(t, r, "app-v1")
	require.Equal(t, "Failed", lcGet(t, c, "app-v1").Status.Phase)

	rb := lcBundle("app-rollback-x", "image", "", t0.Add(time.Minute))
	rb.Labels = map[string]string{lifecycle.LabelRollback: "true"}
	require.NoError(t, c.Create(ctx, rb))

	// The rollback Bundle's create event re-queues the Failed Bundle.
	reqs := r.WaitingSiblings(ctx, rb)
	assert.Contains(t, reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}})

	lcReconcile(t, r, "app-v1")
	got := lcGet(t, c, "app-v1")
	assert.Equal(t, "Superseded", got.Status.Phase)
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready)
	assert.Equal(t, "Superseded", ready.Reason)

	// Idempotent: a second reconcile writes nothing.
	rv := got.ResourceVersion
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, rv, lcGet(t, c, "app-v1").ResourceVersion)
}

// #1428: only a Bundle whose every failing environment is RollingBack yields
// to a newer Bundle while it is still failing. A Bundle with a plain Failed
// step stays Failed, as before: it is the failure evidence until a newer
// Bundle is Verified or the step is retried.
func TestLifecycle_FailedStepBundleNotSupersededWhileFailing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []string
		want   string
	}{
		{name: "rolling back", states: []string{"RollingBack"}, want: "Superseded"},
		{name: "failed", states: []string{"Failed"}, want: "Failed"},
		{name: "aborted", states: []string{"AbortedByAlarm"}, want: "Failed"},
		{name: "rolling back and failed", states: []string{"RollingBack", "Failed"}, want: "Failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t0 := time.Now().UTC().Add(-time.Hour)
			v1 := lcBundle("app-v1", "image", "Failed", t0)
			v1.Status.GraphRef = "app-app-v1"
			envs := []string{"a", "b"}
			c := lcClient(lcPipeline("app", lcEnvs(envs[:len(tc.states)]...)...), v1,
				lcBundle("app-v2", "image", "Promoting", t0.Add(time.Minute)))
			for i, s := range tc.states {
				require.NoError(t, c.Create(context.Background(), lcStep("app-v1", envs[i], "s-"+envs[i], s)))
				lcSetStepState(t, c, "s-"+envs[i], s)
			}
			lcReconcile(t, &bundle.Reconciler{Client: c}, "app-v1")
			assert.Equal(t, tc.want, lcGet(t, c, "app-v1").Status.Phase)
		})
	}
}

// #1349: with maxConcurrentPromotions 1, a Failed Bundle does not take a
// second slot when its failed environment recovers. While another Bundle of
// the Pipeline is Promoting, the Failed Bundle is held: condition
// WaitingForSlot is True, which holds its Graph (spec.bundleName does not
// resolve) and its Pending steps. A recovered step does not move it to
// Promoting. When the slot frees, the hold is lifted and it recovers.
func TestLifecycle_FailedBundleRecoveryWaitsForSlot(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-time.Hour)
	p := lcPipeline("app", lcEnvs("test")...)
	p.Spec.MaxConcurrentPromotions = 1
	a := lcBundle("app-a", "image", "Failed", t0)
	a.Status.GraphRef = "app-app-a"
	c := lcClient(p, a, lcBundle("app-b", "config", "Promoting", t0.Add(time.Minute)),
		lcStep("app-a", "test", "s-a", "Failed"))
	r := &bundle.Reconciler{Client: c}

	held := func(b kardinalv1alpha1.Bundle) bool {
		return meta.IsStatusConditionTrue(b.Status.Conditions, graph.CondBundleWaitingForSlot)
	}

	lcReconcile(t, r, "app-a")
	got := lcGet(t, c, "app-a")
	assert.Equal(t, "Failed", got.Status.Phase)
	assert.True(t, held(got), "a Failed Bundle is held while the cap is full: %v", got.Status.Conditions)

	// The failed step is retried (kro recreated it): the Bundle stays Failed
	// and held, so two Bundles are never Promoting at once.
	lcSetStepState(t, c, "s-a", "Pending")
	res := lcReconcile(t, r, "app-a")
	got = lcGet(t, c, "app-a")
	assert.Equal(t, "Failed", got.Status.Phase, "no second slot")
	assert.True(t, held(got))
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready)
	assert.Equal(t, "WaitingForSlot", ready.Reason)
	assert.Equal(t, "maxConcurrentPromotions (1) reached; waiting for a promoting bundle to finish", ready.Message)
	assert.Zero(t, res.RequeueAfter, "no polling: a sibling's phase change lifts the hold")

	rv := got.ResourceVersion
	lcReconcile(t, r, "app-a")
	assert.Equal(t, rv, lcGet(t, c, "app-a").ResourceVersion, "a held Bundle is not rewritten")

	// The other Bundle finishes; its phase change re-queues the held one.
	var b kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "app-b", Namespace: "default"}, &b))
	b.Status.Phase = "Verified"
	require.NoError(t, c.Status().Update(ctx, &b))
	assert.Contains(t, r.WaitingSiblings(ctx, &b),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "app-a", Namespace: "default"}})

	lcReconcile(t, r, "app-a")
	got = lcGet(t, c, "app-a")
	assert.Equal(t, "Promoting", got.Status.Phase)
	assert.False(t, held(got))
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, graph.CondBundleWaitingForSlot),
		"the hold condition is removed")
}

// #1349: the hold is only for a capped Pipeline, and only while the cap is
// full; a Failed Bundle without a cap, or with a free slot, is never held.
func TestLifecycle_FailedBundleHeldOnlyWhenCapFull(t *testing.T) {
	for _, tc := range []struct {
		name      string
		limit     int
		promoting int
		want      bool
	}{
		{name: "no cap", limit: 0, promoting: 1},
		{name: "free slot", limit: 2, promoting: 1},
		{name: "full", limit: 2, promoting: 2, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t0 := time.Now().UTC().Add(-time.Hour)
			p := lcPipeline("app", lcEnvs("test")...)
			p.Spec.MaxConcurrentPromotions = tc.limit
			a := lcBundle("app-a", "image", "Failed", t0)
			a.Status.GraphRef = "app-app-a"
			c := lcClient(p, a, lcStep("app-a", "test", "s-a", "Failed"))
			for i := 0; i < tc.promoting; i++ {
				require.NoError(t, c.Create(context.Background(),
					lcBundle(fmt.Sprintf("app-c%d", i), "config", "Promoting", t0.Add(time.Duration(i+1)*time.Minute))))
			}
			lcReconcile(t, &bundle.Reconciler{Client: c}, "app-a")
			got := lcGet(t, c, "app-a")
			assert.Equal(t, "Failed", got.Status.Phase)
			assert.Equal(t, tc.want, meta.IsStatusConditionTrue(got.Status.Conditions, graph.CondBundleWaitingForSlot))
		})
	}
}

// #1349 (QA on #1487): a Failed Bundle that a newer Bundle of its type
// replaced is never held for a slot. With cap 1 and steady traffic, every
// abandoned Failed Bundle would otherwise show WaitingForSlot, re-read the
// namespace's Bundles from the API server and wake its steps on every
// sibling change. A newer Bundle that is in flight or Verified both count.
func TestLifecycle_ReplacedFailedBundleNotHeld(t *testing.T) {
	for _, newerPhase := range []string{"Promoting", "Verified", "Available"} {
		t.Run(newerPhase, func(t *testing.T) {
			t0 := time.Now().UTC().Add(-time.Hour)
			p := lcPipeline("app", lcEnvs("test")...)
			p.Spec.MaxConcurrentPromotions = 1
			a := lcBundle("app-v1", "image", "Failed", t0)
			a.Status.GraphRef = "app-app-v1"
			listed := 0
			c := indexedBuilder(newScheme()).
				WithObjects(p, a, lcBundle("app-v2", "image", newerPhase, t0.Add(time.Minute)),
					lcBundle("app-c1", "config", "Promoting", t0.Add(2*time.Minute)),
					lcStep("app-v1", "test", "s-v1", "Failed")).
				WithStatusSubresource(&kardinalv1alpha1.Bundle{}, &kardinalv1alpha1.Pipeline{}, &kardinalv1alpha1.PromotionStep{}).
				Build()
			api := interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
				List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					listed++
					return cl.List(ctx, list, opts...)
				},
			})
			r := &bundle.Reconciler{Client: c, APIReader: api}
			res := lcReconcile(t, r, "app-v1")
			got := lcGet(t, c, "app-v1")
			assert.Equal(t, "Failed", got.Status.Phase)
			assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, graph.CondBundleWaitingForSlot))
			assert.Zero(t, res.RequeueAfter)
			assert.Zero(t, listed, "no uncached cap count for a replaced Bundle")

			rv := got.ResourceVersion
			lcReconcile(t, r, "app-v1")
			assert.Equal(t, rv, lcGet(t, c, "app-v1").ResourceVersion, "nothing is written")
		})
	}
}
