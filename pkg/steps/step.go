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

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// StepStatus is the outcome state of a step execution.
type StepStatus string

const (
	// StepSuccess indicates the step completed successfully.
	StepSuccess StepStatus = "Success"

	// StepFailed indicates the step encountered an unrecoverable error.
	StepFailed StepStatus = "Failed"

	// StepPending indicates the step is still in progress and the reconciler should requeue.
	StepPending StepStatus = "Pending"
)

// StepResult is the outcome of a step execution.
type StepResult struct {
	// Status is the execution outcome.
	Status StepStatus

	// Message is a human-readable description of the outcome.
	Message string

	// Outputs holds key/value pairs to pass to subsequent steps.
	Outputs map[string]string
}

// GitConfig holds Git repository connection details for a step.
type GitConfig struct {
	// URL is the GitOps repository HTTPS URL.
	URL string

	// Branch is the base branch.
	Branch string

	// Token is the SCM authentication token.
	Token string

	// AuthorName is the git commit author name.
	AuthorName string

	// AuthorEmail is the git commit author email.
	AuthorEmail string
}

// StepState carries all context needed by a step during execution.
type StepState struct {
	// Pipeline is the Pipeline CRD spec.
	Pipeline v1alpha1.PipelineSpec

	// PipelineName is the Pipeline resource name.
	PipelineName string

	// Environment is the target environment configuration.
	Environment v1alpha1.EnvironmentSpec

	// Bundle holds the Bundle being promoted.
	Bundle v1alpha1.BundleSpec

	// BundleName is the Bundle resource name.
	BundleName string

	// WorkDir is the local directory where the Git work tree is checked out.
	WorkDir string

	// Outputs accumulates key/value results from previous steps.
	Outputs map[string]string

	// Git holds Git repository connection details.
	Git GitConfig

	// SCM is the SCM provider for PR operations.
	SCM scm.SCMProvider

	// GitClient is the Git operations client.
	GitClient scm.GitClient

	// GateResults holds the PolicyGate evaluations for this environment.
	GateResults []v1alpha1.GateResult

	// UpstreamEnvironments holds verification evidence from upstream environments.
	UpstreamEnvironments []v1alpha1.EnvironmentStatus

	// Inputs holds step-specific configuration values from PromotionStep.Spec.Inputs.
	// Custom webhook steps read their configuration (webhook.url, webhook.timeoutSeconds,
	// webhook.secretRef.name, webhook.authorization) from this map.
	Inputs map[string]string
}

// Step is a single unit of promotion work.
// Implementations must be idempotent: safe to re-execute after a crash.
type Step interface {
	// Execute runs the step. Returns a StepResult and any fatal error.
	// A non-nil error causes the reconciler to mark the PromotionStep as Failed.
	// A nil error with StepPending status causes the reconciler to requeue.
	Execute(ctx context.Context, state *StepState) (StepResult, error)

	// Name returns the step identifier (e.g., "git-clone", "open-pr").
	Name() string
}
