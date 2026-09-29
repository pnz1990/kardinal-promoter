// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// uiGet serves one GET request against a UI API server backed by objs.
func uiGet(t *testing.T, path string, objs ...client.Object) *httptest.ResponseRecorder {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(objs...).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return w
}

func uiBundle(name, ns, pipeline string, created time.Time) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created)},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: pipeline},
		Status:     v1alpha1.BundleStatus{Phase: "Superseded"},
	}
}

// TestUIAPI_BundlesForPipeline_OrderAndNamespace checks that the bundle list is
// newest-first with a name tiebreak (the cache returns map order) and that
// ?namespace= keeps a same-named pipeline in another namespace out.
func TestUIAPI_BundlesForPipeline_OrderAndNamespace(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	objs := []client.Object{
		// Names deliberately do not sort in creation order.
		uiBundle("app-c", "team-a", "app", t0),
		uiBundle("app-a", "team-a", "app", t0.Add(2*time.Hour)),
		uiBundle("app-b", "team-a", "app", t0.Add(3*time.Hour)),
		uiBundle("app-d", "team-a", "app", t0.Add(time.Hour)),
		uiBundle("app-e", "team-a", "app", t0.Add(time.Hour)),
		uiBundle("app-other-ns", "team-b", "app", t0.Add(4*time.Hour)),
		uiBundle("web-1", "team-a", "web", t0.Add(5*time.Hour)),
	}
	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "all namespaces, newest first, name descending on ties",
			path: "/api/v1/ui/pipelines/app/bundles",
			want: []string{"app-other-ns", "app-b", "app-a", "app-e", "app-d", "app-c"},
		},
		{
			name: "namespace filter",
			path: "/api/v1/ui/pipelines/app/bundles?namespace=team-a",
			want: []string{"app-b", "app-a", "app-e", "app-d", "app-c"},
		},
		{
			name: "namespace with no bundles",
			path: "/api/v1/ui/pipelines/app/bundles?namespace=team-c",
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Repeat: the order must not depend on list order.
			for i := 0; i < 5; i++ {
				w := uiGet(t, tt.path, objs...)
				var resp []uiBundleResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				got := make([]string, 0, len(resp))
				for _, b := range resp {
					got = append(got, b.Name)
				}
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// TestUIAPI_BundlesForPipeline_HealthCheckedAt checks that the per-environment
// health-check time reaches the UI (used for time-to-production).
func TestUIAPI_BundlesForPipeline_HealthCheckedAt(t *testing.T) {
	checked := metav1.NewTime(time.Date(2026, 9, 2, 10, 30, 0, 0, time.UTC))
	b := uiBundle("app-1", "default", "app", time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC))
	b.Status.Environments = []v1alpha1.EnvironmentStatus{
		{Name: "test", Phase: "Verified", HealthCheckedAt: &checked},
		{Name: "prod", Phase: "Promoting"},
	}
	w := uiGet(t, "/api/v1/ui/pipelines/app/bundles", b)
	var resp []uiBundleResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	require.Len(t, resp[0].Environments, 2)
	assert.Equal(t, "2026-09-02T10:30:00Z", resp[0].Environments[0].HealthCheckedAt)
	assert.Empty(t, resp[0].Environments[1].HealthCheckedAt)
}

// TestUIAPI_Gates_LabelFields checks that gate instances carry their pipeline,
// bundle and environment, and that templates are marked so the UI can leave
// them out of the blocked count.
func TestUIAPI_Gates_LabelFields(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   uiGateResponse
	}{
		{
			name:   "template",
			labels: nil,
			want:   uiGateResponse{Name: "g", Namespace: "default", Expression: "true", Template: true},
		},
		{
			name: "instance",
			labels: map[string]string{
				"kardinal.io/pipeline":      "app",
				"kardinal.io/bundle":        "app-1",
				"kardinal.io/environment":   "prod",
				"kardinal.io/gate-template": "no-weekend",
			},
			want: uiGateResponse{
				Name: "g", Namespace: "default", Expression: "true",
				Pipeline: "app", Bundle: "app-1", Environment: "prod",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &v1alpha1.PolicyGate{
				ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "default", Labels: tt.labels},
				Spec:       v1alpha1.PolicyGateSpec{Expression: "true"},
			}
			w := uiGet(t, "/api/v1/ui/gates", gate)
			var resp []uiGateResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.Len(t, resp, 1)
			assert.Equal(t, tt.want, resp[0])
		})
	}
}

// TestUIAPI_BundleSteps_StepList checks that status.steps[] reaches the UI so
// NodeDetail can show the real step sequence instead of a hard-coded one.
func TestUIAPI_BundleSteps_StepList(t *testing.T) {
	started := metav1.NewTime(time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC))
	done := metav1.NewTime(time.Date(2026, 9, 2, 10, 0, 2, 0, time.UTC))
	tests := []struct {
		name  string
		steps []v1alpha1.StepStatus
		want  []uiStepStatus
	}{
		{name: "no steps yet", steps: nil, want: nil},
		{
			name: "argocd sequence in progress",
			steps: []v1alpha1.StepStatus{
				{Name: "argocd-set-image", State: "Completed", StartedAt: &started, CompletedAt: &done, DurationMs: 2000},
				{Name: "health-check", State: "InProgress", StartedAt: &done},
			},
			want: []uiStepStatus{
				{Name: "argocd-set-image", State: "Completed", StartedAt: "2026-09-02T10:00:00Z", CompletedAt: "2026-09-02T10:00:02Z", DurationMs: 2000},
				{Name: "health-check", State: "InProgress", StartedAt: "2026-09-02T10:00:02Z"},
			},
		},
		{
			name:  "failed step keeps its message",
			steps: []v1alpha1.StepStatus{{Name: "health-check", State: "Failed", Message: "timed out after 10m"}},
			want:  []uiStepStatus{{Name: "health-check", State: "Failed", Message: "timed out after 10m"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := &v1alpha1.PromotionStep{
				ObjectMeta: metav1.ObjectMeta{Name: "app-1-prod", Namespace: "default"},
				Spec:       v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-1", Environment: "prod"},
				Status:     v1alpha1.PromotionStepStatus{State: "HealthChecking", Steps: tt.steps},
			}
			w := uiGet(t, "/api/v1/ui/bundles/app-1/steps", ps)
			var resp []uiStepResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.Len(t, resp, 1)
			assert.Equal(t, tt.want, resp[0].Steps)
		})
	}
}

// TestUIAPI_Pipelines_OpsColumns checks the ops-table columns: AbortedByAlarm
// counts as a failed step, and a bundle created today reports 0 days (not an
// omitted field).
func TestUIAPI_Pipelines_OpsColumns(t *testing.T) {
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec:       v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}},
	}
	b := uiBundle("app-1", "default", "app", time.Now().Add(-time.Hour))
	b.Status.Phase = "Promoting"
	tests := []struct {
		name       string
		states     []string
		wantFailed int
	}{
		{name: "failed", states: []string{"Failed", "Verified"}, wantFailed: 1},
		{name: "aborted by alarm", states: []string{"AbortedByAlarm", "Verified"}, wantFailed: 1},
		{name: "both", states: []string{"AbortedByAlarm", "Failed"}, wantFailed: 2},
		{name: "none", states: []string{"Verified", "Promoting"}, wantFailed: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []client.Object{pipeline.DeepCopy(), b.DeepCopy()}
			for i, st := range tt.states {
				objs = append(objs, &v1alpha1.PromotionStep{
					ObjectMeta: metav1.ObjectMeta{Name: "app-1-" + []string{"test", "prod"}[i], Namespace: "default"},
					Spec:       v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-1"},
					Status:     v1alpha1.PromotionStepStatus{State: st},
				})
			}
			w := uiGet(t, "/api/v1/ui/pipelines", objs...)
			var raw []map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
			require.Len(t, raw, 1)
			age, ok := raw[0]["inventoryAgeDays"]
			require.True(t, ok, "inventoryAgeDays must be present for a bundle created today")
			assert.EqualValues(t, 0, age)
			if tt.wantFailed == 0 {
				assert.NotContains(t, raw[0], "failedStepCount")
			} else {
				assert.EqualValues(t, tt.wantFailed, raw[0]["failedStepCount"])
			}
		})
	}
}
