// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

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

	// Namespace must be empty or equal to the Subscription's own namespace.
	// Bundles are always created in the Subscription's namespace; any other
	// value puts the Subscription in phase Error and creates no Bundle. Leave
	// it empty: the field is kept only so existing manifests still apply.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ImageSubscriptionSpec configures OCI registry watching.
type ImageSubscriptionSpec struct {
	// Registry is the image repository to poll, without a tag or digest
	// (e.g. "ghcr.io/myorg/myapp", "docker.io/library/nginx", or
	// "http://localhost:5000/myapp" for a plain-HTTP registry). Only public
	// repositories are supported: the watcher uses the registry's anonymous
	// token flow and sends no credentials.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Registry string `json:"registry"`

	// TagFilter is an optional regular expression that image tags must match.
	// Empty string matches all tags. With one matching tag its digest is
	// tracked (a moving tag such as "^main$"); when every matching tag is a
	// semantic version the highest wins; otherwise the most recently built
	// image wins (at most 50 matching tags). No matching tag is an error.
	// +optional
	TagFilter string `json:"tagFilter,omitempty"`

	// Interval is how often to poll the registry.
	// Uses Go duration format (e.g. "5m", "1h").
	// +kubebuilder:default="5m"
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
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

	// PathGlob is reserved for path filtering, which is not implemented.
	// A non-empty value puts the Subscription in phase Error; leave it empty
	// (every new commit on the branch creates a Bundle).
	// +optional
	PathGlob string `json:"pathGlob,omitempty"`

	// Interval is how often to poll the repository.
	// Uses Go duration format (e.g. "5m", "1h").
	// +kubebuilder:default="5m"
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
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
	// The first check only records it (no Bundle); a later check that sees a
	// different value creates a Bundle.
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
