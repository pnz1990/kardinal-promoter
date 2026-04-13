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

	// Environments lists the promotion path.
	// Sequential ordering (GB-1): when an environment does not specify dependsOn,
	// it implicitly depends on the previous entry in this list. The first environment
	// has no upstream dependency. This sequential default means a list of N environments
	// without dependsOn fields produces a linear chain. Override with dependsOn to
	// express parallel fan-out or explicit DAG structure.
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

	// PolicyNamespaces lists additional namespaces to scan for org-level PolicyGates.
	// The pipeline's own namespace is always included. When unset, the controller
	// defaults to "platform-policies". Setting this field makes the namespace list
	// explicit in the Pipeline spec rather than hardcoded in the controller.
	// Eliminates TR-2 from docs/design/11-graph-purity-tech-debt.md.
	// +optional
	PolicyNamespaces []string `json:"policyNamespaces,omitempty"`
}

// PipelineGit holds the shared GitOps repository configuration for a Pipeline.
type PipelineGit struct {
	// URL is the GitOps repository URL (HTTPS).
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Branch is the default base branch for this repository.
	// +optional
	Branch string `json:"branch,omitempty"`

	// Layout controls how environment paths are organized in the repository.
	// "directory": environments as subdirectories on one branch (default).
	// "branch": environments as separate branches.
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
	// Used when spec.git.layout is "directory". Defaults to "environments/<name>".
	// +optional
	Path string `json:"path,omitempty"`

	// Approval controls whether promotion into this environment requires a PR review.
	// +kubebuilder:validation:Enum=auto;pr-review
	// +kubebuilder:default=auto
	// +optional
	Approval string `json:"approval,omitempty"`

	// Update holds the manifest update configuration for this environment.
	// +optional
	Update UpdateConfig `json:"update,omitempty"`

	// Health holds the health check configuration for this environment.
	// +optional
	Health HealthConfig `json:"health,omitempty"`

	// Delivery holds in-cluster progressive delivery delegation configuration.
	// +optional
	Delivery DeliveryConfig `json:"delivery,omitempty"`

	// DependsOn lists names of other environments in this pipeline that must
	// reach Verified state before this environment can start.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Wave assigns this environment to a numbered deployment wave (K-06).
	// Environments with the same wave number are promoted in parallel.
	// Wave N environments automatically depend on ALL wave (N-1) environments —
	// the translator generates the edges so users need not write explicit dependsOn.
	// Wave and explicit DependsOn are composable: the final dependency set is the
	// union of wave-derived edges and any explicit DependsOn entries.
	// Wave values must be >= 1. Environments without a Wave use the default
	// sequential ordering (each depends on the previous in the list).
	// +kubebuilder:validation:Minimum=1
	// +optional
	Wave int `json:"wave,omitempty"`

	// Shard pins this environment to a specific kardinal-controller agent shard
	// in distributed mode. Leave empty for single-controller deployments.
	// +optional
	Shard string `json:"shard,omitempty"`

	// AutoRollback configures automatic rollback when health checks fail repeatedly.
	// When not set, automatic rollback is disabled.
	// +optional
	AutoRollback *AutoRollbackSpec `json:"autoRollback,omitempty"`

	// Bake configures a contiguous-healthy soak window for this environment (K-01).
	// When set, the health check must pass continuously for Bake.Minutes before
	// the step transitions to Verified. A health failure resets the timer if
	// policy is "reset-on-alarm" (default), or fails the step if "fail-on-alarm".
	// +optional
	Bake *BakeConfig `json:"bake,omitempty"`

	// OnHealthFailure controls what the reconciler does when health fails during
	// bake or health checking (K-03).
	// "rollback": create a rollback Bundle at the previous version; step → RollingBack.
	// "abort": freeze the step; state → AbortedByAlarm; requires human intervention.
	// "none" (default): step → Failed; downstream stops.
	// +kubebuilder:validation:Enum=rollback;abort;none
	// +kubebuilder:default=none
	// +optional
	OnHealthFailure string `json:"onHealthFailure,omitempty"`

	// Layout configures how the promotion interacts with the Git repo layout.
	// "directory" (default): env manifests are in a subdirectory of the main branch.
	// "branch": rendered manifests are committed to a separate env-specific branch.
	//   In this mode the step sequence includes kustomize-build to render templates
	//   before committing to the target branch.
	// +kubebuilder:validation:Enum=directory;branch
	// +kubebuilder:default=directory
	// +optional
	Layout string `json:"layout,omitempty"`

	// Steps overrides the default step sequence for this environment.
	// If empty, the default sequence is used (see DefaultSequenceForBundle).
	// Steps can include custom webhook steps alongside built-in step names.
	// +optional
	Steps []StepSpec `json:"steps,omitempty"`
}

// AutoRollbackSpec defines the automatic rollback policy for an environment.
type AutoRollbackSpec struct {
	// FailureThreshold is the number of consecutive health-check failures
	// that trigger an automatic rollback Bundle creation. Default: 3.
	// +kubebuilder:default=3
	// +optional
	FailureThreshold int `json:"failureThreshold,omitempty"`
}

// BakeConfig defines a contiguous-healthy soak window for an environment (K-01).
// The health check must pass continuously for Minutes before the step is Verified.
type BakeConfig struct {
	// Minutes is the required contiguous healthy duration in minutes.
	// The timer resets on health failure when Policy is "reset-on-alarm".
	// +kubebuilder:validation:Minimum=1
	Minutes int `json:"minutes"`

	// Policy controls behavior when health fails during the bake window.
	// "reset-on-alarm" (default): timer resets to 0, step stays in HealthChecking.
	// "fail-on-alarm": step transitions to Failed immediately on first health failure.
	// +kubebuilder:validation:Enum=reset-on-alarm;fail-on-alarm
	// +kubebuilder:default=reset-on-alarm
	// +optional
	Policy string `json:"policy,omitempty"`
}

// WebhookConfig defines the HTTP webhook endpoint for a custom promotion step.
type WebhookConfig struct {
	// URL is the HTTP(S) endpoint to POST to.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// TimeoutSeconds is the per-call timeout. Defaults to 300.
	// +kubebuilder:default=300
	// +optional
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	// SecretRef references a Kubernetes Secret whose "Authorization" key is
	// sent as the Authorization header.
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// StepSpec declares a custom or built-in step override in a Pipeline environment.
// When Uses matches a built-in step name the built-in takes precedence.
// When Uses is an unknown name the Webhook config is required.
type StepSpec struct {
	// Uses identifies the step to execute (built-in name or custom name).
	// +kubebuilder:validation:MinLength=1
	Uses string `json:"uses"`

	// Webhook configures the HTTP endpoint for custom (non-built-in) steps.
	// Required when Uses does not match any registered built-in step.
	// +optional
	Webhook *WebhookConfig `json:"webhook,omitempty"`
}

// UpdateConfig holds manifest update strategy configuration.
type UpdateConfig struct {
	// Strategy selects the manifest update strategy.
	// +kubebuilder:validation:Enum=kustomize;helm
	// +kubebuilder:default=kustomize
	// +optional
	Strategy string `json:"strategy,omitempty"`

	// Helm holds Helm-specific update configuration.
	// Used when Strategy is "helm".
	// +optional
	Helm *HelmUpdateConfig `json:"helm,omitempty"`
}

// HelmUpdateConfig holds Helm-specific update strategy configuration.
type HelmUpdateConfig struct {
	// ImagePathTemplate is the YAML dot-path to the image tag in values.yaml.
	// Example: ".image.tag" updates the `image.tag` key.
	// If empty, defaults to ".image.tag".
	// +optional
	ImagePathTemplate string `json:"imagePathTemplate,omitempty"`

	// ValuesFile is the name of the values file to update (relative to the
	// environment path). Defaults to "values.yaml".
	// +optional
	ValuesFile string `json:"valuesFile,omitempty"`
}

// HealthConfig holds health check configuration for an environment.
type HealthConfig struct {
	// Type selects the health check backend.
	// Supported values: resource, argocd, flux, argoRollouts, flagger.
	// +kubebuilder:validation:Enum=resource;argocd;flux;argoRollouts;flagger
	// +optional
	Type string `json:"type,omitempty"`

	// Timeout is the maximum time to wait for health checks to pass.
	// Uses Go duration format (e.g. "30m", "1h"). Defaults to "10m".
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// Cluster is the kubeconfig Secret name for remote cluster health checks.
	// +optional
	Cluster string `json:"cluster,omitempty"`
}

// DeliveryConfig holds in-cluster progressive delivery delegation configuration.
type DeliveryConfig struct {
	// Delegate offloads in-cluster progressive delivery to an external controller.
	// Supported values: none, argoRollouts, flagger.
	// +kubebuilder:validation:Enum=none;argoRollouts;flagger
	// +optional
	Delegate string `json:"delegate,omitempty"`
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
