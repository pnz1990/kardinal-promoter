// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NotificationHookEventType enumerates the events that can trigger a NotificationHook delivery.
// +kubebuilder:validation:Enum=Bundle.Verified;Bundle.Failed;Bundle.Superseded;Bundle.RollbackStarted;Bundle.RolledBack;PolicyGate.Blocked;PolicyGate.Unblocked;PromotionStep.Failed;PromotionStep.PROpened;PromotionStep.WaitingForApproval
type NotificationHookEventType string

const (
	// NotificationEventBundleVerified fires when a Bundle transitions to Phase=Verified.
	NotificationEventBundleVerified NotificationHookEventType = "Bundle.Verified"
	// NotificationEventBundleFailed fires when a Bundle transitions to Phase=Failed.
	NotificationEventBundleFailed NotificationHookEventType = "Bundle.Failed"
	// NotificationEventBundleSuperseded fires when a Bundle transitions to
	// Phase=Superseded: a newer Bundle of the same Pipeline and type replaced it.
	NotificationEventBundleSuperseded NotificationHookEventType = "Bundle.Superseded"
	// NotificationEventBundleRollbackStarted fires when a rollback Bundle
	// (spec.provenance.rollbackOf set) starts promoting.
	NotificationEventBundleRollbackStarted NotificationHookEventType = "Bundle.RollbackStarted"
	// NotificationEventBundleRolledBack fires when a rollback Bundle reaches
	// Phase=Verified: the environment runs the restored Bundle's artifacts.
	NotificationEventBundleRolledBack NotificationHookEventType = "Bundle.RolledBack"
	// NotificationEventPolicyGateBlocked fires when a PolicyGate transitions to ready=false.
	// Only the first block per evaluation session is delivered (not every re-eval).
	NotificationEventPolicyGateBlocked NotificationHookEventType = "PolicyGate.Blocked"
	// NotificationEventPolicyGateUnblocked fires when a PolicyGate instance
	// that was blocking allows again (Ready condition reason Unblocked). A gate
	// that allows on its first evaluation does not fire it.
	NotificationEventPolicyGateUnblocked NotificationHookEventType = "PolicyGate.Unblocked"
	// NotificationEventPromotionStepFailed fires when a PromotionStep transitions to state=Failed.
	NotificationEventPromotionStepFailed NotificationHookEventType = "PromotionStep.Failed"
	// NotificationEventPromotionStepPROpened fires when a PromotionStep opens
	// its pull request (status.prURL set).
	NotificationEventPromotionStepPROpened NotificationHookEventType = "PromotionStep.PROpened"
	// NotificationEventPromotionStepWaitingForApproval fires when a
	// PromotionStep enters state=WaitingForMerge: its PR waits for a human to
	// review and merge it (approval: pr-review).
	NotificationEventPromotionStepWaitingForApproval NotificationHookEventType = "PromotionStep.WaitingForApproval"
)

// NotificationHookFormat is the shape of the body a NotificationHook POSTs.
// +kubebuilder:validation:Enum=json;slack;teams;template;cloudevents
type NotificationHookFormat string

const (
	// NotificationFormatJSON is the kardinal JSON payload (docs/notifications.md#webhook-payload).
	NotificationFormatJSON NotificationHookFormat = "json"
	// NotificationFormatSlack is a Slack incoming-webhook message with Block Kit blocks.
	NotificationFormatSlack NotificationHookFormat = "slack"
	// NotificationFormatTeams is a Microsoft Teams Workflows webhook message
	// carrying an Adaptive Card.
	NotificationFormatTeams NotificationHookFormat = "teams"
	// NotificationFormatTemplate renders spec.template.body, a Go text/template,
	// over the event.
	NotificationFormatTemplate NotificationHookFormat = "template"
	// NotificationFormatCloudEvents is the kardinal JSON payload as the data of
	// a CloudEvents 1.0 event in structured content mode
	// (application/cloudevents+json).
	NotificationFormatCloudEvents NotificationHookFormat = "cloudevents"
)

// NotificationSecretRef names a Secret in the NotificationHook's namespace
// that holds the webhook credentials.
type NotificationSecretRef struct {
	// Name of the Secret. The key "authorization" is sent as the
	// Authorization header; the key "url", when present, is the webhook URL
	// and takes precedence over spec.webhook.url (Slack and Teams URLs carry
	// their token in the path). At least one of the two keys must be set.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// NotificationWebhookConfig describes a single HTTP webhook endpoint.
// +kubebuilder:validation:XValidation:rule="(has(self.url) && size(self.url) > 0) || has(self.secretRef)",message="webhook: set url, or secretRef with a url key"
// +kubebuilder:validation:XValidation:rule="!(has(self.authorizationHeader) && size(self.authorizationHeader) > 0 && has(self.secretRef))",message="webhook: authorizationHeader and secretRef are mutually exclusive; move the header into the Secret's authorization key"
type NotificationWebhookConfig struct {
	// URL is the HTTPS URL to POST the notification payload to. Optional when
	// secretRef names a Secret with a url key, which takes precedence.
	// +optional
	URL string `json:"url,omitempty"`

	// AuthorizationHeader is the value of the Authorization header to include in the POST.
	// Typically "Bearer <token>" or "Token <secret>".
	//
	// Deprecated: use secretRef. The value is stored in plain text in the spec
	// and sent as is: anyone who can read this NotificationHook can read it.
	// A hook that sets it still delivers, and has the condition
	// PlaintextCredential=True.
	// +optional
	AuthorizationHeader string `json:"authorizationHeader,omitempty"`

	// SecretRef names a Secret in the hook's namespace holding the
	// Authorization header value (key authorization) and/or the webhook URL
	// (key url). The Secret is read on every reconcile, so a rotated value is
	// used for the next delivery.
	// +optional
	SecretRef *NotificationSecretRef `json:"secretRef,omitempty"`
}

// NotificationSigning configures HMAC signatures on a hook's requests.
type NotificationSigning struct {
	// SecretRef names the Secret, in the hook's namespace and labeled
	// kardinal.io/referenceable=true, whose key holds the signing key.
	SecretRef NotificationSigningSecretRef `json:"secretRef"`
}

// NotificationSigningSecretRef names a key of a Secret.
type NotificationSigningSecretRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// Key of the signing key in the Secret. Defaults to signing-key.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Key string `json:"key,omitempty"`
}

// NotificationTemplate is a user-defined request body.
type NotificationTemplate struct {
	// Body is a Go text/template rendered over the event
	// (docs/notifications.md#templated-body). range, define, template and
	// block are not allowed; the rendered body is at most 64 KiB.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=16384
	Body string `json:"body"`

	// ContentType is the Content-Type header of the POST. Defaults to
	// application/json, in which case the rendered body must be valid JSON.
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9!#$&^_.+-]+/[a-zA-Z0-9!#$&^_.+-]+( *;.*)?$`
	// +optional
	ContentType string `json:"contentType,omitempty"`
}

// NotificationHookSpec defines the desired state of a NotificationHook.
// +kubebuilder:validation:XValidation:rule="(has(self.format) && self.format == 'template') == has(self.template)",message="template is required with format: template and not allowed with any other format"
type NotificationHookSpec struct {
	// Webhook defines the HTTP endpoint to deliver notifications to.
	// +kubebuilder:validation:Required
	Webhook NotificationWebhookConfig `json:"webhook"`

	// Events is the list of event types that trigger delivery.
	// At least one event type is required.
	// See docs/notifications.md#events.
	// +kubebuilder:validation:MinItems=1
	Events []NotificationHookEventType `json:"events"`

	// Format is the shape of the request body: json (the kardinal payload,
	// the default), slack (an incoming-webhook message with blocks), teams (a
	// Workflows webhook message with an Adaptive Card) or template
	// (spec.template).
	// +optional
	Format NotificationHookFormat `json:"format,omitempty"`

	// Template is the request body for format: template.
	// +optional
	Template *NotificationTemplate `json:"template,omitempty"`

	// Signing, when set, signs every request so the receiver can check that it
	// comes from this controller, unchanged and not replayed
	// (docs/notifications.md#signed-requests).
	// +optional
	Signing *NotificationSigning `json:"signing,omitempty"`

	// PipelineSelector restricts notifications to events originating from the named Pipeline.
	// When empty, events from all Pipelines are delivered.
	// +optional
	PipelineSelector string `json:"pipelineSelector,omitempty"`
}

// NotificationHookStatus defines the observed state of a NotificationHook.
type NotificationHookStatus struct {
	// LastSentAt is the RFC3339 timestamp of the last successful webhook delivery.
	// +optional
	LastSentAt string `json:"lastSentAt,omitempty"`

	// LastEvent is the event type of the last successfully delivered notification.
	// +optional
	LastEvent string `json:"lastEvent,omitempty"`

	// LastEventKey is a deterministic string identifying the last delivered event
	// (e.g. "Bundle.Verified/nginx-demo-abc123"). Idempotency uses
	// processedEventKeys; this field is informational.
	// +optional
	LastEventKey string `json:"lastEventKey,omitempty"`

	// ObservedGeneration is the hook generation the controller last reconciled.
	// Zero means the hook was never reconciled. On that first reconcile only the
	// newest qualifying event that already exists is delivered; older ones are
	// recorded as processed rather than backfilled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ProcessedEventKeys lists the keys of the qualifying events that were
	// delivered, or given up on after the retry limit. Each event is delivered
	// once. The list is pruned to events that still qualify, so it stays bounded.
	// +optional
	ProcessedEventKeys []string `json:"processedEventKeys,omitempty"`

	// FailedAttempts counts consecutive failed deliveries. The controller retries
	// with exponential backoff and gives up on an event after 10 attempts.
	// Reset to zero on a successful delivery.
	// +optional
	FailedAttempts int32 `json:"failedAttempts,omitempty"`

	// NextRetryAt is the RFC3339 time of the next delivery attempt after a
	// failed one. No webhook is sent before it, however often the hook is
	// reconciled. Cleared on a successful delivery, when the controller gives
	// up on the event, and when the hook's spec changes.
	// +optional
	NextRetryAt string `json:"nextRetryAt,omitempty"`

	// FailureMessage records the last webhook delivery failure, if any.
	// Cleared on next successful delivery.
	// +optional
	FailureMessage string `json:"failureMessage,omitempty"`

	// Conditions: Ready is False when the hook cannot deliver (its Secret is
	// missing or lacks a key, or its template does not parse); events wait
	// until it is fixed. PlaintextCredential is True while
	// spec.webhook.authorizationHeader is set.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nhook
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.webhook.url`
// +kubebuilder:printcolumn:name="Format",type=string,JSONPath=`.spec.format`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Last-Sent",type=string,JSONPath=`.status.lastSentAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NotificationHook defines an outbound webhook that is triggered when specific
// promotion events occur. The controller delivers a JSON payload to the configured
// URL on each qualifying event.
//
// Architecture: this CRD uses the Owned-node pattern — the reconciler watches
// Bundle, PolicyGate, and PromotionStep objects and writes delivery results to
// status. Each event is delivered at least once and, except after a crash
// between a POST and its status write, exactly once (status.processedEventKeys).
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
