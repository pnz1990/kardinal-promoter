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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// gateTranslator fails with graph.ErrInvalid while the PolicyGate template
// default/permit does not exist or does not grant a skip, the way the Graph
// builder refuses a skip without permission. Like the real Translator it
// returns a translator.BuildError with the gates the build was given.
type gateTranslator struct {
	c     client.Client
	calls int
}

func (m *gateTranslator) Translate(ctx context.Context, p *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) (string, error) {
	m.calls++
	gates, err := translator.CollectGates(ctx, m.c, nil, p)
	if err != nil {
		return "", err
	}
	for _, g := range gates {
		if g.Name == "permit" && g.Spec.SkipPermission {
			return "app-" + b.Name, nil
		}
	}
	return "", &translator.BuildError{Err: fmt.Errorf("build: skip denied for environment uat: %w", graph.ErrInvalid), Gates: gates}
}

func gateTemplate(name, appliesTo string, skip bool) *kardinalv1alpha1.PolicyGate {
	return &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
			Labels: map[string]string{"kardinal.io/applies-to": appliesTo}},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: "true", SkipPermission: skip},
	}
}

// #1312: a Bundle failed with GraphBuildFailed because of a PolicyGate is
// retried when the PolicyGate templates of its Pipeline change. The failure
// stores a hash of the templates; a change to one that applies to the
// Pipeline retries the Bundle through the existing retry path (Available,
// so a newer Bundle still supersedes it). A change to a gate of another
// environment, or a gate instance, does not.
func TestLifecycle_GateChangeRetriesGraphBuildFailed(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-time.Hour)
	permit := gateTemplate("permit", "uat", false)
	other := gateTemplate("other-env", "staging", false)
	c := lcClient(lcPipeline("app", lcEnvs("test", "uat", "prod")...),
		lcBundle("app-v1", "image", "Available", t0), permit, other)
	tr := &gateTranslator{c: c}
	r := &bundle.Reconciler{Client: c, Translator: tr}

	lcReconcile(t, r, "app-v1")
	got := lcGet(t, c, "app-v1")
	require.Equal(t, "Failed", got.Status.Phase)
	inv := meta.FindStatusCondition(got.Status.Conditions, "InvalidSpec")
	require.NotNil(t, inv)
	assert.Equal(t, "GraphBuildFailed", inv.Reason)
	assert.Contains(t, inv.Message, "a change to the Pipeline or its PolicyGates retries this Bundle")
	assert.NotEmpty(t, got.Status.PolicyGatesHash, "the failure records the gates it was built with")

	// The gate watch re-queues the Bundle on a template event, not an instance one.
	assert.Contains(t, r.GateBundles(ctx, permit),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}})
	inst := gateTemplate("app-v1-uat-permit", "uat", false)
	inst.Labels["kardinal.io/gate-template"] = "permit"
	assert.False(t, bundle.GateTemplateChanged.Create(event.CreateEvent{Object: inst}), "gate instances are not templates")
	assert.True(t, bundle.GateTemplateChanged.Create(event.CreateEvent{Object: permit}))

	// Nothing changed: no retry, nothing written.
	rv := got.ResourceVersion
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, rv, lcGet(t, c, "app-v1").ResourceVersion)

	// A gate of an environment the Pipeline does not have changes: no retry.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(other), other))
	other.Spec.Expression = "false"
	require.NoError(t, c.Update(ctx, other))
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, rv, lcGet(t, c, "app-v1").ResourceVersion, "an unrelated gate does not retry")
	assert.Equal(t, 1, tr.calls)

	// The platform team fixes the gate: the Bundle is retried and promotes.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(permit), permit))
	permit.Spec.SkipPermission = true
	require.NoError(t, c.Update(ctx, permit))
	lcReconcile(t, r, "app-v1")
	got = lcGet(t, c, "app-v1")
	assert.Equal(t, "Available", got.Status.Phase, "the gate change retries the Bundle")
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "InvalidSpec"))
	assert.Empty(t, got.Status.PolicyGatesHash)
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, "Promoting", lcGet(t, c, "app-v1").Status.Phase)
	assert.Equal(t, 2, tr.calls)
}

// #1312: a newer Bundle still wins. A gate change does not re-promote an old
// Bundle that a newer one replaced; it is Superseded instead.
func TestLifecycle_GateChangeSupersedesWhenNewer(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-time.Hour)
	permit := gateTemplate("permit", "uat", false)
	c := lcClient(lcPipeline("app", lcEnvs("test", "uat")...),
		lcBundle("app-v1", "image", "Available", t0), permit)
	r := &bundle.Reconciler{Client: c, Translator: &gateTranslator{c: c}}
	lcReconcile(t, r, "app-v1")
	require.Equal(t, "Failed", lcGet(t, c, "app-v1").Status.Phase)

	require.NoError(t, c.Create(ctx, lcBundle("app-v2", "image", "Verified", t0.Add(time.Minute))))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(permit), permit))
	permit.Spec.SkipPermission = true
	require.NoError(t, c.Update(ctx, permit))
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, "Superseded", lcGet(t, c, "app-v1").Status.Phase)
}

// #1312: a Bundle that has a Graph and failed to re-translate it after a
// Pipeline change (InvalidSpec, GraphBuildFailed) re-translates when its
// gates change, and recovers.
func TestLifecycle_GateChangeRetranslatesInFlightGraph(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-time.Hour)
	permit := gateTemplate("permit", "uat", false)
	b := lcBundle("app-v1", "image", "Promoting", t0)
	b.Status.GraphRef = "app-app-v1"
	b.Status.PipelineSpecHash = "stale" // the Pipeline changed since the Graph was built
	c := lcClient(lcPipeline("app", lcEnvs("test", "uat")...), b, permit,
		lcStep("app-v1", "test", "s-test", "Verified"))
	reader := &graphStatusReader{g: &graph.Graph{ObjectMeta: metav1.ObjectMeta{Name: "app-app-v1", Namespace: "default"}}}
	tr := &gateTranslator{c: c}
	r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: reader}

	lcReconcile(t, r, "app-v1")
	got := lcGet(t, c, "app-v1")
	require.Equal(t, "Failed", got.Status.Phase)
	require.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, "InvalidSpec"))
	require.NotEmpty(t, got.Status.PolicyGatesHash)

	lcReconcile(t, r, "app-v1")
	assert.Equal(t, 1, tr.calls, "unchanged gates are not retried")

	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(permit), permit))
	permit.Spec.SkipPermission = true
	require.NoError(t, c.Update(ctx, permit))
	lcReconcile(t, r, "app-v1")
	got = lcGet(t, c, "app-v1")
	assert.Equal(t, 2, tr.calls, "the gate change re-translates the Graph")
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "InvalidSpec"))
	assert.Empty(t, got.Status.PolicyGatesHash)
	assert.Equal(t, "Promoting", got.Status.Phase, "nothing is failing any more")
}

// #1312 (QA on #1487): the hash recorded is of the gates the failed build
// was given (translator.BuildError), not of a second read. A build error
// without them (the gates took no part) records no hash, so no gate change
// retries the Bundle.
func TestLifecycle_GatesHashFromFailedBuild(t *testing.T) {
	t0 := time.Now().UTC().Add(-time.Hour)
	p := lcPipeline("app", lcEnvs("test", "uat")...)
	used := []kardinalv1alpha1.PolicyGate{*gateTemplate("permit", "uat", false)}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "build error with its gates", want: translator.GatesHash(p, used),
			err: &translator.BuildError{Err: fmt.Errorf("build: x: %w", graph.ErrInvalid), Gates: used}},
		{name: "invalid without gates", err: fmt.Errorf("build: x: %w", graph.ErrInvalid)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The cache holds a different gate than the build used.
			c := lcClient(p.DeepCopy(), lcBundle("app-v1", "image", "Available", t0), gateTemplate("permit", "uat", true))
			r := &bundle.Reconciler{Client: c, Translator: &mockTranslator{err: tc.err}}
			lcReconcile(t, r, "app-v1")
			got := lcGet(t, c, "app-v1")
			require.Equal(t, "Failed", got.Status.Phase)
			assert.Equal(t, tc.want, got.Status.PolicyGatesHash)
		})
	}
}
