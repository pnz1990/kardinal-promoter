// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RollbackPolicySpec defines the desired state of a RollbackPolicy.
// Nothing creates RollbackPolicy objects automatically: the controller only
// reconciles the ones that exist. Automatic rollback on health failure is
// configured with Pipeline spec.environments[].onHealthFailure: rollback.
type RollbackPolicySpec struct {
	// PipelineName is the Pipeline this policy monitors.
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`

	// Environment is the environment this policy monitors.
	// +kubebuilder:validation:MinLength=1
	Environment string `json:"environment"`

	// BundleRef is the name of the Bundle being monitored. Only the PromotionSteps
	// of this Bundle in Environment are read: the steps labelled
	// kardinal.io/pipeline and kardinal.io/environment whose spec.bundleName
	// (or, when that is empty, kardinal.io/bundle label) is BundleRef. When the highest
	// ConsecutiveHealthFailures among them (one step per region) reaches
	// FailureThreshold, a rollback Bundle is created.
	// +kubebuilder:validation:MinLength=1
	BundleRef string `json:"bundleRef"`

	// FailureThreshold is the number of consecutive health-check failures
	// required to trigger a rollback. Defaults to 3; the controller also
	// treats a value <= 0 as 3.
	// +kubebuilder:default=3
	// +optional
	FailureThreshold int `json:"failureThreshold,omitempty"`
}

// RollbackPolicyStatus holds the observed state of the rollback policy.
// Written exclusively by the RollbackPolicyReconciler.
type RollbackPolicyStatus struct {
	// ShouldRollback is true while the failure threshold is reached, and
	// stays true once a rollback Bundle has been created. It is false again
	// when the failures drop below the threshold before a rollback Bundle
	// exists (for example after RollbackRefused).
	// The Graph can read this field via a Watch node expression.
	// +optional
	ShouldRollback bool `json:"shouldRollback,omitempty"`

	// ConsecutiveFailures is the most recent consecutive health failure count
	// observed from the associated PromotionStep.
	// +optional
	ConsecutiveFailures int `json:"consecutiveFailures,omitempty"`

	// RollbackBundleName is the name of the rollback Bundle created when
	// ShouldRollback became true. Nil if no rollback has been triggered.
	// +optional
	RollbackBundleName *string `json:"rollbackBundleName,omitempty"`

	// LastEvaluatedAt is the timestamp of the most recent reconcile evaluation.
	// +optional
	LastEvaluatedAt *metav1.Time `json:"lastEvaluatedAt,omitempty"`

	// Conditions holds status conditions. RollbackRefused is True when the
	// failure threshold was reached but no rollback Bundle was created because
	// the rollback planner refused (the message says why, for example when no
	// earlier Bundle was Verified in the environment). It is False once a
	// rollback Bundle is created. Roll back by hand with kardinal rollback.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=rbp
// +kubebuilder:printcolumn:name="ShouldRollback",type=boolean,JSONPath=`.status.shouldRollback`
// +kubebuilder:printcolumn:name="Failures",type=integer,JSONPath=`.status.consecutiveFailures`
// +kubebuilder:printcolumn:name="Threshold",type=integer,JSONPath=`.spec.failureThreshold`
// +kubebuilder:printcolumn:name="Refused",type=string,JSONPath=`.status.conditions[?(@.type=="RollbackRefused")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RollbackPolicy monitors consecutive health-check failures on a PromotionStep
// and triggers an auto-rollback by creating a rollback Bundle when the
// failure threshold is exceeded.
//
// Architecture: the RollbackPolicyReconciler reads
// PromotionStep.status.consecutiveHealthFailures and writes
// status.shouldRollback (own CRD status). This makes the rollback
// decision observable by the Graph, eliminating PS-6 and PS-7 from
// docs/design/11-graph-purity-tech-debt.md.
type RollbackPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RollbackPolicySpec   `json:"spec,omitempty"`
	Status RollbackPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RollbackPolicyList contains a list of RollbackPolicy objects.
type RollbackPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RollbackPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RollbackPolicy{}, &RollbackPolicyList{})
}
