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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SubscriptionType identifies what artifact source a Subscription watches.
// +kubebuilder:validation:Enum=image;git
type SubscriptionType string

const (
	// SubscriptionTypeImage watches an OCI registry for new image tags.
	SubscriptionTypeImage SubscriptionType = "image"
	// SubscriptionTypeGit watches a Git repository for new commits.
	SubscriptionTypeGit SubscriptionType = "git"
)

// SubscriptionSpec defines the desired state of a Subscription.
type SubscriptionSpec struct {
	// Type identifies the artifact source: "image" (OCI) or "git".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=image;git
	Type SubscriptionType `json:"type"`

	// Image holds OCI registry watching parameters. Required when type=image.
	// +optional
	Image *ImageSubscriptionSpec `json:"image,omitempty"`

	// Git holds Git repository watching parameters. Required when type=git.
	// +optional
	Git *GitSubscriptionSpec `json:"git,omitempty"`

	// Pipeline is the name of the Pipeline CRD that Bundles should target.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Pipeline string `json:"pipeline"`

	// Namespace is the namespace where Bundles will be created.
	// Defaults to the Subscription's own namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ImageSubscriptionSpec configures OCI registry watching.
type ImageSubscriptionSpec struct {
	// Registry is the OCI registry URL (e.g. "ghcr.io/myorg/myapp").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Registry string `json:"registry"`

	// TagFilter is an optional regular expression that image tags must match.
	// Empty string matches all tags.
	// +optional
	TagFilter string `json:"tagFilter,omitempty"`

	// Interval is how often to poll the registry.
	// Uses Go duration format (e.g. "5m", "1h").
	// +kubebuilder:default="5m"
	// +optional
	Interval string `json:"interval,omitempty"`
}

// GitSubscriptionSpec configures Git repository watching.
type GitSubscriptionSpec struct {
	// RepoURL is the HTTPS Git repository URL.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	RepoURL string `json:"repoURL"`

	// Branch is the branch to watch. Defaults to "main".
	// +kubebuilder:default="main"
	// +optional
	Branch string `json:"branch,omitempty"`

	// PathGlob is an optional glob pattern for files to watch.
	// Only commits that touch matching paths trigger Bundle creation.
	// Empty string watches all paths.
	// +optional
	PathGlob string `json:"pathGlob,omitempty"`

	// Interval is how often to poll the repository.
	// Uses Go duration format (e.g. "5m", "1h").
	// +kubebuilder:default="5m"
	// +optional
	Interval string `json:"interval,omitempty"`
}

// SubscriptionStatus defines the observed state of a Subscription.
type SubscriptionStatus struct {
	// Phase is the current subscription state.
	// +kubebuilder:validation:Enum=Watching;Idle;Error
	// +optional
	Phase string `json:"phase,omitempty"`

	// LastCheckedAt is the RFC3339 timestamp of the last poll.
	// +optional
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`

	// LastBundleCreated is the name of the last Bundle created by this Subscription.
	// +optional
	LastBundleCreated string `json:"lastBundleCreated,omitempty"`

	// LastSeenDigest is the OCI digest or Git commit SHA from the last successful check.
	// Used for deduplication — a new Bundle is only created when this changes.
	// +optional
	LastSeenDigest string `json:"lastSeenDigest,omitempty"`

	// Message provides a human-readable reason for the current phase (e.g. error details).
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=sub
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Pipeline",type=string,JSONPath=`.spec.pipeline`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Last-Bundle",type=string,JSONPath=`.status.lastBundleCreated`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Subscription watches an OCI registry or Git repository for new artifacts and
// automatically creates Bundle CRDs when new tags or commits are detected.
//
// Architecture: this is an Owned node (Q2 in Graph-first question stack).
// The reconciler polls an external source, writes the result to its own CRD status,
// and creates Bundle objects as child resources. It never mutates other CRDs' status.
//
// Stage 18 implementation. Removes the CI dependency for artifact discovery.
type Subscription struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubscriptionSpec   `json:"spec,omitempty"`
	Status SubscriptionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SubscriptionList contains a list of Subscription.
type SubscriptionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Subscription `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Subscription{}, &SubscriptionList{})
}
