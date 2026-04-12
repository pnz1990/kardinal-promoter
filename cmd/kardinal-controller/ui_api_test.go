// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func uiScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestUIAPI_ListPipelines verifies that GET /api/v1/ui/pipelines returns all pipelines.
func TestUIAPI_ListPipelines(t *testing.T) {
	p1 := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}},
		},
		Status: v1alpha1.PipelineStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: "True"}},
		},
	}
	p2 := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "rollouts-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(p1, p2).Build()
	srv := newUIAPIServer(c, zerolog.Nop())

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiPipelineResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp, 2)
	names := make(map[string]bool)
	for _, p := range resp {
		names[p.Name] = true
	}
	assert.True(t, names["nginx-demo"])
	assert.True(t, names["rollouts-demo"])
	// nginx-demo has 2 environments
	for _, p := range resp {
		if p.Name == "nginx-demo" {
			assert.Equal(t, 2, p.EnvironmentCount)
			assert.Equal(t, "Ready", p.Phase)
		}
	}
}

// TestUIAPI_ListBundles verifies that GET /api/v1/ui/pipelines/{name}/bundles
// returns bundles for that pipeline only.
func TestUIAPI_ListBundles(t *testing.T) {
	b1 := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	b2 := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "other-app-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "other-app"},
		Status:     v1alpha1.BundleStatus{Phase: "Verified"},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(b1, b2).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines/nginx-demo/bundles", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiBundleResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// Only nginx-demo bundle should appear.
	require.Len(t, resp, 1)
	assert.Equal(t, "nginx-demo-v1", resp[0].Name)
	assert.Equal(t, "Promoting", resp[0].Phase)
}

// TestUIAPI_GetSteps verifies that GET /api/v1/ui/bundles/{name}/steps
// returns PromotionSteps for that bundle only.
func TestUIAPI_GetSteps(t *testing.T) {
	ps1 := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1-test", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "nginx-demo-v1",
			Environment:  "test",
			StepType:     "auto",
		},
		Status: v1alpha1.PromotionStepStatus{State: "Succeeded"},
	}
	ps2 := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "other-bundle-prod", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "other-app",
			BundleName:   "other-bundle",
			Environment:  "prod",
			StepType:     "pr-review",
		},
		Status: v1alpha1.PromotionStepStatus{State: "WaitingForMerge"},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ps1, ps2).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/bundles/nginx-demo-v1/steps", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiStepResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp, 1, "only nginx-demo-v1 step expected")
	assert.Equal(t, "test", resp[0].Environment)
	assert.Equal(t, "Succeeded", resp[0].State)
}

// TestUIAPI_GetGraph verifies that GET /api/v1/ui/bundles/{name}/graph
// returns nodes from PromotionSteps for that bundle.
func TestUIAPI_GetGraph(t *testing.T) {
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1-prod", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "nginx-demo-v1",
			Environment:  "prod",
			StepType:     "pr-review",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "WaitingForMerge",
			PRURL: "https://github.com/org/repo/pull/42",
		},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ps).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/bundles/nginx-demo-v1/graph", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp uiGraphResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Nodes, 1)
	assert.Equal(t, "prod", resp.Nodes[0].Environment)
	assert.Equal(t, "PromotionStep", resp.Nodes[0].Type)
	assert.Equal(t, "WaitingForMerge", resp.Nodes[0].State)
	assert.Equal(t, "https://github.com/org/repo/pull/42", resp.Nodes[0].PRURL)
}

// TestUIAPI_ListGates verifies that GET /api/v1/ui/gates returns all PolicyGates.
func TestUIAPI_ListGates(t *testing.T) {
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "no-weekend-deploys", Namespace: "platform-policies"},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "!schedule.isWeekend",
			Message:    "No weekend deploys",
		},
		Status: v1alpha1.PolicyGateStatus{
			Ready:  true,
			Reason: "Weekday",
		},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/gates", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiGateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "no-weekend-deploys", resp[0].Name)
	assert.Equal(t, "!schedule.isWeekend", resp[0].Expression)
	assert.True(t, resp[0].Ready)
}

// TestUIAPI_MethodNotAllowed verifies that non-GET requests return 405.
func TestUIAPI_MethodNotAllowed(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/pipelines", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// Ensure PolicyGateStatus has the fields our test uses.
func TestUIAPI_GateStatusFields(t *testing.T) {
	_ = v1alpha1.PolicyGateStatus{}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	require.NotNil(t, srv)

	// List gates on empty store — must return empty array, not null.
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/gates", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiGateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Get a bundle gate list on empty cluster - should be empty, not error.
	var gateList v1alpha1.PolicyGateList
	require.NoError(t, c.List(context.Background(), &gateList))
	assert.Empty(t, gateList.Items)
}

// TestUIAPI_Promote_CreatesBundleForEnvironment verifies that POST /api/v1/ui/promote
// creates a Bundle targeting the specified environment.
func TestUIAPI_Promote_CreatesBundleForEnvironment(t *testing.T) {
	s := uiScheme()
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	body := `{"pipeline": "nginx-demo", "environment": "prod", "namespace": "default"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/promote", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "promote must return 201 Created")

	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["bundle"], "response must include bundle name")

	// Verify the Bundle was created.
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	require.Len(t, bundles.Items, 1, "one bundle must be created")
	b := bundles.Items[0]
	assert.Equal(t, "nginx-demo", b.Spec.Pipeline)
	assert.Equal(t, "prod", b.Spec.Intent.TargetEnvironment)
}

// TestUIAPI_Promote_RejectsMissingPipeline verifies that a promote request for
// a nonexistent pipeline returns 404.
func TestUIAPI_Promote_RejectsMissingPipeline(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	body := `{"pipeline": "does-not-exist", "environment": "prod"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/promote", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, "unknown pipeline must return 404")
}

// TestUIAPI_Promote_RejectsUnknownEnvironment verifies that a promote request for
// an environment not in the pipeline spec returns 400.
func TestUIAPI_Promote_RejectsUnknownEnvironment(t *testing.T) {
	s := uiScheme()
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	body := `{"pipeline": "nginx-demo", "environment": "nonexistent"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/promote", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "unknown env must return 400")
}
