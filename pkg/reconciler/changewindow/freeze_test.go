// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
)

func freezeWindow(name, actors string, namespaces []string, exemptions ...kardinalv1alpha1.FreezeExemption) *kardinalv1alpha1.ChangeWindow {
	return &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec: kardinalv1alpha1.ChangeWindowSpec{Type: changewindow.TypeBlackout, Start: metav1.NewTime(at("2026-10-14T09:00:00Z")),
			Reason: "INC-4211",
			Freeze: &kardinalv1alpha1.ChangeWindowFreeze{Actors: actors, Namespaces: namespaces, CreatedBy: "alice",
				Exemptions: exemptions}},
	}
}

// lifting marks w as an unfreeze leaves it: with marked, spec.freeze.lifted
// (kardinal unfreeze writes it, kubectl delete does not); with deleted, a
// deletion its audit finalizer holds.
func lifting(w *kardinalv1alpha1.ChangeWindow, marked, deleted bool) *kardinalv1alpha1.ChangeWindow {
	if marked {
		w.Spec.Freeze.Lifted = &kardinalv1alpha1.FreezeLift{By: "carol"}
	}
	if deleted {
		w.Finalizers = []string{changewindow.FreezeAuditFinalizer}
		w.DeletionTimestamp = &metav1.Time{Time: at("2026-10-14T10:00:00Z")}
	}
	return w
}

func exempt(env, bundle, expires string) kardinalv1alpha1.FreezeExemption {
	return kardinalv1alpha1.FreezeExemption{Namespace: "payments", Pipeline: "checkout", Environment: env, Bundle: bundle,
		Reason: "hotfix", CreatedBy: "bob", ExpiresAt: metav1.NewTime(at(expires))}
}

func inNamespace(ns string, e kardinalv1alpha1.FreezeExemption) kardinalv1alpha1.FreezeExemption {
	e.Namespace = ns
	return e
}

func forPipeline(p string, e kardinalv1alpha1.FreezeExemption) kardinalv1alpha1.FreezeExemption {
	e.Pipeline = p
	return e
}

func byAgent(e kardinalv1alpha1.FreezeExemption) kardinalv1alpha1.FreezeExemption {
	e.CreatedBy = "system:serviceaccount:ci:fixer"
	e.Actor = &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Principal: e.CreatedBy, Agent: "fixer", Sponsor: "alice"}
	return e
}

// TestEvaluate_FreezeWithoutEnd: a freeze may omit end and is then active
// from start on; a blackout that is not a freeze still needs one.
func TestEvaluate_FreezeWithoutEnd(t *testing.T) {
	f := freezeWindow("f", kardinalv1alpha1.FreezeAgents, nil)
	before := changewindow.Evaluate(f.Spec, at("2026-10-14T08:00:00Z"))
	assert.False(t, before.Active)
	assert.Equal(t, at("2026-10-14T09:00:00Z"), before.Next)
	during := changewindow.Evaluate(f.Spec, at("2027-01-01T00:00:00Z"))
	assert.True(t, during.Active)
	assert.True(t, during.Next.IsZero(), "no end: nothing to wait for")
	assert.NoError(t, during.Err)

	plain := f.Spec
	plain.Freeze = nil
	assert.Error(t, changewindow.Evaluate(plain, at("2026-10-14T10:00:00Z")).Err)
}

// TestReconciler_FreezeStatus: the ChangeWindow reconciler writes the
// time-based state of a freeze to its status (active, the exemptions in
// effect, the next transition, the observed generation) and requeues at the
// earliest exemption expiry; re-running it writes nothing.
func TestReconciler_FreezeStatus(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	cw := freezeWindow("kardinal-freeze-agents", kardinalv1alpha1.FreezeAgents, nil,
		exempt("prod", "", "2026-10-14T10:30:00Z"), exempt("", "", "2026-10-14T09:30:00Z"))
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cw).WithStatusSubresource(cw).Build()
	now := at("2026-10-14T10:00:00Z")
	r := &changewindow.Reconciler{Client: c, NowFn: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cw.Name}}

	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Minute+time.Second, res.RequeueAfter, "requeue at the 10:30 expiry")
	var got kardinalv1alpha1.ChangeWindow
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.True(t, got.Status.Active)
	require.Len(t, got.Status.Exemptions, 1, "the 09:30 one expired")
	assert.Equal(t, "prod", got.Status.Exemptions[0].Environment)
	require.NotNil(t, got.Status.NextTransition)
	assert.Equal(t, at("2026-10-14T10:30:00Z"), got.Status.NextTransition.UTC())
	assert.Equal(t, got.Generation, got.Status.ObservedGeneration)
	rv := got.ResourceVersion

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, rv, got.ResourceVersion, "idempotent")

	now = at("2026-10-14T10:30:01Z")
	res, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Empty(t, got.Status.Exemptions)
	assert.Nil(t, got.Status.NextTransition)
	assert.Zero(t, res.RequeueAfter)
}

// TestReconciler_FreezeExemptionCap covers the QA finding on #1869 (F3): an
// exemption counts at most --freeze-exemption-max-minutes from when the
// reconciler first saw it, whatever its expiresAt says; the record keeps
// that first sighting across reconciles, and the step's message shows the
// capped end.
func TestReconciler_FreezeExemptionCap(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	cw := freezeWindow("kardinal-freeze-everyone", kardinalv1alpha1.FreezeEveryone, nil,
		exempt("prod", "", "2027-10-14T10:00:00Z")) // a year
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cw).WithStatusSubresource(cw).Build()
	now := at("2026-10-14T10:00:00Z")
	r := &changewindow.Reconciler{Client: c, NowFn: func() time.Time { return now }, MaxExemption: 2 * time.Hour}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cw.Name}}
	get := func() kardinalv1alpha1.ChangeWindow {
		var got kardinalv1alpha1.ChangeWindow
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		return got
	}

	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	got := get()
	require.Len(t, got.Status.Exemptions, 1)
	require.Len(t, got.Status.ExemptionRecords, 1)
	assert.Equal(t, now, got.Status.ExemptionRecords[0].FirstSeen.UTC())
	assert.Equal(t, at("2026-10-14T12:00:00Z"), got.Status.ExemptionRecords[0].Until.UTC(), "the cap, not a year")
	assert.Equal(t, at("2026-10-14T12:00:00Z"), got.Status.NextTransition.UTC())
	assert.Equal(t, time.Hour, res.RequeueAfter, "bounded by maxRequeue; nextTransition is the cap")
	person := changewindow.FreezeTarget{Namespace: "payments", Pipeline: "checkout", Environment: "prod"}
	m := changewindow.FreezesFor([]kardinalv1alpha1.ChangeWindow{got}, person)
	require.Len(t, m, 1)
	assert.Equal(t, at("2026-10-14T12:00:00Z"), m[0].Until.UTC(), "the step sees the capped end")

	now = at("2026-10-14T11:00:00Z")
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	got = get()
	assert.Equal(t, at("2026-10-14T10:00:00Z"), got.Status.ExemptionRecords[0].FirstSeen.UTC(), "first seen stays")
	require.Len(t, got.Status.Exemptions, 1)

	now = at("2026-10-14T12:00:01Z")
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	got = get()
	assert.Empty(t, got.Status.Exemptions, "past the cap it no longer counts")
	require.Len(t, got.Status.ExemptionRecords, 1, "the record stays while the entry does")

	got.Spec.Freeze.Exemptions = nil
	require.NoError(t, c.Update(context.Background(), &got))
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, get().Status.ExemptionRecords, "a removed entry's record goes")
}

// TestReconciler_AgentExemptionIgnored covers the QA finding on #1869 (F4):
// an exemption an agent made is never in effect, and the window says so
// (ExemptionIgnored) until it is removed.
func TestReconciler_AgentExemptionIgnored(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	cw := freezeWindow("kardinal-freeze-agents", kardinalv1alpha1.FreezeAgents, nil,
		byAgent(exempt("prod", "", "2026-10-14T11:00:00Z")), exempt("uat", "", "2026-10-14T11:00:00Z"))
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cw).WithStatusSubresource(cw).Build()
	r := &changewindow.Reconciler{Client: c, NowFn: func() time.Time { return at("2026-10-14T10:00:00Z") }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cw.Name}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var got kardinalv1alpha1.ChangeWindow
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	require.Len(t, got.Status.Exemptions, 1, "only the person's")
	assert.Equal(t, "uat", got.Status.Exemptions[0].Environment)
	cond := meta.FindStatusCondition(got.Status.Conditions, changewindow.ConditionExemptionIgnored)
	require.NotNil(t, cond)
	assert.Contains(t, cond.Message, "break-glass is for people")

	got.Spec.Freeze.Exemptions = got.Spec.Freeze.Exemptions[1:]
	require.NoError(t, c.Update(context.Background(), &got))
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, changewindow.ConditionExemptionIgnored))
}

// TestFreezesFor: which active freezes select a step, from the windows'
// status only: actors, namespaces, exemptions in both status and spec, and a
// status behind the spec counting as active without exemptions.
func TestFreezesFor(t *testing.T) {
	agentVersion := changewindow.FreezeTarget{Namespace: "payments", Pipeline: "checkout", Environment: "prod",
		Bundle: "checkout-v2", Author: kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Agent: "fixer"}}
	personVersion := agentVersion
	personVersion.Author = kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob"}
	unstamped := agentVersion
	unstamped.Author = kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorUnknown, Principal: "bob"}

	reconciled := func(w *kardinalv1alpha1.ChangeWindow, active bool) *kardinalv1alpha1.ChangeWindow {
		w.Status.ObservedGeneration = w.Generation
		w.Status.Active = active
		w.Status.Exemptions = w.Spec.Freeze.Exemptions
		return w
	}
	cases := []struct {
		name    string
		window  *kardinalv1alpha1.ChangeWindow
		target  changewindow.FreezeTarget
		held    bool
		exempts bool
	}{
		{"an agent freeze holds an agent's version", reconciled(freezeWindow("f", "agents", nil), true), agentVersion, true, false},
		{"an agent freeze lets a person's version through", reconciled(freezeWindow("f", "agents", nil), true), personVersion, false, false},
		{"an agent freeze lets an unstamped version through", reconciled(freezeWindow("f", "agents", nil), true), unstamped, false, false},
		{"stop-the-line holds a person's version", reconciled(freezeWindow("f", "everyone", nil), true), personVersion, true, false},
		{"an inactive freeze holds nothing", reconciled(freezeWindow("f", "everyone", nil), false), personVersion, false, false},
		{"another namespace", reconciled(freezeWindow("f", "everyone", []string{"billing-*"}), true), personVersion, false, false},
		{"a namespace prefix", reconciled(freezeWindow("f", "everyone", []string{"pay*"}), true), personVersion, true, false},
		{"a status behind the spec holds", freezeWindow("f", "everyone", nil), personVersion, true, false},
		{"an exemption for the environment", reconciled(freezeWindow("f", "everyone", nil, exempt("prod", "", "2026-10-14T11:00:00Z")), true),
			personVersion, false, true},
		{"an exemption for every environment", reconciled(freezeWindow("f", "everyone", nil, exempt("", "", "2026-10-14T11:00:00Z")), true),
			personVersion, false, true},
		{"an exemption for another environment", reconciled(freezeWindow("f", "everyone", nil, exempt("uat", "", "2026-10-14T11:00:00Z")), true),
			personVersion, true, false},
		{"an exemption for another version", reconciled(freezeWindow("f", "everyone", nil, exempt("", "checkout-v1", "2026-10-14T11:00:00Z")), true),
			personVersion, true, false},
		{"an exemption for the same pipeline in another namespace", reconciled(freezeWindow("f", "everyone", nil,
			inNamespace("billing", exempt("prod", "", "2026-10-14T11:00:00Z"))), true), personVersion, true, false},
		{"an exemption for another pipeline", reconciled(freezeWindow("f", "everyone", nil,
			forPipeline("cart", exempt("prod", "", "2026-10-14T11:00:00Z"))), true), personVersion, true, false},
		{"an agent's exemption never counts", reconciled(freezeWindow("f", "everyone", nil,
			byAgent(exempt("prod", "", "2026-10-14T11:00:00Z"))), true), personVersion, true, false},
		// Being deleted (#2034): its audit finalizer keeps it; a lift mark
		// alone (an unfreeze whose delete failed) changes nothing.
		{"a freeze deleted by kardinal unfreeze holds nothing", lifting(reconciled(freezeWindow("f", "everyone", nil), true), true, true), personVersion, false, false},
		{"a freeze deleted by kubectl holds nothing", lifting(reconciled(freezeWindow("f", "everyone", nil), true), false, true), personVersion, false, false},
		{"a lift mark without a delete still holds", lifting(reconciled(freezeWindow("f", "everyone", nil), true), true, false), personVersion, true, false},
		{"a lift mark, status behind, still holds", lifting(freezeWindow("f", "everyone", nil), true, false), personVersion, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := changewindow.FreezesFor([]kardinalv1alpha1.ChangeWindow{*tc.window}, tc.target)
			assert.Equal(t, tc.held, len(changewindow.Holding(m)) > 0)
			assert.Equal(t, tc.exempts, len(m) > 0 && m[0].Exemption != nil)
		})
	}

	t.Run("an exemption removed from the spec ends at once", func(t *testing.T) {
		other := forPipeline("cart", exempt("prod", "", "2026-10-14T11:00:00Z"))
		w := reconciled(freezeWindow("f", "everyone", nil, exempt("prod", "", "2026-10-14T11:00:00Z"), other), true)
		require.Empty(t, changewindow.Holding(changewindow.FreezesFor([]kardinalv1alpha1.ChangeWindow{*w}, personVersion)))
		w.Spec.Freeze.Exemptions = []kardinalv1alpha1.FreezeExemption{other} // one of two removed
		assert.Len(t, changewindow.Holding(changewindow.FreezesFor([]kardinalv1alpha1.ChangeWindow{*w}, personVersion)), 1)
	})
	t.Run("an exemption added but not yet reconciled waits", func(t *testing.T) {
		w := reconciled(freezeWindow("f", "everyone", nil), true)
		w.Spec.Freeze.Exemptions = []kardinalv1alpha1.FreezeExemption{exempt("prod", "", "2026-10-14T11:00:00Z")}
		w.Generation = 2
		assert.Len(t, changewindow.Holding(changewindow.FreezesFor([]kardinalv1alpha1.ChangeWindow{*w}, personVersion)), 1)
	})
	t.Run("a window that is not a freeze holds nothing", func(t *testing.T) {
		w := reconciled(freezeWindow("f", "everyone", nil), true)
		w.Spec.Freeze = nil
		assert.Empty(t, changewindow.FreezesFor([]kardinalv1alpha1.ChangeWindow{*w}, personVersion))
	})
	t.Run("the message names the freeze, who and the ways on", func(t *testing.T) {
		w := reconciled(freezeWindow("kardinal-freeze-agents", "agents", nil), true)
		m := changewindow.FreezesFor([]kardinalv1alpha1.ChangeWindow{*w}, agentVersion)
		require.Len(t, m, 1)
		msg := changewindow.FrozenMessage(m[0], "checkout", "prod")
		for _, want := range []string{"kardinal-freeze-agents", "agents, every namespace", "by alice", "INC-4211",
			"kardinal unfreeze kardinal-freeze-agents", "kardinal override checkout prod --freeze"} {
			assert.Contains(t, msg, want)
		}
	})
}
