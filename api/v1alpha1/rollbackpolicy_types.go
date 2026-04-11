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

// RollbackPolicySpec defines the desired state of a RollbackPolicy.
// RollbackPolicy objects are created per environment that has autoRollback
// configured. They are typically created by the Pipeline/Graph controller
// when a Bundle is promoted to an environment with AutoRollback enabled.
type RollbackPolicySpec struct {
	// PipelineName is the Pipeline this policy monitors.
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`

	// Environment is the environment this policy monitors.
	// +kubebuilder:validation:MinLength=1
	Environment string `json:"environment"`

	// BundleRef is the name of the Bundle being monitored.
	// When ConsecutiveHealthFailures on the associated PromotionStep reaches
	// FailureThreshold, a rollback Bundle is created from this Bundle's spec.
	// +kubebuilder:validation:MinLength=1
	BundleRef string `json:"bundleRef"`

	// FailureThreshold is the number of consecutive health-check failures
	// required to trigger a rollback. Defaults to 3 if <= 0.
	// +optional
	FailureThreshold int `json:"failureThreshold,omitempty"`
}

// RollbackPolicyStatus holds the observed state of the rollback policy.
// Written exclusively by the RollbackPolicyReconciler.
type RollbackPolicyStatus struct {
	// ShouldRollback is true when the failure threshold has been exceeded
	// and a rollback Bundle has been (or is being) created.
	// The Graph can read this field via a Watch node expression.
	// +optional
	ShouldRollback bool `json:"shouldRollback,omitempty"`

	// ConsecutiveFailures is the most recent consecutive health failure count
	// observed from the associated PromotionStep.
	// +optional
	ConsecutiveFailures int `json:"consecutiveFailures,omitempty"`

	// RollbackBundleName is the name of the rollback Bundle created when
	// ShouldRollback became true. Nil if no rollback has been triggered.
	// +optional
	RollbackBundleName *string `json:"rollbackBundleName,omitempty"`

	// LastEvaluatedAt is the timestamp of the most recent reconcile evaluation.
	// +optional
	LastEvaluatedAt *metav1.Time `json:"lastEvaluatedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=rbp
// +kubebuilder:printcolumn:name="ShouldRollback",type=boolean,JSONPath=`.status.shouldRollback`
// +kubebuilder:printcolumn:name="Failures",type=integer,JSONPath=`.status.consecutiveFailures`
// +kubebuilder:printcolumn:name="Threshold",type=integer,JSONPath=`.spec.failureThreshold`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RollbackPolicy monitors consecutive health-check failures on a PromotionStep
// and triggers an auto-rollback by creating a rollback Bundle when the
// failure threshold is exceeded.
//
// Architecture: the RollbackPolicyReconciler reads
// PromotionStep.status.consecutiveHealthFailures and writes
// status.shouldRollback (own CRD status). This makes the rollback
// decision observable by the Graph, eliminating PS-6 and PS-7 from
// docs/design/11-graph-purity-tech-debt.md.
type RollbackPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RollbackPolicySpec   `json:"spec,omitempty"`
	Status RollbackPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RollbackPolicyList contains a list of RollbackPolicy objects.
type RollbackPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RollbackPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RollbackPolicy{}, &RollbackPolicyList{})
}
