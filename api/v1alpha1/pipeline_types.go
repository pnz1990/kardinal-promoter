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
	// When unset or zero, defaults to 50. Terminal Bundles (Verified, Failed, Superseded)
	// beyond this limit are deleted oldest-first on each new Bundle creation.
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=1
	// +optional
	HistoryLimit int `json:"historyLimit,omitempty"`

	// PolicyNamespaces lists additional namespaces to scan for org-level PolicyGates.
	// The pipeline's own namespace is always included. When unset, the controller
	// defaults to "platform-policies". Setting this field makes the namespace list
	// explicit in the Pipeline spec rather than hardcoded in the controller.
	// Eliminates TR-2 from docs/design/11-graph-purity-tech-debt.md.
	// +optional
	PolicyNamespaces []string `json:"policyNamespaces,omitempty"`

	// MaxConcurrentPromotions caps the number of Bundles in Promoting phase for this
	// pipeline at any given time. When 0 or unset (default), there is no cap and all
	// Available Bundles are promoted concurrently. When set to a positive value, Bundles
	// that exceed the cap are requeued until a promotion slot becomes available.
	// This prevents promotion storms (e.g. a CI burst creating 50 Bundles simultaneously)
	// from saturating git hosts, exhausting GitHub API rate limits, or creating merge
	// conflicts in the GitOps repository.
	//
	// Example: maxConcurrentPromotions: 2 allows at most 2 active promotions at once.
	// Additional Available Bundles wait in a 30-second polling loop.
	//
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxConcurrentPromotions int `json:"maxConcurrentPromotions,omitempty"`
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
	// When both Steps and PromotionTemplate are set, Steps takes precedence
	// (local override wins).
	// +optional
	Steps []StepSpec `json:"steps,omitempty"`

	// PromotionTemplate references a PromotionTemplate CR whose step sequence
	// should be used for this environment. The translator inlines the template's
	// steps at graph-build time; no runtime dependency remains after Graph creation.
	// When Steps is also set, Steps takes precedence.
	// +optional
	PromotionTemplate *PromotionTemplateRef `json:"promotionTemplate,omitempty"`

	// WaitForMergeTimeout is the maximum duration a PromotionStep will wait
	// in the WaitingForMerge state before transitioning to Failed. When not set
	// or zero, the step waits indefinitely (no timeout). Accepts Go duration
	// strings: "24h", "72h", "168h", etc.
	// +optional
	WaitForMergeTimeout string `json:"waitForMergeTimeout,omitempty"`

	// Regions enables multi-region fan-out for this environment (issue #612).
	// When two or more region names are listed, the translator emits a single
	// forEach Graph node that stamps out one PromotionStep per region. Each
	// stamped PromotionStep receives spec.region = the region name, which the
	// reconciler uses when constructing Git paths and PR labels.
	// All regions must be Verified before downstream environments proceed.
	// When empty or only one region is listed, the environment uses the default
	// single-node behaviour (no forEach).
	// +optional
	Regions []string `json:"regions,omitempty"`
}

// PromotionTemplateRef is a reference to a PromotionTemplate CR.
type PromotionTemplateRef struct {
	// Name is the PromotionTemplate resource name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is the namespace of the PromotionTemplate.
	// If empty, the Pipeline's own namespace is used.
	// +optional
	Namespace string `json:"namespace,omitempty"`
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
	// +kubebuilder:validation:Enum=kustomize;helm;argocd
	// +kubebuilder:default=kustomize
	// +optional
	Strategy string `json:"strategy,omitempty"`

	// Helm holds Helm-specific update configuration.
	// Used when Strategy is "helm".
	// +optional
	Helm *HelmUpdateConfig `json:"helm,omitempty"`

	// ArgoCD holds ArgoCD-native update configuration.
	// Used when Strategy is "argocd". Patches the ArgoCD Application's
	// spec.source.helm.valuesObject directly without a git commit.
	// +optional
	ArgoCD *ArgoCDUpdateConfig `json:"argocd,omitempty"`
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

// ArgoCDUpdateConfig holds ArgoCD-native update strategy configuration.
// Used when UpdateConfig.Strategy is "argocd".
// The argocd-set-image step patches the ArgoCD Application's
// spec.source.helm.valuesObject in-place, unlocking teams that store
// application config inside an ArgoCD Application rather than a GitOps repo.
type ArgoCDUpdateConfig struct {
	// Application is the name of the ArgoCD Application resource to patch.
	// +kubebuilder:validation:MinLength=1
	Application string `json:"application"`

	// Namespace is the Kubernetes namespace where the ArgoCD Application lives.
	// Defaults to "argocd" if empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// ImageKey is the dot-separated key path within spec.source.helm.valuesObject
	// where the image tag should be written.
	// Example: "image.tag" writes to spec.source.helm.valuesObject.image.tag.
	// Defaults to "image.tag" if empty.
	// +optional
	ImageKey string `json:"imageKey,omitempty"`
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

	// LabelSelector enables WatchKind mode for health.type=resource.
	// When set, the health node watches ALL Deployments in the environment namespace
	// that match the given labels (krocodile WatchKind — O(1) incremental cache).
	// When unset, a single named Deployment is watched (krocodile Watch — existing behavior).
	//
	// Example: {"app": "my-service", "kardinal.io/pipeline": "nginx-demo"}
	//
	// Only applies to health.type=resource. Ignored for argocd, flux, argoRollouts, flagger
	// (those resource types are always single-named).
	// +optional
	LabelSelector map[string]string `json:"labelSelector,omitempty"`

	// Resource specifies the exact Kubernetes resource to watch for health.type=resource.
	// When set, overrides the default behavior (which watches a Deployment named after
	// the pipeline in the environment namespace). Use this when the health target is
	// in a different namespace or has a different name than the pipeline.
	//
	// Only applies to health.type=resource. Ignored for argocd, flux, argoRollouts, flagger.
	// +optional
	Resource *ResourceRef `json:"resource,omitempty"`
}

// ResourceRef identifies a Kubernetes resource by kind, name, and namespace.
type ResourceRef struct {
	// Kind is the Kubernetes resource kind (e.g. "Deployment", "StatefulSet").
	// Defaults to "Deployment" when unset.
	// +optional
	Kind string `json:"kind,omitempty"`

	// Name is the resource name. Defaults to the pipeline name when unset.
	// +optional
	Name string `json:"name,omitempty"`

	// Namespace is the resource namespace. Defaults to the environment name when unset.
	// +optional
	Namespace string `json:"namespace,omitempty"`
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

	// DeploymentMetrics holds aggregate DORA-style metrics computed from the
	// last 30 Verified Bundles for this Pipeline. Written by PipelineReconciler.
	// +optional
	DeploymentMetrics *PipelineDeploymentMetrics `json:"deploymentMetrics,omitempty"`
}

// PipelineDeploymentMetrics holds aggregate promotion efficiency metrics for a Pipeline.
// Computed by PipelineReconciler from the last 30 Verified Bundles for this pipeline.
// Displayed by `kardinal metrics` and the UI pipeline detail view.
type PipelineDeploymentMetrics struct {
	// RolloutsLast30Days is the number of successful (Verified) promotions to
	// the final pipeline environment in the last 30 calendar days.
	// +optional
	RolloutsLast30Days int `json:"rolloutsLast30Days,omitempty"`

	// P50CommitToProdMinutes is the median time (minutes) from Bundle creation
	// to the final environment reaching Verified, over the sample window.
	// +optional
	P50CommitToProdMinutes int64 `json:"p50CommitToProdMinutes,omitempty"`

	// P90CommitToProdMinutes is the 90th-percentile time (minutes) from Bundle
	// creation to the final environment reaching Verified, over the sample window.
	// +optional
	P90CommitToProdMinutes int64 `json:"p90CommitToProdMinutes,omitempty"`

	// AutoRollbackRateMillis is the fraction of sampled Bundles that triggered an
	// automatic rollback, expressed as integer thousandths (e.g. 83 = 8.3%).
	// Stored as integer to avoid floating-point in CRD YAML.
	// +optional
	AutoRollbackRateMillis int `json:"autoRollbackRateMillis,omitempty"`

	// OperatorInterventionRateMillis is the fraction of sampled Bundles that had
	// at least one PolicyGate override applied, expressed as integer thousandths.
	// +optional
	OperatorInterventionRateMillis int `json:"operatorInterventionRateMillis,omitempty"`

	// StaleProdDays is the number of days since the last successful promotion to
	// the final pipeline environment. 0 means a promotion completed today.
	// -1 means no promotion has ever completed.
	// +optional
	StaleProdDays int `json:"staleProdDays,omitempty"`

	// SampleSize is the number of Bundles included in this computation.
	// +optional
	SampleSize int `json:"sampleSize,omitempty"`

	// ComputedAt is when these metrics were last written by the PipelineReconciler.
	// +optional
	ComputedAt *metav1.Time `json:"computedAt,omitempty"`
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
