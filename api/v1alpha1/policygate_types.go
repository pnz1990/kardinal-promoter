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
	// Uses Go duration format (e.g. "5m", "1h").
	// +kubebuilder:default="5m"
	// +optional
	RecheckInterval string `json:"recheckInterval,omitempty"`

	// SkipPermission controls whether bundles with intent.skipEnvironments can
	// bypass this gate. When false, skip requests are denied.
	// +kubebuilder:default=false
	// +optional
	SkipPermission bool `json:"skipPermission,omitempty"`

	// Selector is a label selector for org-level auto-injection: this gate is
	// automatically applied to any Pipeline whose labels match the selector.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// When controls when this gate is evaluated in the promotion lifecycle (K-02).
	// "pre-deploy" (default: post-deploy): evaluated before git operations start.
	//   If not ready, the PromotionStep stays in Pending and no git-clone begins.
	// "post-deploy": evaluated after deployment (during bake/health check phase).
	//   This is the default behavior for all existing gates.
	// +kubebuilder:validation:Enum=pre-deploy;post-deploy
	// +kubebuilder:default=post-deploy
	// +optional
	When string `json:"when,omitempty"`

	// Overrides holds time-limited emergency overrides (K-09).
	// When any non-expired override exists (matching Stage or with empty Stage),
	// the gate passes immediately. Expired overrides are kept as audit records.
	// +optional
	Overrides []PolicyGateOverride `json:"overrides,omitempty"`
}

// PolicyGateOverride is a time-limited emergency override record (K-09).
// When any non-expired override exists for a gate, the gate passes immediately
// without evaluating the CEL expression. The override is visible in the PR
// evidence body with an "OVERRIDDEN" badge.
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

	// CreatedBy is the user who created the override (informational).
	// +optional
	CreatedBy string `json:"createdBy,omitempty"`
}

// PolicyGateStatus defines the observed state of a PolicyGate.
type PolicyGateStatus struct {
	// Ready indicates whether the gate is currently allowing promotion.
	// The Graph propagateWhen expression evaluates status.ready == true.
	// +kubebuilder:default=false
	Ready bool `json:"ready"`

	// Reason explains the current ready state in human-readable form.
	// +optional
	Reason string `json:"reason,omitempty"`

	// LastEvaluatedAt is when the gate was last evaluated.
	// +optional
	LastEvaluatedAt *metav1.Time `json:"lastEvaluatedAt,omitempty"`

	// Conditions holds status conditions.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pg
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`,priority=1
// +kubebuilder:printcolumn:name="Last-Evaluated",type=date,JSONPath=`.status.lastEvaluatedAt`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PolicyGate is a CEL-powered policy check represented as a node in the
// promotion Graph. Platform teams define org-level gates; teams add their own.
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
