// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Pipeline spec.git.writes values.
const (
	// WritesPerEnvironment: each environment's PromotionStep clones, commits
	// and pushes its own change (the default).
	WritesPerEnvironment = "perEnvironment"
	// WritesBatched: the environments of one Bundle that are ready together
	// are written by one BranchWrite round: one clone, one commit per
	// environment, one push (docs/design/30-batched-branch-writes.md).
	WritesBatched = "batched"
)

// BatchedWrite reports whether environment e of a Pipeline with spec p has
// its change written by the Bundle's BranchWrite: spec.git.writes is
// batched and e is a directory-layout approval: auto environment of an
// image Bundle, updated by kustomize, helm or yaml, with no fleet. Every
// other environment writes its own change, as with perEnvironment.
func BatchedWrite(p PipelineSpec, e EnvironmentSpec, bundleType string) bool {
	if p.Git.Writes != WritesBatched {
		return false
	}
	if e.Approval == "pr-review" || RendersToBranch(p, e) || e.Update.Strategy == "argocd" || e.Fleet != nil {
		return false
	}
	return bundleType == "" || bundleType == "image"
}

// BranchWriteEnvironment is one environment a BranchWrite writes.
type BranchWriteEnvironment struct {
	// Name is the environment's name in the Pipeline.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Step is the name of the environment's PromotionStep for the Bundle.
	// +kubebuilder:validation:MinLength=1
	Step string `json:"step"`
}

// BranchWriteRequest is a PromotionStep's request to have its environment
// written: the step, its UID, and the number of its request
// (status.writeRequest of the step).
type BranchWriteRequest struct {
	// Step is the PromotionStep's name.
	// +kubebuilder:validation:MinLength=1
	Step string `json:"step"`

	// UID is the PromotionStep's UID: a request of a step deleted and made
	// again is another step's.
	// +optional
	UID string `json:"uid,omitempty"`

	// Request is the number of the step's request. A retry of the step asks
	// again with the next number, and is written in a new round.
	// +kubebuilder:validation:Minimum=1
	Request int64 `json:"request"`
}

// BranchWriteSpec is the desired state of a BranchWrite. The Graph writes
// it: Environments when it is built, Requested whenever a step asks.
type BranchWriteSpec struct {
	// PipelineName is the Pipeline whose environments are written.
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`

	// BundleName is the Bundle whose version is written.
	// +kubebuilder:validation:MinLength=1
	BundleName string `json:"bundleName"`

	// URL is the repository (the Pipeline's spec.git.url).
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Branch is the branch the environments' changes are pushed to.
	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`

	// Environments are the environments this BranchWrite writes, in the
	// order a round commits them.
	// +listType=map
	// +listMapKey=name
	Environments []BranchWriteEnvironment `json:"environments"`

	// Requested are the steps that asked to have their environment written
	// (status.writeRequest of each), rendered by the Graph from the steps.
	// +listType=map
	// +listMapKey=step
	// +optional
	Requested []BranchWriteRequest `json:"requested,omitempty"`
}

// BranchWriteMember is one environment of a round.
type BranchWriteMember struct {
	// Environment is the environment's name.
	Environment string `json:"environment"`

	// Step, UID and Request are the request the round answers.
	Step string `json:"step"`
	// +optional
	UID     string `json:"uid,omitempty"`
	Request int64  `json:"request"`
}

// BranchWriteRound is the round in flight: its members, frozen before any
// git work, and when it was about to push.
type BranchWriteRound struct {
	// Round is the round's number.
	Round int64 `json:"round"`

	// Members are the requests the round writes.
	// +optional
	Members []BranchWriteMember `json:"members,omitempty"`

	// PushIntent is when the round was about to push. A round found in
	// flight with it set may have pushed: the reconciler looks for its
	// commits (their Kardinal-Write trailer) on the branch before it writes
	// the round again.
	// +optional
	PushIntent *metav1.Time `json:"pushIntent,omitempty"`
}

// BranchWriteResult is the answer to one environment's latest request.
type BranchWriteResult struct {
	// Step, UID and Request are the request answered.
	Step string `json:"step"`
	// +optional
	UID     string `json:"uid,omitempty"`
	Request int64  `json:"request"`

	// Round is the round that wrote it.
	Round int64 `json:"round"`

	// CommitSHA is the environment's commit on the branch: its own commit,
	// or the branch head the round started from when the environment
	// already had the Bundle's version (NoChanges).
	// +optional
	CommitSHA string `json:"commitSHA,omitempty"`

	// NoChanges is true when the environment needed no change.
	// +optional
	NoChanges bool `json:"noChanges,omitempty"`

	// Error says why the environment was not written; the step retries on
	// its own budget.
	// +optional
	Error string `json:"error,omitempty"`

	// Cancelled is true when the round did not write it because the Bundle
	// was superseded, rejected or deleted (Error says which), or a newer
	// Bundle had already pushed to the environment (NewerBundle).
	// +optional
	Cancelled bool `json:"cancelled,omitempty"`

	// NewerBundle is the Bundle that had already pushed to the environment,
	// when that cancelled the write.
	// +optional
	NewerBundle string `json:"newerBundle,omitempty"`
}

// BranchWriteStatus is the observed state of a BranchWrite.
type BranchWriteStatus struct {
	// Round is the number of the last round started.
	// +optional
	Round int64 `json:"round,omitempty"`

	// InFlight is the round being written, set before its git work and
	// cleared once its results are recorded.
	// +optional
	InFlight *BranchWriteRound `json:"inFlight,omitempty"`

	// Environments are the results, by environment name.
	// +optional
	Environments map[string]BranchWriteResult `json:"environments,omitempty"`

	// Message says what the last round did, or why it waits.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories=kardinal-all
// +kubebuilder:printcolumn:name="Pipeline",type=string,JSONPath=`.spec.pipelineName`
// +kubebuilder:printcolumn:name="Bundle",type=string,JSONPath=`.spec.bundleName`
// +kubebuilder:printcolumn:name="Branch",type=string,JSONPath=`.spec.branch`
// +kubebuilder:printcolumn:name="Round",type=integer,JSONPath=`.status.round`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BranchWrite writes the changes of a Bundle's environments that push to one
// repository branch (spec.git.writes: batched): every round takes the
// branch's turn, clones once, commits each requested environment's change
// with that environment's own commit, and pushes once. Created by the
// Bundle's kro Graph, which renders the requests from the steps and mirrors
// the results back onto each step's spec.live.writes; reconciled by the
// BranchWrite reconciler, which writes only its status
// (docs/design/30-batched-branch-writes.md).
type BranchWrite struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BranchWriteSpec   `json:"spec,omitempty"`
	Status BranchWriteStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BranchWriteList contains a list of BranchWrite.
type BranchWriteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BranchWrite `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BranchWrite{}, &BranchWriteList{})
}
