//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// controllerHoldGrace is the controller's --hold-bundle-grace, which the
// core suite sets short (hack/e2e/up.sh).
func controllerHoldGrace(t *testing.T, e *framework.Env) time.Duration {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, e.Client.Get(context.Background(),
		types.NamespacedName{Namespace: framework.ControllerNamespace, Name: framework.ControllerName}, &d))
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, a := range c.Args {
			if v, ok := strings.CutPrefix(a, "--hold-bundle-grace="); ok {
				g, err := time.ParseDuration(v)
				require.NoError(t, err)
				return g
			}
		}
	}
	t.Fatalf("the controller has no --hold-bundle-grace (the core suite sets it in hack/e2e/up.sh)")
	return 0
}

// TestRollback_HoldBundleMissing (#1629) writes a hold with kubectl naming
// a rollback Bundle that does not exist. The hold fails closed: it stays in
// effect, within the grace and after it, so a newer Bundle promotes to test
// but gets no prod step. Past the grace the controller reports it once: the
// HoldBundleMissing condition with the release command, a Warning Event and
// a HoldBundleMissing AuditEvent; explain shows the command, and a second
// --hold is refused. The recovery is release-hold (the newer Bundle then
// promotes into prod), then rollback --hold again.
//
// Covers RB-HOLD-03.
func TestRollback_HoldBundleMissing(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	grace := controllerHoldGrace(t, e)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	ctx := context.Background()
	key := types.NamespacedName{Namespace: a.ns, Name: pipelineName}

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	rbVerified(t, a, b1, "test", "prod")

	ssr, err := e.Kube.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	require.NoError(t, err)
	missing := pipelineName + "-rollback-missing"
	createdAt := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	for range 5 { // the controller writes the Pipeline's status meanwhile
		var p v1alpha1.Pipeline
		require.NoError(t, e.Client.Get(ctx, key, &p))
		p.Spec.Holds = append(p.Spec.Holds, v1alpha1.EnvironmentHold{Environment: "prod", Bundle: missing,
			Reason: "INC-7: written by hand", CreatedBy: ssr.Status.UserInfo.Username, CreatedAt: &createdAt})
		if err = e.Client.Update(ctx, &p); !apierrors.IsConflict(err) {
			break
		}
	}
	require.NoError(t, err)

	pipe := func(ctx context.Context) (*v1alpha1.Pipeline, bool) {
		var p v1alpha1.Pipeline
		if err := e.Client.Get(ctx, key, &p); err != nil {
			return nil, false
		}
		return &p, len(p.Status.HoldStates) == 1
	}
	framework.Eventually(t, time.Minute, "the hold is BundleMissing", func(ctx context.Context) (bool, string) {
		p, ok := pipe(ctx)
		if !ok {
			return false, "no hold state"
		}
		return p.Status.HoldStates[0].State == v1alpha1.HoldStateBundleMissing, p.Status.HoldStates[0].State
	})

	// The newer Bundle promotes to test and gets no prod step: through the
	// grace and after it.
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitStepState(t, a.ns, pipelineName, b2, "test", "Verified", promoteTimeout)
	framework.Eventually(t, grace+time.Minute, "the missing Bundle is reported", func(ctx context.Context) (bool, string) {
		p, ok := pipe(ctx)
		if !ok {
			return false, "no hold state"
		}
		for _, c := range p.Status.Conditions {
			if c.Type == "HoldBundleMissing" {
				return c.Status == metav1.ConditionTrue, string(c.Status) + " " + c.Message
			}
		}
		return false, "no HoldBundleMissing condition"
	})
	e.NoStep(t, a.ns, pipelineName, b2, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V2)
	p, _ := pipe(ctx)
	st := p.Status.HoldStates[0]
	require.NotNil(t, st.ReportedAt)
	assert.False(t, st.ReportedAt.Before(&metav1.Time{Time: st.BundleMissingSince.Add(grace)}), "reported only after the grace")
	assert.Contains(t, st.Message, "the hold stays in effect")
	assert.Contains(t, st.Message, "kardinal release-hold "+pipelineName+" --env prod")
	require.Len(t, p.Spec.Holds, 1, "the controller does not edit spec.holds")

	framework.Eventually(t, time.Minute, "one HoldBundleMissing Warning Event", func(ctx context.Context) (bool, string) {
		evs, err := e.Events(ctx, a.ns, "Pipeline", pipelineName)
		if err != nil {
			return false, err.Error()
		}
		got := rbReason(evs, "HoldBundleMissing")
		return len(got) == 1 && got[0].Type == "Warning" && strings.Contains(got[0].Note, missing), fmt.Sprintf("%d events", len(got))
	})
	framework.Eventually(t, time.Minute, "a HoldBundleMissing AuditEvent", func(ctx context.Context) (bool, string) {
		evs, err := e.AuditEvents(ctx, a.ns, missing)
		if err != nil {
			return false, err.Error()
		}
		for _, ae := range evs {
			if ae.Spec.Action == "HoldBundleMissing" {
				return ae.Spec.Outcome == "Failure", ae.Spec.Outcome
			}
		}
		return false, "none"
	})
	explain := e.MustKardinal(t, a.ns, "explain", pipelineName, "--env", "prod")
	assert.Contains(t, explain, "prod: held on rollback "+missing)
	assert.Contains(t, explain, "but the rollback Bundle does not exist")
	assert.Contains(t, explain, "Release with: kardinal release-hold "+pipelineName+" --env prod")

	// A second hold is refused while this one is there, missing Bundle or not.
	again := rbRefused(t, a, "--env", "prod", "--hold", "--reason", "again")
	assert.Contains(t, again, "environment prod is already held on "+missing)

	// The documented recovery: release the hold (the newer Bundle then
	// promotes to prod), then roll back and hold again.
	e.MustKardinal(t, a.ns, "release-hold", pipelineName, "--env", "prod")
	rbVerified(t, a, b2, "prod")
	assertEnvAt(t, a, "prod", fixtures.V3)
	framework.Eventually(t, time.Minute, "no hold state once released", func(ctx context.Context) (bool, string) {
		p, _ := pipe(ctx)
		return p != nil && len(p.Spec.Holds) == 0 && len(p.Status.HoldStates) == 0, "holds still recorded"
	})
	_, rb := rbRollback(t, a, "prod", "--hold", "--reason", "INC-7: hold again")
	rbVerified(t, a, rb, "prod")
	assertEnvAt(t, a, "prod", fixtures.V2)
	framework.Eventually(t, time.Minute, "the new hold is Active", func(ctx context.Context) (bool, string) {
		p, ok := pipe(ctx)
		if !ok {
			return false, "no hold state"
		}
		st := p.Status.HoldStates[0]
		return st.Bundle == rb && st.State == v1alpha1.HoldStateActive, st.Bundle + " " + st.State
	})
	e.MustKardinal(t, a.ns, "release-hold", pipelineName, "--env", "prod")
}
