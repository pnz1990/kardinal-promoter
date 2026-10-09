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
	"slices"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

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

	// StepRestart asks the engine to run the sequence again from the first
	// step, for example after a push was rejected because the base branch
	// moved. The engine bounds restarts and never returns this status.
	StepRestart StepStatus = "Restart"
)

// StepResult is the outcome of a step execution.
type StepResult struct {
	// Status is the execution outcome.
	Status StepStatus

	// Message is a human-readable description of the outcome.
	Message string

	// Outputs holds key/value pairs to pass to subsequent steps.
	Outputs map[string]string

	// RequeueAfter, when set with StepPending, signals to the reconciler how long
	// to wait before re-executing the step. A zero value means requeue immediately.
	// Used by steps that need non-blocking retry backoff (e.g. wait-for-merge SCM retries).
	RequeueAfter time.Duration
}

// GitConfig holds Git repository connection details for a step.
type GitConfig struct {
	// URL is the GitOps repository HTTPS URL.
	URL string

	// Branch is the base branch: git-clone checks it out, git-push pushes to
	// it when the step opens no PR, and open-pr targets it. The reconciler
	// never leaves it empty; an unset spec.git.branch is main.
	Branch string

	// Token is the HTTP(S) git token: a PAT, an access token or a GitHub App
	// installation token.
	Token string

	// SSHPrivateKey and SSHKnownHosts authenticate an ssh remote: the
	// sshPrivateKey and knownHosts keys of the Pipeline's git Secret.
	SSHPrivateKey []byte
	SSHKnownHosts []byte

	// AuthorName is the git commit author name.
	AuthorName string

	// AuthorEmail is the git commit author email.
	AuthorEmail string
}

// Auth is the git authentication of g.
func (g GitConfig) Auth() scm.GitAuth {
	return scm.GitAuth{Token: g.Token, SSHPrivateKey: g.SSHPrivateKey, SSHKnownHosts: g.SSHKnownHosts}
}

// StepState carries all context needed by a step during execution.
type StepState struct {
	// Pipeline is the Pipeline CRD spec.
	Pipeline v1alpha1.PipelineSpec

	// PipelineName is the Pipeline resource name.
	PipelineName string

	// Namespace is the namespace of the Pipeline, Bundle and PromotionStep.
	Namespace string

	// Environment is the target environment configuration.
	Environment v1alpha1.EnvironmentSpec

	// Bundle holds the Bundle being promoted.
	Bundle v1alpha1.BundleSpec

	// BundleName is the Bundle resource name.
	BundleName string

	// RequestedBy is who asked for the Bundle: its kardinal.io/requested-by
	// annotation (the CLI or UI user, or the controller for an automatic
	// rollback). Empty when it is not recorded.
	RequestedBy string

	// RollbackFrom names the Bundle a rollback Bundle replaces: its
	// kardinal.io/rollback-from annotation. Empty for a promotion.
	RollbackFrom string

	// RollbackFromBundle is the spec of the RollbackFrom Bundle. Nil when
	// there is none or it cannot be read.
	RollbackFromBundle *v1alpha1.BundleSpec

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

	// K8sClient is the Kubernetes API client for steps that read or patch
	// cluster resources (e.g., argocd-set-image patching an Application).
	// May be nil for unit tests or environments that do not use such steps.
	K8sClient client.Client

	// StepTimeoutSeconds is the per-step execution timeout in seconds.
	// When > 0, ExecuteFrom wraps each step's context with context.WithTimeout.
	// When 0, no per-step timeout is applied (default behaviour).
	StepTimeoutSeconds int

	// Sequence is the step list being run: the one the PromotionStep recorded
	// when it left Pending. A step reads it, not Environment.Approval, to know
	// whether the promotion goes through a PR, so an approval edit made while
	// the step runs applies from the next Bundle. The Graph does not follow
	// the approval either (its PRStatus node has no readyWhen), so the Bundle
	// in flight still turns GraphReady once its steps are Verified and its gates
	// pass (B69).
	Sequence []string
}

// OpenPRStepName is the name of the step that opens the promotion PR.
const OpenPRStepName = "open-pr"

// OutputPRLabelsError is the open-pr output that keeps the error of a failed
// attempt to label the PR it opened. The PR stays open without the labels,
// and the PromotionStep's WaitingForMerge message repeats the error.
const OutputPRLabelsError = "prLabelsError"

// OpensPR reports whether the sequence being run opens a PR.
func (s *StepState) OpensPR() bool {
	return slices.Contains(s.Sequence, OpenPRStepName)
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
