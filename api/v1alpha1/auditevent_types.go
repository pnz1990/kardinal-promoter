// Copyright 2026 The kardinal-promoter Authors.
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

// AuditEvent is an immutable record of a significant promotion lifecycle event.
// Events are written by the PromotionStep reconciler at key state transitions
// (pending → promoting, verified, failed). They are never updated after creation.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ae
// +kubebuilder:printcolumn:name="Bundle",type=string,JSONPath=".spec.bundleName"
// +kubebuilder:printcolumn:name="Environment",type=string,JSONPath=".spec.environment"
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=".spec.action"
// +kubebuilder:printcolumn:name="Outcome",type=string,JSONPath=".spec.outcome"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type AuditEvent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AuditEventSpec `json:"spec"`
}

// AuditEventSpec holds the immutable event data.
// Fields must not be updated after creation — AuditEvent is append-only.
type AuditEventSpec struct {
	// Timestamp is the wall-clock time of the event in RFC3339 format.
	// +kubebuilder:validation:Required
	Timestamp string `json:"timestamp"`

	// PipelineName is the name of the Pipeline CRD that this promotion belongs to.
	// +kubebuilder:validation:Required
	PipelineName string `json:"pipelineName"`

	// BundleName is the name of the Bundle CRD being promoted.
	// +kubebuilder:validation:Required
	BundleName string `json:"bundleName"`

	// Environment is the target environment name.
	// +kubebuilder:validation:Required
	Environment string `json:"environment"`

	// Action describes the lifecycle event that was observed.
	// +kubebuilder:validation:Enum=PromotionStarted;PromotionVerified;PromotionFailed;RollbackStarted;RollbackVerified;GateBlocked;GateApproved;Override
	// +kubebuilder:validation:Required
	Action string `json:"action"`

	// Actor is the identity that triggered this event.
	// For automated actions: "controller". For human overrides: the username.
	// +optional
	Actor string `json:"actor,omitempty"`

	// Outcome summarises the result: Success, Failure, or Blocked.
	// +kubebuilder:validation:Enum=Success;Failure;Blocked
	// +optional
	Outcome string `json:"outcome,omitempty"`

	// Message is a human-readable description of the event, including
	// reasons for failure or blocking.
	// +optional
	Message string `json:"message,omitempty"`

	// PRNumber is the pull request number, if the event involved a PR.
	// +optional
	PRNumber int `json:"prNumber,omitempty"`

	// PRUrl is the URL of the pull request, if applicable.
	// +optional
	PRUrl string `json:"prUrl,omitempty"`

	// PromotionStepRef is the name of the PromotionStep CRD that generated this event.
	// +optional
	PromotionStepRef string `json:"promotionStepRef,omitempty"`
}

// AuditEventList contains a list of AuditEvent objects.
// +kubebuilder:object:root=true
type AuditEventList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AuditEvent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AuditEvent{}, &AuditEventList{})
}
