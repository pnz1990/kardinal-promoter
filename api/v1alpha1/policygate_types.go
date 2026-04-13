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

	// UpstreamEnvironment is set by the kro Graph controller via CEL expression
	// substitution. It carries the resolved state of the upstream PromotionStep
	// that must be Verified before this gate is evaluated.
	// +optional
	UpstreamEnvironment string `json:"upstreamEnvironment,omitempty"`
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
