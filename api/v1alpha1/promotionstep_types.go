// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PromotionStepSpec defines the desired state of a PromotionStep.
// PromotionStep objects are created by the Graph controller — not by users.
type PromotionStepSpec struct {
	// PipelineName is the Pipeline this step belongs to.
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`

	// BundleName is the Bundle being promoted.
	// +kubebuilder:validation:MinLength=1
	BundleName string `json:"bundleName"`

	// Environment is the environment this step promotes into.
	// +kubebuilder:validation:MinLength=1
	Environment string `json:"environment"`

	// StepType identifies the built-in step to execute.
	// Examples: git-clone, kustomize-set-image, git-commit, open-pr,
	//           wait-for-merge, health-check.
	// +kubebuilder:validation:MinLength=1
	StepType string `json:"stepType"`

	// UpstreamStates holds the resolved state of all upstream PromotionSteps.
	// Each entry is a string like "Verified", set by the kro Graph controller via CEL
	// expression substitution. Replaces the N-field upstreamVerified/upstreamVerified2
	// pattern (issue 625) -- a single list scales to any number of upstream environments.
	// kro scans list items for CEL references, so each entry creates a DAG edge.
	// +optional
	UpstreamStates []string `json:"upstreamStates,omitempty"`

	// RequiredGates holds the names of PolicyGate instances that must be ready
	// before this PromotionStep can be promoted. Set by the Graph controller via CEL.
	// +optional
	RequiredGates []string `json:"requiredGates,omitempty"`

	// PRStatusRef is the name of the companion PRStatus CRD in the same namespace.
	// Set by the Graph controller from the PRStatus Watch node's metadata.name CEL reference.
	// The PromotionStep reconciler reads the PRStatus CRD instead of polling GitHub
	// directly, eliminating the PS-4 / SCM-2 external API call on the reconcile hot path.
	// +optional
	PRStatusRef string `json:"prStatusRef,omitempty"`

	// ScmProvider is the provider of the Pipeline's spec.git.providerRef,
	// as the translator resolved it when it built the Graph. Unset uses the
	// controller's --scm-provider.
	// +optional
	ScmProvider *ScmProviderIdentity `json:"scmProvider,omitempty"`

	// PreHooks names the HookRuns of the environment's pre-deploy hooks, in
	// order. The step stays Pending until every one of them Succeeded in
	// spec.live.hooks, and fails when one Failed. Set by the Graph: the first
	// entry references the first HookRun node, so the step is created only
	// after it.
	// +optional
	PreHooks []string `json:"preHooks,omitempty"`

	// PostHooks names the HookRuns of the environment's post-deploy hooks, in
	// order. A step with post hooks goes from HealthChecking to Verifying,
	// and is Verified only when every one of them Succeeded in
	// spec.live.hooks; a Failed one applies onHealthFailure.
	// +optional
	PostHooks []string `json:"postHooks,omitempty"`

	// Analyses names the AnalysisTemplates of the environment's
	// verification. A step with analyses goes from HealthChecking to
	// Verifying and is Verified only when, for every template, the newest
	// AnalysisRun in spec.live.analyses is Successful: a run that a later
	// translation replaced (the template changed) is not waited for, and the
	// timeout keeps counting from status.verificationStartedAt.
	// +optional
	Analyses []string `json:"analyses,omitempty"`

	// AnalysisPolicy is the verification's verdict policy, copied from the
	// Pipeline when the Graph was built, so a Pipeline edit does not change
	// the verdict of a step in flight.
	// +optional
	AnalysisPolicy *StepAnalysisPolicy `json:"analysisPolicy,omitempty"`

	// ImageVerification names the Bundle's ImageVerification when this step
	// must wait for it: a step with no upstream in the Graph stays Pending
	// until spec.live.imageVerification.phase is Verified, and fails when it
	// is Failed.
	// +optional
	ImageVerification string `json:"imageVerification,omitempty"`

	// Live holds results the Graph mirrors onto the step while it runs, each
	// part with its own patch node (its own field manager), not the step's
	// template, so they keep updating after the step's own template stopped
	// resolving: the environment's hook and analysis runs, the current
	// results of its gates, and the Bundle's image verification. The
	// reconciler reads only this copy, never the source objects; while the
	// step's PR waits for its merge it mirrors the gates to the PR's head
	// commit as the kardinal/gates commit status. Do not set it.
	// +optional
	Live *PromotionStepLive `json:"live,omitempty"`

	// Region was set on the per-region PromotionSteps of a Pipeline
	// environment with two or more spec.regions. The Graph builder no longer
	// sets it; the reconciler fails a step that still has one (created by a
	// Graph built before the upgrade) with "regions is not supported".
	//
	// Deprecated: declare one environment per region (prod-us, prod-eu) and
	// use wave.
	// +optional
	Region string `json:"region,omitempty"`
}

// LiveGate is one gate instance's current result.
type LiveGate struct {
	// Name is the gate instance name.
	Name string `json:"name"`
	// Ready is the instance's status.ready.
	Ready bool `json:"ready"`
	// Reason is the instance's status.reason.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// PromotionStepLive is what the Graph mirrors onto a PromotionStep.
type PromotionStepLive struct {
	// Hooks are the environment's HookRuns for this Bundle.
	// +optional
	Hooks []LiveHookRun `json:"hooks,omitempty"`

	// Analyses are the environment's AnalysisRuns for this Bundle.
	// +optional
	Analyses []LiveAnalysisRun `json:"analyses,omitempty"`

	// Gates are the gate instances of the step's environment for its Bundle,
	// with their current result.
	// +optional
	Gates []LiveGate `json:"gates,omitempty"`

	// ImageVerification is the Bundle's ImageVerification result.
	// +optional
	ImageVerification *LiveImageVerification `json:"imageVerification,omitempty"`
}

// LiveImageVerification is the result of the Bundle's ImageVerification.
type LiveImageVerification struct {
	// Name is the ImageVerification's name.
	// +optional
	Name string `json:"name,omitempty"`
	// Images are the images it verifies, "repository@digest" (repository
	// normalized). The step refuses to promote a Bundle whose images differ.
	// +optional
	Images []string `json:"images,omitempty"`
	// Phase is its status.phase (Pending when it has none yet).
	// +optional
	Phase string `json:"phase,omitempty"`
	// Message is its status.message.
	// +optional
	Message string `json:"message,omitempty"`
}

// StepAnalysisPolicy is a step's copy of spec.verification's verdict policy.
type StepAnalysisPolicy struct {
	// Inconclusive is "fail" (default) or "pass".
	// +optional
	Inconclusive string `json:"inconclusive,omitempty"`
	// Timeout is the verification timeout (default 30m).
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// LiveAnalysisRun is the result of one Argo Rollouts AnalysisRun.
type LiveAnalysisRun struct {
	// Name is the AnalysisRun name.
	Name string `json:"name"`
	// Created is the AnalysisRun's creationTimestamp (RFC 3339). The newest
	// run of a template is the one the step waits for.
	// +optional
	Created string `json:"created,omitempty"`
	// Template is the AnalysisTemplate or ClusterAnalysisTemplate it runs.
	// +optional
	Template string `json:"template,omitempty"`
	// Phase is the AnalysisRun's status.phase (Pending when it has none
	// yet): Pending, Running, Successful, Failed, Error or Inconclusive.
	// +optional
	Phase string `json:"phase,omitempty"`
	// Message is the AnalysisRun's status.message.
	// +optional
	Message string `json:"message,omitempty"`
}

// LiveHookRun is the result of one HookRun.
type LiveHookRun struct {
	// Name is the HookRun name.
	Name string `json:"name"`
	// Hook is the hook's name in the Pipeline.
	// +optional
	Hook string `json:"hook,omitempty"`
	// Phase is the hook phase: pre or post.
	// +optional
	Phase string `json:"phase,omitempty"`
	// Result is the HookRun's status.phase (Pending when it has none yet).
	// +optional
	Result string `json:"result,omitempty"`
	// Message is the HookRun's status.message.
	// +optional
	Message string `json:"message,omitempty"`
	// SpecHash is the HookRun's status.specHash: which job and timeout ran.
	// +optional
	SpecHash string `json:"specHash,omitempty"`
}

// HookRecord is the step's own record that a hook ran for it: once a
// HookRun's Job started, the step keeps its result here, so a HookRun
// recreated for the same hook (deleted and applied again by the Graph) does
// not run the Job a second time and takes the recorded result instead.
type HookRecord struct {
	// Hook is the hook's name in the Pipeline.
	// +optional
	Hook string `json:"hook,omitempty"`
	// Phase is pre or post.
	// +optional
	Phase string `json:"phase,omitempty"`
	// SpecHash is the HookRun's spec hash (job and timeout) that ran.
	// +optional
	SpecHash string `json:"specHash,omitempty"`
	// Result is Running, Succeeded or Failed. Succeeded and Failed are final.
	// +optional
	Result string `json:"result,omitempty"`
	// Message is the HookRun's last message.
	// +optional
	Message string `json:"message,omitempty"`
}

// StepExecutionState is the execution state of a single step within a PromotionStep.
// +kubebuilder:validation:Enum=Pending;InProgress;Completed;Failed
type StepExecutionState string

const (
	// StepExecutionPending means the step has not started yet.
	StepExecutionPending StepExecutionState = "Pending"
	// StepExecutionInProgress means the step is currently executing.
	StepExecutionInProgress StepExecutionState = "InProgress"
	// StepExecutionCompleted means the step finished successfully.
	StepExecutionCompleted StepExecutionState = "Completed"
	// StepExecutionFailed means the step encountered a terminal error.
	StepExecutionFailed StepExecutionState = "Failed"
)

// StepStatus captures the observable state of one step in the promotion sequence.
type StepStatus struct {
	// Name is the step type identifier (e.g. "git-clone", "open-pr").
	Name string `json:"name"`

	// State is the execution state of this step.
	// +kubebuilder:validation:Enum=Pending;InProgress;Completed;Failed
	State StepExecutionState `json:"state"`

	// StartedAt is when the step began executing.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the step finished (success or failure).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// DurationMs is the wall-clock duration in milliseconds from startedAt to completedAt.
	// Zero when the step has not completed.
	// +optional
	DurationMs int64 `json:"durationMs,omitempty"`

	// Message provides human-readable detail for Failed steps.
	// +optional
	Message string `json:"message,omitempty"`
}

// PromotionStepStatus defines the observed state of a PromotionStep.
type PromotionStepStatus struct {
	// State is the step execution state.
	// The Graph controller uses readyWhen expressions of the form
	// ${step.status.state == "Verified"} to advance the promotion DAG.
	// Verifying: the health check passed and the post-deploy hooks and
	// analyses run.
	// +kubebuilder:validation:Enum=Pending;Promoting;WaitingForMerge;HealthChecking;Verifying;Verified;Failed;AbortedByAlarm;RollingBack
	State string `json:"state,omitempty"`

	// Message provides human-readable detail about the current state.
	// +optional
	Message string `json:"message,omitempty"`

	// CurrentStepIndex is the index into the step sequence that the reconciler
	// is currently executing. Persisted to etcd for idempotent crash recovery
	// (spec 003 FR-002).
	// +optional
	CurrentStepIndex int `json:"currentStepIndex,omitempty"`

	// PRURL is the GitHub pull request URL opened for this promotion.
	// Set when the step enters WaitingForMerge state.
	// +optional
	PRURL string `json:"prURL,omitempty"`

	// Outputs accumulates key/value results from completed steps in the
	// sequence (e.g. prURL from the open-pr step).
	// +optional
	Outputs map[string]string `json:"outputs,omitempty"`

	// Conditions holds status conditions.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ConsecutiveHealthFailures tracks the number of consecutive health-check
	// failures for this step. Reset to 0 on a successful health check.
	// Used by the auto-rollback policy in the pipeline environment spec.
	// +optional
	ConsecutiveHealthFailures int `json:"consecutiveHealthFailures,omitempty"`

	// HealthCheckExpiry is the deadline for a healthy check: health.timeout
	// after the health check began, moved to health.timeout after the moment
	// a bake window stops. It does not apply while a bake window runs.
	// A Graph CEL expression can observe this field to detect a stale health check.
	// Graph-purity: replaces the time.Since() call (PS-5 in 11-graph-purity-tech-debt.md).
	// +optional
	HealthCheckExpiry *metav1.Time `json:"healthCheckExpiry,omitempty"`

	// WaitForMergeExpiry is the deadline for the PR merge, computed as
	// (time step entered WaitingForMerge) + env.waitForMergeTimeout. Set once on
	// the first reconcile in WaitingForMerge state when the environment configures
	// a non-zero waitForMergeTimeout. Nil when no timeout is configured.
	// Graph-purity: same pattern as HealthCheckExpiry — time.Now() called only when
	// writing to CRD status.
	// +optional
	WaitForMergeExpiry *metav1.Time `json:"waitForMergeExpiry,omitempty"`

	// WorkDir is the working directory on the controller node used for git operations
	// (clone, commit, push) and kustomize builds. Persisted to etcd so that a restarted
	// controller can re-use the same directory and resume in-flight git work.
	// ST-7/ST-8/ST-9 short-term mitigation: the workdir path is made observable via
	// CRD status, enabling crash-recovery without re-cloning.
	// Long-term: git operations become Kubernetes Jobs (owned nodes in the Graph).
	// +optional
	WorkDir string `json:"workDir,omitempty"`

	// BakeStartedAt is when the contiguous-healthy soak window began (K-01).
	// Set on the first successful health check when env.bake is configured.
	// Reset when BakeElapsedMinutes resets (health failure with reset-on-alarm).
	// +optional
	BakeStartedAt *metav1.Time `json:"bakeStartedAt,omitempty"`

	// BakeElapsedMinutes is the number of contiguous healthy minutes accumulated
	// so far in the current bake window (K-01). Resets to 0 on health failure
	// when policy=reset-on-alarm. When this reaches env.bake.minutes, the step
	// transitions to Verified.
	// +optional
	BakeElapsedMinutes int64 `json:"bakeElapsedMinutes,omitempty"`

	// BakeFirstStartedAt is when the first bake window of this step began:
	// the first healthy check with env.bake configured. It is never reset.
	// With bake.policy reset-on-alarm, the step must complete one full
	// window by BakeFirstStartedAt + bake.minutes + health.timeout, so a
	// release that keeps flapping ends (#1423).
	// +optional
	BakeFirstStartedAt *metav1.Time `json:"bakeFirstStartedAt,omitempty"`

	// BakeResets is the number of times the bake timer was reset due to a
	// health alarm during the current bake window (K-01).
	// +optional
	BakeResets int `json:"bakeResets,omitempty"`

	// RetryCount is the number of consecutive step-engine errors retried in the
	// current state. Reset when a step makes progress. When it reaches the retry
	// limit the PromotionStep fails. Retries counted in gitCredentialRetries
	// are not counted here.
	// +optional
	RetryCount int `json:"retryCount,omitempty"`

	// GitCredentialRetries is the number of consecutive retries of a git-clone
	// or git-push that the remote refused while git had no credentials because
	// spec.git.secretRef is not set, or names a Secret that does not exist or
	// has no token key (condition GitCredentialMissing). These retries have no
	// limit, so creating the Secret is enough for the step to continue, and
	// they do not use up the retries of retryCount. Reset with retryCount, and
	// when git has a token.
	// +optional
	GitCredentialRetries int `json:"gitCredentialRetries,omitempty"`

	// ContendedRetries is the number of consecutive retries of a git-push that
	// lost to other writers of the base branch every time within one reconcile
	// (the base branch kept moving). They have no limit, since contention is
	// not a fault of the step, and do not use up the retries of retryCount;
	// they only back off. Reset with retryCount.
	// +optional
	ContendedRetries int `json:"contendedRetries,omitempty"`

	// NextRetryAt is when a step that failed with a retryable error runs
	// again, or when a superseded step whose PR close failed retries the close
	// (condition SupersededCloseFailed). A reconcile before then waits for it,
	// so the retry backoff holds however often the step is reconciled (a gate
	// re-evaluation, a PRStatus change, a controller restart). Cleared when the
	// step runs again.
	// +optional
	NextRetryAt *metav1.Time `json:"nextRetryAt,omitempty"`

	// SCMWaitSince is when the step started waiting for an open SCM circuit
	// (condition SCMUnavailable): its SCM host failed, so the step makes no
	// call and waits without spending retryCount. The wait ends when the
	// SCM answers, or fails the step after the environment's
	// stepTimeoutSeconds, else the controller's --scm-wait-timeout (30m).
	// +optional
	SCMWaitSince *metav1.Time `json:"scmWaitSince,omitempty"`

	// LastHealthCheckAt records when the health adapter was last called. Used to
	// space health checks at the health-check interval regardless of how often
	// the step is reconciled.
	// +optional
	LastHealthCheckAt *metav1.Time `json:"lastHealthCheckAt,omitempty"`

	// TargetUpdatedAt is when a health check of this step first found the
	// workload's target running the Bundle images (health.type resource: the
	// Deployment's pod template; flagger: the Canary's target Deployment).
	// Set once. The flagger check counts a Failed phase, and the resource
	// check a ProgressDeadlineExceeded when it cannot read the ReplicaSet the
	// condition names, only when set after this time: the earlier rollout's
	// condition or phase can outlast the Bundle's update.
	// +optional
	TargetUpdatedAt *metav1.Time `json:"targetUpdatedAt,omitempty"`

	// VerificationStartedAt is when the step entered Verifying (its health
	// check passed and its post-deploy hooks may start). Set once; the
	// Graph creates the post-deploy HookRuns once it is set.
	// +optional
	VerificationStartedAt *metav1.Time `json:"verificationStartedAt,omitempty"`

	// HookRecords records each hook that ran for this step (HookRecord).
	// +kubebuilder:validation:MaxItems=40
	// +optional
	HookRecords []HookRecord `json:"hookRecords,omitempty"`

	// Steps is the per-step execution history for this PromotionStep.
	// Populated by the reconciler as each step in the sequence starts, completes, or fails.
	// Provides fine-grained visibility into which sub-step is running without reading
	// controller logs. Initialized when the step sequence starts (state → Promoting).
	// +optional
	Steps []StepStatus `json:"steps,omitempty"`

	// PendingAuditEvents are AuditEvents for this step's transitions that are
	// not yet written (the audit outbox, #1552). Each entry is stored in the
	// same status patch as its transition and removed once the AuditEvent
	// exists, so an API error or a controller restart between the two cannot
	// lose the record. Normally empty.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	PendingAuditEvents []PendingAuditEvent `json:"pendingAuditEvents,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ps
// +kubebuilder:printcolumn:name="Pipeline",type=string,JSONPath=`.spec.pipelineName`
// +kubebuilder:printcolumn:name="Env",type=string,JSONPath=`.spec.environment`
// +kubebuilder:printcolumn:name="Bundle",type=string,JSONPath=`.spec.bundleName`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromotionStep is a controller-internal CRD representing one step in a
// promotion sequence. Created by the Graph controller; reconciled by the
// PromotionStep reconciler.
type PromotionStep struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromotionStepSpec   `json:"spec,omitempty"`
	Status PromotionStepStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PromotionStepList contains a list of PromotionStep.
type PromotionStepList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionStep `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PromotionStep{}, &PromotionStepList{})
}
