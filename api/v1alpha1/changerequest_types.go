// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Change request providers, modes and normalised states
// (docs/design/23-change-requests.md).
const (
	ChangeRequestServiceNow = "servicenow"
	ChangeRequestJira       = "jira"

	ChangeRequestModeOpen = "open"
	ChangeRequestModeLink = "link"

	ChangeRequestPending   = "pending"
	ChangeRequestApproved  = "approved"
	ChangeRequestRejected  = "rejected"
	ChangeRequestCancelled = "cancelled"
	ChangeRequestClosed    = "closed"
	ChangeRequestUnknown   = "unknown"
)

// ChangeRequestSpec defines a change request: a ticket in a ticketing system
// (ServiceNow, Jira) that kardinal opens, or links, for a promotion into an
// environment, and whose state a PolicyGate waits for
// (changeRequests.<name> in its expression).
//
// A template (perPromotion: true) is not itself a ticket: the promotion
// Graph creates one instance per Bundle and environment whose gates read
// changeRequests.<name>, once the environment's upstreams are Verified. Only
// the ChangeRequest reconciler calls the provider, and only it writes
// status.
// +kubebuilder:validation:XValidation:rule="self.provider != 'servicenow' || has(self.servicenow)",message="provider servicenow needs spec.servicenow"
// +kubebuilder:validation:XValidation:rule="self.provider != 'jira' || has(self.jira)",message="provider jira needs spec.jira"
// +kubebuilder:validation:XValidation:rule="self.mode != 'link' || has(self.link)",message="mode link needs spec.link: where the ticket key comes from"
type ChangeRequestSpec struct {
	// PerPromotion makes this ChangeRequest a template: the Graph creates an
	// instance of it per Bundle and environment whose gates read
	// changeRequests.<name>. A ChangeRequest without it is not used by a
	// promotion.
	// +optional
	PerPromotion bool `json:"perPromotion,omitempty"`

	// AppliesTo, when set, limits the environments that get an instance to
	// these (a fleet target counts as its fleet). Unset: every environment
	// whose gates read changeRequests.<name>.
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=63
	// +optional
	AppliesTo []string `json:"appliesTo,omitempty"`

	// Provider is the ticketing system.
	// +kubebuilder:validation:Enum=servicenow;jira
	Provider string `json:"provider"`

	// Mode is open (kardinal creates the ticket) or link (kardinal reads its
	// key from spec.link and tracks it, never creating one).
	// +kubebuilder:validation:Enum=open;link
	// +kubebuilder:default=open
	// +optional
	Mode string `json:"mode,omitempty"`

	// ServiceNow configures provider servicenow.
	// +optional
	ServiceNow *ServiceNowChangeRequest `json:"servicenow,omitempty"`

	// Jira configures provider jira.
	// +optional
	Jira *JiraChangeRequest `json:"jira,omitempty"`

	// Link says where mode link reads the ticket key.
	// +optional
	Link *ChangeRequestLink `json:"link,omitempty"`

	// PollInterval is how often the ticket's state is read: at least 30s,
	// default 1m. A webhook trigger (POST /webhook/changerequests/...) reads
	// it at once.
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
	// +kubebuilder:default="1m"
	// +optional
	PollInterval string `json:"pollInterval,omitempty"`

	// Timeout is how long after the instance's first read status.timedOut
	// becomes true; the gate's expression decides what that means. Default
	// 24h; "0" for none.
	// +kubebuilder:validation:Pattern=`^(0|([0-9]+(\.[0-9]+)?(ms|s|m|h))+)$`
	// +kubebuilder:default="24h"
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// Suspend stops reading the ticket (the Graph sets it on an instance
	// once its Bundle stops promoting). The last state stays in status and
	// goes stale.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// ChangeRequestAuth is how a provider request authenticates.
type ChangeRequestAuth struct {
	// CredentialsSecretRef names the Secret key that holds the credential:
	// a token, or "user:password" for basic. The Secret must be in the
	// ChangeRequest's namespace and labelled kardinal.io/referenceable=true.
	CredentialsSecretRef SecretKeyRef `json:"credentialsSecretRef"`

	// Scheme is basic (the key holds user:password, or Jira Cloud's
	// email:api-token) or bearer (an OAuth token, or a Jira Data Center PAT).
	// +kubebuilder:validation:Enum=basic;bearer
	// +kubebuilder:default=basic
	// +optional
	Scheme string `json:"scheme,omitempty"`
}

// ServiceNowChangeRequest configures provider servicenow.
type ServiceNowChangeRequest struct {
	// InstanceURL is the ServiceNow instance, https://<instance>.service-now.com.
	// +kubebuilder:validation:Pattern=`^https://[^/?#@]+/?$`
	// +kubebuilder:validation:MaxLength=253
	InstanceURL string `json:"instanceURL"`

	ChangeRequestAuth `json:",inline"`

	// Type is the change model of the Change Management API: normal,
	// standard or emergency.
	// +kubebuilder:validation:Enum=normal;standard;emergency
	// +kubebuilder:default=normal
	// +optional
	Type string `json:"type,omitempty"`

	// Template are the fields the change request is created with
	// (short_description, assignment_group, ...): string values, with the
	// placeholders of a per-promotion MetricCheck ({{ bundle.version }},
	// {{ pipeline.name }}, {{ environment.name }}, ...).
	// +kubebuilder:validation:MaxProperties=32
	// +optional
	Template map[string]string `json:"template,omitempty"`

	// States maps state values of the change_request state field (the raw
	// value, -2, or its display name, Scheduled) to kardinal states, before
	// the default Change Management state model (docs/change-requests.md).
	// +kubebuilder:validation:MaxProperties=32
	// +kubebuilder:validation:XValidation:rule="self.all(k, self[k] in ['pending', 'approved', 'rejected', 'cancelled', 'closed', 'unknown'])",message="a state maps to pending, approved, rejected, cancelled, closed or unknown"
	// +optional
	States map[string]string `json:"states,omitempty"`
}

// JiraChangeRequest configures provider jira.
type JiraChangeRequest struct {
	// BaseURL is the Jira site, https://<site>.atlassian.net or a Data
	// Center base URL.
	// +kubebuilder:validation:Pattern=`^https://[^?#@]+$`
	// +kubebuilder:validation:MaxLength=253
	BaseURL string `json:"baseURL"`

	ChangeRequestAuth `json:",inline"`

	// Project is the key of the project issues are opened in.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Project string `json:"project,omitempty"`

	// IssueType is the issue type opened (Change, Task, ...).
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:default=Task
	// +optional
	IssueType string `json:"issueType,omitempty"`

	// Template are the fields the issue is created with (summary,
	// description), with the same placeholders as ServiceNow's.
	// +kubebuilder:validation:MaxProperties=32
	// +optional
	Template map[string]string `json:"template,omitempty"`

	// ApprovedStatuses are the status names that mean approved; when unset,
	// a status in ApprovedStatusCategory does.
	// +kubebuilder:validation:MaxItems=32
	// +optional
	ApprovedStatuses []string `json:"approvedStatuses,omitempty"`

	// ApprovedStatusCategory is the status category (new, indeterminate,
	// done) that means approved when ApprovedStatuses is unset.
	// +kubebuilder:validation:Enum=new;indeterminate;done
	// +kubebuilder:default=done
	// +optional
	ApprovedStatusCategory string `json:"approvedStatusCategory,omitempty"`

	// RejectedStatuses are the status names that mean rejected.
	// +kubebuilder:validation:MaxItems=32
	// +optional
	RejectedStatuses []string `json:"rejectedStatuses,omitempty"`

	// CancelledResolutions are the resolution names that mean cancelled
	// (default: Won't Do).
	// +kubebuilder:validation:MaxItems=32
	// +optional
	CancelledResolutions []string `json:"cancelledResolutions,omitempty"`
}

// Where a linked change request's key comes from.
const (
	ChangeRequestLinkApprovalField    = "approvalField"
	ChangeRequestLinkBundleLabel      = "bundleLabel"
	ChangeRequestLinkBundleAnnotation = "bundleAnnotation"
)

// ChangeRequestLink says where mode link reads the ticket key, and how it is
// checked.
type ChangeRequestLink struct {
	// From is approvalField (a structured approval field of the
	// environment's counted approvals), bundleLabel or bundleAnnotation.
	// +kubebuilder:validation:Enum=approvalField;bundleLabel;bundleAnnotation
	From string `json:"from"`

	// Name is the approval field, label or annotation.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=317
	Name string `json:"name"`

	// RequireField, when set, verifies a linked ticket that does not carry
	// the promotion's correlation id: its field Name must equal Value (for
	// example cmdb_ci), it must have been created after the Bundle, and no
	// other ChangeRequest may hold it since earlier. Without it, a ticket is
	// verified only when it carries the correlation id; otherwise
	// status.verified is false.
	// +optional
	RequireField *ChangeRequestFieldMatch `json:"requireField,omitempty"`
}

// ChangeRequestFieldMatch is a ticket field and the value it must have.
type ChangeRequestFieldMatch struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=1024
	Value string `json:"value"`
}

// ChangeRequestTicket identifies a ticket.
type ChangeRequestTicket struct {
	// Key is the ServiceNow number (CHG0031337) or the Jira issue key (CHG-42).
	Key string `json:"key"`
	// ID is the ServiceNow sys_id or the Jira issue id.
	// +optional
	ID string `json:"id,omitempty"`
	// URL is the ticket's page.
	// +optional
	URL string `json:"url,omitempty"`
}

// ChangeRequestApprover is one approver's decision, when the provider
// exposes it.
type ChangeRequestApprover struct {
	Name string `json:"name"`
	// Decision is the provider's (approved, rejected, requested).
	// +optional
	Decision string `json:"decision,omitempty"`
	// +optional
	At *metav1.Time `json:"at,omitempty"`
}

// ChangeRequestPendingCreate is a create the reconciler is about to send,
// written to status before it is sent (an outbox): a reconcile that finds it
// searches by the correlation id before it creates, so a lost response or a
// crash never opens a second ticket.
type ChangeRequestPendingCreate struct {
	// CorrelationID is the id the create carries.
	CorrelationID string `json:"correlationID"`
	// Since is when the create was first attempted.
	Since metav1.Time `json:"since"`
}

// ChangeRequestStatus is written by the ChangeRequest reconciler only.
type ChangeRequestStatus struct {
	// ObservedGeneration is the generation of the spec last acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Ready is true once the reconciler has a ticket and has read its state.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// Ticket is the ticket the instance tracks.
	// +optional
	Ticket *ChangeRequestTicket `json:"ticket,omitempty"`

	// State is the normalised state: pending, approved, rejected, cancelled,
	// closed or unknown.
	// +kubebuilder:validation:Enum=pending;approved;rejected;cancelled;closed;unknown
	// +optional
	State string `json:"state,omitempty"`

	// ProviderState is the raw state, for people.
	// +optional
	ProviderState string `json:"providerState,omitempty"`

	// Approvers are the ticket's approvers, when the provider exposes them.
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Approvers []ChangeRequestApprover `json:"approvers,omitempty"`

	// CorrelationID is the promotion's id written into the ticket
	// (kardinal:<namespace>/<bundle>/<environment>/<bundle UID>).
	// +optional
	CorrelationID string `json:"correlationID,omitempty"`

	// Verified, for a linked ticket, reports whether it is this
	// promotion's: it carries the correlation id, or it satisfies
	// link.requireField, was created after the Bundle, and no other
	// ChangeRequest holds it since earlier. An unverified link is tracked but
	// says so (VerifiedReason).
	// +optional
	Verified *bool `json:"verified,omitempty"`

	// VerifiedReason says why a linked ticket is, or is not, verified.
	// +optional
	VerifiedReason string `json:"verifiedReason,omitempty"`

	// LinkedAt is when this instance linked its ticket key; of two
	// ChangeRequests that link one ticket, the earlier keeps it.
	// +optional
	LinkedAt *metav1.Time `json:"linkedAt,omitempty"`

	// PendingCreate is a create in flight (the outbox, see
	// ChangeRequestPendingCreate).
	// +optional
	PendingCreate *ChangeRequestPendingCreate `json:"pendingCreate,omitempty"`

	// StartedAt is the instance's first reconcile: the timeout counts from it.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// ObservedAt is the last successful read of the ticket.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`

	// ValidUntil is ObservedAt plus twice pollInterval: a gate evaluated
	// after it reads changeRequests.<name>.stale as true.
	// +optional
	ValidUntil *metav1.Time `json:"validUntil,omitempty"`

	// TimedOut is true once spec.timeout has passed since StartedAt.
	// +optional
	TimedOut bool `json:"timedOut,omitempty"`

	// RetryAt, after a provider error or a 429, is when the next request is
	// sent.
	// +optional
	RetryAt *metav1.Time `json:"retryAt,omitempty"`

	// Conditions: Ready, Stale, CredentialsInvalid, Refused (egress) and
	// ProviderError.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// PendingAuditEvents are ChangeRequestOpened, ChangeRequestLinked,
	// ChangeRequestApproved and ChangeRequestRejected records not yet written
	// (the audit outbox).
	// +optional
	PendingAuditEvents []PendingAuditEvent `json:"pendingAuditEvents,omitempty"`
}

// ChangeRequest is a change request a promotion waits for
// (docs/design/23-change-requests.md).
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories=kardinal;kardinal-all
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Ticket",type=string,JSONPath=`.status.ticket.key`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ChangeRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChangeRequestSpec   `json:"spec,omitempty"`
	Status ChangeRequestStatus `json:"status,omitempty"`
}

// ChangeRequestList contains a list of ChangeRequest.
// +kubebuilder:object:root=true
type ChangeRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChangeRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ChangeRequest{}, &ChangeRequestList{})
}
