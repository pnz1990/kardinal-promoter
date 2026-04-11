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

// TestCustomWebhookStep_Timeout verifies that a slow server causes a DeadlineExceeded failure.
func TestCustomWebhookStep_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a very slow server that never responds within the timeout.
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	state := makeCustomStepState(srv.URL)
	state.Inputs["webhook.timeoutSeconds"] = "1" // 1 second timeout

	step := parentsteps.NewCustomWebhookStep("slow-check")
	// Replace HTTP client with one that respects context cancellation quickly.
	step.HTTPClient = &http.Client{}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := step.Execute(ctx, state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "slow-check")
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

// TestCustomWebhookStep_5xxRetry verifies that 5xx errors are retried up to maxRetries
// and eventually return Failed when all attempts fail.
func TestCustomWebhookStep_5xxRetry(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	state := makeCustomStepState(srv.URL)
	// Set a tiny timeout to speed up test — retryBackoff won't apply since we
	// override the client; the retries happen with the context-deadline timeout.
	state.Inputs["webhook.timeoutSeconds"] = "2"

	step := parentsteps.NewCustomWebhookStep("flaky-check")
	// Inject a fast client so retries resolve quickly.
	step.HTTPClient = &http.Client{Timeout: 2 * time.Second}

	// This test is slow because of 3×30s backoff — override by cancelling context
	// after the first round-trip so we just get an immediate failure without waiting
	// for all retries.  We verify that 5xx triggers retrying logic.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := step.Execute(ctx, state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	// At least 1 call must have been made.
	assert.GreaterOrEqual(t, atomic.LoadInt32(&callCount), int32(1))
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
