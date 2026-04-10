// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PipelineSpec defines the desired state of a Pipeline.
type PipelineSpec struct {
	// Git holds the shared GitOps repository configuration for all
	// environments in this pipeline.
	Git PipelineGit `json:"git"`

	// Environments lists the promotion path in order.
	// +kubebuilder:validation:MinItems=1
	Environments []EnvironmentSpec `json:"environments"`

	// PolicyGates lists org- or team-level PolicyGate references that apply to
	// every promotion in this pipeline.
	// +optional
	PolicyGates []PipelinePolicyGateRef `json:"policyGates,omitempty"`

	// Paused suspends all promotions in this pipeline when true.
	// +kubebuilder:default=false
	// +optional
	Paused bool `json:"paused,omitempty"`

	// HistoryLimit is the number of completed Bundle promotions to retain.
	// +optional
	HistoryLimit int `json:"historyLimit,omitempty"`
}

// PipelineGit holds the shared GitOps repository configuration for a Pipeline.
type PipelineGit struct {
	// URL is the GitOps repository URL (e.g. https://github.com/myorg/gitops.git).
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Branch is the default base branch for this repository.
	// +optional
	Branch string `json:"branch,omitempty"`

	// Layout controls how environment paths are organized in the repository.
	// +kubebuilder:validation:Enum=directory;branch
	// +kubebuilder:default=directory
	// +optional
	Layout string `json:"layout,omitempty"`

	// Provider is the SCM provider.
	// +kubebuilder:validation:Enum=github;gitlab
	// +kubebuilder:default=github
	// +optional
	Provider string `json:"provider,omitempty"`

	// SecretRef references a Kubernetes Secret containing the SCM token.
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// SecretRef is a reference to a Kubernetes Secret by name and optional namespace.
type SecretRef struct {
	// Name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is the Secret namespace. If empty, the Pipeline's namespace is used.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// EnvironmentSpec defines one environment in a Pipeline.
type EnvironmentSpec struct {
	// Name is the environment identifier (e.g. "test", "uat", "prod").
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Path is the subdirectory within the GitOps repository for this environment.
	// Used when spec.git.layout is "directory".
	// +optional
	Path string `json:"path,omitempty"`

	// ApprovalMode controls whether promotion requires a PR review.
	// +kubebuilder:validation:Enum=auto;pr-review
	// +kubebuilder:default=auto
	// +optional
	ApprovalMode string `json:"approvalMode,omitempty"`

	// UpdateStrategy selects the manifest update strategy.
	// +kubebuilder:validation:Enum=kustomize;helm
	// +kubebuilder:default=kustomize
	// +optional
	UpdateStrategy string `json:"updateStrategy,omitempty"`

	// HealthAdapter selects the health check backend for this environment.
	// Supported values: deployment, argocd, flux, argoRollouts.
	// +optional
	HealthAdapter string `json:"healthAdapter,omitempty"`

	// HealthTimeout is the maximum time to wait for health checks to pass.
	// Uses Go duration format (e.g. "30m", "1h").
	// +kubebuilder:default="30m"
	// +optional
	HealthTimeout string `json:"healthTimeout,omitempty"`

	// DeliveryDelegate offloads in-cluster progressive delivery to an external
	// controller. Supported values: argoRollouts, flagger.
	// +optional
	DeliveryDelegate string `json:"deliveryDelegate,omitempty"`

	// DependsOn lists names of other environments in this pipeline that must
	// reach Verified state before this environment can start.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Shard pins this environment to a specific kardinal-controller agent shard
	// in distributed mode. Leave empty for single-controller deployments.
	// +optional
	Shard string `json:"shard,omitempty"`
}

// PipelinePolicyGateRef is a reference to a PolicyGate that must pass before
// any promotion in this pipeline can proceed.
type PipelinePolicyGateRef struct {
	// Name is the PolicyGate resource name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is the PolicyGate resource namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// PipelineStatus defines the observed state of a Pipeline.
type PipelineStatus struct {
	// Phase is the overall pipeline phase.
	// +kubebuilder:validation:Enum=Ready;Degraded;Unknown
	// +kubebuilder:default=Unknown
	Phase string `json:"phase,omitempty"`

	// Conditions holds status conditions.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pipe
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Paused",type=boolean,JSONPath=`.spec.paused`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Pipeline defines a promotion pipeline for one application.
// It specifies the ordered environments an artifact Bundle travels through.
type Pipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PipelineSpec   `json:"spec,omitempty"`
	Status PipelineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PipelineList contains a list of Pipeline.
type PipelineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Pipeline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Pipeline{}, &PipelineList{})
}
