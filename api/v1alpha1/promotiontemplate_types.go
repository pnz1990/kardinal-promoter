// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PromotionTemplateSpec defines the desired state of a PromotionTemplate.
type PromotionTemplateSpec struct {
	// Steps is the named step sequence for this template.
	// Environments that reference this template inherit these steps unless
	// they specify their own steps (local spec.environments[].steps takes precedence).
	// When empty, environments referencing this template use the default step sequence.
	// +optional
	Steps []StepSpec `json:"steps,omitempty"`

	// Description is a human-readable explanation of what this template represents.
	// +optional
	Description string `json:"description,omitempty"`
}

// PromotionTemplateStatus defines the observed state of a PromotionTemplate.
type PromotionTemplateStatus struct {
	// Conditions reports status conditions for this template.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pt,scope=Namespaced
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// PromotionTemplate is a reusable named step sequence that Pipeline environments
// can reference via spec.environments[].promotionTemplate.
// The translator inlines the template's steps at graph-build time, so there is
// no runtime dependency on the PromotionTemplate after Graph creation.
//
// Example usage:
//
//	apiVersion: kardinal.io/v1alpha1
//	kind: PromotionTemplate
//	metadata:
//	  name: standard-with-webhook
//	spec:
//	  description: "Standard promotion with post-deploy webhook notification"
//	  steps:
//	    - uses: git-clone
//	    - uses: kustomize-set-image
//	    - uses: git-commit
//	    - uses: open-pr
//	    - uses: wait-for-merge
//	    - uses: notify-slack
//	      webhook:
//	        url: https://hooks.slack.com/...
//	    - uses: health-check
type PromotionTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromotionTemplateSpec   `json:"spec,omitempty"`
	Status PromotionTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PromotionTemplateList contains a list of PromotionTemplate.
type PromotionTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PromotionTemplate{}, &PromotionTemplateList{})
}
