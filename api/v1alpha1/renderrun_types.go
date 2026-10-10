// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RenderRun phases. Succeeded and Failed are terminal and never change.
const (
	RenderRunPending   = "Pending"
	RenderRunRunning   = "Running"
	RenderRunSucceeded = "Succeeded"
	RenderRunFailed    = "Failed"
)

// RenderRunGit is where a RenderRun reads the DRY source and writes the
// rendered manifests.
type RenderRunGit struct {
	// URL is the repository (Pipeline spec.git.url).
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// SecretName is the Secret in the RenderRun's namespace whose "token" key
	// the render Job uses for git (Pipeline spec.git.secretRef). Empty means
	// no credentials.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// SourceBranch is the DRY source branch (Pipeline spec.git.branch).
	// +kubebuilder:validation:MinLength=1
	SourceBranch string `json:"sourceBranch"`

	// RenderedBranch is the branch the rendered manifests are committed to.
	// +kubebuilder:validation:MinLength=1
	RenderedBranch string `json:"renderedBranch"`

	// PullRequest pushes the render to the promotion branch
	// kardinal/<bundle>/<environment>, from which the step opens a PR into
	// RenderedBranch (approval: pr-review), instead of to RenderedBranch.
	// +optional
	PullRequest bool `json:"pullRequest,omitempty"`
}

// RenderRunBundle is the part of a Bundle a render needs.
type RenderRunBundle struct {
	// Type is the Bundle type: image, config or mixed.
	// +optional
	Type string `json:"type,omitempty"`
	// Images are the images to set in the DRY checkout.
	// +optional
	Images []ImageRef `json:"images,omitempty"`
	// ConfigRef is the config commit to render (a config or mixed Bundle,
	// or an image Bundle that pins its DRY commit).
	// +optional
	ConfigRef *ConfigRef `json:"configRef,omitempty"`
	// RollbackOf is the Bundle a rollback restores: its render's DRY commit
	// is rendered again.
	// +optional
	RollbackOf string `json:"rollbackOf,omitempty"`
}

// RenderRunSpec is one render of a layout: branch environment for one
// Bundle. The kro Graph of the Bundle writes it from the Pipeline and the
// Bundle; the RenderRun reconciler runs it as a Job.
type RenderRunSpec struct {
	// PipelineName is the Pipeline the environment belongs to.
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`

	// BundleName is the Bundle being promoted.
	// +kubebuilder:validation:MinLength=1
	BundleName string `json:"bundleName"`

	// Environment is the environment rendered.
	// +kubebuilder:validation:MinLength=1
	Environment string `json:"environment"`

	// Path is the environment path in the DRY source (environments/<name>
	// when the Pipeline sets none).
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	// Git is where the DRY source is read and the render is written.
	Git RenderRunGit `json:"git"`

	// Bundle is what is promoted: the images to set, the config commit to
	// render (a config or mixed Bundle) and the Bundle a rollback restores.
	Bundle RenderRunBundle `json:"bundle"`

	// Update is the environment's update configuration: how the images are
	// set in the DRY checkout before it is rendered.
	// +optional
	Update UpdateConfig `json:"update,omitempty"`

	// Render is the environment's render configuration.
	// +optional
	Render *RenderConfig `json:"render,omitempty"`
}

// RenderRunResult is what a finished render Job reported.
type RenderRunResult struct {
	// CommitSHA is the rendered commit pushed (empty when nothing changed).
	// +optional
	CommitSHA string `json:"commitSHA,omitempty"`

	// Branch is the branch it was pushed to.
	// +optional
	Branch string `json:"branch,omitempty"`

	// DryCommit is the DRY commit that was rendered.
	// +optional
	DryCommit string `json:"dryCommit,omitempty"`

	// Renderer is kustomize or helm.
	// +optional
	Renderer string `json:"renderer,omitempty"`

	// Objects is how many objects were rendered.
	// +optional
	Objects int `json:"objects,omitempty"`

	// MarkerDigest is the sha256 of the render marker
	// (.kardinal/rendered.yaml) this render wrote: the list of files and
	// their sha256. The next render of the environment accepts the rendered
	// branch only when its marker has the digest of a render kardinal made.
	// +optional
	MarkerDigest string `json:"markerDigest,omitempty"`

	// NoChanges is true when the render matched the rendered branch and
	// nothing was pushed.
	// +optional
	NoChanges bool `json:"noChanges,omitempty"`

	// DriftOverwritten lists the changes made outside kardinal that this
	// render overwrote (render.onDrift: overwrite).
	// +optional
	DriftOverwritten string `json:"driftOverwritten,omitempty"`
}

// RenderRunStatus is the observed state of a RenderRun.
type RenderRunStatus struct {
	// Phase is Pending until the Job is created, Running while it runs, and
	// Succeeded or Failed once it finished. Succeeded and Failed are terminal:
	// the API server refuses to change them, and the Job is never created
	// again.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	// +kubebuilder:validation:XValidation:rule="!(oldSelf in ['Succeeded', 'Failed']) || self == oldSelf",message="a finished RenderRun's phase cannot change"
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message says why the RenderRun is in its phase.
	// +optional
	Message string `json:"message,omitempty"`

	// JobName is the name of the Job the reconciler created.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// JobUID is the UID of that Job. A Job of that name with another UID, or
	// none at all, while the RenderRun runs, fails the RenderRun.
	// +optional
	JobUID string `json:"jobUID,omitempty"`

	// SpecHash is a hash of the spec when the Job was created. A later spec
	// change is not applied.
	// +optional
	SpecHash string `json:"specHash,omitempty"`

	// KnownMarkerDigests are the marker digests of the earlier renders of
	// this Pipeline environment the Job accepted on the rendered branch
	// (RenderRunResult.MarkerDigest of the newest Succeeded RenderRuns).
	// +optional
	KnownMarkerDigests []string `json:"knownMarkerDigests,omitempty"`

	// UnconfirmedBundles holds the Bundle of the environment's newest
	// RenderRun when it Failed after the last Succeeded one (at most one
	// entry): its Job may have pushed before its result was lost. A rendered
	// branch whose marker names it, and whose files match that marker, is
	// accepted as kardinal's; so is a rollback to it.
	// +optional
	UnconfirmedBundles []string `json:"unconfirmedBundles,omitempty"`

	// StartedAt is when the Job was created.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// Deadline is StartedAt plus the render timeout.
	// +optional
	Deadline *metav1.Time `json:"deadline,omitempty"`

	// FinishedAt is when the RenderRun reached a terminal phase.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// Result is what the Job reported (Succeeded only).
	// +optional
	Result *RenderRunResult `json:"result,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Pipeline",type=string,JSONPath=`.spec.pipelineName`
// +kubebuilder:printcolumn:name="Env",type=string,JSONPath=`.spec.environment`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Commit",type=string,JSONPath=`.status.result.commitSHA`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RenderRun is one render of a layout: branch environment for one Bundle:
// a Kubernetes Job that clones the DRY source, sets the Bundle's images,
// renders the environment (kustomize build or helm template) and commits the
// plain manifests to the rendered branch. Created by the Bundle's kro Graph;
// reconciled by the RenderRun reconciler, which runs the Job in the
// Pipeline's namespace and records its result. Rendering never runs in the
// controller.
type RenderRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RenderRunSpec   `json:"spec,omitempty"`
	Status RenderRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RenderRunList contains a list of RenderRun.
type RenderRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RenderRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RenderRun{}, &RenderRunList{})
}
