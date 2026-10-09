// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AuditEventSpec defines the immutable record of a single promotion event.
// AuditEvents are created by the PromotionStep and PolicyGate reconcilers at
// key lifecycle transitions (started, succeeded, failed). The spec is set at
// creation; the CRD rejects any later change to it (the rule is on
// AuditEvent.Spec, so the outbox entries that embed the type,
// PendingAuditEvent, can be stored in a list).
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
	//               "PromotionSuperseded", "RollbackStarted", "RollbackSucceeded",
	//               "HealthCheckFailed", "GateBlocked", "GateEvaluated", "HoldCreated", "HoldReleased".
	// HealthCheckFailed and GateBlocked are accepted but never written: a
	// failed health check records PromotionFailed (RollbackStarted when
	// onHealthFailure is rollback), and a blocked gate records GateEvaluated
	// with outcome Failure.
	// +kubebuilder:validation:Enum=PromotionStarted;PromotionSucceeded;PromotionFailed;PromotionSuperseded;RollbackStarted;RollbackSucceeded;HealthCheckFailed;GateBlocked;GateEvaluated;HoldCreated;HoldReleased
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
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="AuditEvent spec is immutable"
	Spec AuditEventSpec `json:"spec,omitempty"`
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

// MaxPendingAuditEvents bounds status.pendingAuditEvents, the audit outbox
// of a PromotionStep or PolicyGate.
const MaxPendingAuditEvents = 32

// PendingAuditEvent is an AuditEvent that a reconciler has decided to write
// but has not yet seen created. The reconciler stores it in its own status in
// the same patch as the transition it records. It creates the AuditEvent on
// that reconcile or a later one, then removes the entry. The name is fixed
// when the entry is stored, so a retried create that the API server already
// applied returns AlreadyExists, which counts as written (#1552).
type PendingAuditEvent struct {
	// Name is the AuditEvent's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Labels are the AuditEvent's labels.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Spec is the AuditEvent's spec, timestamp included: the record carries
	// the time of the transition, not the time it was written.
	Spec AuditEventSpec `json:"spec"`

	// CreatedAt is the transition time in RFC 3339 with nanoseconds, written
	// to the AuditEvent's kardinal.io/created-at annotation: spec.timestamp
	// has one-second resolution.
	// +optional
	CreatedAt string `json:"createdAt,omitempty"`
}
