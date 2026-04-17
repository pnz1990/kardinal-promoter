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

// AuditEventSpec defines the immutable record of a single promotion event.
// AuditEvents are written by the PromotionStep reconciler at key lifecycle
// transitions (started, succeeded, failed). They are append-only — the
// spec is set at creation and never mutated.
type AuditEventSpec struct {
	// Timestamp is when the event occurred (RFC 3339 format).
	// +kubebuilder:validation:Format=date-time
	Timestamp metav1.Time `json:"timestamp"`

	// BundleName is the name of the Bundle being promoted.
	// +kubebuilder:validation:MinLength=1
	BundleName string `json:"bundleName"`

	// PipelineName is the name of the Pipeline the Bundle is promoting through.
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`

	// Environment is the environment name where the event occurred.
	// +kubebuilder:validation:MinLength=1
	Environment string `json:"environment"`

	// Action is a short verb describing what happened.
	// Valid values: "PromotionStarted", "PromotionSucceeded", "PromotionFailed",
	//               "PromotionSuperseded", "RollbackStarted", "RollbackSucceeded",
	//               "HealthCheckFailed", "GateBlocked".
	// +kubebuilder:validation:Enum=PromotionStarted;PromotionSucceeded;PromotionFailed;PromotionSuperseded;RollbackStarted;RollbackSucceeded;HealthCheckFailed;GateBlocked
	Action string `json:"action"`

	// Actor is the identity that triggered the action.
	// For automated promotions this is the controller service account.
	// For human-initiated actions (rollback, override) this is the author from Bundle provenance.
	// +optional
	Actor string `json:"actor,omitempty"`

	// Outcome describes the result of the action.
	// Valid values: "Success", "Failure", "Pending".
	// +kubebuilder:validation:Enum=Success;Failure;Pending
	Outcome string `json:"outcome"`

	// Message is a human-readable description of the event.
	// +optional
	Message string `json:"message,omitempty"`

	// BundleImage is the container image tag being promoted, if applicable.
	// +optional
	BundleImage string `json:"bundleImage,omitempty"`
}

// AuditEvent is an immutable record of a single promotion event.
// It is written once by the PromotionStep reconciler and never updated.
// AuditEvents form an append-only log of all promotion activity across
// all pipelines.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ae;audit
// +kubebuilder:printcolumn:name="Pipeline",type="string",JSONPath=".spec.pipelineName"
// +kubebuilder:printcolumn:name="Bundle",type="string",JSONPath=".spec.bundleName"
// +kubebuilder:printcolumn:name="Environment",type="string",JSONPath=".spec.environment"
// +kubebuilder:printcolumn:name="Action",type="string",JSONPath=".spec.action"
// +kubebuilder:printcolumn:name="Outcome",type="string",JSONPath=".spec.outcome"
// +kubebuilder:printcolumn:name="Timestamp",type="string",JSONPath=".spec.timestamp"
type AuditEvent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AuditEventSpec `json:"spec,omitempty"`
}

// AuditEventList contains a list of AuditEvent.
// +kubebuilder:object:root=true
type AuditEventList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AuditEvent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AuditEvent{}, &AuditEventList{})
}
