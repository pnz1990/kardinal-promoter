//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// guideChartV0121 is the chart reference of the "Upgrading from v0.12.1"
// guide: the test replaces it with the checkout's chart.
const guideChartV0121 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3"

// u0121AlarmGate is the gate that holds the controller's automatic
// rollbacks at test (their actor stamp), so the one the release starts is still
// in flight when the upgrade starts.
const u0121AlarmGate = "hold-auto-rollbacks"

// TestUpgradeV0121_Estate is TestUpgradeV0120_Estate from kardinal-promoter
// v0.12.1 as released (its chart, digest-pinned image, CLI and kro;
// components/kardinal-v0121.sh): it builds the same estate under v0.12.1,
// follows "Upgrading from v0.12.1" in docs/upgrade.md while versions are in
// flight, and makes the same checks. The upgrade is checked to have
// happened by the controller's image, new Pods, a new deployed helm
// revision and the guide's server-side apply owning every kardinal CRD,
// not by the chart version: until the release pin, the checkout's chart is
// still 0.12.1.
//
// It adds an automatic rollback in flight across the upgrade
// (u012AutoRollback): the code v0.12.2 changes (#2187, #2223).
//
// Covers UPG-V0121-01, UPG-V0121-02, UPG-V0121-03, UPG-V0121-04, UPG-V0121-05, UPG-V0121-06.
func TestUpgradeV0121_Estate(t *testing.T) {
	u012Upgrade(t, u012From{pins: "V0121", cli: (*framework.Env).KardinalV0121, guideChart: guideChartV0121,
		extra: u012AutoRollback})
}

// u0121Rollbacks are the rollback Bundles of ns (label kardinal.io/rollback)
// by name, with their uids.
func u0121Rollbacks(ctx context.Context, e *framework.Env, ns string) (map[string]types.UID, error) {
	var list v1alpha1.BundleList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/rollback": "true"}); err != nil {
		return nil, fmt.Errorf("list rollback Bundles in %s: %w", ns, err)
	}
	out := map[string]types.UID{}
	for _, b := range list.Items {
		out[b.Name] = b.UID
	}
	return out, nil
}

// u0121FailedStep checks that the failed version's step in test is the step
// that started the rollback rb: the step itself (uid), still RollingBack and
// naming rb, or, once the version's Graph is retired (a Superseded version
// retires after a minute), its record in status.retiredSteps: RollingBack,
// with the step's message. A step of the version with another uid means the
// version promoted again.
func u0121FailedStep(ctx context.Context, e *framework.Env, ns, bundle string, failed *v1alpha1.PromotionStep, rb string) (bool, string) {
	ps, ok, err := e.Step(ctx, ns, pipelineName, bundle, "test")
	switch {
	case err != nil:
		return false, err.Error()
	case ok:
		return ps.UID == failed.UID && ps.Status.State == "RollingBack" && strings.Contains(ps.Status.Message, "rollback Bundle "+rb),
			fmt.Sprintf("step %s (uid %s, was %s): state %q, message %q", ps.Name, ps.UID, failed.UID, ps.Status.State, ps.Status.Message)
	}
	var b v1alpha1.Bundle
	if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundle}, &b); err != nil {
		return false, err.Error()
	}
	// The record keeps the message cut to 512 bytes, which can drop the
	// rollback's name: it is the step's message, or its start.
	for _, r := range b.Status.RetiredSteps {
		if r.Name == failed.Name {
			return r.State == "RollingBack" && r.Message != "" && strings.HasPrefix(failed.Status.Message, r.Message),
				fmt.Sprintf("retired step %s: state %q, message %q (the step's was %q)", r.Name, r.State, r.Message, failed.Status.Message)
		}
	}
	return false, fmt.Sprintf("no step %s and no record of it in %s's status.retiredSteps", failed.Name, bundle)
}

// u012AutoRollbackOf checks that rb is the controller's automatic rollback
// of from (onHealthFailure: rollback): its stamp, its marker and the image it
// restores.
func u012AutoRollbackOf(t *testing.T, e *framework.Env, ns, rb, from string) *v1alpha1.Bundle {
	t.Helper()
	b := getBundle(t, e, ns, rb)
	require.NotNil(t, b.Spec.Actor, "%s has an actor stamp", rb)
	assert.Equal(t, v1alpha1.ActorController, b.Spec.Actor.Kind, "%s is the controller's", rb)
	assert.Equal(t, v1alpha1.ViaAutoRollback, b.Spec.Actor.Via, "%s is an automatic rollback", rb)
	assert.Equal(t, "kardinal-controller", b.Annotations["kardinal.io/created-by"], "%s: created by the controller", rb)
	assert.Equal(t, from, b.Annotations["kardinal.io/rollback-from"], "%s rolls %s back", rb, from)
	assert.Equal(t, podinfoImages(fixtures.V2), b.Spec.Images, "%s restores the Verified image", rb)
	return b
}

// u012AutoRollback is a case of the upgrade-v0121 and upgrade-v0122 suites:
// an automatic rollback in flight across the upgrade. Under the release (its
// CLI), a podinfo Pipeline with onHealthFailure: rollback on test
// (rbAlarmPipeline, the core suite's fixture) has a version Verified, then a
// version whose rollout fails (ProgressDeadlineExceeded): its step turns
// RollingBack and the release creates the rollback Bundle
// <version>-rollback-alarm, which a gate on the
// controller's auto-rollback stamp holds at test. The failed version is
// Superseded, and its Graph retires a minute later (its step then is a
// record in status.retiredSteps). After the upgrade:
//   - the new controller adopts that rollback: for 30s the namespace has it
//     alone (the same uid), the failed version is the same Bundle, and its
//     step is the same step (or its record), RollingBack and naming it;
//   - released by kardinal override, it promotes the Verified image back and
//     is Verified, the same Bundle, and the failed version ends Superseded;
//   - a version that fails after the upgrade gets exactly one new automatic
//     rollback, which is Verified: the namespace then has those two.
func u012AutoRollback(t *testing.T, e *framework.Env, from u012From) func() {
	t.Helper()
	ctx := context.Background()
	a := rbAlarmPipeline(t, e, nil)
	gate := framework.Gate(a.ns, u0121AlarmGate, "test", `!(actor.kind == "controller" && actor.via == "auto-rollback")`, recheck)
	e.CreateGate(t, gate)
	create := func(tag string) string {
		out, err := from.cli(e, t, a.ns, "create", "bundle", pipelineName, "--image", fixtures.Image+":"+tag)
		require.NoError(t, err, out)
		m := v081BundleCreated.FindStringSubmatch(out)
		require.Len(t, m, 2, "kardinal create bundle names the Bundle it created:\n%s", out)
		return m[1]
	}
	rollbacks := func(ctx context.Context) map[string]types.UID {
		got, err := u0121Rollbacks(ctx, e, a.ns)
		require.NoError(t, err)
		return got
	}

	// 1. Under the release: a version fails and its automatic rollback waits.
	b1 := create(fixtures.V2)
	rbVerified(t, a, b1, "test")
	b2 := create(fixtures.BrokenTag)
	failed := e.WaitStepState(t, a.ns, pipelineName, b2, "test", "RollingBack", promoteTimeout)
	rb := b2 + "-rollback-alarm"
	require.True(t, strings.HasSuffix(failed.Status.Message, " — rollback Bundle "+rb+" created"), failed.Status.Message)
	held := u012AutoRollbackOf(t, e, a.ns, rb, b2)
	failedB := getBundle(t, e, a.ns, b2)
	e.WaitGateReady(t, a.ns, rb, "test", u0121AlarmGate, false, "", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, rb, "test", 10*time.Second)
	want := map[string]types.UID{rb: held.UID}
	require.Equal(t, want, rollbacks(ctx), "the release created one automatic rollback")
	t.Logf("automatic rollback %s (uid %s) of %s held at test", rb, held.UID, b2)

	return func() {
		// 3. The new controller adopts it: no second rollback, the same step.
		framework.Consistently(t, 30*time.Second, "the automatic rollback "+rb+" is adopted, not made again", func(ctx context.Context) (bool, string) {
			got, err := u0121Rollbacks(ctx, e, a.ns)
			if err != nil {
				return false, err.Error()
			}
			if !maps.Equal(want, got) {
				return false, fmt.Sprintf("rollback Bundles %v, want %v", got, want)
			}
			var b v1alpha1.Bundle
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: b2}, &b); err != nil {
				return false, err.Error()
			}
			if b.UID != failedB.UID {
				return false, fmt.Sprintf("%s is uid %s, was %s", b2, b.UID, failedB.UID)
			}
			return u0121FailedStep(ctx, e, a.ns, b2, failed, rb)
		})
		me := whoAmI(t, e)
		e.MustKardinal(t, a.ns, "override", pipelineName, "--stage", "test", "--gate", u0121AlarmGate, "--reason", "e2e: release the rollback")
		e.WaitGateReady(t, a.ns, rb, "test", u0121AlarmGate, true, "OVERRIDDEN by "+me, gateTimeout)
		rbVerified(t, a, rb, "test")
		assertEnvAt(t, a, "test", fixtures.V2)
		assert.Equal(t, held.UID, getBundle(t, e, a.ns, rb).UID, "%s is the Bundle the release created", rb)
		e.WaitBundlePhase(t, a.ns, b2, "Superseded", time.Minute)
		assert.Equal(t, want, rollbacks(ctx), "the adopted rollback is still the only one")

		// 4. A failure after the upgrade gets exactly one new rollback. The
		// gate goes first, so the new rollback does not wait at it.
		require.NoError(t, e.Client.Delete(ctx, gate))
		b3 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
		failed3 := e.WaitStepState(t, a.ns, pipelineName, b3, "test", "RollingBack", promoteTimeout)
		rb3 := b3 + "-rollback-alarm"
		assert.True(t, strings.HasSuffix(failed3.Status.Message, " — rollback Bundle "+rb3+" created"), failed3.Status.Message)
		fresh := u012AutoRollbackOf(t, e, a.ns, rb3, b3)
		rbVerified(t, a, rb3, "test")
		assertEnvAt(t, a, "test", fixtures.V2)
		e.WaitBundlePhase(t, a.ns, b3, "Superseded", time.Minute)
		want[rb3] = fresh.UID
		framework.Consistently(t, 20*time.Second, "one rollback per failed version", func(ctx context.Context) (bool, string) {
			got, err := u0121Rollbacks(ctx, e, a.ns)
			if err != nil {
				return false, err.Error()
			}
			return maps.Equal(want, got), fmt.Sprintf("rollback Bundles %v, want %v", got, want)
		})
	}
}
