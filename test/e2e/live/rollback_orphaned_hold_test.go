//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
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

// TestRollback_OrphanedHold (#1629) writes a hold with kubectl naming a
// rollback Bundle that does not exist. Within the grace the hold is
// BundleMissing and still in effect: a newer Bundle promotes to test but
// gets no prod step. Past the grace it is Orphaned: the hold stays in
// spec.holds, explain shows it NOT IN EFFECT, and the newer Bundle promotes
// into prod. release-hold removes it.
//
// Covers RB-HOLD-03.
func TestRollback_OrphanedHold(t *testing.T) {
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
	deadline := createdAt.Add(grace)

	holdState := func(ctx context.Context) (v1alpha1.EnvironmentHoldState, bool) {
		var p v1alpha1.Pipeline
		if err := e.Client.Get(ctx, key, &p); err != nil || len(p.Status.HoldStates) != 1 {
			return v1alpha1.EnvironmentHoldState{}, false
		}
		return p.Status.HoldStates[0], true
	}
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	if time.Now().Before(deadline) {
		st, ok := holdState(ctx)
		assert.True(t, ok && st.State == v1alpha1.HoldStateBundleMissing, "within the grace: %+v", st)
	}

	framework.Eventually(t, grace+time.Minute, "the hold is Orphaned", func(ctx context.Context) (bool, string) {
		st, ok := holdState(ctx)
		return ok && st.State == v1alpha1.HoldStateOrphaned, st.State + " " + st.Message
	})
	assert.False(t, time.Now().Before(deadline), "not Orphaned before the grace ends")
	st, _ := holdState(ctx)
	assert.Contains(t, st.Message, "rollback Bundle "+missing+" does not exist, so the hold is not in effect")
	explain := e.MustKardinal(t, a.ns, "explain", pipelineName, "--env", "prod")
	assert.Contains(t, explain, "prod: held on rollback "+missing)
	assert.Contains(t, explain, "NOT IN EFFECT")

	// The newer Bundle promotes into prod, and only once the hold was no
	// longer in effect.
	rbVerified(t, a, b2, "test", "prod")
	assertEnvAt(t, a, "prod", fixtures.V3)
	step, ok, err := e.Step(ctx, a.ns, pipelineName, b2, "prod")
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, step.CreationTimestamp.Time.Before(deadline.Add(-time.Second)),
		"the prod step was created at %s, before the grace ended at %s", step.CreationTimestamp.UTC(), deadline)

	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, key, &p))
	require.Len(t, p.Spec.Holds, 1, "the controller does not edit spec.holds")
	e.MustKardinal(t, a.ns, "release-hold", pipelineName, "--env", "prod")
	framework.Eventually(t, time.Minute, "no hold state once released", func(ctx context.Context) (bool, string) {
		var p v1alpha1.Pipeline
		if err := e.Client.Get(ctx, key, &p); err != nil {
			return false, err.Error()
		}
		return len(p.Spec.Holds) == 0 && len(p.Status.HoldStates) == 0, "holds still recorded"
	})
}
