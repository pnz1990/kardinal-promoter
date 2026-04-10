// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PromotionStepSpec defines the desired state of a PromotionStep.
// PromotionStep objects are created by the Graph controller — not by users.
type PromotionStepSpec struct {
	// PipelineName is the Pipeline this step belongs to.
	PipelineName string `json:"pipelineName,omitempty"`

	// BundleName is the Bundle being promoted.
	BundleName string `json:"bundleName,omitempty"`

	// Environment is the environment this step promotes into.
	Environment string `json:"environment,omitempty"`

	// StepType identifies the built-in or custom step to execute.
	StepType string `json:"stepType,omitempty"`
}

// PromotionStepStatus defines the observed state of a PromotionStep.
type PromotionStepStatus struct {
	// Phase is the step execution phase.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Blocked
	Phase string `json:"phase,omitempty"`

	// Message provides human-readable detail about the current phase.
	Message string `json:"message,omitempty"`

	// Conditions holds status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ps
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromotionStep is a controller-internal CRD representing one step in a
// promotion sequence. Created by the Graph controller; reconciled by the
// PromotionStep reconciler.
type PromotionStep struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromotionStepSpec   `json:"spec,omitempty"`
	Status PromotionStepStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PromotionStepList contains a list of PromotionStep.
type PromotionStepList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionStep `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PromotionStep{}, &PromotionStepList{})
}
