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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func newIntegrationTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	_ = batchv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

func integrationTestState(image string) *parentsteps.StepState {
	return &parentsteps.StepState{
		BundleName: "myapp-v1",
		Environment: v1alpha1.EnvironmentSpec{
			Name: "staging",
		},
		Bundle: v1alpha1.BundleSpec{Type: "image"},
		Inputs: map[string]string{
			"integration_test.image":   image,
			"integration_test.timeout": "5m",
		},
	}
}

// TestIntegrationTestStep_MissingK8sClient verifies that the step fails
// gracefully when no K8s client is provided.
func TestIntegrationTestStep_MissingK8sClient(t *testing.T) {
	step, err := parentsteps.Lookup("integration-test")
	require.NoError(t, err)

	state := integrationTestState("ghcr.io/myorg/tests:latest")
	// K8sClient is nil by default
	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "K8s client not available")
}

// TestIntegrationTestStep_MissingImage verifies that the step fails
// when integration_test.image is not configured.
func TestIntegrationTestStep_MissingImage(t *testing.T) {
	step, err := parentsteps.Lookup("integration-test")
	require.NoError(t, err)

	fc := fake.NewClientBuilder().WithScheme(newIntegrationTestScheme()).Build()
	state := integrationTestState("")
	state.K8sClient = fc

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "integration_test.image is required")
}

// TestIntegrationTestStep_CreatesPending verifies that on first call the step
// creates a Job and returns StepPending.
func TestIntegrationTestStep_CreatesPending(t *testing.T) {
	step, err := parentsteps.Lookup("integration-test")
	require.NoError(t, err)

	fc := fake.NewClientBuilder().WithScheme(newIntegrationTestScheme()).Build()
	state := integrationTestState("ghcr.io/myorg/integration-tests:latest")
	state.K8sClient = fc

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepPending, result.Status)
	assert.Contains(t, result.Message, "created")

	// Verify Job was created
	var jobList batchv1.JobList
	require.NoError(t, fc.List(context.Background(), &jobList))
	assert.Len(t, jobList.Items, 1, "exactly one Job must have been created")
	job := jobList.Items[0]
	assert.Equal(t, "staging", job.Namespace)
	assert.Contains(t, job.Labels, "kardinal.io/bundle")
	assert.Equal(t, "myapp-v1", job.Labels["kardinal.io/bundle"])
}

// TestIntegrationTestStep_JobSucceeded verifies that when the Job has succeeded,
// the step returns StepSuccess with the correct outputs.
func TestIntegrationTestStep_JobSucceeded(t *testing.T) {
	step, err := parentsteps.Lookup("integration-test")
	require.NoError(t, err)

	// Pre-create a job that is already succeeded
	jobName := "kt-myapp-v1-staging"
	succeededJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              jobName,
			Namespace:         "staging",
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				"kardinal.io/bundle":      "myapp-v1",
				"kardinal.io/environment": "staging",
			},
		},
		Status: batchv1.JobStatus{
			Succeeded: 1,
		},
	}

	// Build fake client without WithStatusSubresource so status is read directly.
	fc := fake.NewClientBuilder().
		WithScheme(newIntegrationTestScheme()).
		WithObjects(succeededJob).
		WithStatusSubresource().
		Build()

	state := integrationTestState("ghcr.io/myorg/integration-tests:latest")
	state.K8sClient = fc

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Contains(t, result.Message, "succeeded")
	assert.Equal(t, "passed", result.Outputs["integration_test.result"])
	assert.Equal(t, jobName, result.Outputs["integration_test.job"])
}

// TestIntegrationTestStep_JobFailed verifies that when the Job has failed,
// the step returns StepFailed.
func TestIntegrationTestStep_JobFailed(t *testing.T) {
	step, err := parentsteps.Lookup("integration-test")
	require.NoError(t, err)

	jobName := "kt-myapp-v1-staging"
	failedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              jobName,
			Namespace:         "staging",
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				"kardinal.io/bundle":      "myapp-v1",
				"kardinal.io/environment": "staging",
			},
		},
		Status: batchv1.JobStatus{
			Failed: 1,
		},
	}

	fc := fake.NewClientBuilder().
		WithScheme(newIntegrationTestScheme()).
		WithObjects(failedJob).
		WithStatusSubresource().
		Build()

	state := integrationTestState("ghcr.io/myorg/integration-tests:latest")
	state.K8sClient = fc

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "failed")
	assert.Equal(t, "failed", result.Outputs["integration_test.result"])
}

// TestIntegrationTestStep_JobRunning verifies that when the Job is still running,
// the step returns StepPending.
func TestIntegrationTestStep_JobRunning(t *testing.T) {
	step, err := parentsteps.Lookup("integration-test")
	require.NoError(t, err)

	jobName := "kt-myapp-v1-staging"
	runningJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              jobName,
			Namespace:         "staging",
			CreationTimestamp: metav1.Now(),
		},
		Status: batchv1.JobStatus{
			Active: 1,
		},
	}

	fc := fake.NewClientBuilder().
		WithScheme(newIntegrationTestScheme()).
		WithObjects(runningJob).
		Build()

	state := integrationTestState("ghcr.io/myorg/integration-tests:latest")
	state.K8sClient = fc

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepPending, result.Status)
	assert.Contains(t, result.Message, "running")
}

// TestIntegrationTestStep_InvalidTimeout verifies that an invalid timeout
// string causes the step to fail with a clear error.
func TestIntegrationTestStep_InvalidTimeout(t *testing.T) {
	step, err := parentsteps.Lookup("integration-test")
	require.NoError(t, err)

	fc := fake.NewClientBuilder().WithScheme(newIntegrationTestScheme()).Build()
	state := integrationTestState("ghcr.io/myorg/tests:latest")
	state.K8sClient = fc
	state.Inputs["integration_test.timeout"] = "not-a-duration"

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "invalid timeout")
}

// TestIntegrationTestStep_ParseCommand verifies that command strings are parsed
// correctly into args.
func TestIntegrationTestStep_ParseCommand(t *testing.T) {
	step, err := parentsteps.Lookup("integration-test")
	require.NoError(t, err)

	fc := fake.NewClientBuilder().WithScheme(newIntegrationTestScheme()).Build()
	state := integrationTestState("ghcr.io/myorg/tests:latest")
	state.K8sClient = fc
	state.Inputs["integration_test.command"] = "./run-tests.sh --env staging"

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepPending, result.Status) // Job created

	var job batchv1.Job
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{
		Name:      "kt-myapp-v1-staging",
		Namespace: "staging",
	}, &job))
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, []string{"./run-tests.sh", "--env", "staging"},
		job.Spec.Template.Spec.Containers[0].Command)
}

// TestIntegrationTestStep_Idempotent verifies that calling Execute twice
// (simulating reconciler requeue) doesn't create duplicate Jobs.
func TestIntegrationTestStep_Idempotent(t *testing.T) {
	step, err := parentsteps.Lookup("integration-test")
	require.NoError(t, err)

	fc := fake.NewClientBuilder().WithScheme(newIntegrationTestScheme()).Build()
	state := integrationTestState("ghcr.io/myorg/tests:latest")
	state.K8sClient = fc

	// First call: creates Job, returns Pending
	result1, err1 := step.Execute(context.Background(), state)
	require.NoError(t, err1)
	assert.Equal(t, parentsteps.StepPending, result1.Status)

	// Second call: Job already exists (still running), returns Pending without error
	result2, err2 := step.Execute(context.Background(), state)
	require.NoError(t, err2)
	assert.Equal(t, parentsteps.StepPending, result2.Status)

	// Only one Job should exist
	var jobList batchv1.JobList
	require.NoError(t, fc.List(context.Background(), &jobList))
	assert.Len(t, jobList.Items, 1, "idempotent: only one Job must exist after two Execute calls")
}
