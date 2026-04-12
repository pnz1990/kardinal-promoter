// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	// Set to false when the PR is closed or merged.
	// +optional
	Open bool `json:"open,omitempty"`

	// LastCheckedAt records the timestamp of the most recent SCM API poll.
	// +optional
	LastCheckedAt *metav1.Time `json:"lastCheckedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=prs
// +kubebuilder:printcolumn:name="Merged",type=boolean,JSONPath=`.status.merged`
// +kubebuilder:printcolumn:name="Open",type=boolean,JSONPath=`.status.open`
// +kubebuilder:printcolumn:name="PR",type=integer,JSONPath=`.spec.prNumber`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PRStatus is a controller-internal CRD that tracks the merge state of a GitHub
// pull request, making it observable by the krocodile Graph via a Watch node.
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
