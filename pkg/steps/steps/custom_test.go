// Copyright 2026 The kardinal-promoter Authors.
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

package steps_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// webhookResponseBody is the JSON structure returned by the mock webhook server.
type webhookResponseBody struct {
	Result  string            `json:"result"`
	Outputs map[string]string `json:"outputs,omitempty"`
	Message string            `json:"message,omitempty"`
}

// webhookRequestBody mirrors the request the custom step sends.
type webhookRequestBody struct {
	Bundle       v1alpha1.BundleSpec `json:"bundle"`
	Environment  string              `json:"environment"`
	Inputs       map[string]string   `json:"inputs"`
	OutputsSoFar map[string]string   `json:"outputs_so_far"`
}

func makeCustomStepState(webhookURL string) *parentsteps.StepState {
	return &parentsteps.StepState{
		PipelineName: "my-pipeline",
		BundleName:   "my-bundle-v1",
		Environment:  v1alpha1.EnvironmentSpec{Name: "staging"},
		Bundle: v1alpha1.BundleSpec{
			Type:   "image",
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/myorg/app", Tag: "v2.0.0"}},
		},
		Outputs: map[string]string{"previous": "value"},
		Inputs: map[string]string{
			"webhook.url": webhookURL,
		},
	}
}

// TestCustomWebhookStep_Pass verifies that a "pass" response succeeds and propagates outputs.
func TestCustomWebhookStep_Pass(t *testing.T) {
	var capturedBody webhookRequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(webhookResponseBody{ //nolint:errcheck
			Result:  "pass",
			Outputs: map[string]string{"custom_key": "custom_value"},
			Message: "all checks passed",
		})
	}))
	defer srv.Close()

	state := makeCustomStepState(srv.URL)
	step := parentsteps.NewCustomWebhookStep("my-custom-check")

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "all checks passed", result.Message)
	assert.Equal(t, "custom_value", result.Outputs["custom_key"])

	// Verify request body contained the right fields.
	assert.Equal(t, "staging", capturedBody.Environment)
	assert.Equal(t, "value", capturedBody.OutputsSoFar["previous"])
}

// TestCustomWebhookStep_Fail verifies that a "fail" response sets StepFailed.
func TestCustomWebhookStep_Fail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(webhookResponseBody{ //nolint:errcheck
			Result:  "fail",
			Message: "integration tests did not pass",
		})
	}))
	defer srv.Close()

	state := makeCustomStepState(srv.URL)
	step := parentsteps.NewCustomWebhookStep("integration-tests")

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "integration tests did not pass")
}

// TestCustomWebhookStep_Timeout verifies that a slow server causes a non-blocking retry
// (StepPending with RequeueAfter) on the first timeout, not immediate StepFailed.
// After maxRetries timeouts the step permanently fails.
func TestCustomWebhookStep_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a very slow server that never responds within the timeout.
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	step := parentsteps.NewCustomWebhookStep("slow-check")
	step.HTTPClient = &http.Client{}

	// First attempt: timeout → StepPending (non-blocking retry scheduled).
	state := makeCustomStepState(srv.URL)
	state.Inputs["webhook.timeoutSeconds"] = "1" // 1 second timeout

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := step.Execute(ctx, state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, result.Status, "first timeout should return StepPending (non-blocking retry)")
	assert.Equal(t, parentsteps.RetryBackoff, result.RequeueAfter, "should request retry backoff requeue")
	assert.Equal(t, "1", state.Outputs["_custom.retryAttempt"], "attempt counter must be persisted in outputs")

	// Simulate the reconciler persisting outputs and requeueing for attempt 2.
	state.Inputs["webhook.timeoutSeconds"] = "1"

	result2, err2 := step.Execute(context.Background(), state)
	require.NoError(t, err2)
	assert.Equal(t, parentsteps.StepPending, result2.Status, "second timeout should still return StepPending")
	assert.Equal(t, "2", state.Outputs["_custom.retryAttempt"])

	// Attempt 3 (maxRetries=3, attempt index 2 → next would be 3 = maxRetries): permanently failed.
	result3, err3 := step.Execute(context.Background(), state)
	require.NoError(t, err3)
	assert.Equal(t, parentsteps.StepFailed, result3.Status, "after maxRetries the step must permanently fail")
	assert.Contains(t, result3.Message, "all 3 attempts failed")
}

// TestCustomWebhookStep_AuthHeader verifies that the Authorization header is sent from Inputs.
func TestCustomWebhookStep_AuthHeader(t *testing.T) {
	var capturedAuthHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(webhookResponseBody{Result: "pass"}) //nolint:errcheck
	}))
	defer srv.Close()

	state := makeCustomStepState(srv.URL)
	// Pre-loaded auth value (as the reconciler would inject it after reading the secret).
	state.Inputs["webhook.secretRef.name"] = "my-secret"
	state.Inputs["webhook.secretRef.namespace"] = "my-ns"
	state.Inputs["webhook.authorization"] = "Bearer super-secret-token"

	step := parentsteps.NewCustomWebhookStep("secure-check")

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "Bearer super-secret-token", capturedAuthHeader)
}

// TestCustomWebhookStep_5xxRetry_NonBlocking verifies that a 5xx response returns
// StepPending with RequeueAfter on the first failure — no blocking goroutine sleep.
// The retry counter is persisted in state.Outputs (which maps to PromotionStep.status.outputs).
func TestCustomWebhookStep_5xxRetry_NonBlocking(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	state := makeCustomStepState(srv.URL)
	step := parentsteps.NewCustomWebhookStep("flaky-check")
	step.HTTPClient = &http.Client{Timeout: 2 * time.Second}

	// Execute must return within milliseconds — no blocking time.After.
	start := time.Now()
	result, err := step.Execute(context.Background(), state)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, result.Status, "5xx on attempt 0 should return StepPending")
	assert.Equal(t, parentsteps.RetryBackoff, result.RequeueAfter, "should set RequeueAfter for retry backoff")
	assert.Equal(t, "1", state.Outputs["_custom.retryAttempt"], "attempt counter must be stored in outputs")
	assert.Less(t, elapsed, 5*time.Second, "Execute must return without blocking for retryBackoff duration")
	assert.EqualValues(t, 1, atomic.LoadInt32(&callCount), "exactly one HTTP call on first attempt")
}

// TestCustomWebhookStep_5xxRetryExhausted verifies that after maxRetries 5xx failures
// the step returns StepFailed.
func TestCustomWebhookStep_5xxRetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	step := parentsteps.NewCustomWebhookStep("exhausted-check")
	step.HTTPClient = &http.Client{Timeout: 2 * time.Second}

	// Simulate 3 reconcile cycles (one per attempt, each reconciler iteration persists outputs).
	// The reconciler restores state.Outputs from PromotionStep.status.outputs between cycles.
	state := makeCustomStepState(srv.URL)

	// Cycle 1: attempt 0 → StepPending, attempt=1 in outputs
	r1, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, r1.Status)
	assert.Equal(t, "1", state.Outputs["_custom.retryAttempt"])

	// Cycle 2: attempt 1 → StepPending, attempt=2 in outputs
	r2, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, r2.Status)
	assert.Equal(t, "2", state.Outputs["_custom.retryAttempt"])

	// Cycle 3: attempt 2 → last attempt, StepFailed (nextAttempt=3 == maxRetries)
	r3, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepFailed, r3.Status)
	assert.Contains(t, r3.Message, "all 3 attempts failed")
}

// TestCustomWebhookStep_5xxThenPass verifies recovery: after a 5xx the server recovers
// and returns "pass" on the next reconcile cycle.
func TestCustomWebhookStep_5xxThenPass(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&callCount, 1)
		if call == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Second call succeeds.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(webhookResponseBody{ //nolint:errcheck
			Result:  "pass",
			Message: "recovered",
		})
	}))
	defer srv.Close()

	step := parentsteps.NewCustomWebhookStep("recovering-check")
	step.HTTPClient = &http.Client{Timeout: 2 * time.Second}

	// Cycle 1: 5xx → StepPending
	state := makeCustomStepState(srv.URL)
	r1, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, r1.Status)
	assert.Equal(t, "1", state.Outputs["_custom.retryAttempt"])

	// Cycle 2: pass → StepSuccess; retry counter cleared from outputs
	r2, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, r2.Status)
	assert.Equal(t, "recovered", r2.Message)
	assert.Empty(t, state.Outputs["_custom.retryAttempt"], "retry counter must be cleared on success")
}

// TestCustomWebhookStep_Idempotent verifies that re-executing after a crash
// (same attempt counter in outputs) does not double-count attempts.
func TestCustomWebhookStep_Idempotent(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(webhookResponseBody{Result: "pass"})
	}))
	defer srv.Close()

	step := parentsteps.NewCustomWebhookStep("idempotent-check")
	step.HTTPClient = &http.Client{Timeout: 2 * time.Second}

	// Simulate: controller crashed after Execute returned StepPending on attempt 0
	// but before the reconciler could patch status (attempt counter in outputs = "1").
	// On restart, state.Outputs is restored from CRD status — still "1".
	state := makeCustomStepState(srv.URL)
	state.Outputs["_custom.retryAttempt"] = "1" // pre-loaded from restored CRD outputs

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	// Attempt 1 should now execute (not skip) and succeed.
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.EqualValues(t, 1, atomic.LoadInt32(&callCount), "one HTTP call on retry attempt 1")
}

// TestCustomWebhookStep_MissingURL verifies that a missing webhook.url input returns Failed.
func TestCustomWebhookStep_MissingURL(t *testing.T) {
	state := &parentsteps.StepState{
		Environment: v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle:      v1alpha1.BundleSpec{Type: "image"},
		Outputs:     map[string]string{},
		Inputs:      map[string]string{}, // no URL
	}

	step := parentsteps.NewCustomWebhookStep("no-url-step")

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "webhook.url")
}

// TestCustomWebhookStep_UnexpectedResult verifies that an unknown result value returns Failed.
func TestCustomWebhookStep_UnexpectedResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"result": "unknown"}) //nolint:errcheck
	}))
	defer srv.Close()

	state := makeCustomStepState(srv.URL)
	step := parentsteps.NewCustomWebhookStep("bad-result-step")

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "unexpected result")
}

// TestCustomWebhookStep_5xxRetryAttemptTrackedInOutputs verifies that the retry counter
// in state.Outputs is correctly serialised as a decimal integer string (so the reconciler
// can persist it to PromotionStep.status.outputs which stores map[string]string).
func TestCustomWebhookStep_5xxRetryAttemptTrackedInOutputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	step := parentsteps.NewCustomWebhookStep("tracker-check")
	step.HTTPClient = &http.Client{Timeout: 2 * time.Second}

	state := makeCustomStepState(srv.URL)

	for wantAttempt := 1; wantAttempt <= 2; wantAttempt++ {
		result, err := step.Execute(context.Background(), state)
		require.NoError(t, err)
		assert.Equal(t, parentsteps.StepPending, result.Status)
		got, ok := state.Outputs["_custom.retryAttempt"]
		require.True(t, ok, "retry attempt must be present in outputs")
		n, err := strconv.Atoi(got)
		require.NoError(t, err, "retry attempt must be a valid integer")
		assert.Equal(t, wantAttempt, n)
	}
}

// TestLookup_CustomStepFallback verifies that Lookup returns a CustomWebhookStep for unknown names.
func TestLookup_CustomStepFallback(t *testing.T) {
	step, err := parentsteps.Lookup("my-team/integration-tests")
	require.NoError(t, err, "Lookup should not error for custom (non-built-in) step names")
	assert.Equal(t, "my-team/integration-tests", step.Name())
}

// TestEngine_CustomStep_PassPropagatesOutputs verifies that a custom step's outputs
// are merged into state.Outputs and visible to the next step.
func TestEngine_CustomStep_PassPropagatesOutputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(webhookResponseBody{ //nolint:errcheck
			Result:  "pass",
			Outputs: map[string]string{"custom_token": "abc123"},
		})
	}))
	defer srv.Close()

	state := &parentsteps.StepState{
		PipelineName: "my-pipeline",
		BundleName:   "my-bundle",
		Environment:  v1alpha1.EnvironmentSpec{Name: "staging"},
		Bundle:       v1alpha1.BundleSpec{Type: "image"},
		Outputs:      map[string]string{},
		Inputs:       map[string]string{"webhook.url": srv.URL},
		GitClient:    &mockGitClient{},
	}

	// Use a custom step followed by health-check to verify output accumulation.
	engine := parentsteps.NewEngine([]string{"my-team/run-tests", "health-check"})
	_, result, err := engine.ExecuteFrom(context.Background(), state, 0)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "abc123", state.Outputs["custom_token"],
		"custom step outputs must be accumulated in state.Outputs")
}

// TestEngine_CustomStep_FailStopsExecution verifies that a custom step failure stops the pipeline.
func TestEngine_CustomStep_FailStopsExecution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(webhookResponseBody{ //nolint:errcheck
			Result:  "fail",
			Message: "security scan failed",
		})
	}))
	defer srv.Close()

	state := &parentsteps.StepState{
		PipelineName: "my-pipeline",
		BundleName:   "my-bundle",
		Environment:  v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle:       v1alpha1.BundleSpec{Type: "image"},
		Outputs:      map[string]string{},
		Inputs:       map[string]string{"webhook.url": srv.URL},
		GitClient:    &mockGitClient{},
	}

	engine := parentsteps.NewEngine([]string{"security-scan", "health-check"})
	_, result, err := engine.ExecuteFrom(context.Background(), state, 0)
	require.Error(t, err, "failed step should return an error from the engine")
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, err.Error(), "security-scan")
}
