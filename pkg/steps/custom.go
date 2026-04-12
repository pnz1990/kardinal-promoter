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

package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

const (
	// inputKeyWebhookURL is the Inputs key for the webhook URL.
	inputKeyWebhookURL = "webhook.url"

	// inputKeyWebhookTimeout is the Inputs key for the timeout in seconds.
	inputKeyWebhookTimeout = "webhook.timeoutSeconds"

	// inputKeyWebhookSecretName is the Inputs key for the auth secret name.
	inputKeyWebhookSecretName = "webhook.secretRef.name"

	// defaultWebhookTimeout is the default timeout when none is specified.
	defaultWebhookTimeout = 300 * time.Second

	// maxRetries is the maximum number of retry attempts for 5xx errors.
	maxRetries = 3

	// RetryBackoff is the wait duration between retries.
	// Exported so tests can assert that StepResult.RequeueAfter is set correctly.
	// Instead of blocking with time.After, the step returns StepPending with
	// RequeueAfter=RetryBackoff, letting controller-runtime handle the delay.
	RetryBackoff = 30 * time.Second
)

// outputKeyRetryAttempt is the state.Outputs key used to persist retry attempt count
// across reconcile iterations. Prefixed with "_custom." to avoid collision with
// step-provided outputs.
const outputKeyRetryAttempt = "_custom.retryAttempt"

// CustomWebhookStep dispatches a custom promotion step via HTTP POST.
// The step name is any non-built-in uses: value from the Pipeline spec.
// Configuration (URL, timeout, auth) is passed via PromotionStep.Spec.Inputs.
//
// Non-blocking retries: on 5xx responses the step returns StepPending with
// RequeueAfter=retryBackoff. The attempt count is stored in state.Outputs
// (persisted to PromotionStep.status.outputs) so retries survive controller restarts.
// This eliminates the time.After blocking sleep that was previously in Execute().
type CustomWebhookStep struct {
	// stepName is the step name (the uses: value from the Pipeline spec).
	stepName string

	// HTTPClient is overridable for testing.
	HTTPClient *http.Client
}

// NewCustomWebhookStep creates a CustomWebhookStep for the given step name.
func NewCustomWebhookStep(name string) *CustomWebhookStep {
	return &CustomWebhookStep{stepName: name}
}

func (s *CustomWebhookStep) Name() string { return s.stepName }

// webhookRequest is the JSON body sent to the custom step endpoint.
type webhookRequest struct {
	Bundle       v1alpha1.BundleSpec `json:"bundle"`
	Environment  string              `json:"environment"`
	Inputs       map[string]string   `json:"inputs"`
	OutputsSoFar map[string]string   `json:"outputs_so_far"`
}

// webhookResponse is the JSON body returned by the custom step endpoint.
type webhookResponse struct {
	// Result is "pass" or "fail".
	Result string `json:"result"`

	// Outputs holds key/value pairs to pass to subsequent steps.
	Outputs map[string]string `json:"outputs,omitempty"`

	// Message is a human-readable explanation.
	Message string `json:"message,omitempty"`
}

// Execute sends a POST request to the webhook URL and interprets the response.
// Idempotent: safe to call multiple times (webhook server must also be idempotent).
//
// Retry behaviour (Graph-first, non-blocking):
//   - On 5xx or network error: increment attempt counter in state.Outputs (persisted to
//     PromotionStep.status.outputs by the reconciler), return StepPending with
//     RequeueAfter=retryBackoff. Controller-runtime requeueing replaces blocking
//     time.After — no goroutine is ever blocked.
//   - After maxRetries failures: return StepFailed.
func (s *CustomWebhookStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
	log := zerolog.Ctx(ctx).With().Str("custom_step", s.stepName).Logger()

	webhookURL := state.Inputs[inputKeyWebhookURL]
	if webhookURL == "" {
		msg := fmt.Sprintf("custom step %q: missing input %q", s.stepName, inputKeyWebhookURL)
		return StepResult{Status: StepFailed, Message: msg}, nil
	}

	timeout := defaultWebhookTimeout
	if ts := state.Inputs[inputKeyWebhookTimeout]; ts != "" {
		if secs, err := strconv.Atoi(ts); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}

	// Build the Authorization header from a Kubernetes Secret if configured.
	// The PromotionStep reconciler loads the secret and places the value in
	// state.Inputs["webhook.authorization"] before calling the step.
	authHeader := ""
	if state.Inputs[inputKeyWebhookSecretName] != "" {
		authHeader = state.Inputs["webhook.authorization"]
	}

	// Read the current attempt count from persisted outputs.
	// attempt is 0-indexed: on the first call attempt==0, on first retry attempt==1.
	attempt := 0
	if raw, ok := state.Outputs[outputKeyRetryAttempt]; ok {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			attempt = n
		}
	}

	// Check if we have exhausted all retries before making another call.
	if attempt >= maxRetries {
		msg := fmt.Sprintf("custom step %q: all %d attempts failed — step is permanently failed", s.stepName, maxRetries)
		return StepResult{Status: StepFailed, Message: msg}, nil
	}

	log.Info().Int("attempt", attempt+1).Int("max", maxRetries).Str("url", webhookURL).Msg("invoking custom webhook")

	// Build request body.
	reqBody := webhookRequest{
		Bundle:       state.Bundle,
		Environment:  state.Environment.Name,
		Inputs:       state.Inputs,
		OutputsSoFar: state.Outputs,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return StepResult{Status: StepFailed, Message: fmt.Sprintf("marshal request: %v", err)}, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	resp, callErr := s.doCall(callCtx, webhookURL, bodyBytes, authHeader)
	cancel()

	if callErr != nil {
		log.Warn().Err(callErr).Int("attempt", attempt+1).Msg("webhook call failed, scheduling retry")
		return s.scheduleRetry(state, attempt, fmt.Sprintf("attempt %d/%d: %v", attempt+1, maxRetries, callErr))
	}

	if resp.statusCode >= 500 {
		log.Warn().Int("status", resp.statusCode).Int("attempt", attempt+1).Msg("webhook returned 5xx, scheduling retry")
		return s.scheduleRetry(state, attempt, fmt.Sprintf("attempt %d/%d: HTTP %d", attempt+1, maxRetries, resp.statusCode))
	}

	// Non-5xx response — clear the retry counter and process it.
	delete(state.Outputs, outputKeyRetryAttempt)
	return s.processResponse(resp)
}

// scheduleRetry increments the attempt counter in state.Outputs (which are persisted to
// PromotionStep.status.outputs by the reconciler) and returns StepPending with
// RequeueAfter=retryBackoff. This is non-blocking: controller-runtime handles the wait.
func (s *CustomWebhookStep) scheduleRetry(state *StepState, currentAttempt int, reason string) (StepResult, error) {
	nextAttempt := currentAttempt + 1
	if state.Outputs == nil {
		state.Outputs = make(map[string]string)
	}
	state.Outputs[outputKeyRetryAttempt] = strconv.Itoa(nextAttempt)

	if nextAttempt >= maxRetries {
		msg := fmt.Sprintf("custom step %q: all %d attempts failed (last: %s)", s.stepName, maxRetries, reason)
		return StepResult{Status: StepFailed, Message: msg}, nil
	}

	return StepResult{
		Status:       StepPending,
		Message:      fmt.Sprintf("custom step %q: %s; retry %d/%d after %s", s.stepName, reason, nextAttempt+1, maxRetries, RetryBackoff),
		RequeueAfter: RetryBackoff,
	}, nil
}

// callResult holds the parsed HTTP response.
type callResult struct {
	statusCode int
	response   webhookResponse
	rawBody    []byte
}

// doCall performs one HTTP POST and parses the response.
func (s *CustomWebhookStep) doCall(ctx context.Context, url string, body []byte, authHeader string) (*callResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	hc := s.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	result := &callResult{statusCode: resp.StatusCode, rawBody: rawBody}
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &result.response); err != nil {
			return nil, fmt.Errorf("decode response JSON (HTTP %d): %w", resp.StatusCode, err)
		}
	}
	return result, nil
}

// processResponse interprets the webhook response and returns a StepResult.
func (s *CustomWebhookStep) processResponse(r *callResult) (StepResult, error) {
	if r.statusCode < 200 || r.statusCode >= 300 {
		return StepResult{
			Status:  StepFailed,
			Message: fmt.Sprintf("custom step %q: HTTP %d: %s", s.stepName, r.statusCode, string(r.rawBody)),
		}, nil
	}

	switch r.response.Result {
	case "pass":
		return StepResult{
			Status:  StepSuccess,
			Message: r.response.Message,
			Outputs: r.response.Outputs,
		}, nil
	case "fail":
		return StepResult{
			Status:  StepFailed,
			Message: fmt.Sprintf("custom step %q returned fail: %s", s.stepName, r.response.Message),
		}, nil
	default:
		return StepResult{
			Status: StepFailed,
			Message: fmt.Sprintf("custom step %q: unexpected result %q (expected pass|fail)",
				s.stepName, r.response.Result),
		}, nil
	}
}
