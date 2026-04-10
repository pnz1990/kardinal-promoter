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
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`

	// BundleName is the Bundle being promoted.
	// +kubebuilder:validation:MinLength=1
	BundleName string `json:"bundleName"`

	// Environment is the environment this step promotes into.
	// +kubebuilder:validation:MinLength=1
	Environment string `json:"environment"`

	// StepType identifies the built-in or custom step to execute.
	// Examples: git-clone, kustomize-set-image, git-commit, open-pr,
	//           wait-for-merge, health-check.
	// +kubebuilder:validation:MinLength=1
	StepType string `json:"stepType"`

	// Inputs carries step-specific configuration values derived from the
	// Pipeline and Bundle at graph generation time.
	// +optional
	Inputs map[string]string `json:"inputs,omitempty"`
}

// PromotionStepStatus defines the observed state of a PromotionStep.
type PromotionStepStatus struct {
	// Phase is the step execution phase.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Blocked
	Phase string `json:"phase,omitempty"`

	// Message provides human-readable detail about the current phase.
	// +optional
	Message string `json:"message,omitempty"`

	// Outputs accumulates key/value results from completed steps in the
	// sequence (e.g. prURL from the open-pr step).
	// +optional
	Outputs map[string]string `json:"outputs,omitempty"`

	// Conditions holds status conditions.
	// +optional
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
