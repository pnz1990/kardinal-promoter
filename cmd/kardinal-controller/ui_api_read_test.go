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
	pipeline := func(ns string) *v1alpha1.Pipeline {
		return &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}}}}
	}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		pipeline("team-a"), pipeline("team-b"),
		// The older bundle is listed first (names sort by creation time).
		bundle("team-a", "app-1-old", older),
		bundle("team-a", "app-2-new", newer),
		bundle("team-b", "app-2-new", newer),
		uiGateInstance("team-a", "g1", "app-2-new", "no-weekend", "test", false),
		uiGateInstance("team-b", "g2", "app-2-new", "no-weekend", "test", false),
		uiGateInstance("team-b", "g3", "app-2-new", "freeze", "test", false),
		uiStep("team-b", "s1", "app-2-new", "test", "Failed"),
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
