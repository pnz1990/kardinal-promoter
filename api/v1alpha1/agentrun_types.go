// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentRun phases. Succeeded, Failed and Skipped are terminal and never
// change.
const (
	AgentRunPending   = "Pending"
	AgentRunRunning   = "Running"
	AgentRunSucceeded = "Succeeded"
	AgentRunFailed    = "Failed"
	// AgentRunSkipped: the run's Bundle was superseded or rejected before
	// its Job started.
	AgentRunSkipped = "Skipped"
)

// AgentRun conditions.
const (
	// AgentRunConditionBounded is True once the run passed its bounds
	// checks (a ready StepTemplate, an agent ServiceAccount whose RBAC is
	// read-only, Secrets labelled for the template), and False with the
	// reason it was refused.
	AgentRunConditionBounded = "Bounded"
)

// AgentRunSpec is one attempt of one in-cluster verifier for one Bundle,
// environment and check (docs/design/19-verdicts.md, section 8). The kro
// Graph of the Bundle creates it; the AgentRun reconciler runs the Job whose
// Pod writes the Verdict.
type AgentRunSpec struct {
	// PipelineName is the Bundle's Pipeline.
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`

	// BundleName is the Bundle verified.
	// +kubebuilder:validation:MinLength=1
	BundleName string `json:"bundleName"`

	// BundleUID is that Bundle's UID.
	// +kubebuilder:validation:MinLength=1
	BundleUID string `json:"bundleUID"`

	// Version is the Bundle's version.
	// +optional
	Version string `json:"version,omitempty"`

	// Environment is the environment verified.
	// +kubebuilder:validation:MinLength=1
	Environment string `json:"environment"`

	// TargetNamespace is the namespace the agent tests: its Pod may reach
	// every Pod there.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	TargetNamespace string `json:"targetNamespace"`

	// Check is the verify gate's check.
	// +kubebuilder:validation:MinLength=1
	Check string `json:"check"`

	// Template is the StepTemplate the run runs.
	// +kubebuilder:validation:MinLength=1
	Template string `json:"template"`

	// Run is the run's number among the check's runs (1-based).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	Run int `json:"run"`

	// Attempt is the attempt's number (1-based).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=6
	Attempt int `json:"attempt"`

	// Recorded is set by the Graph: the result of the Verdict named after
	// this AgentRun, when one exists. An AgentRun that starts with it set
	// does not run its Job (an AgentRun deleted and applied again does not
	// run twice).
	// +optional
	Recorded string `json:"recorded,omitempty"`
}

// AgentRunStatus is the observed state of an AgentRun.
type AgentRunStatus struct {
	// Phase is Pending until the Job is created, Running while it runs, and
	// Succeeded (the Job completed) or Failed (it failed, timed out, or the
	// run was refused) once it finished, or Skipped. The verify result is
	// the Verdict's, not the phase.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Skipped
	// +kubebuilder:validation:XValidation:rule="!(oldSelf in ['Succeeded', 'Failed', 'Skipped']) || self == oldSelf",message="a finished AgentRun's phase cannot change"
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message says why the AgentRun is in its phase.
	// +optional
	Message string `json:"message,omitempty"`

	// Agent is the agent the run's ServiceAccount is (AgentIdentity name).
	// +optional
	Agent string `json:"agent,omitempty"`

	// JobName is the name of the Job the reconciler created.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// JobUID is the UID of that Job. A Job of that name with another UID,
	// or none at all, while the run runs fails it: it is not run again.
	// +optional
	JobUID string `json:"jobUID,omitempty"`

	// StartedAt is when the Job was created.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// Deadline is StartedAt plus the template's timeout.
	// +optional
	Deadline *metav1.Time `json:"deadline,omitempty"`

	// FinishedAt is when the run reached a terminal phase.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// Conditions: Bounded.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ar,categories=kardinal-all
// +kubebuilder:printcolumn:name="Bundle",type=string,JSONPath=`.spec.bundleName`
// +kubebuilder:printcolumn:name="Env",type=string,JSONPath=`.spec.environment`
// +kubebuilder:printcolumn:name="Check",type=string,JSONPath=`.spec.check`
// +kubebuilder:printcolumn:name="Run",type=integer,JSONPath=`.spec.run`
// +kubebuilder:printcolumn:name="Attempt",type=integer,JSONPath=`.spec.attempt`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentRun is one attempt of an in-cluster verifier (a StepTemplate's agent
// image) for one Bundle, environment and check. Created by the Bundle's kro
// Graph for a verify gate with verify.run; reconciled by the AgentRun
// reconciler, which checks its bounds, creates its NetworkPolicy and Job,
// and records the Job's result.
type AgentRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRunSpec   `json:"spec,omitempty"`
	Status AgentRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentRunList contains a list of AgentRun.
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRun{}, &AgentRunList{})
}
