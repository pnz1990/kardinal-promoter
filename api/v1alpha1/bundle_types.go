// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BundleSpec defines the desired state of a Bundle.
type BundleSpec struct {
	// Type classifies the bundle content.
	// Supersession rule (BU-4): each bundle type supersedes only bundles of the same type.
	// An image bundle does NOT supersede a config bundle and vice versa.
	// This allows image and config promotions to coexist independently in the same pipeline.
	// +kubebuilder:validation:Enum=image;config;mixed
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Pipeline is the name of the Pipeline this Bundle targets.
	// +kubebuilder:validation:MinLength=1
	Pipeline string `json:"pipeline"`

	// Images lists the container images included in this Bundle.
	// +optional
	Images []ImageRef `json:"images,omitempty"`

	// ConfigRef points to the GitOps repository commit this Bundle represents
	// when the bundle type is "config" or "mixed".
	// +optional
	ConfigRef *ConfigRef `json:"configRef,omitempty"`

	// Provenance carries build metadata for audit and rollback.
	// +optional
	Provenance *BundleProvenance `json:"provenance,omitempty"`

	// Intent declares optional targeting and skip overrides for this Bundle.
	// +optional
	Intent *BundleIntent `json:"intent,omitempty"`
}

// ImageRef identifies a container image by repository, tag, and/or digest.
type ImageRef struct {
	// Repository is the image repository (e.g. "ghcr.io/nginx/nginx").
	// +kubebuilder:validation:MinLength=1
	Repository string `json:"repository"`

	// Tag is the image tag.
	// +optional
	Tag string `json:"tag,omitempty"`

	// Digest is the image digest (sha256:...).
	// +optional
	Digest string `json:"digest,omitempty"`
}

// ConfigRef identifies a GitOps repository commit.
type ConfigRef struct {
	// GitRepo is the GitOps repository URL.
	// +optional
	GitRepo string `json:"gitRepo,omitempty"`

	// CommitSHA is the exact commit SHA for this config snapshot.
	// +optional
	CommitSHA string `json:"commitSHA,omitempty"`
}

// BundleProvenance carries build origin metadata.
type BundleProvenance struct {
	// CommitSHA is the application source commit that produced this Bundle.
	// +optional
	CommitSHA string `json:"commitSHA,omitempty"`

	// CIRunURL is the URL of the CI run that built this Bundle.
	// +optional
	CIRunURL string `json:"ciRunURL,omitempty"`

	// Author is the committer or triggering actor for this build.
	// +optional
	Author string `json:"author,omitempty"`

	// Timestamp is when the bundle was built.
	// +optional
	Timestamp metav1.Time `json:"timestamp,omitempty"`

	// RollbackOf is the name of the Bundle this Bundle rolls back (if any).
	// +optional
	RollbackOf string `json:"rollbackOf,omitempty"`
}

// BundleIntent declares optional promotion targeting and skip overrides.
type BundleIntent struct {
	// TargetEnvironment restricts this Bundle to promoting only up to and
	// including this environment. Empty means promote through all environments.
	// +optional
	TargetEnvironment string `json:"targetEnvironment,omitempty"`

	// SkipEnvironments lists environment names to exclude from this promotion,
	// subject to the PolicyGate SkipPermission check.
	// +optional
	SkipEnvironments []string `json:"skipEnvironments,omitempty"`
}

// BundleStatus defines the observed state of a Bundle.
type BundleStatus struct {
	// Phase is the bundle promotion phase.
	// +kubebuilder:validation:Enum=Available;Promoting;Verified;Failed;Superseded
	Phase string `json:"phase,omitempty"`

	// Conditions holds status conditions.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Environments holds per-environment promotion evidence.
	// +optional
	Environments []EnvironmentStatus `json:"environments,omitempty"`
}

// EnvironmentStatus captures per-environment promotion evidence for a Bundle.
type EnvironmentStatus struct {
	// Name is the environment name.
	Name string `json:"name"`

	// Phase is the promotion phase for this environment.
	Phase string `json:"phase,omitempty"`

	// PRURL is the URL of the pull request opened for this promotion.
	// +optional
	PRURL string `json:"prURL,omitempty"`

	// PRMergedAt is when the promotion PR was merged.
	// +optional
	PRMergedAt *metav1.Time `json:"prMergedAt,omitempty"`

	// MergedBy is the actor who merged the promotion PR.
	// +optional
	MergedBy string `json:"mergedBy,omitempty"`

	// HealthCheckedAt is when the post-merge health check completed.
	// +optional
	HealthCheckedAt *metav1.Time `json:"healthCheckedAt,omitempty"`

	// SoakMinutes is the number of minutes that have elapsed since HealthCheckedAt.
	// Written by the BundleReconciler as part of its own CRD status write.
	// The PolicyGate reconciler reads this field from Bundle.status.environments
	// to populate bundle.upstreamSoakMinutes in the CEL context. This eliminates
	// the time.Since() call from the PolicyGate reconciler hot path (PG-3 fix).
	// +optional
	SoakMinutes int64 `json:"soakMinutes,omitempty"`

	// GateResults holds the result of each PolicyGate evaluation for this
	// environment.
	// +optional
	GateResults []GateResult `json:"gateResults,omitempty"`
}

// GateResult records the outcome of a single PolicyGate evaluation.
type GateResult struct {
	// GateName is the name of the PolicyGate.
	GateName string `json:"gateName"`

	// GateNamespace is the namespace of the PolicyGate.
	// +optional
	GateNamespace string `json:"gateNamespace,omitempty"`

	// Result is the evaluation outcome: "pass" or "block".
	Result string `json:"result"`

	// Reason is the human-readable explanation for the result.
	// +optional
	Reason string `json:"reason,omitempty"`

	// EvaluatedAt is when the gate was evaluated.
	EvaluatedAt metav1.Time `json:"evaluatedAt"`
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
