// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// TestAdapters_NotFound (#2006): a target that does not exist is Unhealthy
// with NotFound set, in every adapter, so bake.policy fail-on-alarm can wait
// for the GitOps tool to create it (the first promotion into a new
// environment) instead of failing the step at the first check.
func TestAdapters_NotFound(t *testing.T) {
	dyn := func(objs ...runtime.Object) *dynfake.FakeDynamicClient {
		return dynfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)
	}
	workloadRef := map[string]interface{}{"workloadRef": map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "Deployment", "name": "web"}}
	tests := []struct {
		name  string
		check func() (health.HealthStatus, error)
	}{
		{"resource: Deployment", func() (health.HealthStatus, error) {
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
			return health.NewDeploymentAdapter(c, nil).Check(context.Background(),
				health.CheckOptions{Resource: health.ResourceConfig{Name: "web", Namespace: "prod"}})
		}},
		{"argocd: Application", func() (health.HealthStatus, error) {
			return health.NewArgoCDAdapter(dyn()).Check(context.Background(),
				health.CheckOptions{ArgoCD: health.ArgoCDConfig{Name: "web-prod", Namespace: "argocd"}})
		}},
		{"flux: Kustomization", func() (health.HealthStatus, error) {
			return health.NewFluxAdapter(dyn()).Check(context.Background(),
				health.CheckOptions{Flux: health.FluxConfig{Name: "web-prod", Namespace: "flux-system"}})
		}},
		{"argoRollouts: Rollout", func() (health.HealthStatus, error) {
			return health.NewArgoRolloutsAdapter(dyn()).Check(context.Background(),
				health.CheckOptions{ArgoRollouts: health.ArgoRolloutsConfig{Name: "web", Namespace: "prod"}})
		}},
		{"argoRollouts: the workloadRef Deployment", func() (health.HealthStatus, error) {
			return health.NewArgoRolloutsAdapter(dyn(rolloutObj(2, workloadRef, healthyRolloutStatus("2")))).Check(context.Background(),
				health.CheckOptions{ArgoRollouts: health.ArgoRolloutsConfig{Name: "web", Namespace: "prod"}})
		}},
		{"flagger: Canary", func() (health.HealthStatus, error) {
			return health.NewFlaggerAdapter(dyn()).Check(context.Background(),
				health.CheckOptions{Flagger: health.FlaggerConfig{Name: "web", Namespace: "prod"}})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.check()
			require.NoError(t, err)
			assert.True(t, got.NotFound, "NotFound: %s", got.Reason)
			assert.False(t, got.Healthy || got.Progressing || got.Terminal, "Unhealthy: %+v", got)
			assert.Contains(t, got.Reason, "not found")
		})
	}
}

// TestArgoRolloutsAdapter_StaleAbort (#2006, QA): when the images cannot
// tell the promoted change from the release before it (a config-only Bundle,
// or the same image again), an abort counts only when Argo Rollouts set it
// after the change can have reached git (its Progressing condition with
// reason RolloutAborted): an earlier release's abort is Waiting, not a
// Failed result that would fail the change before the GitOps tool applies
// it. The abort time is the condition's lastUpdateTime, which moves when the
// reason changes, with lastTransitionTime as the fallback. A Rollout without
// the condition cannot tell, and stays Unhealthy.
func TestArgoRolloutsAdapter_StaleAbort(t *testing.T) {
	changed := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	withAbortAt := func(at time.Time) map[string]interface{} {
		st := abortedRolloutStatus("6")
		st["conditions"] = []interface{}{map[string]interface{}{"type": "Progressing", "status": "False",
			"reason": "RolloutAborted", "lastTransitionTime": at.Format(time.RFC3339)}}
		return st
	}
	withAbortUpdated := func(transition, update time.Time) map[string]interface{} {
		st := abortedRolloutStatus("6")
		st["conditions"] = []interface{}{map[string]interface{}{"type": "Progressing", "status": "False",
			"reason": "RolloutAborted", "lastTransitionTime": transition.Format(time.RFC3339),
			"lastUpdateTime": update.Format(time.RFC3339)}}
		return st
	}
	same := []health.ImageExpectation{{Repository: podinfo, Tag: "6.15.0"}}
	tests := []struct {
		name     string
		status   map[string]interface{}
		expected []health.ImageExpectation
		want     wantKind
		reason   string
	}{
		{"config-only: an abort from before the change waits", withAbortAt(changed.Add(-time.Hour)), nil,
			isProgressing, "the abort is from an earlier release"},
		{"same image: an abort from before the change waits", withAbortAt(changed.Add(-time.Hour)), same,
			isProgressing, "the abort is from an earlier release"},
		{"config-only: an abort after the change fails", withAbortAt(changed.Add(time.Minute)), nil,
			isTerminal, "aborted the update (status.abort)"},
		{"same image: an abort after the change fails", withAbortAt(changed.Add(time.Minute)), same,
			isTerminal, "aborted the update (status.abort)"},
		// Argo Rollouts keeps lastTransitionTime when only the reason changes
		// (Progressing was already False, ProgressDeadlineExceeded); the
		// abort time is lastUpdateTime (#2010, QA).
		{"config-only: an abort after the change whose status did not change fails",
			withAbortUpdated(changed.Add(-time.Hour), changed.Add(time.Minute)), nil,
			isTerminal, "aborted the update (status.abort)"},
		{"config-only: an abort updated before the change waits",
			withAbortUpdated(changed.Add(-2*time.Hour), changed.Add(-time.Hour)), nil,
			isProgressing, "the abort is from an earlier release"},
		{"no RolloutAborted condition: cannot tell, Unhealthy", abortedRolloutStatus("6"), nil,
			isUnhealthy, "cannot tell whether the abort is from this release"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), rolloutObj(6, templateSpec(podinfo+":6.15.0"), tc.status))
			got, err := health.NewArgoRolloutsAdapter(dyn).Check(context.Background(), health.CheckOptions{
				ArgoRollouts:   health.ArgoRolloutsConfig{Name: "web", Namespace: "prod"},
				ExpectedImages: tc.expected,
				Since:          changed.Add(5 * time.Minute),
				ChangedAt:      changed,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tc.reason)
		})
	}
}
