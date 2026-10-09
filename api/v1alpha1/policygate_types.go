// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PolicyGateSpec defines the desired state of a PolicyGate.
type PolicyGateSpec struct {
	// Expression is the CEL expression evaluated to determine if promotion
	// is allowed. Must evaluate to a boolean.
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`

	// Message is a human-readable explanation shown when the gate blocks.
	// +optional
	Message string `json:"message,omitempty"`

	// RecheckInterval is how often to re-evaluate time-based gates.
	// Uses Go duration format (e.g. "5m", "1h"). The minimum is 10s: a smaller
	// value is raised to 10s, and "0" or an invalid value means the default.
	// +kubebuilder:default="5m"
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	RecheckInterval string `json:"recheckInterval,omitempty"`

	// SkipPermission marks a skip-permission gate as granting skips. A Bundle
	// may skip (intent.skipEnvironments) an environment an org gate applies to
	// only when a gate labelled kardinal.io/type=skip-permission, with
	// skipPermission true, in an org policy namespace, applies to that
	// environment. Gates in other namespaces never grant a skip. The permission
	// gate's expression is evaluated like any gate, in front of the next
	// environment the Bundle promotes, so that environment waits until it is true.
	// On any other gate this field has no effect.
	// +kubebuilder:default=false
	// +optional
	SkipPermission bool `json:"skipPermission,omitempty"`

	// Selector is not implemented: nothing reads it, and the API server rejects
	// a PolicyGate that sets it. An org gate applies to the environments named
	// in its kardinal.io/applies-to label.
	//
	// Deprecated: remove the field; use the kardinal.io/applies-to label.
	// +kubebuilder:validation:XValidation:rule="false",message="spec.selector is not implemented; use the kardinal.io/applies-to label"
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// When has no effect. Every gate on an environment is re-checked right
	// before that environment's PromotionStep starts: the step stays in
	// Pending, with no git operation, until each gate it requires exists, is
	// ready, and was evaluated at or after the step was created. A step that
	// has started is not stopped by a gate that turns false later.
	//
	// Deprecated: remove this field. Every gate is re-checked before its
	// PromotionStep starts, whatever the value; pre-deploy and post-deploy
	// behave the same.
	// +kubebuilder:validation:Enum=pre-deploy;post-deploy
	// +kubebuilder:default=post-deploy
	// +optional
	When string `json:"when,omitempty"`

	// Overrides holds time-limited emergency overrides (K-09).
	// When any non-expired override exists (matching Stage or with empty Stage),
	// the gate passes immediately. Expired overrides are kept as audit records.
	// +optional
	Overrides []PolicyGateOverride `json:"overrides,omitempty"`

	// Approval makes the gate wait for people: it is ready only when its
	// expression is true and at least approval.required allowed people have
	// approved the Bundle for the environment (kardinal approve), and none of
	// them rejected it. Copied from the template to every gate instance. For
	// a gate that only waits for approvals, use the expression "true".
	// +optional
	Approval *GateApprovalPolicy `json:"approval,omitempty"`

	// Approvals is written by the promotion Graph on gate instances: the
	// Approvals of the instance's Bundle and environment, copied from the
	// Approval objects, at most 101 (more than 100 blocks the gate). Do not
	// set it; on a template it is ignored.
	// +kubebuilder:validation:MaxItems=101
	// +optional
	Approvals []GateApproval `json:"approvals,omitempty"`

	// Generated is set by kardinal on the PolicyGates it creates: the gate
	// instances a promotion Graph makes from a template, and the freeze gate
	// of a paused Pipeline. Kardinal never uses a generated PolicyGate as a
	// template, so only a generated PolicyGate may have a name longer than 63
	// characters. Do not set it on a gate you write: a generated gate never
	// applies to an environment.
	// +optional
	Generated bool `json:"generated,omitempty"`
}

// GateApprovalPolicy says whose approvals a gate counts and how many it needs.
type GateApprovalPolicy struct {
	// Required is how many distinct allowed people must approve.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=1
	// +optional
	Required int `json:"required,omitempty"`

	// AllowedUsers are Kubernetes usernames whose approvals count.
	// +optional
	AllowedUsers []string `json:"allowedUsers,omitempty"`

	// AllowedGroups are Kubernetes groups: an approval counts when one of the
	// approval's groups is listed. With neither allowedUsers nor
	// allowedGroups, every approval counts; who may approve is then decided
	// by RBAC on approvals.
	// +optional
	AllowedGroups []string `json:"allowedGroups,omitempty"`

	// ExcludeAuthor does not count an approval whose user created the Bundle
	// (no self-approval): the kardinal.io/created-by annotation, which the
	// chart's admission policy pins to the creating user (kardinal create
	// bundle, the Bundle API and the UI set it). A Bundle without it blocks
	// the gate: the rule cannot be enforced.
	// +optional
	ExcludeAuthor bool `json:"excludeAuthor,omitempty"`
}

// GateApproval is an Approval's spec as the Graph copies it into a gate
// instance (the fields of ApprovalSpec).
type GateApproval struct {
	// Bundle is the Bundle the decision is about.
	Bundle string `json:"bundle"`
	// BundleUID is that Bundle's UID.
	BundleUID string `json:"bundleUID"`
	// Environment is the environment the decision is for.
	Environment string `json:"environment"`
	// User is the approver's Kubernetes username.
	User string `json:"user"`
	// Groups are the approver's groups that count for allowedGroups.
	// +optional
	Groups []string `json:"groups"`
	// Decision is approve or reject.
	Decision string `json:"decision"`
	// Comment is the approver's note.
	// +optional
	Comment string `json:"comment"`
}

// GateApprovalStatus records how the gate counted one approval.
type GateApprovalStatus struct {
	// User is the approver.
	User string `json:"user"`
	// Decision is approve or reject.
	Decision string `json:"decision"`
	// Counted reports whether the decision counts for the gate.
	Counted bool `json:"counted"`
	// Reason says why a decision does not count.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Comment is the approver's comment.
	// +optional
	Comment string `json:"comment,omitempty"`
	// FirstSeenAt is when the gate first saw the decision.
	// +optional
	FirstSeenAt *metav1.Time `json:"firstSeenAt,omitempty"`
}

// PolicyGateOverride is a time-limited emergency override record (K-09).
// When any non-expired override exists for a gate, the gate passes immediately
// without evaluating the CEL expression, and status.reason reads
// "OVERRIDDEN by <user>: <reason> (expires <time>)". A PR opened while the
// override is active lists the gate as Pass with that reason. Expired entries
// are kept as an audit record.
type PolicyGateOverride struct {
	// Reason is the mandatory human-readable justification for the override.
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`

	// Stage is the environment name this override applies to.
	// An empty string applies to all environments.
	// +optional
	Stage string `json:"stage,omitempty"`

	// ExpiresAt is when this override stops being effective.
	// After this time the gate evaluates CEL normally.
	// +kubebuilder:validation:Format=date-time
	ExpiresAt metav1.Time `json:"expiresAt"`

	// CreatedAt is when the override was created (set by the CLI).
	// +optional
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`

	// CreatedBy is the Kubernetes username of whoever created the override.
	// The chart's ValidatingAdmissionPolicy admits a new override only when
	// createdBy equals the requesting user (kardinal override reads it with a
	// SelfSubjectReview), or when the controller writes it for the UI.
	// +optional
	CreatedBy string `json:"createdBy,omitempty"`
}

// PolicyGateStatus defines the observed state of a PolicyGate.
type PolicyGateStatus struct {
	// Ready indicates whether the gate is currently allowing promotion.
	// The kro Graph gates downstream nodes on status.ready == true.
	// +kubebuilder:default=false
	Ready bool `json:"ready"`

	// Reason explains the current ready state in human-readable form.
	// +optional
	Reason string `json:"reason,omitempty"`

	// LastEvaluatedAt is when the gate's result was last written. The
	// controller re-evaluates more often, but writes the status only when the
	// result or reason changes, when a PromotionStep that has not started
	// needs a newer result, after a spec change, and otherwise at least every
	// 10 minutes.
	// +optional
	LastEvaluatedAt *metav1.Time `json:"lastEvaluatedAt,omitempty"`

	// Conditions holds status conditions.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// PendingAuditEvents are AuditEvents for this gate's transitions that are
	// not yet written (the audit outbox, #1552). Each entry is stored in the
	// same status patch as its transition and removed once the AuditEvent
	// exists, so an API error or a controller restart between the two cannot
	// lose the record. Normally empty.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	PendingAuditEvents []PendingAuditEvent `json:"pendingAuditEvents,omitempty"`

	// Approvals records each decision in spec.approvals and whether the gate
	// counted it (spec.approval), for kardinal explain, the PR evidence and
	// the UI.
	// +optional
	Approvals []GateApprovalStatus `json:"approvals,omitempty"`

	// Overrides records each spec.overrides entry the controller has seen:
	// when it first saw it, whether its createdBy was checked by the chart's
	// identity admission policy, and whether its GateOverridden AuditEvent
	// is recorded. An override counts from firstSeen: it ends at the earlier
	// of its expiresAt and firstSeen plus the override cap
	// (--gate-override-max-minutes). Records are never dropped, so an entry
	// removed and added again keeps its firstSeen; past 200 records new
	// overrides are not counted (condition OverrideIgnored).
	// +optional
	// +listType=map
	// +listMapKey=key
	// +kubebuilder:validation:MaxItems=200
	Overrides []OverrideRecord `json:"overrides,omitempty"`

	// OverridesVerifiedSince is when the controller first reconciled this
	// gate with override identity checks (the chart's admission policy).
	// Overrides already on the gate then were not checked: their createdBy is
	// shown as unverified.
	// +optional
	OverridesVerifiedSince *metav1.Time `json:"overridesVerifiedSince,omitempty"`
}

// OverrideRecord is the controller's record of one spec.overrides entry.
type OverrideRecord struct {
	// Key identifies the override: a hash of its stage, reason, createdBy,
	// createdAt and expiresAt, so an edited entry is a new override.
	// +kubebuilder:validation:MaxLength=64
	Key string `json:"key"`
	// FirstSeen is when the controller first saw the override. It is the
	// AuditEvent timestamp and the start of the override cap.
	FirstSeen metav1.Time `json:"firstSeen"`
	// Verified is true when createdBy was checked: the chart's identity
	// admission policy was bound when the controller first saw the override,
	// on a gate it was already checking.
	// +optional
	Verified bool `json:"verified,omitempty"`
	// Audited is true once the GateOverridden record is stored: in
	// status.pendingAuditEvents until the AuditEvent is written (#1552).
	// +optional
	Audited bool `json:"audited,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pg
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`,priority=1
// +kubebuilder:printcolumn:name="Last-Evaluated",type=date,JSONPath=`.status.lastEvaluatedAt`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63 || (has(self.spec) && has(self.spec.generated) && self.spec.generated)",message="PolicyGate names are at most 63 characters: the name is copied into the kardinal.io/gate-template label of every gate instance; use a name of at most 63 characters"

// PolicyGate is a CEL-powered policy check represented as a node in the
// promotion Graph. Platform teams define org-level gates; teams add their own.
//
// A gate's name must be at most 63 characters, because the Graph copies it
// into a label of each instance. Only the PolicyGates kardinal creates may be
// longer: gate instances ("<gate>-<namespace>-<env>--<bundle>", see pkg/graph
// gateNodeK8sName) and pause freeze gates ("freeze-<pipeline>"). Kardinal
// sets spec.generated on them and never uses a gate with spec.generated as a
// template. The exemption does not go by name, because a template can have
// any name; a template that sets spec.generated is no longer a template.
type PolicyGate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PolicyGateSpec   `json:"spec,omitempty"`
	Status PolicyGateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PolicyGateList contains a list of PolicyGate.
type PolicyGateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolicyGate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PolicyGate{}, &PolicyGateList{})
}
