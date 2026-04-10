// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PolicyGateSpec defines the desired state of a PolicyGate.
// Full field definitions are added in Stage 1.
type PolicyGateSpec struct {
	// Expression is the CEL expression evaluated to determine if promotion
	// is allowed. Must evaluate to a boolean.
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`

	// Message is a human-readable explanation shown when the gate blocks.
	Message string `json:"message,omitempty"`

	// RecheckInterval is how often to re-evaluate time-based gates.
	// +kubebuilder:default="5m"
	RecheckInterval string `json:"recheckInterval,omitempty"`

	// SkipPermission controls whether bundles with intent.skip can bypass this gate.
	SkipPermission bool `json:"skipPermission,omitempty"`
}

// PolicyGateStatus defines the observed state of a PolicyGate.
type PolicyGateStatus struct {
	// Ready indicates whether the gate is currently allowing promotion.
	// The Graph propagateWhen expression evaluates status.ready == true.
	// +kubebuilder:default=false
	Ready bool `json:"ready"`

	// Reason explains the current ready state in human-readable form.
	Reason string `json:"reason,omitempty"`

	// LastEvaluatedAt is when the gate was last evaluated.
	LastEvaluatedAt *metav1.Time `json:"lastEvaluatedAt,omitempty"`

	// Conditions holds status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pg
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
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
