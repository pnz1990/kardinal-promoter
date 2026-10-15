// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Actor kinds (docs/design/17-actors.md, section 4.2).
const (
	ActorHuman      = "human"
	ActorAgent      = "agent"
	ActorBot        = "bot"
	ActorController = "controller"
	// ActorUnknown is a read-side value for records without a stamp; the
	// schema does not accept it in one.
	ActorUnknown = "unknown"
)

// Actor via values: how the controller came to write a record.
const (
	ViaUI           = "ui"
	ViaAPI          = "api"
	ViaSubscription = "subscription"
	ViaAutoRollback = "auto-rollback"
)

// Actor records who made a change: the kardinal.io/actor stamp. Kind,
// principal, agent, sponsor and via are verified: the chart's
// <release>-actor ValidatingAdmissionPolicy admits a stamp only when it says
// what request.userInfo resolves to through the AgentIndex, and only the
// controller may set via. Everything under asserted is written by the client
// and never trusted.
// +kubebuilder:validation:XValidation:rule="self.kind == 'agent' ? (has(self.agent) && has(self.sponsor)) : (!has(self.agent) && !has(self.sponsor))",message="actor.agent and actor.sponsor are set exactly when actor.kind is agent"
// +kubebuilder:validation:XValidation:rule="self.kind != 'controller' || has(self.via)",message="a controller actor says why it acted (actor.via)"
type Actor struct {
	// Kind is human, agent, bot or controller (docs/actors.md).
	// +kubebuilder:validation:Enum=human;agent;bot;controller
	// +kubebuilder:validation:MaxLength=16
	Kind string `json:"kind"`

	// Principal is the username the API server authenticated.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Principal string `json:"principal"`

	// Agent is the AgentIdentity name, when kind is agent.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Agent string `json:"agent,omitempty"`

	// Sponsor is the agent's sponsor (AgentIdentity spec.sponsor.user) when
	// the record was written, when kind is agent.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Sponsor string `json:"sponsor,omitempty"`

	// Via says how the controller came to write the record: ui and api for a
	// caller it authenticated, subscription and auto-rollback for its own
	// Bundles. Only the controller may set it.
	// +kubebuilder:validation:Enum=ui;api;subscription;auto-rollback
	// +kubebuilder:validation:MaxLength=16
	// +optional
	Via string `json:"via,omitempty"`

	// Asserted holds what the client says about itself. It is recorded as
	// written and never used for a security decision.
	// +optional
	Asserted *AssertedActor `json:"asserted,omitempty"`
}

// AssertedActor is what a client says about itself: not verified.
type AssertedActor struct {
	// Model is the AI model the client says it runs.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Model string `json:"model,omitempty"`

	// Session is the client's session or run identifier.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Session string `json:"session,omitempty"`

	// Client is the tool that wrote the record (for example kardinal/0.12.0).
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Client string `json:"client,omitempty"`
}

// AgentSubject is one principal that is the agent. (CEL escapes the
// keyword namespace as __namespace__ in rules.)
// +kubebuilder:validation:XValidation:rule="self.kind == 'ServiceAccount' ? has(self.__namespace__) : !has(self.__namespace__)",message="a ServiceAccount subject needs a namespace; User and Group subjects have none"
// +kubebuilder:validation:XValidation:rule="self.kind != 'Group' || !(self.name in ['system:authenticated', 'system:unauthenticated', 'system:serviceaccounts', 'system:masters'])",message="this group holds most of the cluster: it cannot be an agent"
type AgentSubject struct {
	// Kind is ServiceAccount, User (a username exactly as the API server
	// authenticates it, for example an OIDC user) or Group.
	// +kubebuilder:validation:Enum=ServiceAccount;User;Group
	// +kubebuilder:validation:MaxLength=16
	Kind string `json:"kind"`

	// Name is the ServiceAccount, user or group name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace is the ServiceAccount's namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// AgentSponsor is the person accountable for an agent.
// +kubebuilder:validation:XValidation:rule="!self.user.startsWith('system:')",message="the sponsor is a person: a ServiceAccount or built-in identity cannot sponsor an agent"
type AgentSponsor struct {
	// User is the sponsor's Kubernetes username.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	User string `json:"user"`

	// Team is the sponsor's team: shown and exported, not used by policy.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Team string `json:"team,omitempty"`
}

// AgentScope is where an agent may act. Each entry is an exact name, or a
// prefix ending in "*"; "*" alone matches everything.
type AgentScope struct {
	// Namespaces the agent may write in. Empty: nowhere.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=64
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9*])?$|^\*$`
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// Pipelines the agent may create Bundles for. Default: all.
	// +kubebuilder:default={"*"}
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^[^*]*\*?$`
	// +optional
	Pipelines []string `json:"pipelines,omitempty"`

	// Environments the agent may approve or override in. Default: all.
	// +kubebuilder:default={"*"}
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[^*]*\*?$`
	// +optional
	Environments []string `json:"environments,omitempty"`
}

// Autonomy values: the most an agent may do without a person.
const (
	AutonomyAuto    = "auto"
	AutonomyApprove = "approve"
	AutonomyDeny    = "deny"
)

// AgentIdentitySpec registers an agent (docs/actors.md).
// +kubebuilder:validation:XValidation:rule="!self.subjects.exists(s, s.kind == 'User' && s.name == self.sponsor.user)",message="an agent cannot sponsor itself: the sponsor is one of its User subjects"
type AgentIdentitySpec struct {
	// Description says what the agent does.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Description string `json:"description,omitempty"`

	// Subjects are the principals that are this agent.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Subjects []AgentSubject `json:"subjects"`

	// Sponsor is the person accountable for the agent.
	Sponsor AgentSponsor `json:"sponsor"`

	// Scope is where the agent may act.
	Scope AgentScope `json:"scope"`

	// Autonomy is the agent's ceiling: auto, approve (a person approves its
	// promotions) or deny. A Pipeline environment can only tighten it.
	// +kubebuilder:validation:Enum=auto;approve;deny
	// +kubebuilder:default=approve
	// +optional
	Autonomy string `json:"autonomy,omitempty"`

	// Suspended denies every write by the agent's subjects. An AgentIdentity
	// can be deleted only once it is suspended.
	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

// AgentIdentityStatus is the reconciler's verdict on the registration.
type AgentIdentityStatus struct {
	// Ready is true when the registration is valid and takes effect.
	// +optional
	Ready bool `json:"ready"`

	// Reason says why the AgentIdentity is not ready.
	// +optional
	Reason string `json:"reason,omitempty"`

	// ObservedGeneration is the generation the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready, with reasons Valid, SubjectConflict, InvalidSponsor
	// and IndexFull.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=agentid,categories=kardinal;kardinal-all
// +kubebuilder:printcolumn:name="Sponsor",type=string,JSONPath=`.spec.sponsor.user`
// +kubebuilder:printcolumn:name="Autonomy",type=string,JSONPath=`.spec.autonomy`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentIdentity registers an AI agent: the principals that are the agent,
// the person who answers for it, where it may act and how far on its own.
type AgentIdentity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentIdentitySpec   `json:"spec"`
	Status AgentIdentityStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentIdentityList contains a list of AgentIdentity.
type AgentIdentityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentIdentity `json:"items"`
}

// AgentIndexEntry is what admission needs to know about one subject.
type AgentIndexEntry struct {
	// Agent is the AgentIdentity name.
	Agent string `json:"agent"`
	// Sponsor is its sponsor's username.
	Sponsor string `json:"sponsor"`
	// Namespaces, Pipelines and Environments are its scope.
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`
	// +optional
	Pipelines []string `json:"pipelines,omitempty"`
	// +optional
	Environments []string `json:"environments,omitempty"`
	// Autonomy is its ceiling.
	// +optional
	Autonomy string `json:"autonomy,omitempty"`
	// Blocked, when set, says why the subject may not act: the
	// AgentIdentity is suspended or not ready.
	// +optional
	Blocked string `json:"blocked,omitempty"`
}

// AgentIndexStatus maps every registered subject to its agent.
type AgentIndexStatus struct {
	// ObservedIdentities is how many AgentIdentities the index was built from.
	// +optional
	ObservedIdentities int `json:"observedIdentities"`

	// Users maps a username (ServiceAccount subjects as
	// system:serviceaccount:<namespace>:<name>) to its agent.
	// +optional
	Users map[string]AgentIndexEntry `json:"users,omitempty"`

	// Groups maps a group to its agent.
	// +optional
	Groups map[string]AgentIndexEntry `json:"groups,omitempty"`

	// Conditions: Ready; IndexFull when an entry was left out to keep the
	// index under its size limit.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=agentindexes,scope=Cluster,shortName=agentidx,categories=kardinal-all
// +kubebuilder:printcolumn:name="Identities",type=integer,JSONPath=`.status.observedIdentities`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentIndex is the registry of agents in the shape admission reads: the
// <release>-actor ValidatingAdmissionPolicy binds it as params. The
// controller writes its status from the AgentIdentities; one per release,
// named after the release.
type AgentIndex struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Status AgentIndexStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentIndexList contains a list of AgentIndex.
type AgentIndexList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentIndex `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentIdentity{}, &AgentIdentityList{}, &AgentIndex{}, &AgentIndexList{})
}
