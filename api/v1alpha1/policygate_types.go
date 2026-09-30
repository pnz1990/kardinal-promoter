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

	// Selector is a label selector for org-level auto-injection: this gate is
	// automatically applied to any Pipeline whose labels match the selector.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// When (K-02). Every gate on an environment holds that environment's
	// PromotionStep back until the gate is ready: the Graph does not create the
	// step before then, whatever the value of this field.
	// "pre-deploy": the PromotionStep reconciler also re-checks the gate right
	//   before git operations start; if it is not ready, the step stays in
	//   Pending and no git-clone begins.
	// "post-deploy" (default): no additional check. The gate is not evaluated
	//   again after the deployment.
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
	// The kro Graph gates downstream nodes on status.ready == true.
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
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63 || self.metadata.name.contains('--') || self.metadata.name.startsWith('freeze-')",message="PolicyGate names are at most 63 characters: the name is copied into the kardinal.io/gate-template label of every gate instance"

// PolicyGate is a CEL-powered policy check represented as a node in the
// promotion Graph. Platform teams define org-level gates; teams add their own.
//
// A gate's name must be at most 63 characters, because the Graph copies it
// into a label of each instance. The rule exempts the two kinds of PolicyGate
// kardinal names itself: gate instances ("<gate>-<namespace>-<env>--<bundle>",
// see pkg/graph gateNodeK8sName, which can be longer) and pause freeze gates
// ("freeze-<pipeline>"). Neither is ever used as a template.
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
