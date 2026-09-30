// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// PRStatusSpec defines the desired state of a PRStatus object.
// PRStatus objects are created by the PromotionStep reconciler's open-pr step,
// and updated by the PRStatus reconciler via polling or webhook events.
type PRStatusSpec struct {
	// PRURL is the full GitHub pull request URL.
	// Example: https://github.com/owner/repo/pull/42
	// Set by the open-pr step after the PR is created. Empty in the placeholder.
	// +optional
	PRURL string `json:"prURL,omitempty"`

	// PRNumber is the pull request number (numeric ID within the repo).
	// Set by the open-pr step after the PR is created. Zero in the placeholder.
	// +optional
	PRNumber int `json:"prNumber,omitempty"`

	// Repo is the "owner/repo" slug identifying the GitHub repository.
	// Example: acme/my-service
	// Set by the open-pr step after the PR is created. Empty in the placeholder.
	// +optional
	Repo string `json:"repo,omitempty"`
}

// PRStatusStatus holds the observed state of the pull request.
// Written exclusively by the PRStatusReconciler.
type PRStatusStatus struct {
	// Merged is true when the pull request has been merged.
	// The Graph Watch node uses this field: readyWhen: ${prStatus.status.merged == true}
	// +optional
	Merged bool `json:"merged,omitempty"`

	// Open is true when the pull request is still open (not merged, not closed).
	// Set to false when the PR is closed or merged. A closed PR is still polled
	// for 5 minutes (see closedAt), so open can turn true again when it is
	// reopened.
	// +optional
	Open bool `json:"open,omitempty"`

	// Approved is true when the pull request has at least one approved review
	// and no outstanding change-request reviews. Written by PRStatusReconciler.
	// CEL: bundle.pr["staging"].isApproved
	// +optional
	Approved bool `json:"approved,omitempty"`

	// ApprovalCount is the number of distinct approved reviews on this PR.
	// Written by PRStatusReconciler. CEL: bundle.pr["staging"].approvalCount >= 2
	// +optional
	ApprovalCount int `json:"approvalCount,omitempty"`

	// LastCheckedAt records when the status was last written from an SCM API
	// poll. Polls that change nothing refresh it at most every 5 minutes, so it
	// can lag the most recent poll by up to that much.
	// +optional
	LastCheckedAt *metav1.Time `json:"lastCheckedAt,omitempty"`

	// MergeCommitSHA is the commit the PR was merged as (the merge, squash or
	// rebase commit on the base branch). Written once the PR is merged, when the
	// SCM provider reports it. Health adapters use it to confirm that the GitOps
	// tool deployed this exact revision.
	// +optional
	MergeCommitSHA string `json:"mergeCommitSHA,omitempty"`

	// PollError is the SCM API error of the last poll when a retry cannot fix
	// it: HTTP 401, 403 (not a rate limit), 404 or 410. The PromotionStep
	// waiting for this PR fails with it. Cleared by the next successful poll.
	// Transient errors (429, 5xx, network) are only logged and retried.
	// +optional
	PollError string `json:"pollError,omitempty"`

	// ClosedAt is when the PRStatus reconciler first saw the PR closed without
	// merging. The PR is still polled for 5 minutes after this time: a poll
	// that sees it open again clears closedAt, and the PromotionStep keeps
	// waiting for the merge.
	// +optional
	ClosedAt *metav1.Time `json:"closedAt,omitempty"`

	// ClosedFinal is true once the PR was still closed 5 minutes after
	// closedAt. The reconciler then commented on the PR and stopped polling
	// it, and the PromotionStep waiting for it fails. A closed PRStatus
	// (open=false with lastCheckedAt set) that has no closedAt was written by
	// an older release and counts as final too.
	// +optional
	ClosedFinal bool `json:"closedFinal,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=prs
// +kubebuilder:printcolumn:name="Merged",type=boolean,JSONPath=`.status.merged`
// +kubebuilder:printcolumn:name="Open",type=boolean,JSONPath=`.status.open`
// +kubebuilder:printcolumn:name="Approved",type=boolean,JSONPath=`.status.approved`
// +kubebuilder:printcolumn:name="Approvals",type=integer,JSONPath=`.status.approvalCount`
// +kubebuilder:printcolumn:name="PR",type=integer,JSONPath=`.spec.prNumber`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PRStatus is a controller-internal CRD that tracks the merge state of a GitHub
// pull request, making it observable by the kro Graph.
//
// Architecture: PromotionStep open-pr step creates a PRStatus CR. The
// PRStatusReconciler polls GitHub (or receives webhook events) and writes
// status.merged. The Graph Watch node propagates when status.merged == true,
// replacing the previous polling loop in handleWaitingForMerge.
//
// Graph-purity: eliminates PS-4, SCM-2, ST-10, ST-11, BU-3, WH-1 from
// docs/design/11-graph-purity-tech-debt.md.
type PRStatus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PRStatusSpec   `json:"spec,omitempty"`
	Status PRStatusStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PRStatusList contains a list of PRStatus objects.
type PRStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PRStatus `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PRStatus{}, &PRStatusList{})
}
