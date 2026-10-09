//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestBundle_FailedBundleWaitsForSlot checks maxConcurrentPromotions 1 with
// a Failed Bundle (#1349). An image Bundle fails its health check in test,
// which frees the slot, and a config Bundle (another type, so not a
// supersession) takes it and waits on its PR. The Failed Bundle then has
// WaitingForSlot=True. Its failed step is deleted, which would let kro
// recreate it and the Bundle recover: while the slot is taken kro creates no
// step for it and it stays Failed, so only one Bundle is Promoting. Once the
// config Bundle is deleted the hold is lifted, kro recreates the step and the
// Failed Bundle is Promoting again.
//
// Covers BUNDLE-SLOT-02.
func TestBundle_FailedBundleWaitsForSlot(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	p := a.pipeline(map[string]string{"test": "pr-review"})
	p.Spec.MaxConcurrentPromotions = 1
	envSpec(t, p, "test").Health.Timeout = "20s"
	a.apply(t, p)
	cfg, sha := a.configRepo(t, "test")
	ctx := context.Background()

	failing := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	e.WaitStepState(t, a.ns, pipelineName, failing, "test", "WaitingForMerge", promoteTimeout)
	a.merge(t, a.openPR(t, failing, "test"))
	failedStep := e.WaitStepState(t, a.ns, pipelineName, failing, "test", "Failed", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, failing, "Failed", time.Minute)

	other := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	e.WaitStepState(t, a.ns, pipelineName, other, "test", "WaitingForMerge", promoteTimeout)
	held := e.WaitBundle(t, a.ns, failing, time.Minute, "held for a slot", func(b *v1alpha1.Bundle) (bool, string) {
		ok, seen := framework.CondIs(b.Status.Conditions, "WaitingForSlot", metav1.ConditionTrue, "SlotTaken")
		return ok && b.Status.Phase == "Failed", fmt.Sprintf("phase=%q %s", b.Status.Phase, seen)
	})
	assert.Contains(t, findCond(held.Status.Conditions, "WaitingForSlot").Message, "maxConcurrentPromotions (1) reached")

	// Retry the failed step the way an operator would: delete it.
	require.NoError(t, e.Client.Delete(ctx, failedStep))
	framework.Eventually(t, time.Minute, "the failed step is gone", func(ctx context.Context) (bool, string) {
		ps, found, err := e.Step(ctx, a.ns, pipelineName, failing, "test")
		if err != nil {
			return false, err.Error()
		}
		return !found || ps.UID != failedStep.UID, "the step still exists"
	})
	framework.Consistently(t, 30*time.Second, "the held Bundle takes no second slot", func(ctx context.Context) (bool, string) {
		promoting, err := promotingBundles(ctx, e, a.ns)
		if err != nil {
			return false, err.Error()
		}
		_, found, err := e.Step(ctx, a.ns, pipelineName, failing, "test")
		if err != nil {
			return false, err.Error()
		}
		cur := a.bundle(t, failing)
		return len(promoting) == 1 && promoting[0] == other && !found && cur.Status.Phase == "Failed" &&
				meta.IsStatusConditionTrue(cur.Status.Conditions, "WaitingForSlot"),
			fmt.Sprintf("promoting=%v failing step=%v phase=%q", promoting, found, cur.Status.Phase)
	})

	// Free the slot.
	require.NoError(t, e.Client.Delete(ctx, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: other, Namespace: a.ns}}))
	a.waitGone(t, other)
	e.WaitBundle(t, a.ns, failing, 2*time.Minute, "recovered once the slot is free", func(b *v1alpha1.Bundle) (bool, string) {
		return b.Status.Phase == "Promoting" && meta.FindStatusCondition(b.Status.Conditions, "WaitingForSlot") == nil,
			fmt.Sprintf("phase=%q conditions=%v", b.Status.Phase, b.Status.Conditions)
	})
	ps, found, err := e.Step(ctx, a.ns, pipelineName, failing, "test")
	require.NoError(t, err)
	require.True(t, found, "kro recreated the step")
	assert.NotEqual(t, failedStep.UID, ps.UID)
}

// promotingBundles lists the Promoting Bundles of the test Pipeline in ns.
func promotingBundles(ctx context.Context, e *framework.Env, ns string) ([]string, error) {
	var list v1alpha1.BundleList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	var out []string
	for _, b := range list.Items {
		if b.Spec.Pipeline == pipelineName && b.Status.Phase == "Promoting" {
			out = append(out, b.Name)
		}
	}
	return out, nil
}

// TestGraph_GateFixRetriesFailedBundle checks that fixing the PolicyGate that
// broke a Bundle's Graph retries that Bundle (#1312). A Bundle skips an
// org-gated environment without a skip-permission gate, so its Graph cannot
// be built: Failed, InvalidSpec GraphBuildFailed "skip denied". When the
// platform team adds the skip-permission gate to the org policy namespace,
// the same Bundle is retried (event Retrying) and promotes, without a
// Pipeline change or a new Bundle.
//
// Covers GRAPH-RETRY-01.
func TestGraph_GateFixRetriesFailedBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	// A name no other test uses: the skip-permission gate lives in the shared
	// org policy namespace and applies by environment name.
	gated := "gated-" + ns[len(ns)-8:]
	envs := []string{"test", gated, "prod"}
	a := &app{e: e, ns: ns, envs: envs, repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))}
	for _, env := range envs {
		e.ArgoApp(t, a.argoApp(env), a.repo, fixtures.Path(env), ns)
	}
	for _, env := range envs {
		e.WaitArgoApp(t, a.argoApp(env), syncTimeout)
	}
	a.apply(t, a.pipeline(nil))
	ctx := context.Background()
	require.NoError(t, e.Client.Create(ctx, &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "org-gated", Namespace: ns, Labels: map[string]string{
			"kardinal.io/scope": "org", "kardinal.io/applies-to": gated, "kardinal.io/type": "gate"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Message: "org gate", RecheckInterval: "10s"},
	}))

	denied := a.createBundle(t, v1alpha1.BundleSpec{Images: podinfoImages(fixtures.V2),
		Intent: &v1alpha1.BundleIntent{SkipEnvironments: []string{gated}}})
	b := e.WaitBundle(t, ns, denied, time.Minute, "refused", failedWith("GraphBuildFailed", "skip denied"))
	assert.Contains(t, findCond(b.Status.Conditions, "InvalidSpec").Message,
		"a change to the Pipeline or its PolicyGates retries this Bundle")
	assert.NotEmpty(t, b.Status.PolicyGatesHash)
	framework.Consistently(t, 10*time.Second, "the refused Bundle stays Failed", func(ctx context.Context) (bool, string) {
		cur := a.bundle(t, denied)
		return cur.Status.Phase == "Failed" && a.stepCount(t, denied) == 0, "phase=" + cur.Status.Phase
	})

	const policyNS = "platform-policies"
	if err := e.Client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: policyNS}}); err != nil &&
		!apierrors.IsAlreadyExists(err) {
		t.Fatalf("create %s: %v", policyNS, err)
	}
	permit := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "skip-" + ns[len(ns)-8:], Namespace: policyNS, Labels: map[string]string{
			"kardinal.io/type": "skip-permission", "kardinal.io/applies-to": gated}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", SkipPermission: true, RecheckInterval: "10s"},
	}
	require.NoError(t, e.Client.Create(ctx, permit))
	t.Cleanup(func() {
		if err := e.Client.Delete(context.Background(), permit); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete %s/%s: %v", policyNS, permit.Name, err)
		}
	})

	framework.Eventually(t, time.Minute, "a Retrying event on "+denied, func(ctx context.Context) (bool, string) {
		evs, err := e.Events(ctx, ns, "Bundle", denied)
		if err != nil {
			return false, err.Error()
		}
		for _, ev := range evs {
			if ev.Reason == "Retrying" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d events, none Retrying", len(evs))
	})
	e.WaitStepState(t, ns, pipelineName, denied, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, ns, pipelineName, denied, "prod", "Verified", promoteTimeout)
	got := e.WaitBundlePhase(t, ns, denied, "Verified", time.Minute)
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "InvalidSpec"))
	assert.Empty(t, got.Status.PolicyGatesHash)
	steps, err := e.Steps(ctx, ns, pipelineName, denied)
	require.NoError(t, err)
	assert.Equal(t, []string{"prod", "test"}, framework.StepEnvs(steps), "the gated environment is skipped")
	a.running(t, "prod", imageV2, "the retried Bundle promotes")
	a.fileHas(t, gated, fixtures.V1, "the skipped environment is untouched")
}
