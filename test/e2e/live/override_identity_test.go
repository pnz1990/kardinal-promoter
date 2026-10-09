//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// overriddenAudits lists the GateOverridden AuditEvents of bundle.
func overriddenAudits(t *testing.T, e *framework.Env, ns, bundle string) []v1alpha1.AuditEvent {
	t.Helper()
	var list v1alpha1.AuditEventList
	require.NoError(t, e.Client.List(context.Background(), &list, client.InNamespace(ns),
		client.MatchingLabels{"kardinal.io/action": "GateOverridden", "kardinal.io/bundle": bundle}))
	return list.Items
}

// TestGate_OverrideIdentity overrides a blocking prod gate with kardinal
// override. The override names the user the API server authenticated
// (SelfSubjectReview), the gate passes on it, and the controller writes one
// GateOverridden AuditEvent for it, recorded in status.overrides as verified
// (the controller found the chart's policy bound) and audited, and no second
// one however often the gate is re-evaluated. The chart's
// gate-overrides policy refuses, for a real user (impersonated, with patch
// on PolicyGates): an override in someone else's name, editing another
// user's override, changing the instance's expression, message, labels,
// annotations or finalizers,
// and creating a gate instance by hand; even the test's cluster admin cannot
// change the expression of a gate instance.
//
// Covers GATE-OVERRIDE-ID-01, GATE-OVERRIDE-ID-02.
func TestGate_OverrideIdentity(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "hold", "prod", openExpr, recheck))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	gate := e.WaitGateReady(t, a.ns, bundle, "prod", "hold", false, "= false", gateTimeout)

	// The policy, for a real user who may patch PolicyGates.
	mallory := impersonating(t, e, a.ns, "mallory@example.com", nil, []string{"policygates"}, "get", "create", "patch", "update")
	forge := func(overrides []v1alpha1.PolicyGateOverride) error {
		raw, err := json.Marshal(map[string]any{"spec": map[string]any{"overrides": overrides}})
		require.NoError(t, err)
		return mallory.Patch(ctx, gate.DeepCopy(), client.RawPatch(types.MergePatchType, raw))
	}
	in := metav1.NewTime(time.Now().Add(time.Hour))
	err := forge([]v1alpha1.PolicyGateOverride{{Reason: "forged", ExpiresAt: in, CreatedBy: "alice@example.com"}})
	require.Error(t, err, "an override in someone else's name is refused")
	assert.Contains(t, err.Error(), `every new spec.overrides entry must have createdBy "mallory@example.com"`)
	err = mallory.Patch(ctx, gate.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"expression":"true"}}`)))
	require.Error(t, err, "editing an instance's expression is refused")
	assert.Contains(t, err.Error(), "only kardinal creates a gate instance (label kardinal.io/bundle) or changes its spec or metadata")
	err = mallory.Patch(ctx, gate.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"kardinal.io/environment":"test"}}}`)))
	require.Error(t, err, "relabelling an instance is refused")
	err = mallory.Patch(ctx, gate.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"e2e":"x"}}}`)))
	require.Error(t, err, "annotating an instance is refused: its metadata is frozen")
	err = mallory.Patch(ctx, gate.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":["e2e/hold"]}}`)))
	require.Error(t, err, "adding a finalizer to an instance is refused")
	err = mallory.Patch(ctx, gate.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"message":"x"}}`)))
	require.Error(t, err, "changing anything but overrides on an instance is refused")
	err = e.Client.Patch(ctx, gate.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"expression":"true"}}`)))
	require.Error(t, err, "even a cluster admin cannot change a gate instance's expression")
	// Nor can a forged instance be created ahead of kro.
	forgedInstance := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "forged-instance", Namespace: a.ns, Labels: map[string]string{
			"kardinal.io/bundle": bundle, "kardinal.io/environment": "prod", "kardinal.io/pipeline": pipelineName}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true"},
	}
	err = mallory.Create(ctx, forgedInstance)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only kardinal creates a gate instance")
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", 5*time.Second)

	// kardinal override records the authenticated user.
	user := whoAmI(t, e)
	out := e.MustKardinal(t, a.ns, "override", pipelineName, "--stage", "prod", "--gate", "hold", "--reason", "e2e: INC-7")
	assert.Contains(t, out, "Created by: "+user)
	passed := e.WaitGateReady(t, a.ns, bundle, "prod", "hold", true, "OVERRIDDEN by "+user+": e2e: INC-7", gateTimeout)
	require.Len(t, passed.Spec.Overrides, 1)
	assert.Equal(t, user, passed.Spec.Overrides[0].CreatedBy)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)

	var audits []v1alpha1.AuditEvent
	framework.Eventually(t, time.Minute, "one GateOverridden AuditEvent", func(context.Context) (bool, string) {
		audits = overriddenAudits(t, e, a.ns, bundle)
		return len(audits) == 1, fmt.Sprintf("%d records", len(audits))
	})
	ae := audits[0]
	assert.Equal(t, "Success", ae.Spec.Outcome)
	assert.Equal(t, "prod", ae.Spec.Environment)
	assert.Equal(t, pipelineName, ae.Spec.PipelineName)
	assert.Equal(t, "hold", ae.Labels["kardinal.io/gate"])
	assert.Contains(t, ae.Spec.Message, fmt.Sprintf("gate %s overridden by %s for prod until ", passed.Name, user))
	assert.Contains(t, ae.Spec.Message, ": e2e: INC-7")
	var cur v1alpha1.PolicyGate
	require.NoError(t, e.Client.Get(ctx, client.ObjectKeyFromObject(passed), &cur))
	require.Len(t, cur.Status.Overrides, 1)
	assert.True(t, cur.Status.Overrides[0].Verified, "the chart's identity policy is bound: %+v", cur.Status.Overrides[0])
	assert.True(t, cur.Status.Overrides[0].Audited)
	assert.NotContains(t, cur.Status.Reason, "unverified")
	framework.Consistently(t, 25*time.Second, "no second GateOverridden record over two rechecks", func(context.Context) (bool, string) {
		n := len(overriddenAudits(t, e, a.ns, bundle))
		return n == 1, fmt.Sprintf("%d records", n)
	})

	// Someone else's override cannot be edited in place either.
	edited := append([]v1alpha1.PolicyGateOverride(nil), cur.Spec.Overrides...)
	edited[0].ExpiresAt = metav1.NewTime(time.Now().Add(48 * time.Hour))
	require.Error(t, forge(edited), "extending another user's override is refused")
}
