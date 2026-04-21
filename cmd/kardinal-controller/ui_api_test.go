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
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func uiScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
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

// TestUIAPI_ListBundles_Environments verifies that per-environment statuses
// including PR URLs are included in bundle responses (#503).
func TestUIAPI_ListBundles_Environments(t *testing.T) {
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v2", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: v1alpha1.BundleStatus{
			Phase: "Promoting",
			Environments: []v1alpha1.EnvironmentStatus{
				{Name: "test", Phase: "Verified"},
				{Name: "prod", Phase: "WaitingForMerge",
					PRURL: "https://github.com/org/repo/pull/42"},
			},
		},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(b).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines/nginx-demo/bundles", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiBundleResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	require.Len(t, resp[0].Environments, 2, "both environment statuses must be returned")

	// test environment
	testEnv := resp[0].Environments[0]
	assert.Equal(t, "test", testEnv.Name)
	assert.Equal(t, "Verified", testEnv.Phase)
	assert.Empty(t, testEnv.PRURL, "no PR for test")

	// prod environment with PR link
	prodEnv := resp[0].Environments[1]
	assert.Equal(t, "prod", prodEnv.Name)
	assert.Equal(t, "WaitingForMerge", prodEnv.Phase)
	assert.Equal(t, "https://github.com/org/repo/pull/42", prodEnv.PRURL, "PR URL must be set")
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1-prod",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/bundle":      "nginx-demo-v1",
				"kardinal.io/environment": "prod",
				"kardinal.io/pipeline":    "nginx-demo",
			},
		},
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
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Pipeline: "nginx-demo",
		},
	}
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "prod"},
			},
		},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ps, bundle, pipeline).Build()
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

// TestUIAPI_ValidateCEL_ValidExpression verifies that a valid CEL expression returns valid=true.
func TestUIAPI_ValidateCEL_ValidExpression(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	body := `{"expression": "!schedule.isWeekend"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/validate-cel", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["valid"], "valid expression must return valid=true")
}

// TestUIAPI_ValidateCEL_InvalidExpression verifies that a malformed CEL expression
// returns valid=false with an error message.
func TestUIAPI_ValidateCEL_InvalidExpression(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	body := `{"expression": "this is not valid CEL @@@"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/validate-cel", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["valid"], "invalid expression must return valid=false")
	assert.NotEmpty(t, resp["error"], "error message must be provided")
}

// TestUIAPI_ValidateCEL_KroFunctionsAvailable verifies that kro CEL library functions
// (lists.*, random.*) are available in the expression validator.
func TestUIAPI_ValidateCEL_KroFunctionsAvailable(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	tests := []struct {
		expression string
		wantValid  bool
	}{
		{`lists.setAtIndex([1,2,3], 0, 99)[0] == 99`, true},
		{`!schedule.isWeekend`, true},
		{`bundle.upstreamSoakMinutes >= 30`, true},
		{`upstream.uat.soakMinutes >= 30`, true},
	}
	for _, tc := range tests {
		// Use json.Marshal to correctly encode expression strings with special chars.
		bodyMap := map[string]string{"expression": tc.expression}
		bodyBytes, err := json.Marshal(bodyMap)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/validate-cel", strings.NewReader(string(bodyBytes)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, "expression: %s", tc.expression)
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, tc.wantValid, resp["valid"], "expression: %s", tc.expression)
	}
}

// TestUIAPI_ListGates_NoDuplicates is a regression test for the "176 PolicyGates" bug
// (issue #410 — proof(UI)). When multiple PolicyGate CRs exist (e.g. one per
// environment per gate name from the Graph), the /api/v1/ui/gates endpoint must
// return exactly the number of CRs that exist — not inflate them via deduplication
// or template expansion.
//
// The current implementation lists PolicyGate CRs directly (one entry per CR),
// which is correct. This test verifies that 3 distinct gate CRs → 3 response items.
func TestUIAPI_ListGates_NoDuplicates(t *testing.T) {
	gates := []v1alpha1.PolicyGate{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "no-weekend-deploys", Namespace: "platform-policies"},
			Spec:       v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
			Status:     v1alpha1.PolicyGateStatus{Ready: true, Reason: "Weekday"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "staging-soak-30m", Namespace: "platform-policies"},
			Spec:       v1alpha1.PolicyGateSpec{Expression: "bundle.upstreamSoakMinutes >= 30"},
			Status:     v1alpha1.PolicyGateStatus{Ready: true, Reason: "soak=45m"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "no-bot-deploys", Namespace: "my-team"},
			Spec:       v1alpha1.PolicyGateSpec{Expression: `bundle.provenance.author != "dependabot[bot]"`},
			Status:     v1alpha1.PolicyGateStatus{Ready: false, Reason: "author is dependabot"},
		},
	}

	s := uiScheme()
	clientObjects := make([]client.Object, len(gates))
	for i := range gates {
		clientObjects[i] = &gates[i]
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(clientObjects...).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/gates", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiGateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Exactly 3 gates — no duplicates, no inflation (#410 regression)
	assert.Len(t, resp, 3, "API must return exactly 3 gates — no duplicates from templates")

	// Names match the CRs
	names := make([]string, len(resp))
	for i, g := range resp {
		names[i] = g.Name
	}
	assert.Contains(t, names, "no-weekend-deploys")
	assert.Contains(t, names, "staging-soak-30m")
	assert.Contains(t, names, "no-bot-deploys")

	// Namespaces preserved (org vs team)
	namespaces := make(map[string]string)
	for _, g := range resp {
		namespaces[g.Name] = g.Namespace
	}
	assert.Equal(t, "platform-policies", namespaces["no-weekend-deploys"])
	assert.Equal(t, "my-team", namespaces["no-bot-deploys"])
}

// TestUIAPI_ListPipelines_PausedBadge verifies that a paused Pipeline is reflected
// in the pipeline list response (issue #410 — proof(UI): PAUSED badge in pipeline list).
func TestUIAPI_ListPipelines_PausedBadge(t *testing.T) {
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Paused: true,
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod"},
			},
		},
		Status: v1alpha1.PipelineStatus{Phase: "Ready"},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(p).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiPipelineResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp, 1)

	// UI should surface Paused=true so the frontend can show the PAUSED badge (#410)
	assert.True(t, resp[0].Paused, "paused pipeline must have Paused=true in API response")
	assert.Equal(t, "my-app", resp[0].Name)
}

// TestUIAPI_ListPipelines_OpsFields verifies that the operations table fields
// (blockerCount, failedStepCount, inventoryAgeDays, lastMergedAt, cdLevel) are
// populated correctly from active Bundle, PolicyGate, and PromotionStep CRDs (#462).
func TestUIAPI_ListPipelines_OpsFields(t *testing.T) {
	now := metav1.Now()
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod"},
			},
			// 2 pipeline-level gates → cdLevel = "mostly-cd"
			PolicyGates: []v1alpha1.PipelinePolicyGateRef{
				{Name: "gate-1"},
				{Name: "gate-2"},
			},
		},
		Status: v1alpha1.PipelineStatus{Phase: "Ready"},
	}

	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-app-v1",
			Namespace:         "default",
			CreationTimestamp: now,
		},
		Spec:   v1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"},
	}

	// Two PolicyGates blocking (ready=false) for this bundle
	gate1 := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "no-weekend", Namespace: "default",
			Labels: map[string]string{"kardinal.io/bundle": "my-app-v1"},
		},
		Spec:   v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend()"},
		Status: v1alpha1.PolicyGateStatus{Ready: false},
	}
	gate2 := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "needs-approval", Namespace: "default",
			Labels: map[string]string{"kardinal.io/bundle": "my-app-v1"},
		},
		Spec:   v1alpha1.PolicyGateSpec{Expression: "bundle.pr[\"prod\"].isApproved"},
		Status: v1alpha1.PolicyGateStatus{Ready: false},
	}

	// One PromotionStep in Failed state for this bundle
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1-prod", Namespace: "default"},
		Spec:       v1alpha1.PromotionStepSpec{BundleName: "my-app-v1", Environment: "prod"},
		Status:     v1alpha1.PromotionStepStatus{State: "Failed"},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(p, bundle, gate1, gate2, step).
		WithStatusSubresource(gate1, gate2, step).
		Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiPipelineResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp, 1)

	got := resp[0]
	assert.Equal(t, "my-app", got.Name)
	assert.Equal(t, 2, got.BlockerCount, "2 blocking PolicyGates")
	assert.Equal(t, 1, got.FailedStepCount, "1 Failed PromotionStep")
	assert.Equal(t, "mostly-cd", got.CDLevel, "2 pipeline-level gates → mostly-cd")
	// InventoryAgeDays should be 0 (bundle just created)
	assert.Equal(t, 0, got.InventoryAgeDays, "just-created bundle → 0 days inventory age")
}

// TestUIAPI_GetSteps_BakeFields verifies that bake countdown fields are populated
// from PromotionStep.status and Pipeline spec (#501).
func TestUIAPI_GetSteps_BakeFields(t *testing.T) {
	pl := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name: "prod",
					Bake: &v1alpha1.BakeConfig{Minutes: 30},
				},
			},
		},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1-prod", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-app",
			BundleName:   "my-app-v1",
			Environment:  "prod",
			StepType:     "health-check",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:              "HealthChecking",
			BakeElapsedMinutes: 15,
			BakeResets:         1,
		},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pl, ps).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/bundles/my-app-v1/steps", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiStepResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	assert.Equal(t, int64(15), resp[0].BakeElapsedMinutes, "BakeElapsedMinutes from step status")
	assert.Equal(t, 30, resp[0].BakeTargetMinutes, "BakeTargetMinutes from pipeline spec")
	assert.Equal(t, 1, resp[0].BakeResets, "BakeResets from step status")
}

// TestUIAPI_GetSteps_NoBakeFieldsWhenNoPipeline verifies that bake target is 0
// when the Pipeline spec has no bake configuration (#501).
func TestUIAPI_GetSteps_NoBakeFieldsWhenNoPipeline(t *testing.T) {
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1-test", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-app",
			BundleName:   "my-app-v1",
			Environment:  "test",
			StepType:     "health-check",
		},
		Status: v1alpha1.PromotionStepStatus{State: "HealthChecking"},
	}
	// No Pipeline object created — bake target should default to 0

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ps).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/bundles/my-app-v1/steps", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiStepResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	assert.Equal(t, 0, resp[0].BakeTargetMinutes, "BakeTargetMinutes must be 0 when no bake config")
}

// TestUIAPI_ListGates_OverrideHistory verifies that PolicyGate override records
// are included in the gate response (#502).
func TestUIAPI_ListGates_OverrideHistory(t *testing.T) {
	future := metav1.NewTime(time.Now().Add(time.Hour))
	past := metav1.NewTime(time.Now().Add(-time.Hour))
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "no-weekend", Namespace: "default"},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "!schedule.isWeekend()",
			Overrides: []v1alpha1.PolicyGateOverride{
				{
					Reason:    "P0 incident",
					Stage:     "prod",
					ExpiresAt: future,
					CreatedBy: "alice",
				},
				{
					Reason:    "old override",
					ExpiresAt: past,
					CreatedBy: "bob",
				},
			},
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
	assert.Len(t, resp[0].Overrides, 2, "both overrides must be returned")

	// Active override
	active := resp[0].Overrides[0]
	assert.Equal(t, "P0 incident", active.Reason)
	assert.Equal(t, "alice", active.CreatedBy)
	assert.Equal(t, "prod", active.Stage)
	assert.NotEmpty(t, active.ExpiresAt, "ExpiresAt must be set")

	// Expired override
	expired := resp[0].Overrides[1]
	assert.Equal(t, "old override", expired.Reason)
	assert.Equal(t, "bob", expired.CreatedBy)
}

// TestUIAPI_ListGates_NoOverrides verifies that Overrides is omitted when empty (#502).
func TestUIAPI_ListGates_NoOverrides(t *testing.T) {
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "no-weekend", Namespace: "default"},
		Spec:       v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend()"},
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
	assert.Empty(t, resp[0].Overrides, "empty overrides must return nil/empty slice")
}

// TestBuildUIConditions_ReasonAndSort verifies that buildUIConditions includes the reason field
// and sorts conditions with failing/unknown first, healthy last (#529).
func TestBuildUIConditions_ReasonAndSort(t *testing.T) {
	now := metav1.Now()
	conditions := []metav1.Condition{
		{Type: "Ready", Status: "True", Reason: "AllHealthy", Message: "step healthy", LastTransitionTime: now},
		{Type: "GitPushed", Status: "False", Reason: "GitPushFailed", Message: "auth error", LastTransitionTime: now},
		{Type: "HealthChecked", Status: "Unknown", Reason: "HealthCheckPending", Message: "waiting", LastTransitionTime: now},
	}

	result := buildUIConditions(conditions)
	require.Len(t, result, 3)

	// Order: False first, Unknown second, True last
	assert.Equal(t, "False", result[0].Status)
	assert.Equal(t, "GitPushFailed", result[0].Reason)
	assert.Equal(t, "auth error", result[0].Message)

	assert.Equal(t, "Unknown", result[1].Status)
	assert.Equal(t, "HealthCheckPending", result[1].Reason)

	assert.Equal(t, "True", result[2].Status)
	assert.Equal(t, "AllHealthy", result[2].Reason)
}

// TestBuildUIConditions_EmptyReasonOmitted verifies that empty reason is not included in JSON.
func TestBuildUIConditions_EmptyReasonOmitted(t *testing.T) {
	conditions := []metav1.Condition{
		{Type: "Ready", Status: "True", Reason: "", Message: "", LastTransitionTime: metav1.Now()},
	}
	result := buildUIConditions(conditions)
	require.Len(t, result, 1)
	assert.Empty(t, result[0].Reason)

	// Verify JSON omits empty reason
	b, err := json.Marshal(result[0])
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"reason"`)
}

// TestUIAPI_StepEvents_ReturnsSortedEvents verifies that GET
// /api/v1/ui/steps/{namespace}/{name}/events returns events sorted newest-first
// and capped at 20 (#527).
func TestUIAPI_StepEvents_ReturnsSortedEvents(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	stepName := "test-step-abc"

	// Create 3 events: one Warning (oldest), two Normal (newer).
	events := []corev1.Event{
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "ev-1", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Name: stepName, Namespace: "default"},
			Type:           "Warning",
			Reason:         "StepFailed",
			Message:        "git push failed: 403 forbidden",
			Count:          3,
			FirstTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
			LastTimestamp:  metav1.NewTime(now.Add(-5 * time.Minute)),
		},
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "ev-2", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Name: stepName, Namespace: "default"},
			Type:           "Normal",
			Reason:         "StepStarted",
			Message:        "git-clone completed",
			Count:          1,
			FirstTimestamp: metav1.NewTime(now.Add(-12 * time.Minute)),
			LastTimestamp:  metav1.NewTime(now.Add(-11 * time.Minute)),
		},
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "ev-3", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Name: stepName, Namespace: "default"},
			Type:           "Normal",
			Reason:         "StepProgressing",
			Message:        "kustomize-set-image completed",
			Count:          1,
			FirstTimestamp: metav1.NewTime(now.Add(-7 * time.Minute)),
			LastTimestamp:  metav1.NewTime(now.Add(-6 * time.Minute)),
		},
		// Different step — must NOT appear in results.
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "ev-other", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Name: "other-step", Namespace: "default"},
			Type:           "Normal",
			Reason:         "OtherStep",
			Message:        "should be filtered out",
			Count:          1,
			FirstTimestamp: metav1.NewTime(now.Add(-1 * time.Minute)),
			LastTimestamp:  metav1.NewTime(now.Add(-30 * time.Second)),
		},
	}

	objs := make([]client.Object, len(events))
	for i := range events {
		objs[i] = &events[i]
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/steps/default/"+stepName+"/events", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiEventResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Should return 3 events (not the "other" step event).
	assert.Len(t, resp, 3)

	// First event must be the most recent (ev-1 lastTimestamp = now-5min).
	assert.Equal(t, "Warning", resp[0].Type)
	assert.Equal(t, "StepFailed", resp[0].Reason)
	assert.Equal(t, int32(3), resp[0].Count)

	// All types must be present (order: newest-first by lastTimestamp).
	reasons := make([]string, len(resp))
	for i, r := range resp {
		reasons[i] = r.Reason
	}
	assert.Contains(t, reasons, "StepFailed")
	assert.Contains(t, reasons, "StepStarted")
	assert.Contains(t, reasons, "StepProgressing")
	assert.NotContains(t, reasons, "OtherStep")
}

// TestUIAPI_StepEvents_EmptyWhenNoEvents returns an empty array (not 404) when no events exist (#527).
func TestUIAPI_StepEvents_EmptyWhenNoEvents(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/steps/default/no-such-step/events", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiEventResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp, "no events for unknown step must return empty array")
}

// TestUIAPI_StepEvents_InvalidPath returns 404 for malformed paths (#527).
func TestUIAPI_StepEvents_InvalidPath(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	for _, path := range []string{
		"/api/v1/ui/steps/",
		"/api/v1/ui/steps/default/",
		"/api/v1/ui/steps/default/step-abc/",      // missing "events" suffix
		"/api/v1/ui/steps/default/step-abc/other", // wrong suffix
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.NotEqual(t, http.StatusOK, w.Code, "path %s should not return 200", path)
	}
}

// TestUIAPI_ListBundles_ImagesIncluded verifies that container images are included
// in the bundle response for the NodeDetail diff preview (#563).
func TestUIAPI_ListBundles_ImagesIncluded(t *testing.T) {
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v2", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "my-app",
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/pnz1990/kardinal-test-app", Tag: "sha-9349a3f"},
			},
		},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"},
	}

	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(b).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines/my-app/bundles", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []uiBundleResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	require.Len(t, resp[0].Images, 1, "images must be included in bundle response")
	assert.Equal(t, "ghcr.io/pnz1990/kardinal-test-app", resp[0].Images[0].Repository)
	assert.Equal(t, "sha-9349a3f", resp[0].Images[0].Tag)
}

// ─── TestUIAPI_CreateBundle ───────────────────────────────────────────────────

// TestUIAPI_CreateBundle_Success verifies that POST /api/v1/ui/bundles with
// a valid image creates a Bundle CRD and returns 201 (#917).
func TestUIAPI_CreateBundle_Success(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	body := `{"pipeline":"nginx-demo","image":"ghcr.io/example/app:sha-abc1234","commitSHA":"abc1234","author":"alice"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/bundles", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "create bundle must return 201")

	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["bundle"], "response must include bundle name")

	// Verify the Bundle CRD was created with the correct spec.
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	require.Len(t, bundles.Items, 1, "one bundle must be created")
	b := bundles.Items[0]
	assert.Equal(t, "nginx-demo", b.Spec.Pipeline)
	assert.Equal(t, "image", b.Spec.Type)
	require.Len(t, b.Spec.Images, 1)
	assert.Equal(t, "ghcr.io/example/app", b.Spec.Images[0].Repository)
	assert.Equal(t, "sha-abc1234", b.Spec.Images[0].Tag)
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, "abc1234", b.Spec.Provenance.CommitSHA)
	assert.Equal(t, "alice", b.Spec.Provenance.Author)
}

// TestUIAPI_CreateBundle_RejectsMissingImage verifies that a request with no image
// returns 400 and does NOT create a Bundle (#917).
func TestUIAPI_CreateBundle_RejectsMissingImage(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	body := `{"pipeline":"nginx-demo","image":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/bundles", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "missing image must return 400")

	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	assert.Empty(t, bundles.Items, "no bundle must be created on validation error")
}

// TestUIAPI_CreateBundle_RejectsMissingPipeline verifies that a request with no
// pipeline returns 400 (#917).
func TestUIAPI_CreateBundle_RejectsMissingPipeline(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	body := `{"image":"ghcr.io/example/app:sha-abc1234"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/bundles", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "missing pipeline must return 400")
}

// TestUIAPI_CreateBundle_RejectsWrongMethod verifies that GET /api/v1/ui/bundles
// returns 405 Method Not Allowed (#917).
func TestUIAPI_CreateBundle_RejectsWrongMethod(t *testing.T) {
	s := uiScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	srv := newUIAPIServer(c, zerolog.Nop())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/bundles", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// ─── TestParseUIImageRef ──────────────────────────────────────────────────────

// TestParseUIImageRef verifies the image reference parsing helper (#917).
func TestParseUIImageRef(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantRepo   string
		wantTag    string
		wantDigest string
	}{
		{
			name:     "repo with tag",
			input:    "ghcr.io/example/app:sha-abc1234",
			wantRepo: "ghcr.io/example/app",
			wantTag:  "sha-abc1234",
		},
		{
			name:       "repo with digest",
			input:      "ghcr.io/example/app@sha256:deadbeef",
			wantRepo:   "ghcr.io/example/app",
			wantDigest: "sha256:deadbeef",
		},
		{
			name:     "bare repo no tag",
			input:    "ghcr.io/example/app",
			wantRepo: "ghcr.io/example/app",
		},
		{
			name:     "repo with port and tag",
			input:    "registry.example.com:5000/myapp:v1.2.3",
			wantRepo: "registry.example.com:5000/myapp",
			wantTag:  "v1.2.3",
		},
		{
			name:     "registry:port with path — no explicit tag",
			input:    "registry.example.com:5000/org/myapp",
			wantRepo: "registry.example.com:5000/org/myapp",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := parseUIImageRef(tc.input)
			assert.Equal(t, tc.wantRepo, ref.Repository, "Repository")
			assert.Equal(t, tc.wantTag, ref.Tag, "Tag")
			assert.Equal(t, tc.wantDigest, ref.Digest, "Digest")
		})
	}
}
