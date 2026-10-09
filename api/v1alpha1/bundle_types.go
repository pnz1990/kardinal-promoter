// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BundleSpec defines the desired state of a Bundle.
// An image Bundle deploys only its images, so a configRef on it is refused
// instead of ignored (#1353). Bundles stored before the rule keep working:
// CRD validation ratcheting (on by default from Kubernetes 1.30, the oldest
// supported) lets an update through when spec is unchanged.
//
// The artifact a Bundle names is immutable: spec.type, spec.pipeline,
// spec.images, spec.chart, spec.configRef and spec.provenance cannot change after
// creation (they are what gates, verifications and evidence were checked
// against; to promote something else, create a new Bundle). spec.intent stays
// mutable. The rules are transition rules, so they run only on update.
// +kubebuilder:validation:XValidation:rule="self.type == oldSelf.type",message="spec.type is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="self.pipeline == oldSelf.pipeline",message="spec.pipeline is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="has(self.images) == has(oldSelf.images) && (!has(self.images) || self.images == oldSelf.images)",message="spec.images is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="has(self.chart) == has(oldSelf.chart) && (!has(self.chart) || self.chart == oldSelf.chart)",message="spec.chart is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="has(self.configRef) == has(oldSelf.configRef) && (!has(self.configRef) || self.configRef == oldSelf.configRef)",message="spec.configRef is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="has(self.provenance) == has(oldSelf.provenance) && (!has(self.provenance) || self.provenance == oldSelf.provenance)",message="spec.provenance is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="!(self.type == 'image' && has(self.configRef))",message="spec.configRef is used only by config and mixed Bundles: an image Bundle deploys only its images; set type config or mixed, or remove configRef"
type BundleSpec struct {
	// Type classifies the bundle content.
	// Supersession rule (BU-4): each bundle type supersedes only bundles of the same type.
	// An image bundle does NOT supersede a config bundle and vice versa.
	// This allows image and config promotions to coexist independently in the same pipeline.
	// A chart Bundle promotes a Helm chart version (spec.chart) with
	// update.strategy helm.
	// +kubebuilder:validation:Enum=image;config;mixed;chart
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

	// Chart is the Helm chart version a "chart" Bundle promotes. The
	// helm-set-image step writes chart.version at update.helm.chartVersionPath.
	// +optional
	Chart *ChartRef `json:"chart,omitempty"`

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

	// Tag is the image tag, in the OCI distribution grammar: up to 128
	// characters of [A-Za-z0-9_.-], not starting with "." or "-".
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`
	// +optional
	Tag string `json:"tag,omitempty"`

	// Digest is the image digest (sha256:...), in the OCI digest grammar.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+([+._-][a-z0-9]+)*:[a-zA-Z0-9=_-]{32,}$`
	// +optional
	Digest string `json:"digest,omitempty"`
}

// ConfigRef identifies a GitOps repository commit.
type ConfigRef struct {
	// GitRepo is the GitOps repository URL.
	// +optional
	GitRepo string `json:"gitRepo,omitempty"`

	// CommitSHA is the exact commit SHA for this config snapshot: 4 to 64
	// hex characters.
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{4,64}$`
	// +optional
	CommitSHA string `json:"commitSHA,omitempty"`
}

// ChartRef identifies a Helm chart version.
type ChartRef struct {
	// RepoURL is the chart repository (https://... or oci://...).
	// +optional
	RepoURL string `json:"repoURL,omitempty"`

	// Name is the chart name: letters, digits, ".", "_" and "-", starting
	// and ending with a letter or digit. It is joined into the chart's
	// index and OCI paths, so a "/", "]" or other path character would
	// point the version lookup elsewhere.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=250
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`
	Name string `json:"name"`

	// Version is the chart version.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// Digest is the chart package digest (index.yaml digest, or the OCI
	// manifest digest).
	// +optional
	Digest string `json:"digest,omitempty"`
}

// BundleProvenance carries build origin metadata.
type BundleProvenance struct {
	// CommitSHA is the application source commit that produced this Bundle:
	// 4 to 64 hex characters, or an image digest (a Subscription records
	// the digest it found).
	// +kubebuilder:validation:Pattern=`^([0-9a-fA-F]{4,64}|[a-z0-9]+([+._-][a-z0-9]+)*:[a-zA-Z0-9=_-]{32,})$`
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

	// Metrics holds deployment efficiency metrics for this Bundle (K-05).
	// Populated by the BundleReconciler when all environments reach Verified.
	// +optional
	Metrics *BundleMetrics `json:"metrics,omitempty"`

	// GraphRef is the name of the kro Graph CR backing this Bundle's promotion DAG.
	// Populated by the BundleReconciler when the Graph is first created.
	// Used to detect Graph deletion and trigger recreation. The Graph of a
	// Bundle that failed promoting is not recreated (GraphSynced=False,
	// reason GraphDeleted), so the failed artifacts are not promoted again;
	// a Pipeline change rebuilds it.
	// +optional
	GraphRef string `json:"graphRef,omitempty"`

	// PipelineSpecHash is the SHA-256 hash of the Pipeline spec (spec.paused
	// excluded) the Graph was last built from. When the Bundle reconciler is
	// re-queued by a Pipeline watch event, it compares the current Pipeline spec
	// hash to this field. A mismatch re-translates the Graph in place with the
	// updated spec.
	// +optional
	PipelineSpecHash string `json:"pipelineSpecHash,omitempty"`

	// PolicyGatesHash is the SHA-256 hash of the PolicyGate templates that
	// apply to the Pipeline's environments, recorded when the Graph could not
	// be built (InvalidSpec, reason GraphBuildFailed). A change to those
	// templates retries the Bundle, as a Pipeline change does. Empty
	// otherwise.
	// +optional
	PolicyGatesHash string `json:"policyGatesHash,omitempty"`
}

// BundleMetrics holds deployment efficiency metrics for a single Bundle (K-05).
type BundleMetrics struct {
	// CommitToProductionMinutes is the time from Bundle creation to the last
	// environment reaching Verified. Indicates total promotion pipeline latency.
	// +optional
	CommitToProductionMinutes int64 `json:"commitToProductionMinutes,omitempty"`

	// BakeResets is the total number of bake timer resets across all environments.
	// High bake reset count indicates flaky health or over-sensitive bake windows.
	// +optional
	BakeResets int `json:"bakeResets,omitempty"`

	// OperatorInterventions is the number of PolicyGate overrides recorded on
	// this Bundle's gate instances (kardinal override), counted when the Bundle
	// becomes Verified.
	// +optional
	OperatorInterventions int `json:"operatorInterventions,omitempty"`
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
// +kubebuilder:printcolumn:name="Pipeline",type=string,JSONPath=`.spec.pipeline`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Bundle is a versioned snapshot of what to deploy. Treat it as immutable:
// the API does not reject changes to spec, but nothing re-reads a changed one.
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
