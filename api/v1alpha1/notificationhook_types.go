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

// NotificationHookEventType enumerates the events that can trigger a NotificationHook delivery.
// +kubebuilder:validation:Enum=Bundle.Verified;Bundle.Failed;PolicyGate.Blocked;PromotionStep.Failed
type NotificationHookEventType string

const (
	// NotificationEventBundleVerified fires when a Bundle transitions to Phase=Verified.
	NotificationEventBundleVerified NotificationHookEventType = "Bundle.Verified"
	// NotificationEventBundleFailed fires when a Bundle transitions to Phase=Failed.
	NotificationEventBundleFailed NotificationHookEventType = "Bundle.Failed"
	// NotificationEventPolicyGateBlocked fires when a PolicyGate transitions to ready=false.
	// Only the first block per evaluation session is delivered (not every re-eval).
	NotificationEventPolicyGateBlocked NotificationHookEventType = "PolicyGate.Blocked"
	// NotificationEventPromotionStepFailed fires when a PromotionStep transitions to state=Failed.
	NotificationEventPromotionStepFailed NotificationHookEventType = "PromotionStep.Failed"
)

// NotificationWebhookConfig describes a single HTTP webhook endpoint.
type NotificationWebhookConfig struct {
	// URL is the HTTPS URL to POST the notification payload to.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// AuthorizationHeader is the value of the Authorization header to include in the POST.
	// Typically "Bearer <token>" or "Token <secret>".
	// Store sensitive values in a Kubernetes Secret and reference it via envFrom if needed.
	// +optional
	AuthorizationHeader string `json:"authorizationHeader,omitempty"`
}

// NotificationHookSpec defines the desired state of a NotificationHook.
type NotificationHookSpec struct {
	// Webhook defines the HTTP endpoint to deliver notifications to.
	// +kubebuilder:validation:Required
	Webhook NotificationWebhookConfig `json:"webhook"`

	// Events is the list of event types that trigger delivery.
	// At least one event type is required.
	// Valid values: Bundle.Verified, Bundle.Failed, PolicyGate.Blocked, PromotionStep.Failed.
	// +kubebuilder:validation:MinItems=1
	Events []NotificationHookEventType `json:"events"`

	// PipelineSelector restricts notifications to events originating from the named Pipeline.
	// When empty, events from all Pipelines are delivered.
	// +optional
	PipelineSelector string `json:"pipelineSelector,omitempty"`
}

// NotificationHookStatus defines the observed state of a NotificationHook.
type NotificationHookStatus struct {
	// LastSentAt is the RFC3339 timestamp of the last successful webhook delivery.
	// Used for idempotency: the reconciler will not re-deliver the same event if
	// lastEvent and lastSentAt match the current event key.
	// +optional
	LastSentAt string `json:"lastSentAt,omitempty"`

	// LastEvent is the event type of the last successfully delivered notification.
	// +optional
	LastEvent string `json:"lastEvent,omitempty"`

	// LastEventKey is a deterministic string identifying the last delivered event
	// (e.g. "Bundle.Verified/nginx-demo-abc123"). Used for idempotency.
	// +optional
	LastEventKey string `json:"lastEventKey,omitempty"`

	// FailureMessage records the last webhook delivery failure, if any.
	// Cleared on next successful delivery.
	// +optional
	FailureMessage string `json:"failureMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nhook
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.webhook.url`
// +kubebuilder:printcolumn:name="Events",type=string,JSONPath=`.spec.events`
// +kubebuilder:printcolumn:name="Last-Sent",type=string,JSONPath=`.status.lastSentAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NotificationHook defines an outbound webhook that is triggered when specific
// promotion events occur. The controller delivers a JSON payload to the configured
// URL on each qualifying event.
//
// Architecture: this CRD uses the Owned-node pattern — the reconciler watches
// Bundle, PolicyGate, and PromotionStep objects and writes delivery results to
// status. HTTP calls are made at-most-once per event (idempotent via LastEventKey).
type NotificationHook struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NotificationHookSpec   `json:"spec,omitempty"`
	Status NotificationHookStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NotificationHookList contains a list of NotificationHook.
type NotificationHookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NotificationHook `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NotificationHook{}, &NotificationHookList{})
}
