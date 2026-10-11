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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// refreshHealthTimeout is the health.timeout of the refresh tests: with the
// refresh off, a stock Argo CD (timeout.reconciliation 180s) takes up to
// its poll interval plus jitter per environment.
const refreshHealthTimeout = 15 * time.Minute

// syncLatency is how long the step's commit took to be Verified: from the
// start of its health check (the commit is in git) to its Verified
// condition. timeout is the step's health.timeout.
func syncLatency(ps *v1alpha1.PromotionStep, timeout time.Duration) time.Duration {
	var verified time.Time
	for _, c := range ps.Status.Conditions {
		if c.Type == "Verified" && c.Status == metav1.ConditionTrue {
			verified = c.LastTransitionTime.Time
		}
	}
	if ps.Status.HealthCheckExpiry == nil || verified.IsZero() {
		return 0
	}
	return verified.Sub(ps.Status.HealthCheckExpiry.Add(-timeout))
}

// refreshEvent reports whether the step has an Event of reason.
func refreshEvent(t *testing.T, e *framework.Env, ps *v1alpha1.PromotionStep, reason string) bool {
	t.Helper()
	evs, err := e.Events(context.Background(), ps.Namespace, "PromotionStep", ps.Name)
	require.NoError(t, err)
	for _, ev := range evs {
		if ev.Reason == reason && ev.Type == corev1.EventTypeNormal {
			return true
		}
	}
	return false
}

// TestHealth_ArgoCDRefresh promotes one Bundle through test, uat and prod
// twice at once: on a Pipeline with the default health.refresh and on one
// with health.refresh: false on every environment. The first asks each
// environment's Application to refresh once its commit is pushed: the step
// records refreshRequestedAt and the pushed commit, and emits
// GitOpsRefreshRequested. The second records nothing and is verified on Argo
// CD's own poll. Both run the new image everywhere. The log prints each
// environment's time from commit to Verified for both; on a stock Argo CD
// (KARDINAL_E2E_ARGOCD_RECONCILIATION=180s) they show the difference.
// Covers HEALTH-REFRESH-01, HEALTH-REFRESH-03.
func TestHealth_ArgoCDRefresh(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	envs := []string{"test", "uat", "prod"}
	off := false
	type run struct {
		name    string
		a       *app
		refresh *bool
		bundle  string
	}
	runs := []*run{{name: "refresh"}, {name: "no refresh", refresh: &off}}
	for _, r := range runs {
		r.a = newArgoApp(t, e, envs...)
		p := r.a.pipeline(nil)
		for i := range p.Spec.Environments {
			p.Spec.Environments[i].Health.Timeout = refreshHealthTimeout.String()
			p.Spec.Environments[i].Health.Refresh = r.refresh
		}
		r.a.apply(t, p)
	}
	newImage := fixtures.Image + ":" + fixtures.V2
	for _, r := range runs {
		r.bundle = e.CreateBundle(t, r.a.ns, pipelineName, "--image", newImage)
	}

	latency := map[string][]string{}
	for _, env := range envs {
		for _, r := range runs {
			ps := e.WaitStepState(t, r.a.ns, pipelineName, r.bundle, env, "Verified", refreshHealthTimeout+time.Minute)
			assert.Equal(t, newImage, e.DeploymentImage(t, r.a.ns, fixtures.Workload(env)), "%s %s", r.name, env)
			latency[r.name] = append(latency[r.name], fmt.Sprintf("%s %s", env, syncLatency(ps, refreshHealthTimeout).Round(time.Second)))
			commit := ps.Status.Outputs["commitSHA"]
			require.NotEmpty(t, commit, "%s %s: auto promotion pushed a commit", r.name, env)
			if r.refresh == nil {
				require.NotNil(t, ps.Status.RefreshRequestedAt, "%s %s: refresh requested", r.name, env)
				assert.Equal(t, commit, ps.Status.RefreshRevision, "%s %s", r.name, env)
				assert.Equal(t, fmt.Sprintf("refresh requested: Application %s/%s", framework.ArgoCDNamespace, r.a.argoApp(env)),
					ps.Status.RefreshMessage, "%s %s", r.name, env)
				assert.True(t, refreshEvent(t, e, ps, "GitOpsRefreshRequested"), "%s %s: GitOpsRefreshRequested Event", r.name, env)
			} else {
				assert.Nil(t, ps.Status.RefreshRequestedAt, "%s %s: health.refresh: false asks nothing", r.name, env)
				assert.False(t, refreshEvent(t, e, ps, "GitOpsRefreshRequested"), "%s %s", r.name, env)
			}
		}
	}
	for _, r := range runs {
		e.WaitBundlePhase(t, r.a.ns, r.bundle, "Verified", time.Minute)
		t.Logf("commit to Verified, %s: %v", r.name, latency[r.name])
	}
}

// TestFlux_RefreshOnPromotion runs Flux with a one-hour GitRepository and
// Kustomization interval, so only a refresh request syncs a commit within
// the test. test promotes with the default health.refresh: the step sets
// reconcile.fluxcd.io/requestedAt to its status.refreshRequestedAt on the
// GitRepository and the Kustomization, Flux handles that request
// (status.lastHandledReconcileAt) and applies the commit, and the step is
// Verified. uat has health.refresh: false: its commit is not applied while
// the source is not fetched again, and the step stays HealthChecking, until
// the test asks Flux to reconcile the source itself (the poll a real
// interval would make). Covers HEALTH-REFRESH-02, HEALTH-REFRESH-03.
func TestFlux_RefreshOnPromotion(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	envs := []string{"test", "uat"}
	a := &fluxApp{e: e, ns: ns, envs: envs,
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))}
	e.FluxSource(t, ns, fluxSource, a.repo, time.Hour)
	for _, env := range envs {
		a.kustomize(t, env, fluxSource, time.Hour)
	}
	for _, env := range envs {
		a.waitSynced(t, env)
	}
	p := a.pipeline(nil)
	off := false
	for i := range p.Spec.Environments {
		p.Spec.Environments[i].Health.Timeout = "10m"
	}
	p.Spec.Environments[1].Health.Refresh = &off
	a.apply(t, p)

	newImage := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, ns, pipelineName, "--image", newImage)

	ps := e.WaitStepState(t, ns, pipelineName, bundle, "test", "Verified", 3*time.Minute)
	require.NotNil(t, ps.Status.RefreshRequestedAt)
	commit := ps.Status.Outputs["commitSHA"]
	require.NotEmpty(t, commit)
	assert.Equal(t, commit, ps.Status.RefreshRevision)
	requested := ps.Status.RefreshRequestedAt.UTC().Format(time.RFC3339)
	assert.Equal(t, fmt.Sprintf("refresh requested: GitRepository %s/%s, Kustomization %s/%s", ns, fluxSource, ns, fixtures.Workload("test")),
		ps.Status.RefreshMessage)
	assert.True(t, refreshEvent(t, e, ps, "GitOpsRefreshRequested"))
	ctx := context.Background()
	for _, obj := range []struct{ kind, name string }{{"GitRepository", fluxSource}, {"Kustomization", fixtures.Workload("test")}} {
		gvr := framework.GitRepositoryGVR
		if obj.kind == "Kustomization" {
			gvr = framework.KustomizationGVR
		}
		got, err := e.FluxObject(ctx, gvr, ns, obj.name)
		require.NoError(t, err)
		assert.Equal(t, requested, got.GetAnnotations()["reconcile.fluxcd.io/requestedAt"], obj.kind)
		handled, _, _ := unstructured.NestedString(got.Object, "status", "lastHandledReconcileAt")
		assert.Equal(t, requested, handled, "%s: Flux handled the step's request", obj.kind)
	}
	ks := a.kustomization(t, "test")
	assert.Contains(t, framework.FluxAppliedRevision(ks), commit)
	assert.Equal(t, newImage, e.DeploymentImage(t, ns, fixtures.Workload("test")))
	t.Logf("test: commit to Verified %s", syncLatency(ps, 10*time.Minute).Round(time.Second))

	// uat: no refresh, so the one-hour interval holds the commit back.
	e.WaitStepState(t, ns, pipelineName, bundle, "uat", "HealthChecking", 3*time.Minute)
	framework.Consistently(t, 45*time.Second, "uat waits for Flux's own poll", func(ctx context.Context) (bool, string) {
		st, _, err := e.Step(ctx, ns, pipelineName, bundle, "uat")
		if err != nil || st == nil {
			return false, fmt.Sprint("step lookup: ", err)
		}
		return st.Status.State == "HealthChecking" && st.Status.RefreshRequestedAt == nil,
			fmt.Sprintf("%s refreshRequestedAt=%v", st.Status.State, st.Status.RefreshRequestedAt)
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, ns, fixtures.Workload("uat")))
	e.ReconcileFlux(t, framework.GitRepositoryGVR, ns, fluxSource)
	uat := e.WaitStepState(t, ns, pipelineName, bundle, "uat", "Verified", 3*time.Minute)
	assert.Nil(t, uat.Status.RefreshRequestedAt)
	assert.False(t, refreshEvent(t, e, uat, "GitOpsRefreshRequested"))
	assert.Equal(t, newImage, e.DeploymentImage(t, ns, fixtures.Workload("uat")))
	e.WaitBundlePhase(t, ns, bundle, "Verified", time.Minute)
}
