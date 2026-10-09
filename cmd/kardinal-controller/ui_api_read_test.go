// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// uiReadGet serves one GET through the UI API routes, without auth or CORS.
func uiReadGet(t *testing.T, c client.Client, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	newUIAPIServer(c, zerolog.Nop()).RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func uiStep(ns, name, bundle, env, state string) *v1alpha1.PromotionStep {
	return &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"kardinal.io/bundle": bundle}},
		Spec:       v1alpha1.PromotionStepSpec{BundleName: bundle, Environment: env, PipelineName: "app"},
		Status:     v1alpha1.PromotionStepStatus{State: state},
	}
}

func uiGateInstance(ns, name, bundle, gateName, env string, ready bool) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{
			"kardinal.io/bundle":      bundle,
			"kardinal.io/gate-name":   gateName,
			"kardinal.io/environment": env,
		}},
		Spec:   v1alpha1.PolicyGateSpec{Expression: "true"},
		Status: v1alpha1.PolicyGateStatus{Ready: ready},
	}
}

func graphEdges(g uiGraphResponse) []string {
	out := make([]string, 0, len(g.Edges))
	for _, e := range g.Edges {
		out = append(out, e.From+"->"+e.To)
	}
	sort.Strings(out)
	return out
}

// TestUIAPI_BundleGraph_FollowsPipelineDependencies covers C07-controller-12:
// the graph used list order (i-1 -> i) and ignored dependsOn and waves.
func TestUIAPI_BundleGraph_FollowsPipelineDependencies(t *testing.T) {
	tests := []struct {
		name  string
		envs  []v1alpha1.EnvironmentSpec
		edges []string
	}{
		{
			name: "linear default",
			envs: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "uat"}, {Name: "prod"}},
			edges: []string{
				"step-test->step-uat",
				"step-uat->step-prod",
			},
		},
		{
			name: "fan-out and fan-in with dependsOn",
			envs: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat-a", DependsOn: []string{"test"}},
				{Name: "uat-b", DependsOn: []string{"test"}},
				{Name: "prod", DependsOn: []string{"uat-a", "uat-b"}},
			},
			edges: []string{
				"step-test->step-uat-a",
				"step-test->step-uat-b",
				"step-uat-a->step-prod",
				"step-uat-b->step-prod",
			},
		},
		{
			name: "waves",
			envs: []v1alpha1.EnvironmentSpec{
				{Name: "eu-1", Wave: 1},
				{Name: "eu-2", Wave: 1},
				{Name: "us-1", Wave: 2},
			},
			edges: []string{
				"step-eu-1->step-us-1",
				"step-eu-2->step-us-1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
				&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
					Spec: v1alpha1.PipelineSpec{Environments: tt.envs}},
				&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
					Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
			).Build()

			rec := uiReadGet(t, c, "/api/v1/ui/bundles/app-v1/graph")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var g uiGraphResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &g))
			assert.Len(t, g.Nodes, len(tt.envs))
			assert.Equal(t, tt.edges, graphEdges(g))
		})
	}
}

// TestUIAPI_BundleGraph_GatesPerEnvironment covers C07-controller-13: the
// same gate in two environments collapsed into one node labelled with the
// gate name as its Environment, and gates on the first environment were
// never drawn.
func TestUIAPI_BundleGraph_GatesPerEnvironment(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "uat"}, {Name: "prod"}}}},
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
			Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
		uiStep("default", "app-v1-test", "app-v1", "test", "Verified"),
		uiGateInstance("default", "app-v1-freeze-test", "app-v1", "freeze", "test", true),
		uiGateInstance("default", "app-v1-no-weekend-uat", "app-v1", "no-weekend", "uat", true),
		uiGateInstance("default", "app-v1-no-weekend-prod", "app-v1", "no-weekend", "prod", false),
	).Build()

	rec := uiReadGet(t, c, "/api/v1/ui/bundles/app-v1/graph")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var g uiGraphResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &g))

	gates := map[string]uiGraphNode{}
	for _, n := range g.Nodes {
		if n.Type == "PolicyGate" {
			gates[n.ID] = n
		}
	}
	require.Len(t, gates, 3, "one gate node per gate and environment")
	for id, env := range map[string]string{
		"gate-app-v1-freeze-test":     "test",
		"gate-app-v1-no-weekend-uat":  "uat",
		"gate-app-v1-no-weekend-prod": "prod",
	} {
		require.Contains(t, gates, id)
		assert.Equal(t, env, gates[id].Environment)
	}
	assert.Equal(t, "no-weekend", gates["gate-app-v1-no-weekend-prod"].Label)
	assert.Equal(t, "Pass", gates["gate-app-v1-no-weekend-uat"].State)
	assert.Equal(t, "Pending", gates["gate-app-v1-no-weekend-prod"].State)

	assert.Equal(t, []string{
		"app-v1-test->gate-app-v1-no-weekend-uat",
		"gate-app-v1-freeze-test->app-v1-test",
		"gate-app-v1-no-weekend-prod->step-prod",
		"gate-app-v1-no-weekend-uat->step-uat",
		"step-uat->gate-app-v1-no-weekend-prod",
	}, graphEdges(g))
}

// TestUIAPI_BundleGraph_ScopedToBundleNamespace covers the graph half of
// C07-controller-15: steps and gates of a same-named bundle in another
// namespace were mixed into the graph.
func TestUIAPI_BundleGraph_ScopedToBundleNamespace(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}}},
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "team-a"},
			Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
		uiStep("team-a", "a-test", "app-v1", "test", "Verified"),
		uiStep("team-b", "b-test", "app-v1", "test", "Failed"),
		uiStep("team-b", "b-staging", "app-v1", "staging", "Failed"),
		uiGateInstance("team-b", "b-gate", "app-v1", "no-weekend", "prod", false),
	).Build()

	rec := uiReadGet(t, c, "/api/v1/ui/bundles/app-v1/graph?namespace=team-a")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var g uiGraphResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &g))
	ids := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	assert.ElementsMatch(t, []string{"a-test", "step-prod"}, ids)
}

// TestUIAPI_BundleSteps_ScopedToBundleNamespace covers the steps half of
// C07-controller-15: /bundles/{name}/steps returned the steps of every
// same-named bundle in every namespace.
func TestUIAPI_BundleSteps_ScopedToBundleNamespace(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "team-a"},
			Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "team-b"},
			Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
		uiStep("team-a", "a-test", "app-v1", "test", "Verified"),
		uiStep("team-b", "b-test", "app-v1", "test", "Failed"),
		uiStep("team-b", "b-prod", "app-v1", "prod", "Failed"),
	).Build()

	tests := []struct {
		path  string
		steps []string
	}{
		{path: "/api/v1/ui/bundles/app-v1/steps?namespace=team-a", steps: []string{"team-a/a-test"}},
		{path: "/api/v1/ui/bundles/app-v1/steps?namespace=team-b", steps: []string{"team-b/b-prod", "team-b/b-test"}},
		{path: "/api/v1/ui/bundles/app-v1/steps", steps: []string{"team-a/a-test"}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := uiReadGet(t, c, tt.path)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var resp []uiStepResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			got := make([]string, 0, len(resp))
			for _, st := range resp {
				got = append(got, st.Namespace+"/"+st.Name)
			}
			sort.Strings(got)
			assert.Equal(t, tt.steps, got)
		})
	}
}

// TestUIAPI_ReadHandlersReportListErrors covers the swallowed List errors of
// C07-controller-31: the UI showed "no bundle" during an API or RBAC outage.
func TestUIAPI_ReadHandlersReportListErrors(t *testing.T) {
	tests := []struct {
		name string
		fail client.ObjectList
		path string
	}{
		{name: "pipelines: bundles", fail: &v1alpha1.BundleList{}, path: "/api/v1/ui/pipelines"},
		{name: "pipelines: gates", fail: &v1alpha1.PolicyGateList{}, path: "/api/v1/ui/pipelines"},
		{name: "pipelines: steps", fail: &v1alpha1.PromotionStepList{}, path: "/api/v1/ui/pipelines"},
		{name: "graph: bundles", fail: &v1alpha1.BundleList{}, path: "/api/v1/ui/bundles/app-v1/graph"},
		{name: "graph: gates", fail: &v1alpha1.PolicyGateList{}, path: "/api/v1/ui/bundles/app-v1/graph"},
		{name: "graph: steps", fail: &v1alpha1.PromotionStepList{}, path: "/api/v1/ui/bundles/app-v1/graph"},
		{name: "bundle steps: bundles", fail: &v1alpha1.BundleList{}, path: "/api/v1/ui/bundles/app-v1/steps"},
		{name: "bundle steps: steps", fail: &v1alpha1.PromotionStepList{}, path: "/api/v1/ui/bundles/app-v1/steps"},
		{name: "step events", fail: &corev1.EventList{}, path: "/api/v1/ui/steps/default/app-v1-test/events"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
				&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
					Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}}}},
				&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
					Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
				uiStep("default", "app-v1-test", "app-v1", "test", "Verified"),
			).WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if sameListType(list, tt.fail) {
						return errors.New("apiserver unavailable")
					}
					return c.List(ctx, list, opts...)
				},
			}).Build()

			rec := uiReadGet(t, c, tt.path)
			assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		})
	}
}

func sameListType(a, b client.ObjectList) bool {
	switch a.(type) {
	case *v1alpha1.BundleList:
		_, ok := b.(*v1alpha1.BundleList)
		return ok
	case *v1alpha1.PolicyGateList:
		_, ok := b.(*v1alpha1.PolicyGateList)
		return ok
	case *v1alpha1.PromotionStepList:
		_, ok := b.(*v1alpha1.PromotionStepList)
		return ok
	case *corev1.EventList:
		_, ok := b.(*corev1.EventList)
		return ok
	}
	return false
}

// TestUIAPI_Pipelines_ActiveBundleAndCounts covers C07-controller-14 (the
// oldest of two bundles in the same phase was shown as active) and the
// blocker half of C07-controller-15 (blocker and failed-step counts were
// keyed by bare bundle name and mixed across namespaces).
func TestUIAPI_Pipelines_ActiveBundleAndCounts(t *testing.T) {
	older := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	newer := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	bundle := func(ns, name string, created metav1.Time) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: created},
			Spec:       v1alpha1.BundleSpec{Pipeline: "app"},
			Status:     v1alpha1.BundleStatus{Phase: "Verified"},
		}
	}
	// test fans out to prod-eu and prod-us.
	pipeline := func(ns string) *v1alpha1.Pipeline {
		return &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod-eu", DependsOn: []string{"test"}},
				{Name: "prod-us", DependsOn: []string{"test"}},
			}}}
	}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		pipeline("team-a"), pipeline("team-b"),
		// The older bundle is listed first (names sort by creation time).
		bundle("team-a", "app-1-old", older),
		bundle("team-a", "app-2-new", newer),
		bundle("team-b", "app-2-new", newer),
		uiGateInstance("team-a", "g1", "app-2-new", "no-weekend", "test", false),
		uiStep("team-b", "s0", "app-2-new", "test", "Verified"),
		uiStep("team-b", "s1", "app-2-new", "prod-eu", "Failed"),
		uiGateInstance("team-b", "g2", "app-2-new", "no-weekend", "prod-us", false),
		uiGateInstance("team-b", "g3", "app-2-new", "freeze", "prod-us", false),
	).Build()

	rec := uiReadGet(t, c, "/api/v1/ui/pipelines")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp []uiPipelineResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	byNS := map[string]uiPipelineResponse{}
	for _, p := range resp {
		byNS[p.Namespace] = p
	}
	require.Len(t, byNS, 2)
	assert.Equal(t, "app-2-new", byNS["team-a"].ActiveBundleName, "newest bundle wins a phase tie")
	assert.Equal(t, 1, byNS["team-a"].BlockerCount)
	assert.Equal(t, 0, byNS["team-a"].FailedStepCount)
	assert.Equal(t, 2, byNS["team-b"].BlockerCount)
	assert.Equal(t, 1, byNS["team-b"].FailedStepCount)
}

// TestUIAPI_Pipelines_CurrentBundleIsNewestNotSuperseded covers E2E-R15: the
// API preferred an older Verified bundle over a newer Failed one, so the
// pipeline showed activeBundleName=<old Verified bundle>, no failedStepCount
// and all environments Verified while its newest bundle had failed. The current
// bundle is now the newest non-Superseded bundle by creationTimestamp, whatever
// its phase; the counts and environment states come from that bundle.
func TestUIAPI_Pipelines_CurrentBundleIsNewestNotSuperseded(t *testing.T) {
	base := time.Now().Add(-1 * time.Hour)
	at := func(min int) metav1.Time { return metav1.NewTime(base.Add(time.Duration(min) * time.Minute)) }
	bundle := func(name, phase string, created metav1.Time, envs ...v1alpha1.EnvironmentStatus) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: created},
			Spec:       v1alpha1.BundleSpec{Pipeline: "app"},
			Status:     v1alpha1.BundleStatus{Phase: phase, Environments: envs},
		}
	}
	env := func(name, phase string) v1alpha1.EnvironmentStatus {
		return v1alpha1.EnvironmentStatus{Name: name, Phase: phase}
	}
	pipeline := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "pa"}, {Name: "pb"}}}}

	tests := []struct {
		name        string
		objs        []client.Object
		wantActive  string
		wantFailed  int
		wantBlocker int
		wantStates  map[string]string
	}{
		{
			// The live repro (ui-rerun.log:72): gapp-e2e2-hxklw Failed in pb,
			// the two older bundles Verified.
			name: "newer Failed bundle beats older Verified bundles",
			objs: []client.Object{
				bundle("app-only-pa", "Verified", at(0), env("pa", "Verified")),
				bundle("app-kjkwt", "Verified", at(1), env("pa", "Verified"), env("pb", "Verified")),
				bundle("app-hxklw", "Failed", at(7), env("pa", "Verified"), env("pb", "Failed")),
				uiStep("default", "app-kjkwt-pb", "app-kjkwt", "pb", "Verified"),
				uiStep("default", "app-hxklw-pa", "app-hxklw", "pa", "Verified"),
				uiStep("default", "app-hxklw-pb", "app-hxklw", "pb", "Failed"),
			},
			wantActive: "app-hxklw",
			wantFailed: 1,
			wantStates: map[string]string{"pa": "Verified", "pb": "Failed"},
		},
		{
			name: "newer Verified bundle beats an older Promoting bundle",
			objs: []client.Object{
				bundle("app-1", "Promoting", at(0), env("pa", "Promoting")),
				bundle("app-2", "Verified", at(1), env("pa", "Verified")),
				uiGateInstance("default", "g-old", "app-1", "no-weekend", "pa", false),
			},
			wantActive: "app-2",
			wantStates: map[string]string{"pa": "Verified"},
		},
		{
			name: "a newer Superseded bundle is skipped; blockers come from the current bundle",
			objs: []client.Object{
				bundle("app-1", "Promoting", at(0), env("pa", "WaitingForGate")),
				bundle("app-2", "Superseded", at(1)),
				uiGateInstance("default", "g1", "app-1", "no-weekend", "pa", false),
				uiGateInstance("default", "g2", "app-2", "no-weekend", "pa", false),
				uiGateInstance("default", "g3", "app-2", "freeze", "pa", false),
			},
			wantActive:  "app-1",
			wantBlocker: 1,
			wantStates:  map[string]string{"pa": "WaitingForGate"},
		},
		{
			name: "only Superseded bundles: the newest is shown",
			objs: []client.Object{
				bundle("app-1", "Superseded", at(0)),
				bundle("app-2", "Superseded", at(1)),
			},
			wantActive: "app-2",
		},
		{
			// QA #1489: a Rejected bundle whose change is live stays current.
			name: "a newer Rejected bundle whose change is live is current",
			objs: []client.Object{
				bundle("app-1", "Verified", at(0), env("pa", "Verified")),
				rejectedUIBundle(bundle("app-2", "Rejected", at(1), env("pa", "Verified"))),
				uiStep("default", "app-2-pa", "app-2", "pa", "Verified"),
			},
			wantActive: "app-2",
			wantStates: map[string]string{"pa": "Verified"},
		},
		{
			name: "a newer Rejected bundle that never went live is skipped",
			objs: []client.Object{
				bundle("app-1", "Verified", at(0), env("pa", "Verified")),
				rejectedUIBundle(bundle("app-2", "Rejected", at(1), env("pa", "WaitingForMerge"))),
				uiStep("default", "app-2-pa", "app-2", "pa", "WaitingForMerge"),
			},
			wantActive: "app-1",
			wantStates: map[string]string{"pa": "Verified"},
		},
		{
			name: "same creationTimestamp: the name breaks the tie, as in the web",
			objs: []client.Object{
				bundle("app-b", "Failed", at(0)),
				bundle("app-a", "Promoting", at(0)),
			},
			wantActive: "app-b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).
				WithObjects(append([]client.Object{pipeline.DeepCopy()}, tt.objs...)...).Build()
			rec := uiReadGet(t, c, "/api/v1/ui/pipelines")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var resp []uiPipelineResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Len(t, resp, 1)
			got := resp[0]
			assert.Equal(t, tt.wantActive, got.ActiveBundleName)
			assert.Equal(t, tt.wantFailed, got.FailedStepCount)
			assert.Equal(t, tt.wantBlocker, got.BlockerCount)
			assert.Equal(t, tt.wantStates, got.EnvironmentStates)
		})
	}
}

// TestUIAPI_StepEvents_OnlyPromotionStepEvents covers C07-controller-11: the
// endpoint returned events for any object in any namespace, for example a
// kube-system Pod, by name alone.
func TestUIAPI_StepEvents_OnlyPromotionStepEvents(t *testing.T) {
	ev := func(ns, name, kind, obj string, uid types.UID) *corev1.Event {
		return &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: ns},
			InvolvedObject: corev1.ObjectReference{Kind: kind, Name: obj, Namespace: ns, UID: uid},
			Reason:         name,
			LastTimestamp:  metav1.Now(),
		}
	}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: "app-v1-prod", Namespace: "team-a", UID: "uid-now"}},
		ev("team-a", "step-event", "PromotionStep", "app-v1-prod", "uid-now"),
		ev("team-a", "old-step-event", "PromotionStep", "app-v1-prod", "uid-before"),
		ev("team-a", "pod-event", "Pod", "app-v1-prod", "uid-pod"),
		ev("kube-system", "coredns-event", "Pod", "coredns-abc", "uid-dns"),
	).Build()

	tests := []struct {
		path     string
		wantCode int
		reasons  []string
	}{
		{path: "/api/v1/ui/steps/team-a/app-v1-prod/events", wantCode: http.StatusOK, reasons: []string{"step-event"}},
		{path: "/api/v1/ui/steps/kube-system/coredns-abc/events", wantCode: http.StatusNotFound},
		{path: "/api/v1/ui/steps/team-b/app-v1-prod/events", wantCode: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := uiReadGet(t, c, tt.path)
			require.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "coredns")
			if tt.wantCode != http.StatusOK {
				return
			}
			var resp []uiEventResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			reasons := make([]string, 0, len(resp))
			for _, e := range resp {
				reasons = append(reasons, e.Reason)
			}
			assert.Equal(t, tt.reasons, reasons)
		})
	}
}

// TestUIAPI_GateApprove_RetriesConflicts covers C07-controller-24: a
// concurrent write to the gate (the reconciler, a second approver) made the
// Update fail with 409, shown to the user as a 500. Other Get errors were
// reported as "gate not found".
func TestUIAPI_GateApprove_RetriesConflicts(t *testing.T) {
	gateGR := schema.GroupResource{Group: "kardinal.io", Resource: "policygates"}
	tests := []struct {
		name          string
		getErr        error
		conflicts     int
		wantCode      int
		wantOverrides int
	}{
		{name: "one conflict is retried", conflicts: 1, wantCode: http.StatusOK, wantOverrides: 1},
		{name: "persistent conflict fails", conflicts: 100, wantCode: http.StatusInternalServerError},
		{name: "get error is not reported as not found", getErr: errors.New("apiserver unavailable"),
			wantCode: http.StatusInternalServerError},
		{name: "missing gate", getErr: apierrors.NewNotFound(gateGR, "g"), wantCode: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conflicts := tt.conflicts
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
				&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "team-a"}},
			).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*v1alpha1.PolicyGate); ok && tt.getErr != nil {
						return tt.getErr
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if conflicts > 0 {
						conflicts--
						return apierrors.NewConflict(gateGR, obj.GetName(), errors.New("object was modified"))
					}
					return c.Update(ctx, obj, opts...)
				},
			}).Build()

			mux := http.NewServeMux()
			newUIAPIServer(c, zerolog.Nop()).RegisterRoutes(mux)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/ui/gates/team-a/g/approve",
				strings.NewReader(`{"reason":"hotfix"}`)))
			require.Equal(t, tt.wantCode, rec.Code, rec.Body.String())

			tt.getErr = nil
			var gate v1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "g"}, &gate))
			assert.Len(t, gate.Spec.Overrides, tt.wantOverrides)
		})
	}
}

// gateHoldCase is one state of bundle app-b2 on the Pipeline test -> uat ->
// prod: objs are its Bundle, steps and gates; want is how many of its gates
// hold it back (graph.GateHolds).
type gateHoldCase struct {
	name string
	objs []client.Object
	want int
}

// gateHoldCases returns the Pipeline and the bundle states the UI API's
// holding rule is tested with.
func gateHoldCases() (*v1alpha1.Pipeline, []gateHoldCase) {
	now := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	pipeline := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "uat"}, {Name: "prod"},
		}}}
	bundle := func(phase string) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: "app-b2", Namespace: "default", CreationTimestamp: now},
			Spec:       v1alpha1.BundleSpec{Pipeline: "app"},
			Status:     v1alpha1.BundleStatus{Phase: phase},
		}
	}
	soak := uiGateInstance("default", "app-b2-prod-soak", "app-b2", "require-uat-soak", "prod", false)
	postDeploy := uiGateInstance("default", "app-b2-prod-soak", "app-b2", "require-uat-soak", "prod", false)
	postDeploy.Spec.When = "post-deploy" //nolint:staticcheck // SA1019: when has no effect (#1323)
	// prodStep is the prod step waiting on the soak gate, the way the Graph
	// builder lists gate instances in spec.requiredGates.
	prodStep := func(state string) *v1alpha1.PromotionStep {
		s := uiStep("default", "s-prod", "app-b2", "prod", state)
		s.Spec.RequiredGates = []string{"app-b2-prod-soak"}
		return s
	}
	upstream := func() []client.Object {
		return []client.Object{bundle("Promoting"),
			uiStep("default", "s-test", "app-b2", "test", "Verified"),
			uiStep("default", "s-uat", "app-b2", "uat", "Verified")}
	}
	return pipeline, []gateHoldCase{
		{
			// checkRequiredGates keeps the step Pending, before git.
			name: "gate holds the Pending prod step",
			objs: append(upstream(), prodStep("Pending"), soak),
			want: 1,
		},
		{
			name: "gate holds the new prod step",
			objs: append(upstream(), prodStep(""), soak),
			want: 1,
		},
		{
			name: "gate after the prod step started",
			objs: append(upstream(), prodStep("Promoting"), soak),
			want: 0,
		},
		{
			// #1323: spec.when has no effect.
			name: "post-deploy gate holds the Pending prod step",
			objs: append(upstream(), prodStep("Pending"), postDeploy),
			want: 1,
		},
		{
			name: "health checking in test",
			objs: []client.Object{bundle("Promoting"),
				uiStep("default", "s-test", "app-b2", "test", "HealthChecking"), soak},
			want: 0,
		},
		{
			name: "Verified in test, promoting in uat",
			objs: []client.Object{bundle("Promoting"),
				uiStep("default", "s-test", "app-b2", "test", "Verified"),
				uiStep("default", "s-uat", "app-b2", "uat", "Promoting"), soak},
			want: 0,
		},
		{
			name: "Verified in test and uat",
			objs: []client.Object{bundle("Promoting"),
				uiStep("default", "s-test", "app-b2", "test", "Verified"),
				uiStep("default", "s-uat", "app-b2", "uat", "Verified"), soak},
			want: 1,
		},
		{
			name: "prod step already created",
			objs: []client.Object{bundle("Promoting"),
				uiStep("default", "s-test", "app-b2", "test", "Verified"),
				uiStep("default", "s-uat", "app-b2", "uat", "Verified"),
				uiStep("default", "s-prod", "app-b2", "prod", "Promoting"), soak},
			want: 0,
		},
		{
			name: "gate on the first env blocks at once",
			objs: []client.Object{bundle("Promoting"),
				uiGateInstance("default", "app-b2-test-freeze", "app-b2", "freeze", "test", false), soak},
			want: 1,
		},
		{
			name: "failed bundle's gates do not block",
			objs: []client.Object{bundle("Failed"),
				uiStep("default", "s-test", "app-b2", "test", "Verified"),
				uiStep("default", "s-uat", "app-b2", "uat", "Failed"), soak},
			want: 0,
		},
	}
}

// gateHoldClient builds a fake client with the Pipeline and a copy of objs.
func gateHoldClient(pipeline *v1alpha1.Pipeline, objs []client.Object) client.Client {
	all := []client.Object{pipeline.DeepCopy()}
	for _, o := range objs {
		all = append(all, o.DeepCopyObject().(client.Object))
	}
	return fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(all...).Build()
}

// E2E-R18: blockerCount counts only the active bundle's not-ready gates in
// environments it has reached (every upstream Verified) and not started, the
// gates kardinal status lists as blocking. A soak gate on prod does not count
// while the bundle is still in test, so the sidebar says Promoting, not
// Blocked (j6-supersede.log, ui.log:97).
func TestUIAPI_Pipelines_BlockerCountOnlyReachedEnvs(t *testing.T) {
	pipeline, tests := gateHoldCases()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := gateHoldClient(pipeline, tt.objs)
			rec := uiReadGet(t, c, "/api/v1/ui/pipelines")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var resp []uiPipelineResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Len(t, resp, 1)
			assert.Equal(t, "app-b2", resp[0].ActiveBundleName)
			assert.Equal(t, tt.want, resp[0].BlockerCount)
		})
	}
}

// E2E-R19: the pipeline page counted every not-ready gate node as blocking,
// so a bundle still in test, or one that Failed, showed "1 PolicyGate
// blocking promotion" next to a Promoting or Degraded sidebar
// (e2e/logs4/r18a.log:69, r17a.log:99). The graph and gates endpoints now
// mark the gates that hold the bundle with the rule blockerCount uses, and a
// gate that was evaluated not ready but holds nothing is Waiting, not Block.
func TestUIAPI_GateHolding_MatchesBlockerCount(t *testing.T) {
	evaluated := metav1.NewTime(time.Now().Add(-time.Minute))
	pipeline, tests := gateHoldCases()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]client.Object, 0, len(tt.objs))
			for _, o := range tt.objs {
				o = o.DeepCopyObject().(client.Object)
				if g, ok := o.(*v1alpha1.PolicyGate); ok {
					g.Status.LastEvaluatedAt = &evaluated
				}
				objs = append(objs, o)
			}
			c := gateHoldClient(pipeline, objs)

			rec := uiReadGet(t, c, "/api/v1/ui/bundles/app-b2/graph")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var graph struct {
				Nodes []map[string]any `json:"nodes"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &graph))
			holdingNodes := 0
			for _, n := range graph.Nodes {
				if n["type"] != "PolicyGate" {
					continue
				}
				if n["holding"] == true {
					holdingNodes++
					assert.Equal(t, "Block", n["state"], n["id"])
				} else {
					assert.Equal(t, "Waiting", n["state"], n["id"])
				}
			}
			assert.Equal(t, tt.want, holdingNodes, "graph PolicyGate nodes with holding=true")

			rec = uiReadGet(t, c, "/api/v1/ui/gates")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var gates []map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &gates))
			holdingGates := 0
			for _, g := range gates {
				if g["holding"] == true {
					holdingGates++
					assert.Equal(t, "Block", g["state"], g["name"])
				} else {
					assert.Equal(t, "Waiting", g["state"], g["name"])
				}
			}
			assert.Equal(t, tt.want, holdingGates, "gates with holding=true")
		})
	}
}

// The graph node and the gate list give a gate instance the same state
// (graph.GateState). A holding gate is Block even before its first evaluation, so
// the DAG agrees with the banner; a Superseded bundle's gates are Superseded,
// which is final, while a Failed bundle's gates wait because it can retry.
func TestUIAPI_GateState(t *testing.T) {
	evaluated := metav1.NewTime(time.Now().Add(-time.Minute))
	pipeline, _ := gateHoldCases()
	bundle := func(phase string) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: "app-b2", Namespace: "default"},
			Spec:       v1alpha1.BundleSpec{Pipeline: "app"},
			Status:     v1alpha1.BundleStatus{Phase: phase},
		}
	}
	gate := func(ready, wasEvaluated bool) *v1alpha1.PolicyGate {
		g := uiGateInstance("default", "app-b2-prod-soak", "app-b2", "require-uat-soak", "prod", ready)
		if wasEvaluated {
			g.Status.LastEvaluatedAt = &evaluated
		}
		return g
	}
	verifiedUpstream := []client.Object{
		uiStep("default", "s-test", "app-b2", "test", "Verified"),
		uiStep("default", "s-uat", "app-b2", "uat", "Verified"),
	}
	inTest := []client.Object{uiStep("default", "s-test", "app-b2", "test", "HealthChecking")}
	tests := []struct {
		name        string
		bundlePhase string
		steps       []client.Object
		gate        *v1alpha1.PolicyGate
		wantState   string
		wantHolding bool
	}{
		{"ready", "Promoting", verifiedUpstream, gate(true, true), "Pass", false},
		{"holding, evaluated", "Promoting", verifiedUpstream, gate(false, true), "Block", true},
		{"holding, not evaluated yet", "Promoting", verifiedUpstream, gate(false, false), "Block", true},
		{"not reached, evaluated", "Promoting", inTest, gate(false, true), "Waiting", false},
		{"not reached, not evaluated yet", "Promoting", inTest, gate(false, false), "Pending", false},
		{"Failed bundle, evaluated", "Failed", verifiedUpstream, gate(false, true), "Waiting", false},
		{"Superseded bundle, evaluated", "Superseded", verifiedUpstream, gate(false, true), "Superseded", false},
		{"Superseded bundle, not evaluated yet", "Superseded", inTest, gate(false, false), "Superseded", false},
		{"Superseded bundle, ready", "Superseded", verifiedUpstream, gate(true, true), "Pass", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := append([]client.Object{bundle(tt.bundlePhase), tt.gate}, tt.steps...)
			c := gateHoldClient(pipeline, objs)

			rec := uiReadGet(t, c, "/api/v1/ui/bundles/app-b2/graph")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var graph struct {
				Nodes []map[string]any `json:"nodes"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &graph))
			var node map[string]any
			for _, n := range graph.Nodes {
				if n["type"] == "PolicyGate" {
					node = n
				}
			}
			require.NotNil(t, node, "graph has the gate node")
			assert.Equal(t, tt.wantState, node["state"], "graph node state")
			assert.Equal(t, tt.wantHolding, node["holding"] == true, "graph node holding")

			rec = uiReadGet(t, c, "/api/v1/ui/gates")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var gates []map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &gates))
			require.Len(t, gates, 1)
			assert.Equal(t, tt.wantState, gates[0]["state"], "gate list state")
			assert.Equal(t, tt.wantHolding, gates[0]["holding"] == true, "gate list holding")
		})
	}
}

// TestUIAPI_RetiredBundle checks that the graph and steps of a Bundle whose
// Graph was retired (#1492) still show each environment's state and PR, read
// from status.retiredSteps.
func TestUIAPI_RetiredBundle(t *testing.T) {
	at := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}}},
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
			Spec: v1alpha1.BundleSpec{Pipeline: "app"},
			Status: v1alpha1.BundleStatus{Phase: "Verified",
				Conditions: []metav1.Condition{{Type: "GraphRetired", Status: metav1.ConditionTrue, Reason: "Retired", LastTransitionTime: at}},
				RetiredAt:  &at,
				RetiredSteps: []v1alpha1.RetiredStep{
					{Name: "app-v1-test", Environment: "test", State: "Verified", CreatedAt: at, VerifiedAt: &at},
					{Name: "app-v1-prod", Environment: "prod", State: "Verified", PRURL: "https://git.example/pr/2", CreatedAt: at, VerifiedAt: &at},
				}}},
	).Build()

	rec := uiReadGet(t, c, "/api/v1/ui/bundles/app-v1/graph")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var g uiGraphResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &g))
	states := map[string]string{}
	prs := map[string]string{}
	for _, n := range g.Nodes {
		states[n.Environment] = n.State
		prs[n.Environment] = n.PRURL
	}
	assert.Equal(t, map[string]string{"test": "Verified", "prod": "Verified"}, states)
	assert.Equal(t, "https://git.example/pr/2", prs["prod"])

	rec = uiReadGet(t, c, "/api/v1/ui/bundles/app-v1/steps")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "app-v1-prod")
	assert.Contains(t, rec.Body.String(), "app-v1-test")
}

func rejectedUIBundle(b *v1alpha1.Bundle) *v1alpha1.Bundle {
	b.Spec.Rejected = &v1alpha1.BundleRejection{By: "alice", Reason: "CVE"}
	return b
}

// TestUIAPI_Bundles_RejectedLiveEnvironments (QA #1489): the bundle list
// names, for a Rejected bundle, the environments where its change is live
// (its step there is HealthChecking or Verified), which the UI turns into the
// roll-back hint; other bundles have none.
//
// Covers BUNDLE-REJECT-07.
func TestUIAPI_Bundles_RejectedLiveEnvironments(t *testing.T) {
	step := func(name, bundle, env, state string) *v1alpha1.PromotionStep {
		s := uiStep("default", name, bundle, env, state)
		s.Labels["kardinal.io/pipeline"] = "app"
		return s
	}
	bad := rejectedUIBundle(&v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-2", Namespace: "default", CreationTimestamp: metav1.NewTime(time.Now())},
		Spec:       v1alpha1.BundleSpec{Pipeline: "app"},
		Status:     v1alpha1.BundleStatus{Phase: "Rejected"},
	})
	good := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "default", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
		Spec:       v1alpha1.BundleSpec{Pipeline: "app"},
		Status:     v1alpha1.BundleStatus{Phase: "Verified"},
	}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(good, bad,
		step("app-1-prod", "app-1", "prod", "Verified"),
		step("app-2-test", "app-2", "test", "Verified"),
		step("app-2-uat", "app-2", "uat", "Failed"),
		step("app-2-prod", "app-2", "prod", "HealthChecking"),
	).Build()
	rec := uiReadGet(t, c, "/api/v1/ui/pipelines/app/bundles")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp []uiBundleResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 2)
	assert.Equal(t, "app-2", resp[0].Name)
	assert.Equal(t, []string{"prod", "test"}, resp[0].RejectedLiveEnvironments)
	assert.Empty(t, resp[1].RejectedLiveEnvironments)
}

// TestUIAPI_Bundles_RejectedLiveRetired (QA #1489, #1492): a Bundle rejected
// after its Graph was retired has no PromotionSteps any more; its
// status.retiredSteps still say where its change is live.
func TestUIAPI_Bundles_RejectedLiveRetired(t *testing.T) {
	at := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	bad := rejectedUIBundle(&v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-2", Namespace: "default", CreationTimestamp: metav1.NewTime(time.Now())},
		Spec:       v1alpha1.BundleSpec{Pipeline: "app"},
		Status: v1alpha1.BundleStatus{Phase: "Rejected", RetiredAt: &at, RetiredSteps: []v1alpha1.RetiredStep{
			{Name: "app-2-test", Environment: "test", State: "Verified", CreatedAt: at, VerifiedAt: &at},
			{Name: "app-2-prod", Environment: "prod", State: "Verified", CreatedAt: at, VerifiedAt: &at},
		}},
	})
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(bad).Build()
	rec := uiReadGet(t, c, "/api/v1/ui/pipelines/app/bundles")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp []uiBundleResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	assert.Equal(t, []string{"prod", "test"}, resp[0].RejectedLiveEnvironments)
}
