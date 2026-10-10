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
// A Bundle's spec is immutable: spec.type, spec.pipeline, spec.images,
// spec.chart, spec.configRef, spec.provenance and spec.intent cannot change
// after creation. The artifact is what gates, verifications and evidence were
// checked against, and an intent edit would apply only at some later,
// unrelated re-translation of the Graph; to promote something else, or to
// another target, create a new Bundle. The rules are transition rules, so
// they run only on update.
// +kubebuilder:validation:XValidation:rule="self.type == oldSelf.type",message="spec.type is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="self.pipeline == oldSelf.pipeline",message="spec.pipeline is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="has(self.images) == has(oldSelf.images) && (!has(self.images) || self.images == oldSelf.images)",message="spec.images is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="has(self.chart) == has(oldSelf.chart) && (!has(self.chart) || self.chart == oldSelf.chart)",message="spec.chart is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="has(self.configRef) == has(oldSelf.configRef) && (!has(self.configRef) || self.configRef == oldSelf.configRef)",message="spec.configRef is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="has(self.provenance) == has(oldSelf.provenance) && (!has(self.provenance) || self.provenance == oldSelf.provenance)",message="spec.provenance is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="has(self.intent) == has(oldSelf.intent) && (!has(self.intent) || self.intent == oldSelf.intent)",message="spec.intent is immutable: create a new Bundle"
// +kubebuilder:validation:XValidation:rule="!(self.type == 'image' && has(self.configRef))",message="spec.configRef is used only by config and mixed Bundles: an image Bundle deploys only its images; set type config or mixed, or remove configRef"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.rejected) || has(self.rejected)",message="spec.rejected cannot be removed: a rejected Bundle stays rejected"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.rejected) || !has(self.rejected) || self.rejected == oldSelf.rejected",message="spec.rejected is immutable once set"
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

	// Images lists the container images included in this Bundle, at most
	// 100.
	// +kubebuilder:validation:MaxItems=100
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

	// Rejected marks the Bundle as rejected (kardinal reject): it is never
	// promoted again, its in-flight steps are cancelled, and rollback,
	// promote and Subscriptions skip any Bundle carrying its artifacts.
	// Setting it is one-way: it cannot be changed or removed. The chart's
	// ValidatingAdmissionPolicy requires rejected.by to be the requesting
	// user; without that policy nothing checks it.
	// +optional
	Rejected *BundleRejection `json:"rejected,omitempty"`
}

// BundleRejection records who rejected a Bundle and why.
type BundleRejection struct {
	// Reason says why the Bundle was rejected.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Reason string `json:"reason"`

	// By is the Kubernetes username of whoever rejected the Bundle. The
	// chart's ValidatingAdmissionPolicy (kardinal-identity) admits a new
	// rejection only when by equals the requesting user's username.
	// +kubebuilder:validation:MinLength=1
	By string `json:"by"`

	// At is when the Bundle was rejected.
	// +optional
	At *metav1.Time `json:"at,omitempty"`
}

// RejectedArtifactSet is the part of a rejected Bundle that the rejection
// covers (BundleStatus.RejectedArtifacts).
type RejectedArtifactSet struct {
	// Images are the rejected images: those not Verified, with the same
	// digest (or tag, without a digest), before this Bundle in every
	// environment it reached.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	Images []ImageRef `json:"images,omitempty"`
	// ConfigCommitSHA is the rejected config commit, when it differs.
	// +optional
	ConfigCommitSHA string `json:"configCommitSHA,omitempty"`
	// ComparedWith names, per environment, the Verified Bundle the artifacts
	// were compared with ("<env>=<bundle>"); empty when no environment had
	// one, and then every artifact is rejected.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	ComparedWith []string `json:"comparedWith,omitempty"`
}

// ImageRef identifies a container image by repository, tag, and/or digest.
type ImageRef struct {
	// Repository is the image repository (e.g. "ghcr.io/nginx/nginx").
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Repository string `json:"repository"`

	// Tag is the image tag, in the OCI distribution grammar: up to 128
	// characters of [A-Za-z0-9_.-], not starting with "." or "-".
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Tag string `json:"tag,omitempty"`

	// Digest is the image digest (sha256:...), in the OCI digest grammar.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+([+._-][a-z0-9]+)*:[a-zA-Z0-9=_-]{32,}$`
	// +kubebuilder:validation:MaxLength=256
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
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.retiredAt) || has(self.retiredAt)",message="status.retiredAt cannot be removed: a retired Bundle stays retired"
type BundleStatus struct {
	// Phase is the bundle promotion phase. Rejected is final: spec.rejected
	// is set, and nothing of this Bundle is promoted again.
	// +kubebuilder:validation:Enum=Available;Promoting;Verified;Failed;Superseded;Rejected
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

	// RetiredSteps records the PromotionSteps of a Bundle whose Graph was
	// retired (condition GraphRetired=True). Deleting a finished Bundle's
	// Graph deletes the PromotionSteps, PolicyGate instances and PRStatuses
	// it created, so kro does not hold every finished Graph in memory
	// (#1492). Rollback, promote, history, metrics, the CLI and the UI read
	// these records where they read the steps of a Bundle that is still
	// promoting. The step of a fleet target removed from the Pipeline while the
	// Bundle promoted is recorded here when its Graph is updated, before kro
	// prunes it, and kept at the retirement.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=1000
	RetiredSteps []RetiredStep `json:"retiredSteps,omitempty"`

	// RejectedArtifacts is what a rejection (spec.rejected) rejects: the
	// artifacts of this Bundle that differ from what was Verified before it
	// in the environments it reached, written by the Bundle reconciler when
	// it marks the Bundle Rejected. A sidecar the Bundle carries unchanged is
	// not in it, so rolling back to the Bundle before stays possible. Unset
	// (a rejection not processed yet) means all of the Bundle's artifacts.
	// +optional
	RejectedArtifacts *RejectedArtifactSet `json:"rejectedArtifacts,omitempty"`

	// RetiredAt is when the Bundle's Graph was retired: set in the same
	// write as RetiredSteps, and never cleared. A Bundle with RetiredAt is
	// final; the GraphRetired condition only shows the retirement's progress.
	// +optional
	RetiredAt *metav1.Time `json:"retiredAt,omitempty"`
}

// RetiredStep is what a retired Bundle keeps of one of its PromotionSteps.
type RetiredStep struct {
	// Name is the PromotionStep's name.
	Name string `json:"name"`

	// Environment is the PromotionStep's spec.environment.
	Environment string `json:"environment"`

	// StepType is the PromotionStep's spec.stepType.
	// +optional
	StepType string `json:"stepType,omitempty"`

	// State is the PromotionStep's final status.state.
	// +optional
	State string `json:"state,omitempty"`

	// Message is the PromotionStep's final status.message, cut to 512 bytes.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Message string `json:"message,omitempty"`

	// PRURL is the PromotionStep's status.prURL.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	PRURL string `json:"prURL,omitempty"`

	// CreatedAt is the PromotionStep's creationTimestamp.
	CreatedAt metav1.Time `json:"createdAt"`

	// VerifiedAt is when the PromotionStep became Verified.
	// +optional
	VerifiedAt *metav1.Time `json:"verifiedAt,omitempty"`

	// HealthCheckExpiry is the PromotionStep's status.healthCheckExpiry: set
	// once its change merged and the health check started.
	// +optional
	HealthCheckExpiry *metav1.Time `json:"healthCheckExpiry,omitempty"`
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
