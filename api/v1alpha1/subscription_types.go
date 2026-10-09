// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SubscriptionType identifies what artifact source a Subscription watches.
// +kubebuilder:validation:Enum=image;git;helm
type SubscriptionType string

const (
	// SubscriptionTypeImage watches an OCI registry for new image tags.
	SubscriptionTypeImage SubscriptionType = "image"
	// SubscriptionTypeGit watches a Git repository for new commits.
	SubscriptionTypeGit SubscriptionType = "git"
	// SubscriptionTypeHelm watches a Helm chart repository (HTTP index.yaml or
	// OCI) for new chart versions.
	SubscriptionTypeHelm SubscriptionType = "helm"
)

// RefreshAnnotation is the Subscription annotation that asks the controller
// to poll the source now instead of at the next interval. Its value is the
// RFC3339 time of the request. The webhook receiver
// (/webhook/subscriptions/...) sets it; anyone who can patch the
// Subscription can too (kubectl annotate --overwrite).
const RefreshAnnotation = "kardinal.io/refresh"

// SubscriptionSpec defines the desired state of a Subscription.
// +kubebuilder:validation:XValidation:rule="self.type != 'image' || has(self.image)",message="spec.image is required when type is image"
// +kubebuilder:validation:XValidation:rule="self.type != 'git' || has(self.git)",message="spec.git is required when type is git"
// +kubebuilder:validation:XValidation:rule="self.type != 'helm' || has(self.helm)",message="spec.helm is required when type is helm"
type SubscriptionSpec struct {
	// Type identifies the artifact source: "image" (OCI), "git" or "helm"
	// (a Helm chart repository).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=image;git;helm
	Type SubscriptionType `json:"type"`

	// Image holds OCI registry watching parameters. Required when type=image.
	// +optional
	Image *ImageSubscriptionSpec `json:"image,omitempty"`

	// Git holds Git repository watching parameters. Required when type=git.
	// +optional
	Git *GitSubscriptionSpec `json:"git,omitempty"`

	// Helm holds Helm chart watching parameters. Required when type=helm.
	// +optional
	Helm *HelmSubscriptionSpec `json:"helm,omitempty"`

	// Webhook enables the inbound webhook receiver for this Subscription: a
	// registry or SCM that posts to
	// /webhook/subscriptions/<namespace>/<name>/<provider> makes the
	// controller poll at once. Without it the receiver refuses every request
	// for the Subscription.
	// +optional
	Webhook *SubscriptionWebhook `json:"webhook,omitempty"`

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

// SubscriptionSecretRef names a Secret in the Subscription's own namespace.
// A Subscription cannot read a Secret in another namespace, and reads only a
// Secret labelled kardinal.io/referenceable: "true" (LabelSecretReferenceable).
type SubscriptionSecretRef struct {
	// Name is the Secret name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// SubscriptionWebhook configures the inbound webhook receiver.
type SubscriptionWebhook struct {
	// SecretRef names a Secret in the Subscription's namespace whose key
	// "token" (at least 16 characters) authenticates webhook deliveries: as
	// the last path segment of the receiver URL (Docker Hub, Quay, Harbor,
	// Artifactory, generic), as the Authorization header (Harbor's auth
	// header), the X-JFrog-Event-Auth header (Artifactory), or as the HMAC-SHA256
	// key of the X-Hub-Signature-256 (GitHub) or X-Kardinal-Signature-256
	// (generic) header.
	// +kubebuilder:validation:Required
	SecretRef SubscriptionSecretRef `json:"secretRef"`
}

// TagSelectionStrategy orders the tags that pass the filters.
// +kubebuilder:validation:Enum=Auto;SemVer;Lexical;NewestBuild
type TagSelectionStrategy string

const (
	// TagStrategyAuto picks the only tag, the highest semantic version when
	// every tag is one, and otherwise the newest build.
	TagStrategyAuto TagSelectionStrategy = "Auto"
	// TagStrategySemVer ignores tags that are not semantic versions and picks
	// the highest.
	TagStrategySemVer TagSelectionStrategy = "SemVer"
	// TagStrategyLexical picks the tag that sorts last (for example dated
	// tags such as 2026-10-08.1).
	TagStrategyLexical TagSelectionStrategy = "Lexical"
	// TagStrategyNewestBuild picks the most recently built image.
	TagStrategyNewestBuild TagSelectionStrategy = "NewestBuild"
)

// TagSelection filters the tags (image tags, or chart versions for a Helm
// Subscription) a Subscription considers. The filters apply in order:
// allowTags, tagFilter, excludeTagFilter, ignoreTags, semverConstraint.
// Selection is deterministic: the same tag list always gives the same tag.
type TagSelection struct {
	// TagFilter is an optional regular expression (RE2) that tags must match.
	// Empty matches every tag.
	// +optional
	TagFilter string `json:"tagFilter,omitempty"`

	// ExcludeTagFilter is an optional regular expression (RE2): tags that
	// match it are dropped (for example "-rc\\.|-debug$").
	// +optional
	ExcludeTagFilter string `json:"excludeTagFilter,omitempty"`

	// SemverConstraint keeps only tags that are semantic versions satisfying
	// the constraint, such as ">=1.4.0 <2.0.0", "^1.4" or "~1.4.2"
	// (github.com/Masterminds/semver syntax). A constraint without a
	// pre-release part excludes pre-releases. Tags that are not semantic
	// versions are dropped.
	// +optional
	SemverConstraint string `json:"semverConstraint,omitempty"`

	// AllowTags, when not empty, keeps only the listed tags.
	// +kubebuilder:validation:MaxItems=100
	// +optional
	AllowTags []string `json:"allowTags,omitempty"`

	// IgnoreTags drops the listed tags.
	// +kubebuilder:validation:MaxItems=100
	// +optional
	IgnoreTags []string `json:"ignoreTags,omitempty"`
}

// ImageSubscriptionSpec configures OCI registry watching.
type ImageSubscriptionSpec struct {
	// Registry is the image repository to poll, without a tag or digest
	// (e.g. "ghcr.io/myorg/myapp", "docker.io/library/nginx", or
	// "http://registry.registry.svc.cluster.local:5000/myapp" for a plain-HTTP
	// in-cluster registry). Loopback (the controller's own pod), link-local,
	// cloud metadata, unspecified and multicast addresses are refused.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Registry string `json:"registry"`

	// TagSelection filters the tags. With one remaining tag its digest is
	// tracked (a moving tag such as "^main$"); otherwise strategy picks one.
	// No remaining tag is an error.
	TagSelection `json:",inline"`

	// Strategy orders the remaining tags. Auto (the default): the only tag;
	// the highest semantic version when every tag is one; otherwise the most
	// recently built image. SemVer: the highest semantic version, other tags
	// ignored. Lexical: the tag that sorts last. NewestBuild: the most
	// recently built image.
	// +kubebuilder:default=Auto
	// +optional
	Strategy TagSelectionStrategy `json:"strategy,omitempty"`

	// DiscoveryLimit is the most tags newest-build ordering inspects; it reads
	// each tag's manifest and image config on every poll. More remaining tags
	// is an error. Default 50.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=200
	// +optional
	DiscoveryLimit int32 `json:"discoveryLimit,omitempty"`

	// SecretRef names a Secret with registry credentials, in the
	// Subscription's namespace: a kubernetes.io/dockerconfigjson Secret
	// (the entry for the registry host is used; username/password, auth,
	// identitytoken and registrytoken are understood) or a Secret with keys
	// username and password. Without it only anonymous pulls are made.
	// +optional
	SecretRef *SubscriptionSecretRef `json:"secretRef,omitempty"`

	// Interval is how often to poll the registry.
	// Uses Go duration format (e.g. "5m", "1h"). Values below 30s are raised
	// to 30s; empty or "0" means the 5m default.
	// +kubebuilder:default="5m"
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Interval string `json:"interval,omitempty"`
}

// GitSubscriptionSpec configures Git repository watching.
type GitSubscriptionSpec struct {
	// RepoURL is the Git repository URL: https:// (or http:// for an
	// in-cluster server), ssh://user@host[:port]/path, or the scp-like
	// user@host:path. Loopback (the controller's own pod), link-local, cloud
	// metadata, unspecified and multicast addresses are refused.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	RepoURL string `json:"repoURL"`

	// Branch is the branch to watch. Defaults to "main".
	// +kubebuilder:default="main"
	// +optional
	Branch string `json:"branch,omitempty"`

	// PathGlob limits the commits that create a Bundle to those that change a
	// file matching the glob ("apps/web/**", "*.yaml", "{base,overlays}/**").
	// "**" matches any number of directories. Matching walks the branch's
	// first-parent history back from its head, at most discoveryLimit commits,
	// and the Bundle is for the newest matching commit.
	// +optional
	PathGlob string `json:"pathGlob,omitempty"`

	// DiscoveryLimit is the most commits a pathGlob poll reads back from the
	// branch head (a shallow fetch of that depth). Default 20.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=200
	// +optional
	DiscoveryLimit int32 `json:"discoveryLimit,omitempty"`

	// SecretRef names a Secret with Git credentials, in the Subscription's
	// namespace. HTTPS: key token (sent as the password, with key username
	// or "git" as the user), or keys username and password. SSH: key
	// ssh-privatekey (a kubernetes.io/ssh-auth Secret) and key known_hosts,
	// which is required: the host key is always verified.
	// +optional
	SecretRef *SubscriptionSecretRef `json:"secretRef,omitempty"`

	// Interval is how often to poll the repository.
	// Uses Go duration format (e.g. "5m", "1h"). Values below 30s are raised
	// to 30s; empty or "0" means the 5m default.
	// +kubebuilder:default="5m"
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Interval string `json:"interval,omitempty"`
}

// HelmSubscriptionSpec configures Helm chart watching.
type HelmSubscriptionSpec struct {
	// RepoURL is the chart repository: an HTTP(S) repository that serves
	// index.yaml ("https://charts.example.com"), or an OCI registry path
	// ("oci://ghcr.io/myorg/charts"), under which the chart is a repository
	// named after it. Loopback, link-local, cloud metadata, unspecified and
	// multicast addresses are refused.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	RepoURL string `json:"repoURL"`

	// Chart is the chart name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Chart string `json:"chart"`

	// TagSelection filters the chart versions; the highest remaining
	// semantic version wins. Versions that are not semantic versions are
	// ignored.
	TagSelection `json:",inline"`

	// SecretRef names a Secret with repository credentials, in the
	// Subscription's namespace: keys username and password (HTTP basic auth,
	// or an OCI registry login), or a kubernetes.io/dockerconfigjson Secret
	// for an OCI repository.
	// +optional
	SecretRef *SubscriptionSecretRef `json:"secretRef,omitempty"`

	// Interval is how often to poll the repository.
	// Uses Go duration format (e.g. "5m", "1h"). Values below 30s are raised
	// to 30s; empty or "0" means the 5m default.
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

	// LastSeenDigest is the OCI digest, Git commit SHA or chart digest from
	// the last successful check. The first check only records it (no Bundle);
	// a later check that sees a different value creates a Bundle.
	// +optional
	LastSeenDigest string `json:"lastSeenDigest,omitempty"`

	// LastSeenTag is the image tag, short commit SHA or chart version of
	// lastSeenDigest.
	// +optional
	LastSeenTag string `json:"lastSeenTag,omitempty"`

	// LastSeenRevision is the branch head the last pathGlob poll read up to.
	// The next poll only reads commits after it. Empty without pathGlob.
	// +optional
	LastSeenRevision string `json:"lastSeenRevision,omitempty"`

	// LastRefreshRequest is the kardinal.io/refresh annotation value the last
	// poll answered.
	// +optional
	LastRefreshRequest string `json:"lastRefreshRequest,omitempty"`

	// ObservedPathGlob is the spec.git.pathGlob the last successful poll
	// used. When the glob changes, the next poll records a new baseline and
	// creates no Bundle.
	// +optional
	ObservedPathGlob string `json:"observedPathGlob,omitempty"`

	// Conditions: Ready is True while the Subscription polls its source
	// (phase Watching) and False with the reason otherwise, for example
	// SecretNotReferenceable, SecretNotFound or WatchFailed.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

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

// Subscription watches an OCI registry, a Git repository or a Helm chart
// repository for new artifacts and automatically creates Bundle CRDs when new
// tags, commits or chart versions are detected.
//
// Architecture: this is an Owned node (Q2 in Graph-first question stack).
// The reconciler polls an external source, writes the result to its own CRD status,
// and creates Bundle objects, which enter the normal Graph flow. It never
// mutates other CRDs' status. The webhook receiver only sets the
// kardinal.io/refresh annotation; the reconciler does the poll.
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
