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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// fleetTargets is n targets t00..t<n-1>.
func fleetTargets(n int) ([]v1alpha1.FleetTarget, []string) {
	var targets []v1alpha1.FleetTarget
	var names []string
	for i := range n {
		names = append(names, fmt.Sprintf("t%02d", i))
		targets = append(targets, v1alpha1.FleetTarget{Name: names[i]})
	}
	return targets, names
}

// livePipeline reads Pipeline ns/pipelineName.
func livePipeline(t *testing.T, e *framework.Env, ns string) *v1alpha1.Pipeline {
	t.Helper()
	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: pipelineName}, &p))
	return &p
}

// TestGraph_FleetRollbackWholeFleet (#1835): a 20-target fleet (maxConcurrent
// 5) runs v3 everywhere after v2. kardinal rollback --env prod creates one
// rollback version for the fleet: every target goes back to v2 in git,
// at most 5 at a time. A target removed from the Pipeline while the rollback
// is in flight leaves it: it gets no step and keeps v3, and the rollback is
// Verified with the 19 that remain. kardinal hold --env prod then holds every
// target on the rollback (one spec.holds entry for the fleet): a newer
// version is Verified in test but starts no target, and unhold releases the
// hold.
//
// Covers FLEET-12, FLEET-13.
func TestGraph_FleetRollbackWholeFleet(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	const n, maxConcurrent = 20, 5
	targets, names := fleetTargets(n)
	repo := e.Repo(t, ns, fleetFiles(ns, []string{"test", "post"}, "fleet", names, false))
	createCompactHealth(t, e, ns)
	a := &app{e: e, ns: ns, envs: []string{"test", "prod", "post"}, repo: repo}
	a.apply(t, fleetPromotion(ns, repo, &v1alpha1.FleetSpec{MaxConcurrent: maxConcurrent, Targets: targets}, "auto"))
	for _, img := range []string{imageV2, imageV3} {
		b := e.CreateBundle(t, ns, pipelineName, "--image", img)
		e.WaitBundlePhase(t, ns, b, "Verified", 4*promoteTimeout)
	}

	out, rb := rbRollback(t, a, "prod", "--yes")
	assert.Contains(t, out, "Rolling back fleet prod of "+pipelineName+" to ")
	assert.Contains(t, out, fmt.Sprintf("%d of %d targets go back", n, n))
	// The last target leaves the fleet before its place in the rollback.
	removed := names[n-1]
	a.updatePipeline(t, pipelineName, func(p *v1alpha1.Pipeline) {
		for i := range p.Spec.Environments {
			if f := p.Spec.Environments[i].Fleet; f != nil {
				f.Targets = f.Targets[:n-1]
			}
		}
	})
	e.WaitBundlePhase(t, ns, rb, "Verified", 4*promoteTimeout)

	steps := fleetSteps(t, e, ns, rb, "prod")
	require.Len(t, steps, n-1, "one rollback step per target that remains")
	assert.NotContains(t, steps, "prod-"+removed, "the removed target left the rollback")
	assert.LessOrEqual(t, maxOverlap(t, steps), maxConcurrent, "maxConcurrent paces the rollback")
	for _, name := range names[:n-1] {
		assert.Equal(t, "Verified", steps["prod-"+name].Status.State, name)
		assert.Contains(t, e.ReadFile(t, repo, repo.Branch, "fleet/"+name+"/kustomization.yaml"), "newTag: "+fixtures.V2, name)
	}
	assert.Contains(t, e.ReadFile(t, repo, repo.Branch, "fleet/"+removed+"/kustomization.yaml"), "newTag: "+fixtures.V3,
		"the removed target keeps what it ran")
	var rbObj v1alpha1.Bundle
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: rb}, &rbObj))
	assert.Equal(t, "prod", rbObj.Spec.Intent.TargetEnvironment, "one rollback version for the whole fleet")

	// Hold the whole fleet on what it runs now: the rollback.
	held := e.MustKardinal(t, ns, "hold", pipelineName, "--env", "prod", "--reason", "INC-1835")
	assert.Contains(t, held, "prod of "+pipelineName+" on "+rb)
	p := livePipeline(t, e, ns)
	require.Len(t, p.Spec.Holds, 1)
	assert.Equal(t, "prod", p.Spec.Holds[0].Environment)
	assert.Equal(t, rb, p.Spec.Holds[0].Bundle)
	v4 := e.CreateBundle(t, ns, pipelineName, "--image", imageV1)
	e.WaitStepState(t, ns, pipelineName, v4, "test", "Verified", promoteTimeout)
	a.noStep(t, v4, "prod-"+names[0], 20*time.Second)
	e.MustKardinal(t, ns, "unhold", pipelineName, "--env", "prod")
	assert.Empty(t, livePipeline(t, e, ns).Spec.Holds)
}

// TestGraph_FleetPauseMidWave (#1835): a fleet of 8 (maxConcurrent 3,
// pr-review) is paused with kardinal pause --env prod while its first wave
// waits on its PRs. The three PRs merge and their targets finish Verified,
// but no fourth target starts although places are free, the Pipeline stays
// unpaused, and its summary and kardinal get pipelines say the fleet is
// paused. Across the pause each first-wave target keeps its step (same UID)
// and has exactly one PR. kardinal resume --env prod lets the rollout go on
// to the end.
//
// Covers FLEET-10.
func TestGraph_FleetPauseMidWave(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	targets, names := fleetTargets(8)
	repo := e.Repo(t, ns, fleetFiles(ns, []string{"test", "post"}, "fleet", names, false))
	createCompactHealth(t, e, ns)
	a := &app{e: e, ns: ns, envs: []string{"test", "prod", "post"}, repo: repo}
	a.apply(t, fleetPromotion(ns, repo, &v1alpha1.FleetSpec{MaxConcurrent: 3, Targets: targets}, "pr-review"))

	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	uids := map[string]types.UID{}
	for _, n := range names[:3] {
		uids["prod-"+n] = e.WaitStepState(t, ns, pipelineName, bundle, "prod-"+n, "WaitingForMerge", promoteTimeout).UID
	}
	out := e.MustKardinal(t, ns, "pause", pipelineName, "--env", "prod")
	assert.Contains(t, out, "Fleet prod of pipeline "+pipelineName+" paused. No further target starts")
	p := livePipeline(t, e, ns)
	assert.Equal(t, []string{"prod"}, p.Spec.PausedFleets)
	assert.False(t, p.Spec.Paused, "only the fleet is paused")

	prs := map[string]int{}
	for _, n := range names[:3] {
		pr := a.openPR(t, bundle, "prod-"+n)
		prs["prod-"+n] = pr.Number
		a.merge(t, pr)
	}
	for _, n := range names[:3] {
		s := e.WaitStepState(t, ns, pipelineName, bundle, "prod-"+n, "Verified", promoteTimeout)
		assert.Equal(t, uids["prod-"+n], s.UID, "%s: the step started before the pause is the one that finished", s.Name)
	}
	framework.Consistently(t, 30*time.Second, "no target starts while the fleet is paused", func(context.Context) (bool, string) {
		steps := fleetSteps(t, e, ns, bundle, "prod")
		return len(steps) == 3, fmt.Sprintf("%d target steps, want the 3 in flight when it was paused", len(steps))
	})
	framework.Eventually(t, time.Minute, "status.summary to say the fleet is paused", func(ctx context.Context) (bool, string) {
		s := livePipeline(t, e, ns).Status.Summary
		return strings.HasPrefix(s, "Fleet prod paused; "), s
	})
	assert.Contains(t, e.MustKardinal(t, ns, "get", "pipelines"), "paused")
	// The pause rebuilt the Graph: no first-wave step was re-applied, so each
	// target has exactly the one PR it had (WaitPR fails on a second one).
	for env, number := range prs {
		pr := e.WaitPR(t, repo, time.Minute, "the one PR of "+env, func(pr gitserver.PR) bool { return pr.Head == prHead(ns, bundle, env) })
		assert.Equal(t, number, pr.Number, env)
		assert.Equal(t, "merged", pr.State, env)
	}

	e.MustKardinal(t, ns, "resume", pipelineName, "--env", "prod")
	assert.Empty(t, livePipeline(t, e, ns).Spec.PausedFleets)
	for _, n := range names[3:] {
		a.merge(t, a.openPR(t, bundle, "prod-"+n))
		e.WaitStepState(t, ns, pipelineName, bundle, "prod-"+n, "Verified", promoteTimeout)
	}
	e.WaitBundlePhase(t, ns, bundle, "Verified", 3*time.Minute)
}

// TestGraph_FleetMaxUnavailablePercent (#1835): maxUnavailable "10%" of a
// 50-target fleet (maxConcurrent 10) is 5 targets. The first 5 fail their
// health check: no further target starts, the targets in flight finish
// Verified, post never starts and the version is Failed. The CRD refuses
// 0%, 101%, ten, a quoted "2", and counts of 0 and -1.
//
// Covers FLEET-11.
func TestGraph_FleetMaxUnavailablePercent(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	targets, names := fleetTargets(50)
	for i := range 5 {
		// Its Deployment never exists: the health check times out.
		targets[i].Health = &v1alpha1.HealthConfig{Type: "resource", Timeout: "1s",
			Resource: &v1alpha1.ResourceRef{Name: "missing", Namespace: ns}}
	}
	repo := e.Repo(t, ns, fleetFiles(ns, []string{"test", "post"}, "fleet", names, false))
	createCompactHealth(t, e, ns)

	// The CRD refuses them on every supported Kubernetes (make envtest
	// installs it on the oldest and the newest).
	for _, bad := range []intstr.IntOrString{intstr.FromString("0%"), intstr.FromString("101%"), intstr.FromString("ten"),
		intstr.FromString("2"), intstr.FromInt32(0), intstr.FromInt32(-1)} {
		p := fleetPromotion(ns, repo, &v1alpha1.FleetSpec{MaxUnavailable: &bad, Targets: targets}, "auto")
		p.Name = "bad-max-unavailable"
		err := e.Client.Create(context.Background(), p)
		require.Error(t, err, "maxUnavailable %s", bad.String())
		assert.Contains(t, err.Error(), "fleet.maxUnavailable: a count of at least 1, or a percentage from 1% to 100%", bad.String())
	}

	pct := intstr.FromString("10%")
	a := &app{e: e, ns: ns, envs: []string{"test", "prod", "post"}, repo: repo}
	a.apply(t, fleetPromotion(ns, repo, &v1alpha1.FleetSpec{MaxConcurrent: 10, MaxUnavailable: &pct, Targets: targets}, "auto"))
	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	for _, n := range names[:5] {
		e.WaitStepState(t, ns, pipelineName, bundle, "prod-"+n, "Failed", promoteTimeout)
	}
	stoppedAt := time.Now()
	started := fleetSteps(t, e, ns, bundle, "prod")
	for env := range started {
		if s := started[env]; s.Status.State != "Failed" {
			e.WaitStepState(t, ns, pipelineName, bundle, env, "Verified", promoteTimeout)
		}
	}
	framework.Consistently(t, 45*time.Second, "no target starts after 10% of 50 failed", func(context.Context) (bool, string) {
		for env, s := range fleetSteps(t, e, ns, bundle, "prod") {
			if _, ok := started[env]; !ok && s.CreationTimestamp.After(stoppedAt.Add(time.Second)) {
				return false, fmt.Sprintf("%s started at %s, after the rollout stopped", env, s.CreationTimestamp)
			}
		}
		return true, ""
	})
	final := fleetSteps(t, e, ns, bundle, "prod")
	assert.Less(t, len(final), 50, "the rollout stopped: %d of 50 started", len(final))
	failed := 0
	for _, s := range final {
		if s.Status.State == "Failed" {
			failed++
		}
	}
	assert.Equal(t, 5, failed)
	a.noStep(t, bundle, "post", time.Second)
	e.WaitBundlePhase(t, ns, bundle, "Failed", time.Minute)
}
