// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func verifyFixture(t *testing.T, objs ...sigs_client.Object) sigs_client.Client {
	t.Helper()
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default", UID: "uid-1",
			Annotations: map[string]string{"kardinal.io/created-by": "alice"}},
		Spec:   v1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec:       v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "uat"}, {Name: "prod"}}},
		Status:     v1alpha1.PipelineStatus{Environments: []v1alpha1.PipelineEnvironmentStatus{{Name: "uat", Bundle: "app-v1"}}},
	}
	return fake.NewClientBuilder().WithScheme(rejectScheme(t)).WithObjects(append([]sigs_client.Object{b, p}, objs...)...).Build()
}

func verdictsIn(t *testing.T, c sigs_client.Client) []v1alpha1.Verdict {
	t.Helper()
	var list v1alpha1.VerdictList
	require.NoError(t, c.List(context.Background(), &list))
	return list.Items
}

// TestVerify records a Verdict in the caller's name, owned by the Bundle,
// with the labels the Graph selects on and the caller's stamp; refuses a
// fail without a summary and bad findings; and --revoke deletes it.
func TestVerify(t *testing.T) {
	stubIdentity(t, Identity{Username: "bob", Groups: []string{"qa", "system:authenticated"}})
	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g1", Namespace: "default",
		Labels: map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod", "kardinal.io/gate-name": "explore"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Verify: &v1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat"}}}
	c := verifyFixture(t, gate)
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	var out bytes.Buffer

	o := verifyOptions{env: "uat", check: "explore", pass: true, summary: "all good", evidence: "https://ci/1",
		findings: []string{"low:slow page"}}
	require.NoError(t, verifyFn(ctx, &out, c, "default", "app-v1", o, now))
	list := verdictsIn(t, c)
	require.Len(t, list, 1)
	v := list[0]
	assert.Contains(t, out.String(), "Recorded: bob finds app-v1 pass in uat for explore (Verdict "+v.Name+")")
	assert.Contains(t, out.String(), "counted by gate explore (prod)")
	assert.Equal(t, map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "uat",
		"kardinal.io/check": "explore", "kardinal.io/pipeline": "app"}, v.Labels)
	assert.Equal(t, "Bundle", v.OwnerReferences[0].Kind)
	assert.Equal(t, "uid-1", v.Spec.BundleUID)
	assert.Equal(t, "bob", v.Spec.By)
	assert.Equal(t, []string{"qa", "system:authenticated"}, v.Spec.Groups)
	require.NotNil(t, v.Spec.Actor)
	assert.Equal(t, v1alpha1.ActorHuman, v.Spec.Actor.Kind)
	assert.Equal(t, []v1alpha1.VerdictFinding{{Severity: "low", Title: "slow page"}}, v.Spec.Findings)

	assert.ErrorContains(t, verifyFn(ctx, &out, c, "default", "app-v1",
		verifyOptions{env: "uat", check: "explore", fail: true}, now), "give a summary")
	assert.ErrorContains(t, verifyFn(ctx, &out, c, "default", "app-v1",
		verifyOptions{env: "uat", check: "explore", pass: true, fail: true}, now), "exactly one of")
	assert.ErrorContains(t, verifyFn(ctx, &out, c, "default", "app-v1",
		verifyOptions{env: "uat", check: "explore", pass: true, findings: []string{"urgent:x"}}, now), "severity")
	assert.ErrorContains(t, verifyFn(ctx, &out, c, "default", "app-v1", verifyOptions{env: "uat", pass: true}, now), "which check")

	out.Reset()
	require.NoError(t, verifyFn(ctx, &out, c, "default", "app-v1", verifyOptions{env: "uat", check: "explore", revoke: true}, now))
	assert.Contains(t, out.String(), "Revoked: bob no longer finds app-v1 pass")
	assert.Empty(t, verdictsIn(t, c))
	assert.ErrorContains(t, verifyFn(ctx, &out, c, "default", "app-v1", verifyOptions{env: "uat", check: "explore", revoke: true}, now),
		"have no verdict")
}

// TestVerifyStanding: the author's own verdict is recorded, and the caller
// is told the gate will not count it.
func TestVerifyStanding(t *testing.T) {
	stubIdentity(t, Identity{Username: "alice"})
	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g1", Namespace: "default",
		Labels: map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Verify: &v1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat"}}}
	c := verifyFixture(t, gate)
	var out bytes.Buffer
	require.NoError(t, verifyFn(context.Background(), &out, c, "default", "app-v1",
		verifyOptions{env: "uat", check: "explore", pass: true}, time.Now()))
	assert.Contains(t, out.String(), "not counted by gate g1: the author's party (alice")

	out.Reset()
	require.NoError(t, verifyFn(context.Background(), &out, c, "default", "app-v1",
		verifyOptions{env: "uat", check: "smoke", pass: true}, time.Now()))
	assert.Contains(t, out.String(), "no gate of app-v1 waits for check smoke in uat yet")
}

// TestVerifyTarget: <pipeline> <env> is the Bundle that runs there.
func TestVerifyTarget(t *testing.T) {
	t.Parallel()
	c := verifyFixture(t)
	cmd := newVerifyCmd()
	b, env, err := verifyTarget(context.Background(), c, "default", cmd, []string{"app", "uat"}, "")
	require.NoError(t, err)
	assert.Equal(t, "app-v1", b)
	assert.Equal(t, "uat", env)
	_, _, err = verifyTarget(context.Background(), c, "default", cmd, []string{"app", "prod"}, "")
	assert.ErrorContains(t, err, "no version runs in prod")
	b, _, err = verifyTarget(context.Background(), c, "default", cmd, []string{"app-v1"}, "uat")
	require.NoError(t, err)
	assert.Equal(t, "app-v1", b)
}

// TestVerifyList prints each Verdict with whether a gate counted it.
func TestVerifyList(t *testing.T) {
	stubIdentity(t, Identity{Username: "bob"})
	c := verifyFixture(t)
	ctx := context.Background()
	require.NoError(t, verifyFn(ctx, &bytes.Buffer{}, c, "default", "app-v1",
		verifyOptions{env: "uat", check: "explore", pass: true}, time.Now()))
	name := verdictsIn(t, c)[0].Name
	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g1", Namespace: "default",
		Labels: map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/gate-name": "explore"}},
		Spec:   v1alpha1.PolicyGateSpec{Expression: "true"},
		Status: v1alpha1.PolicyGateStatus{Verdicts: []v1alpha1.GateVerdictStatus{{Name: name, Counted: true}}}}
	require.NoError(t, c.Create(ctx, gate))
	var out bytes.Buffer
	require.NoError(t, verifyListFn(ctx, &out, c, "default", "app-v1", "", ""))
	assert.Contains(t, out.String(), "NAME")
	assert.Contains(t, out.String(), name)
	assert.Contains(t, out.String(), "yes (explore)")
	out.Reset()
	require.NoError(t, verifyListFn(ctx, &out, c, "default", "app-v1", "prod", ""))
	assert.Equal(t, "No verdicts on app-v1.\n", out.String())
}
