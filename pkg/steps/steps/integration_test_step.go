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
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&integrationTestStep{})
}

// integrationTestStep creates a Kubernetes Job, waits for completion, and reports
// the result as a step output. This is the K-07 built-in step.
//
// Architecture note (graph-first):
//   - The reconciler that calls this step is the graph Owned node.
//   - The Job is created and owned by the PromotionStep (via the step engine).
//   - The step reads Job.status.succeeded / Job.status.failed to determine the result.
//   - No out-of-band state. No cross-CRD mutation. Pure K8s resource creation+watch.
//
// Configuration (via state.Inputs, populated from PromotionStep.spec.inputs):
//
//	integration_test.image         (required) — container image to run
//	integration_test.command       (optional) — space-separated command args
//	integration_test.timeout       (optional) — duration string, default "30m"
//	integration_test.on_failure    (optional) — "abort" | "rollback" | "none" (default)
type integrationTestStep struct{}

func (s *integrationTestStep) Name() string { return "integration-test" }

func (s *integrationTestStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.K8sClient == nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: "integration-test: K8s client not available",
		}, nil
	}

	// Parse config from Inputs
	image := state.Inputs["integration_test.image"]
	if image == "" {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: "integration-test: integration_test.image is required",
		}, nil
	}
	cmdStr := state.Inputs["integration_test.command"]
	timeoutStr := state.Inputs["integration_test.timeout"]
	if timeoutStr == "" {
		timeoutStr = "30m"
	}
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("integration-test: invalid timeout %q: %v", timeoutStr, err),
		}, nil
	}

	// Job name: deterministic from bundle + env to ensure idempotency
	namespace := state.Environment.Name
	jobName := integrationTestJobName(state.BundleName, state.Environment.Name)

	// Check if job already exists
	var existing batchv1.Job
	getErr := state.K8sClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: namespace}, &existing)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return parentsteps.StepResult{}, fmt.Errorf("integration-test: get job: %w", getErr)
	}

	if apierrors.IsNotFound(getErr) {
		// Create the Job
		ttl := int32(3600) // 1 hour TTL after completion
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      jobName,
				Namespace: namespace,
				Labels: map[string]string{
					"kardinal.io/bundle":      state.BundleName,
					"kardinal.io/environment": state.Environment.Name,
					"kardinal.io/step":        "integration-test",
				},
			},
			Spec: batchv1.JobSpec{
				TTLSecondsAfterFinished: &ttl,
				BackoffLimit:            int32Ptr(0), // fail immediately, no retries
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"kardinal.io/bundle":      state.BundleName,
							"kardinal.io/environment": state.Environment.Name,
						},
					},
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers: []corev1.Container{
							{
								Name:    "integration-test",
								Image:   image,
								Command: parseCommand(cmdStr),
							},
						},
					},
				},
			},
		}

		if createErr := state.K8sClient.Create(ctx, job); createErr != nil {
			if !apierrors.IsAlreadyExists(createErr) {
				return parentsteps.StepResult{}, fmt.Errorf("integration-test: create job: %w", createErr)
			}
			// Already exists — fall through to status check
			if getErr2 := state.K8sClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: namespace}, &existing); getErr2 != nil {
				return parentsteps.StepResult{}, fmt.Errorf("integration-test: re-get job after conflict: %w", getErr2)
			}
		} else {
			// Job created; return Pending to allow the reconciler to requeue and re-check.
			return parentsteps.StepResult{
				Status:       parentsteps.StepPending,
				Message:      fmt.Sprintf("integration-test: Job %s/%s created, waiting for completion", namespace, jobName),
				RequeueAfter: 15 * time.Second,
			}, nil
		}
	} else {
		existing = existing
	}

	// Re-fetch existing job status
	var current batchv1.Job
	if getErr2 := state.K8sClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: namespace}, &current); getErr2 != nil {
		return parentsteps.StepResult{}, fmt.Errorf("integration-test: get job status: %w", getErr2)
	}

	// Check for timeout: compare job creation time + timeout vs now
	// Skip timeout check if CreationTimestamp is zero (e.g., very fresh job, fake client).
	createdAt := current.CreationTimestamp.Time
	if !createdAt.IsZero() && time.Since(createdAt) > timeout {
		// Clean up the job
		_ = state.K8sClient.Delete(ctx, &current, client.PropagationPolicy(metav1.DeletePropagationBackground))
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("integration-test: Job %s/%s timed out after %s", namespace, jobName, timeoutStr),
		}, nil
	}

	// Check job completion
	if current.Status.Succeeded >= 1 {
		return parentsteps.StepResult{
			Status:  parentsteps.StepSuccess,
			Message: fmt.Sprintf("integration-test: Job %s/%s succeeded", namespace, jobName),
			Outputs: map[string]string{
				"integration_test.result":  "passed",
				"integration_test.job":     jobName,
				"integration_test.elapsed": time.Since(createdAt).Round(time.Second).String(),
			},
		}, nil
	}

	if current.Status.Failed >= 1 {
		// Delete the job to clean up (TTL will handle it otherwise)
		_ = state.K8sClient.Delete(ctx, &current, client.PropagationPolicy(metav1.DeletePropagationBackground))
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("integration-test: Job %s/%s failed (exitCode=%d)", namespace, jobName, jobFailedExitCode(&current)),
			Outputs: map[string]string{
				"integration_test.result": "failed",
				"integration_test.job":    jobName,
			},
		}, nil
	}

	// Job is still running
	return parentsteps.StepResult{
		Status:       parentsteps.StepPending,
		Message:      fmt.Sprintf("integration-test: Job %s/%s running (%s elapsed)", namespace, jobName, time.Since(createdAt).Round(time.Second)),
		RequeueAfter: 15 * time.Second,
	}, nil
}

// integrationTestJobName generates a deterministic Job name from bundle+env.
// Truncated to 63 chars to fit Kubernetes name limits.
func integrationTestJobName(bundleName, envName string) string {
	raw := "kt-" + slugifyJobName(bundleName) + "-" + slugifyJobName(envName)
	if len(raw) > 63 {
		raw = raw[:63]
	}
	return raw
}

// slugifyJobName converts a name to lowercase alphanumeric with hyphens.
func slugifyJobName(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	// Trim leading/trailing hyphens
	result := strings.Trim(b.String(), "-")
	// Replace multiple consecutive hyphens with single
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return result
}

// parseCommand splits a space-separated command string into args.
// Returns nil (uses the container's default entrypoint) if empty.
func parseCommand(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	return strings.Fields(cmd)
}

// int32Ptr returns a pointer to the int32 value.
func int32Ptr(i int32) *int32 { return &i }

// jobFailedExitCode extracts the exit code from a failed job's pod status.
// Returns 1 if unavailable.
func jobFailedExitCode(job *batchv1.Job) int {
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			// Try to parse exit code from reason/message (best-effort)
			if strings.Contains(cond.Message, "exit code") {
				parts := strings.Fields(cond.Message)
				for i, p := range parts {
					if p == "code" && i+1 < len(parts) {
						if n, err := strconv.Atoi(parts[i+1]); err == nil {
							return n
						}
					}
				}
			}
		}
	}
	return 1
}
