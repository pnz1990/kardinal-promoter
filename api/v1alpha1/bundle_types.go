// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BundleSpec defines the desired state of a Bundle.
// Full field definitions are added in Stage 1.
type BundleSpec struct {
	// Type classifies the bundle content.
	// +kubebuilder:validation:Enum=image;config;mixed
	// +kubebuilder:default=image
	Type string `json:"type,omitempty"`

	// Images lists the container images included in this Bundle.
	Images []ImageRef `json:"images,omitempty"`

	// Pipeline is the name of the Pipeline this Bundle targets.
	// +kubebuilder:validation:MinLength=1
	Pipeline string `json:"pipeline"`
}

// ImageRef identifies a container image.
type ImageRef struct {
	// Repository is the image repository (e.g. "ghcr.io/nginx/nginx").
	Repository string `json:"repository"`

	// Tag is the image tag.
	Tag string `json:"tag,omitempty"`

	// Digest is the image digest (sha256:...).
	Digest string `json:"digest,omitempty"`
}

// BundleStatus defines the observed state of a Bundle.
type BundleStatus struct {
	// Phase is the bundle promotion phase.
	// +kubebuilder:validation:Enum=Available;Promoting;Verified;Failed;Superseded
	Phase string `json:"phase,omitempty"`

	// Conditions holds status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=bnd
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Bundle is an immutable, versioned snapshot of what to deploy.
// It carries build provenance and travels through a Pipeline's environments.
type Bundle struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BundleSpec   `json:"spec,omitempty"`
	Status BundleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BundleList contains a list of Bundle.
type BundleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bundle `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Bundle{}, &BundleList{})
}
