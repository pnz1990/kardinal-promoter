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
// +kubebuilder:validation:XValidation:rule="has(self.bundleName) || has(self.pipelineName) || has(self.environment)",message="an AuditEvent names at least one of bundleName, pipelineName and environment"
type AuditEventSpec struct {
	// Timestamp is when the event occurred (RFC 3339 format).
	// +kubebuilder:validation:Format=date-time
	Timestamp metav1.Time `json:"timestamp"`

	// BundleName is the name of the Bundle being promoted.
	// +kubebuilder:validation:MinLength=1
	// +optional
	BundleName string `json:"bundleName,omitempty"`

	// BundleUID is the UID of that Bundle, on the records of its gates
	// (GateEvaluated, GateOverridden, ApprovalRecorded, ApprovalRevoked,
	// VerdictRecorded, VerdictRevoked): a Bundle deleted and created again
	// under its name is another Bundle, with records of its own. Absent on
	// other records, and on records written before v0.14.
	// +optional
	BundleUID string `json:"bundleUID,omitempty"`

	// PipelineName is the name of the Pipeline the Bundle is promoting through.
	// +kubebuilder:validation:MinLength=1
	// +optional
	PipelineName string `json:"pipelineName,omitempty"`

	// Environment is the environment name where the event occurred.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Environment string `json:"environment,omitempty"`

	// Action is a short verb describing what happened.
	// Valid values: "PromotionStarted", "PromotionSucceeded", "PromotionFailed",
	//               "PromotionSuperseded", "PromotionRejected", "RollbackStarted", "RollbackSucceeded",
	//               "HealthCheckFailed", "GateBlocked", "GateEvaluated", "GateOverridden",
	//               "ApprovalRecorded", "ApprovalRevoked", "HoldCreated", "HoldReleased",
	//               "HoldBundleMissing", "BundleRejected", "APIWrite", "VerdictRecorded",
	//               "VerdictRevoked", "FreezeExempted", "PipelinePaused", "PipelineResumed",
	//               "FreezeStarted", "FreezeLifted", "PRMerged", "ReceiptFailed",
	//               "ChangeRequestOpened", "ChangeRequestLinked", "ChangeRequestApproved",
	//               "ChangeRequestRejected", "GateChanged".
	// The ChangeRequest actions are written by a per-promotion ChangeRequest
	// when it opens or links its ticket, and when the ticket is first read
	// approved or rejected (docs/design/23-change-requests.md section 9).
	// ReceiptFailed is written by a promotion step that reached Verified
	// without a PromotionReceipt, because its receipt could not be built
	// (status.receiptError).
	// PRMerged is written by a promotion step when its pull request is merged,
	// naming who merged it as the SCM API reports it.
	// ApprovalRecorded and ApprovalRevoked are written by an approval gate
	// when a decision (kardinal approve) appears in or leaves its
	// spec.approvals.
	// BundleRejected is written once when a Bundle's spec.rejected is set
	// (kardinal reject or the UI), naming who rejected it and why; its
	// environment is "*" (the whole Bundle).
	// GateOverridden is written once per spec.overrides entry of a gate
	// instance, naming who created it (kardinal override or the UI).
	// APIWrite is written for each write through the UI API or the Bundle
	// API (controller.accessLog.auditEvents), naming the caller in actor and
	// the route in request.
	// FreezeExempted is written once per exemption when a PromotionStep goes
	// on through an active freeze under it (kardinal override --freeze),
	// naming who made the exemption and why.
	// GateChanged is written by the BundleReconciler when a PolicyGate
	// template that applies to a Promoting version was created, edited or
	// deleted and the version's Graph got the change; gates lists the
	// templates of that rebuild with their old and new digest, one event per
	// version and rebuild (#2144).
	// HealthCheckFailed and GateBlocked are accepted but never written: a
	// failed health check records PromotionFailed (RollbackStarted when
	// onHealthFailure is rollback), and a blocked gate records GateEvaluated
	// with outcome Failure.
	// +kubebuilder:validation:Enum=PromotionStarted;PromotionSucceeded;PromotionFailed;PromotionSuperseded;PromotionRejected;RollbackStarted;RollbackSucceeded;HealthCheckFailed;GateBlocked;GateEvaluated;GateOverridden;ApprovalRecorded;ApprovalRevoked;HoldCreated;HoldReleased;HoldBundleMissing;BundleRejected;APIWrite;VerdictRecorded;VerdictRevoked;FreezeExempted;PipelinePaused;PipelineResumed;FreezeStarted;FreezeLifted;PRMerged;ReceiptFailed;ChangeRequestOpened;ChangeRequestLinked;ChangeRequestApproved;ChangeRequestRejected;GateChanged
	Action string `json:"action"`

	// Outcome describes the result of the action.
	// Valid values: "Success", "Failure", "Pending".
	// +kubebuilder:validation:Enum=Success;Failure;Pending
	Outcome string `json:"outcome"`

	// Message is a human-readable description of the event.
	// +optional
	Message string `json:"message,omitempty"`

	// Actor is who caused the event (docs/actors.md). Absent on records
	// written before v0.12.
	// +optional
	Actor *Actor `json:"actor,omitempty"`

	// Fields are an ApprovalRecorded or ApprovalRevoked decision's
	// structured values (Approval spec.fields) the gate declares, whole.
	// +kubebuilder:validation:MaxProperties=16
	// +optional
	Fields map[string]string `json:"fields,omitempty"`

	// Request is the API request of an APIWrite event. It records no body
	// and no token.
	// +optional
	Request *AuditRequest `json:"request,omitempty"`

	// Gates are the PolicyGate template changes a GateChanged event records:
	// one event per version and rebuild, listing at most
	// MaxAuditGateChanges changes in template order.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	Gates []AuditGateChange `json:"gates,omitempty"`

	// GatesOmitted counts the changes of the same rebuild that Gates leaves
	// out, past its first MaxAuditGateChanges.
	// +optional
	// +kubebuilder:validation:Minimum=0
	GatesOmitted int32 `json:"gatesOmitted,omitempty"`

	// MergedRecords is how many unwritten records this one replaces: a full
	// audit outbox merges records of the same kind (GateChanged) before it
	// drops any, during an AuditEvent write outage. Absent for a record
	// written as it was recorded.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MergedRecords int32 `json:"mergedRecords,omitempty"`
}

// MaxAuditGateChanges bounds AuditEventSpec.Gates.
const MaxAuditGateChanges = 20

// AuditGateChange is a PolicyGate template's change under a version in
// flight: the template, and what the version's gate instances copied from
// it before and after (kardinal.io/gate-template-digest and -generation).
// An empty digest is a template that did not apply before (created) or no
// longer does (deleted).
type AuditGateChange struct {
	// Template is the PolicyGate template's name.
	// +kubebuilder:validation:MaxLength=253
	Template string `json:"template"`
	// Namespace is the template's namespace.
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// OldDigest is the digest the instances had.
	// +optional
	// +kubebuilder:validation:MaxLength=80
	OldDigest string `json:"oldDigest,omitempty"`
	// NewDigest is the digest the instances have now.
	// +optional
	// +kubebuilder:validation:MaxLength=80
	NewDigest string `json:"newDigest,omitempty"`
	// OldGeneration is the template generation the instances had.
	// +optional
	OldGeneration int64 `json:"oldGeneration,omitempty"`
	// NewGeneration is the template generation the instances have now.
	// +optional
	NewGeneration int64 `json:"newGeneration,omitempty"`
	// Manager is the field manager of the template's newest managedFields
	// entry, as the writing client declared it (kubectl --field-manager sets
	// any name). It is not verified and does not say who wrote the
	// template: the API server's audit log does.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Manager string `json:"manager,omitempty"`
}

// AuditRequest names the API request an APIWrite AuditEvent records.
type AuditRequest struct {
	// Route is the API route pattern, for example
	// /api/v1/ui/pipelines/{name}/pause.
	// +kubebuilder:validation:MaxLength=256
	Route string `json:"route"`
	// Method is the HTTP method.
	// +kubebuilder:validation:MaxLength=16
	Method string `json:"method"`
}

// AuditEvent is an immutable record of a single promotion event.
// It is written once by the PromotionStep reconciler and never updated.
// AuditEvents form an append-only log of all promotion activity across
// all pipelines.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ae;audit,categories=kardinal-all
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

// AuditActions are the values spec.action accepts: the Enum marker on
// AuditEventSpec.Action lists the same (TestAuditActionsMatchEnum).
var AuditActions = []string{
	"PromotionStarted", "PromotionSucceeded", "PromotionFailed", "PromotionSuperseded", "PromotionRejected",
	"RollbackStarted", "RollbackSucceeded", "HealthCheckFailed", "GateBlocked", "GateEvaluated",
	"GateOverridden", "ApprovalRecorded", "ApprovalRevoked", "HoldCreated", "HoldReleased", "HoldBundleMissing",
	"BundleRejected", "APIWrite", "VerdictRecorded", "VerdictRevoked", "FreezeExempted",
	"PipelinePaused", "PipelineResumed", "FreezeStarted", "FreezeLifted",
	"PRMerged", "ReceiptFailed", "ChangeRequestOpened", "ChangeRequestLinked", "ChangeRequestApproved", "ChangeRequestRejected",
	"GateChanged",
}

// AuditOutcomes are the values spec.outcome accepts.
var AuditOutcomes = []string{"Success", "Failure", "Pending"}

// MaxPendingAuditEvents bounds status.pendingAuditEvents, the audit outbox
// of a PromotionStep or PolicyGate.
const MaxPendingAuditEvents = 32

// PendingAuditEvent is an AuditEvent that a reconciler has decided to write
// but has not yet seen created. The reconciler stores it in its own status in
// the same patch as the transition it records. It creates the AuditEvent on
// that reconcile or a later one, then removes the entry. The name is fixed
// when the entry is stored, so a retried create that the API server already
// applied returns AlreadyExists, which counts as written.
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
