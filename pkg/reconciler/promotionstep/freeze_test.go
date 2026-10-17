// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// activeFreeze is a reconciled, active freeze of actors with exemptions in
// both spec and status.
func activeFreeze(actors string, exemptions ...v1alpha1.FreezeExemption) *v1alpha1.ChangeWindow {
	return &v1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "kardinal-freeze-" + actors, Generation: 1},
		Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.NewTime(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)),
			Reason: "INC-4211", Freeze: &v1alpha1.ChangeWindowFreeze{Actors: actors, CreatedBy: "alice", Exemptions: exemptions}},
		Status: v1alpha1.ChangeWindowStatus{Active: true, ObservedGeneration: 1, Exemptions: exemptions},
	}
}

// liftingFreeze is activeFreeze as an unfreeze leaves it: with marked,
// spec.freeze.lifted written (its status a generation behind); with
// deleted, its delete held by the freeze audit finalizer.
func liftingFreeze(actors string, marked, deleted bool) *v1alpha1.ChangeWindow {
	w := activeFreeze(actors)
	if marked {
		w.Generation = 2
		w.Spec.Freeze.Lifted = &v1alpha1.FreezeLift{By: "carol"}
	}
	if deleted {
		w.Finalizers = []string{"kardinal.io/freeze-audit"}
		w.DeletionTimestamp = &metav1.Time{Time: time.Date(2026, 10, 14, 9, 59, 0, 0, time.UTC)}
	}
	return w
}

// TestFreeze_HoldsStep: an active freeze holds the steps it selects at the
// safe points pause uses (before start, before the next git step), whoever
// started them for stop-the-line and only agents' versions for an agent
// freeze; an exemption lets the step go on and writes one FreezeExempted
// AuditEvent; lifting the freeze lets a held step continue
// (docs/design/18-actor-policy.md section 9.2).
func TestFreeze_HoldsStep(t *testing.T) {
	agent := &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer", Agent: "fixer", Sponsor: "alice"}
	person := &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}
	exemption := v1alpha1.FreezeExemption{Namespace: "default", Pipeline: "nginx-demo", Environment: "test",
		Reason: "hotfix", CreatedBy: "carol", ExpiresAt: metav1.NewTime(time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC))}
	tests := []struct {
		name       string
		state      string
		author     *v1alpha1.Actor
		freeze     *v1alpha1.ChangeWindow
		wantHeld   bool
		wantExempt bool
	}{
		{name: "an agent freeze holds an agent's pending step", author: agent, freeze: activeFreeze("agents"), wantHeld: true},
		{name: "an agent freeze holds an agent's next git step", state: "Promoting", author: agent, freeze: activeFreeze("agents"), wantHeld: true},
		{name: "an agent freeze lets a person's step start", author: person, freeze: activeFreeze("agents")},
		{name: "stop-the-line holds a person's step", author: person, freeze: activeFreeze("everyone"), wantHeld: true},
		{name: "stop-the-line holds an unstamped version", freeze: activeFreeze("everyone"), wantHeld: true},
		{name: "an exemption lets the step through and is audited", author: person,
			freeze: activeFreeze("everyone", exemption), wantExempt: true},
		{name: "no freeze", author: agent},
		// #2034: a freeze being deleted (its audit finalizer keeps it) holds
		// nothing, whether kardinal unfreeze marked it or kubectl deleted it;
		// a mark whose delete failed still holds.
		{name: "a freeze deleted by kardinal unfreeze holds nothing", author: person, freeze: liftingFreeze("everyone", true, true)},
		{name: "a freeze deleted by kubectl holds nothing", author: person, freeze: liftingFreeze("everyone", false, true)},
		{name: "a lift mark without a delete still holds", author: person, freeze: liftingFreeze("everyone", true, false), wantHeld: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			step := makeStep("step-test", "nginx-demo", "bundle-1", "test")
			step.Labels = map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1",
				"kardinal.io/environment": "test"}
			if tc.state == "Promoting" {
				asPromoting(step, makePipeline("nginx-demo"))
			}
			step.Status.State = tc.state
			bundle := makeBundle("bundle-1", "nginx-demo")
			bundle.Spec.Actor = tc.author
			objs := []client.Object{step, makePipeline("nginx-demo"), bundle}
			if tc.freeze != nil {
				objs = append(objs, tc.freeze)
			}
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.ChangeWindow{}).
				WithObjects(objs...).Build()
			r := &promotionstep.Reconciler{
				Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() },
				NowFn:     func() time.Time { return time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC) },
			}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"}}
			res, err := r.Reconcile(ctx, req)
			require.NoError(t, err)
			if tc.wantExempt {
				assert.Positive(t, res.RequeueAfter, "the exemption is recorded first, then the step goes on")
				_, err = r.Reconcile(ctx, req)
				require.NoError(t, err)
			}
			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
			if !tc.wantHeld {
				assert.False(t, strings.HasPrefix(got.Status.Message, "frozen by"), got.Status.Message)
				assert.NotEqual(t, tc.state, got.Status.State, "the step advances: %s", got.Status.Message)
				var events v1alpha1.AuditEventList
				require.NoError(t, c.List(ctx, &events))
				exempted := 0
				for _, e := range events.Items {
					if e.Spec.Action == promotionstep.AuditActionFreezeExempted {
						exempted++
						assert.Contains(t, e.Spec.Message, "by carol")
						assert.Contains(t, e.Spec.Message, "hotfix")
					}
				}
				if tc.wantExempt {
					assert.Equal(t, 1, exempted)
					assert.NotNil(t, meta.FindStatusCondition(got.Status.Conditions, "FreezeExempted"))
				} else {
					assert.Zero(t, exempted)
				}
				return
			}
			assert.True(t, strings.HasPrefix(got.Status.Message, "frozen by "+tc.freeze.Name), got.Status.Message)
			assert.Contains(t, got.Status.Message, "INC-4211")
			// #1974: the hold is in status.frozenBy too, for views.
			require.NotNil(t, got.Status.FrozenBy)
			assert.Equal(t, tc.freeze.Name, got.Status.FrozenBy.Name)
			require.NotNil(t, got.Status.FrozenBy.Since)
			assert.Equal(t, tc.state, got.Status.State)
			assert.Positive(t, res.RequeueAfter)
			assert.Empty(t, got.Status.Outputs, "no git step ran")

			rv := got.ResourceVersion
			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
			assert.Equal(t, rv, got.ResourceVersion, "a held step is not rewritten")

			require.NoError(t, c.Delete(ctx, tc.freeze))
			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
			assert.Nil(t, got.Status.FrozenBy, "after unfreeze the hold is cleared first")
			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
			assert.NotEqual(t, tc.state, got.Status.State, "then the step advances")
		})
	}
}

// TestFreeze_PauseClearsFrozenBy (QA F1 on #2149): a step frozen, then
// paused, then unfrozen shows the pause, not the lifted freeze: the pause
// hold, which runs first, clears status.frozenBy. Once resumed, a freeze
// that still holds records itself again.
//
// Covers FREEZE-PAUSE-01.
func TestFreeze_PauseClearsFrozenBy(t *testing.T) {
	ctx := context.Background()
	p := makePipeline("nginx-demo")
	p.UID = "uid-nginx-demo"
	step := makeStep("step-test", "nginx-demo", "bundle-1", "test")
	step.Labels = map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1",
		"kardinal.io/environment": "test"}
	freeze := activeFreeze("everyone")
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.ChangeWindow{}).
		WithObjects(step, p, makeBundle("bundle-1", "nginx-demo"), freeze).Build()
	r := &promotionstep.Reconciler{
		Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
		NowFn:     func() time.Time { return time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"}}
	get := func() v1alpha1.PromotionStep {
		var got v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
		return got
	}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, get().Status.FrozenBy, "frozen")

	pause := lifecycle.DesiredFreezeGate(p)
	require.NoError(t, c.Create(ctx, pause))
	require.NoError(t, c.Delete(ctx, freeze))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	got := get()
	assert.Nil(t, got.Status.FrozenBy, "the lifted freeze is not shown while paused")
	assert.Equal(t, lifecycle.PausedMessage("nginx-demo"), got.Status.Message)
	assert.Empty(t, got.Status.State, "still held, by the pause")

	// A freeze that still holds after resume records itself again.
	again := activeFreeze("everyone")
	require.NoError(t, c.Create(ctx, again))
	require.NoError(t, c.Status().Update(ctx, again))
	require.NoError(t, c.Delete(ctx, pause))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	got = get()
	require.NotNil(t, got.Status.FrozenBy)
	assert.Equal(t, again.Name, got.Status.FrozenBy.Name)
}

// TestFreeze_ExemptionAuditedOnce: a step that goes on under an exemption,
// reconciled again, writes no second FreezeExempted record.
func TestFreeze_ExemptionAuditedOnce(t *testing.T) {
	ctx := context.Background()
	ex := v1alpha1.FreezeExemption{Namespace: "default", Pipeline: "nginx-demo", Reason: "hotfix", CreatedBy: "carol",
		ExpiresAt: metav1.NewTime(time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC))}
	step := makeStep("step-test", "nginx-demo", "bundle-1", "test")
	step.Labels = map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1",
		"kardinal.io/environment": "test"}
	asPromoting(step, makePipeline("nginx-demo"))
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.ChangeWindow{}).
		WithObjects(step, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"), activeFreeze("everyone", ex)).Build()
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
		NowFn:     func() time.Time { return time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC) }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"}}
	for range 3 {
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
	}
	var events v1alpha1.AuditEventList
	require.NoError(t, c.List(ctx, &events))
	n := 0
	for _, e := range events.Items {
		if e.Spec.Action == promotionstep.AuditActionFreezeExempted {
			n++
		}
	}
	assert.Equal(t, 1, n)
	// The exemption is recorded on the step once, for its receipt (#2060).
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	require.Len(t, got.Status.FreezeExemptions, 1)
	x := got.Status.FreezeExemptions[0]
	assert.Equal(t, "everyone", x.Actors)
	assert.Equal(t, "carol", x.Exemption.CreatedBy)
	assert.Equal(t, "hotfix", x.Exemption.Reason)
	assert.Equal(t, ex.ExpiresAt.UTC(), x.Until.UTC())
}

// TestFreeze_EveryExemptingFreezeAudited covers the QA finding on #1869
// (F5): when several freezes hold a step and each has an exemption for it,
// the step goes on and each freeze's exemption gets its FreezeExempted
// AuditEvent, not just the first.
func TestFreeze_EveryExemptingFreezeAudited(t *testing.T) {
	ctx := context.Background()
	ex := v1alpha1.FreezeExemption{Namespace: "default", Pipeline: "nginx-demo", Reason: "hotfix", CreatedBy: "carol",
		ExpiresAt: metav1.NewTime(time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC))}
	cluster := activeFreeze("everyone", ex)
	team := activeFreeze("everyone", ex)
	team.Name = "kardinal-freeze-everyone-default"
	team.Spec.Freeze.Namespaces = []string{"default"}
	step := makeStep("step-test", "nginx-demo", "bundle-1", "test")
	step.Labels = map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1",
		"kardinal.io/environment": "test"}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.ChangeWindow{}).
		WithObjects(step, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"), cluster, team).Build()
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
		NowFn:     func() time.Time { return time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC) }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"}}
	for range 3 {
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
	}
	var events v1alpha1.AuditEventList
	require.NoError(t, c.List(ctx, &events))
	windows := map[string]int{}
	for _, e := range events.Items {
		if e.Spec.Action == promotionstep.AuditActionFreezeExempted {
			for _, w := range []string{cluster.Name, team.Name} {
				if strings.Contains(e.Spec.Message, "freeze "+w+" ") {
					windows[w]++
				}
			}
		}
	}
	assert.Equal(t, map[string]int{cluster.Name: 1, team.Name: 1}, windows, "one record per freeze, once")
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	assert.NotEqual(t, "", got.Status.State, "the step went on: %s", got.Status.Message)
	assert.False(t, strings.HasPrefix(got.Status.Message, "frozen by"), got.Status.Message)
}
