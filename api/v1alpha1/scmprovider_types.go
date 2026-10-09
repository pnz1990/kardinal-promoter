// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// The kinds a Pipeline's spec.git.providerRef names.
const (
	KindScmProvider        = "ScmProvider"
	KindClusterScmProvider = "ClusterScmProvider"
)

// ScmProviderSpec is the SCM a Pipeline opens its PRs on: its type, API and
// credentials.
type ScmProviderSpec struct {
	// Type is the SCM: github, gitlab, forgejo, gitea, bitbucket or
	// azuredevops (the --scm-provider values).
	// +kubebuilder:validation:Enum=github;gitlab;forgejo;gitea;bitbucket;azuredevops
	Type string `json:"type"`

	// APIURL is the SCM's API base URL (the --scm-api-url value). Empty uses
	// the provider's public default (api.github.com, gitlab.com, ...).
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:XValidation:rule="self == '' || self.startsWith('https://') || self.startsWith('http://')",message="apiURL must be an http(s) URL"
	// +optional
	APIURL string `json:"apiURL,omitempty"`

	// SecretRef names the Secret that holds the API token. A ScmProvider's
	// Secret is in its own namespace; a ClusterScmProvider names the
	// namespace.
	SecretRef ScmSecretKeyRef `json:"secretRef"`

	// WebhookSecretRef names the Secret that holds the webhook secret this
	// provider's webhook deliveries are checked with (key "secret" when
	// unset). Without it the provider's webhook endpoint refuses every
	// delivery, and merges are seen by polling.
	// +optional
	WebhookSecretRef *ScmSecretKeyRef `json:"webhookSecretRef,omitempty"`

	// AllowedRepositories, when set, are the only repositories this
	// provider's token is used for: globs over the repository the SCM API
	// names ("acme/*", "group/sub/**"). "*" matches one path segment and a
	// trailing "/**" any depth. A Bundle of a Pipeline whose repository is not
	// allowed fails when its Graph is built.
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:items:MaxLength=256
	// +optional
	AllowedRepositories []string `json:"allowedRepositories,omitempty"`
}

// ScmSecretKeyRef names one key of a Secret.
type ScmSecretKeyRef struct {
	// Name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the data key. Defaults to "token" for secretRef and "secret"
	// for webhookSecretRef.
	// +optional
	Key string `json:"key,omitempty"`

	// Namespace is the Secret's namespace: required on a
	// ClusterScmProvider, and empty on a ScmProvider, whose Secrets are in
	// its own namespace (a Pipeline author cannot borrow another namespace's
	// token).
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ScmProviderStatus is what the controller found when it checked the
// provider.
type ScmProviderStatus struct {
	// Conditions: Ready is True when the spec is valid and its Secrets have
	// their keys.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the generation the conditions describe.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=scmp
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="API",type=string,JSONPath=`.spec.apiURL`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(self.spec.secretRef.__namespace__) || self.spec.secretRef.__namespace__ == ''",message="a ScmProvider's Secrets are in its own namespace: leave spec.secretRef.namespace empty"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.webhookSecretRef) || !has(self.spec.webhookSecretRef.__namespace__) || self.spec.webhookSecretRef.__namespace__ == ''",message="a ScmProvider's Secrets are in its own namespace: leave spec.webhookSecretRef.namespace empty"

// ScmProvider is an SCM that Pipelines in its namespace open their PRs on
// (spec.git.providerRef). With it, one controller serves several SCMs, or
// several tokens of one: a Pipeline without providerRef keeps the
// controller's --scm-provider.
type ScmProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScmProviderSpec   `json:"spec"`
	Status ScmProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ScmProviderList contains a list of ScmProvider.
type ScmProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ScmProvider `json:"items"`
}

// ClusterScmProviderSpec is a ScmProviderSpec that Pipelines of several
// namespaces may use.
type ClusterScmProviderSpec struct {
	ScmProviderSpec `json:",inline"`

	// AllowedNamespaces selects the namespaces whose Pipelines may use the
	// provider, by namespace labels. Empty allows none: a cluster-wide
	// token is shared only on purpose.
	// +optional
	AllowedNamespaces *metav1.LabelSelector `json:"allowedNamespaces,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cscmp
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="API",type=string,JSONPath=`.spec.apiURL`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="has(self.spec.secretRef.__namespace__) && self.spec.secretRef.__namespace__ != ''",message="spec.secretRef.namespace is required on a ClusterScmProvider"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.webhookSecretRef) || (has(self.spec.webhookSecretRef.__namespace__) && self.spec.webhookSecretRef.__namespace__ != '')",message="spec.webhookSecretRef.namespace is required on a ClusterScmProvider"

// ClusterScmProvider is a cluster-scoped ScmProvider: Pipelines of the
// namespaces spec.allowedNamespaces selects may use it.
type ClusterScmProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterScmProviderSpec `json:"spec"`
	Status ScmProviderStatus      `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterScmProviderList contains a list of ClusterScmProvider.
type ClusterScmProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterScmProvider `json:"items"`
}

// ScmProviderRef is a Pipeline's spec.git.providerRef.
type ScmProviderRef struct {
	// Kind is ScmProvider (in the Pipeline's namespace, the default) or
	// ClusterScmProvider.
	// +kubebuilder:validation:Enum=ScmProvider;ClusterScmProvider
	// +kubebuilder:default=ScmProvider
	// +optional
	Kind string `json:"kind,omitempty"`

	// Name is the provider's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// ScmProviderIdentity is the provider a PromotionStep and its PRStatus use,
// as the translator resolved it when it built the Graph. The UID pins the
// object: a provider deleted and created again under the same name is not
// the one a PR was opened on, and the step fails instead of polling another
// SCM for its PR number.
type ScmProviderIdentity struct {
	// Kind is ScmProvider or ClusterScmProvider.
	// +kubebuilder:validation:Enum=ScmProvider;ClusterScmProvider
	Kind string `json:"kind"`
	// Name is the provider's name; a ScmProvider is in the step's namespace.
	Name string `json:"name"`
	// UID is the provider's metadata.uid when the Graph was built.
	UID string `json:"uid"`
}

func init() {
	SchemeBuilder.Register(&ScmProvider{}, &ScmProviderList{}, &ClusterScmProvider{}, &ClusterScmProviderList{})
}
