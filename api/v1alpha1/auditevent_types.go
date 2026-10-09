// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AuditEventSpec defines the immutable record of a single promotion event.
// AuditEvents are created by the PromotionStep and PolicyGate reconcilers at
// key lifecycle transitions (started, succeeded, failed). The spec is set at
// creation; the CRD rejects any later change to it.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="AuditEvent spec is immutable"
type AuditEventSpec struct {
	// Timestamp is when the event occurred (RFC 3339 format).
	// +kubebuilder:validation:Format=date-time
	Timestamp metav1.Time `json:"timestamp"`

	// BundleName is the name of the Bundle being promoted.
	// +kubebuilder:validation:MinLength=1
	BundleName string `json:"bundleName"`

	// PipelineName is the name of the Pipeline the Bundle is promoting through.
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`

	// Environment is the environment name where the event occurred.
	// +kubebuilder:validation:MinLength=1
	Environment string `json:"environment"`

	// Action is a short verb describing what happened.
	// Valid values: "PromotionStarted", "PromotionSucceeded", "PromotionFailed",
	//               "PromotionSuperseded", "PromotionRejected", "RollbackStarted", "RollbackSucceeded",
	//               "HealthCheckFailed", "GateBlocked", "GateEvaluated", "GateOverridden",
	//               "ApprovalRecorded", "ApprovalRevoked", "HoldCreated", "HoldReleased".
	// ApprovalRecorded and ApprovalRevoked are written by an approval gate
	// when a decision (kardinal approve) appears in or leaves its
	// spec.approvals.
	// GateOverridden is written once per spec.overrides entry of a gate
	// instance, naming who created it (kardinal override or the UI).
	// HealthCheckFailed and GateBlocked are accepted but never written: a
	// failed health check records PromotionFailed (RollbackStarted when
	// onHealthFailure is rollback), and a blocked gate records GateEvaluated
	// with outcome Failure.
	// +kubebuilder:validation:Enum=PromotionStarted;PromotionSucceeded;PromotionFailed;PromotionSuperseded;PromotionRejected;RollbackStarted;RollbackSucceeded;HealthCheckFailed;GateBlocked;GateEvaluated;GateOverridden;ApprovalRecorded;ApprovalRevoked;HoldCreated;HoldReleased
	Action string `json:"action"`

	// Outcome describes the result of the action.
	// Valid values: "Success", "Failure", "Pending".
	// +kubebuilder:validation:Enum=Success;Failure;Pending
	Outcome string `json:"outcome"`

	// Message is a human-readable description of the event.
	// +optional
	Message string `json:"message,omitempty"`
}

// AuditEvent is an immutable record of a single promotion event.
// It is written once by the PromotionStep reconciler and never updated.
// AuditEvents form an append-only log of all promotion activity across
// all pipelines.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ae;audit
// +kubebuilder:printcolumn:name="Pipeline",type="string",JSONPath=".spec.pipelineName"
// +kubebuilder:printcolumn:name="Bundle",type="string",JSONPath=".spec.bundleName"
// +kubebuilder:printcolumn:name="Environment",type="string",JSONPath=".spec.environment"
// +kubebuilder:printcolumn:name="Action",type="string",JSONPath=".spec.action"
// +kubebuilder:printcolumn:name="Outcome",type="string",JSONPath=".spec.outcome"
// +kubebuilder:printcolumn:name="Timestamp",type="string",JSONPath=".spec.timestamp"
type AuditEvent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AuditEventSpec `json:"spec,omitempty"`
}

// AuditEventList contains a list of AuditEvent.
// +kubebuilder:object:root=true
type AuditEventList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AuditEvent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AuditEvent{}, &AuditEventList{})
}
