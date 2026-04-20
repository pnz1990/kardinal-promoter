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

	// StepType identifies the built-in or custom step to execute.
	// Examples: git-clone, kustomize-set-image, git-commit, open-pr,
	//           wait-for-merge, health-check.
	// +kubebuilder:validation:MinLength=1
	StepType string `json:"stepType"`

	// Inputs carries step-specific configuration values derived from the
	// Pipeline and Bundle at graph generation time.
	// +optional
	Inputs map[string]string `json:"inputs,omitempty"`

	// UpstreamStates holds the resolved state of all upstream PromotionSteps.
	// Each entry is a string like "Verified", set by the kro Graph controller via CEL
	// expression substitution. Replaces the N-field upstreamVerified/upstreamVerified2
	// pattern (issue 625) -- a single list scales to any number of upstream environments.
	// krocodile collectStrings() scans []any recursively so list items create DAG edges.
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
	// +kubebuilder:validation:Enum=Pending;Promoting;WaitingForMerge;HealthChecking;Verified;Failed;AbortedByAlarm;RollingBack
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

	// HealthCheckExpiry is the deadline for the health check, computed as
	// healthCheckStartedAt + timeout. Set once when the health check begins.
	// A Graph CEL expression can observe this field to detect a stale health check.
	// Graph-purity: replaces the time.Since() call (PS-5 in 11-graph-purity-tech-debt.md).
	// +optional
	HealthCheckExpiry *metav1.Time `json:"healthCheckExpiry,omitempty"`

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

	// BakeResets is the number of times the bake timer was reset due to a
	// health alarm during the current bake window (K-01).
	// +optional
	BakeResets int `json:"bakeResets,omitempty"`

	// Steps is the per-step execution history for this PromotionStep.
	// Populated by the reconciler as each step in the sequence starts, completes, or fails.
	// Provides fine-grained visibility into which sub-step is running without reading
	// controller logs. Initialized when the step sequence starts (state → Promoting).
	// +optional
	Steps []StepStatus `json:"steps,omitempty"`
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
