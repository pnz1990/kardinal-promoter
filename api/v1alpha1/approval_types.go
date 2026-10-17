// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ApprovalSpec is one person's decision on one Bundle in one environment.
// An Approval is created by kardinal approve and never changed: delete it to
// revoke the decision. The chart's ValidatingAdmissionPolicy admits it only
// when spec.user is the requesting user, spec.groups are among the
// requester's groups, and the kardinal.io/bundle and kardinal.io/environment
// labels match spec.bundle and spec.environment.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="an Approval is immutable: delete it and create a new one"
type ApprovalSpec struct {
	// Bundle is the name of the Bundle the decision is about, in the
	// Approval's namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Bundle string `json:"bundle"`

	// BundleUID is the UID of that Bundle: the Graph counts the Approval
	// only for the Bundle with this UID, so an Approval cannot carry over to
	// a new Bundle that reuses the name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	BundleUID string `json:"bundleUID"`

	// Environment is the Pipeline environment the decision is for.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Environment string `json:"environment"`

	// User is the Kubernetes username of the approver, as the API server
	// authenticates them (kubectl auth whoami).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	User string `json:"user"`

	// Groups are the approver's Kubernetes groups that count for the gate's
	// approval.allowedGroups. Each must be one of the requester's groups.
	// +kubebuilder:default={}
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	// +optional
	Groups []string `json:"groups"`

	// Decision is approve, or reject: a reject from an allowed approver
	// blocks the gate whatever the other approvals.
	// +kubebuilder:validation:Enum=approve;reject
	// +kubebuilder:default=approve
	Decision string `json:"decision"`

	// Fields are the structured values the decision carries: the ones the
	// gate's approval.fields declares (a ticket ID, an acknowledgement, a
	// rollback plan URL). An approve that does not satisfy a declared field
	// does not count, and the gate says why (docs/design/23-change-requests.md
	// section 8). They are the approver's statements, verified as theirs,
	// not as true.
	// +kubebuilder:validation:MaxProperties=16
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.matches('^[a-zA-Z][a-zA-Z0-9_]{0,62}$'))",message="a field name is a letter then letters, digits or underscores, at most 63 characters"
	// +kubebuilder:validation:XValidation:rule="self.all(k, size(self[k]) <= 1024)",message="a field value is at most 1024 bytes"
	// +optional
	Fields map[string]string `json:"fields,omitempty"`

	// Actor is who decided (docs/actors.md): the verified kardinal.io/actor
	// stamp. Its principal equals spec.user.
	// +optional
	Actor *Actor `json:"actor,omitempty"`

	// Comment is a free-form note shown with the decision.
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:default=""
	// +optional
	Comment string `json:"comment"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=appr,categories=kardinal-all
// +kubebuilder:printcolumn:name="Bundle",type=string,JSONPath=`.spec.bundle`
// +kubebuilder:printcolumn:name="Environment",type=string,JSONPath=`.spec.environment`
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.user`
// +kubebuilder:printcolumn:name="Decision",type=string,JSONPath=`.spec.decision`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Approval records that a person approved, or rejected, a Bundle for an
// environment. The promotion Graph copies the Approvals of its Bundle into the
// approval gates of that environment (PolicyGate spec.approvals), and the
// PolicyGate reconciler counts them against spec.approval.
type Approval struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ApprovalSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// ApprovalList contains a list of Approval.
type ApprovalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Approval `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Approval{}, &ApprovalList{})
}
