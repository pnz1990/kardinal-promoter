// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Agent budgets (docs/design/24-agent-budgets.md): limits on how much an
// agent changes before a person looks, kept on its AgentIdentity. The budget
// reconciler (pkg/reconciler/agentbudget, --agent-budgets) counts and grants
// in status.usage, and the PromotionStep reconciler holds a step until its
// grant; the chart turns them on with actors.budgets.enabled. "Planned" in a
// comment below marks what later v0.14 work adds.

// AgentBudgetRule limits an agent's promotions into the environments it
// matches. Every rule that matches a step applies. A limit that is not set
// does not apply; a rule sets at least one.
// +kubebuilder:validation:XValidation:rule="has(self.versionsPerDay) || has(self.maxConcurrent) || has(self.fleetTargetsPerHour) || has(self.maxUnavailable) || has(self.fleetShare)",message="a budget rule sets at least one limit: versionsPerDay, maxConcurrent, fleetTargetsPerHour, maxUnavailable or fleetShare"
type AgentBudgetRule struct {
	// Name identifies the rule (a DNS label), unique in the list.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Environments the rule applies to: exact names, or a prefix ending in
	// "*". A fleet target matches through its own name or its fleet's.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[^*]*\*?$`
	Environments []string `json:"environments"`

	// Namespaces the rule applies to, in the same form. Default: every
	// namespace.
	// +kubebuilder:default={"*"}
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=64
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9*])?$|^\*$`
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// VersionsPerDay (L1) is how many versions the agent may start into
	// these environments in any 24 hours. A version counts once per
	// environment; a fleet's targets are one environment.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=500
	// +optional
	VersionsPerDay int32 `json:"versionsPerDay,omitempty"`

	// MaxConcurrent (L2) is how many of the agent's promotions into these
	// environments may be under way at once.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	MaxConcurrent int32 `json:"maxConcurrent,omitempty"`

	// FleetTargetsPerHour (L3) is how many fleet targets the agent may start
	// promoting in any hour. Plain environments do not count.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	// +optional
	FleetTargetsPerHour int32 `json:"fleetTargetsPerHour,omitempty"`

	// MaxUnavailable (L4) is how many of the agent's promotions may have
	// failed while their environment still runs the failed version.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	MaxUnavailable int32 `json:"maxUnavailable,omitempty"`

	// FleetShare (L5) is the share of one fleet's targets one version of the
	// agent may have under way or failed at once, "1%" to "100%", rounded up
	// and at least one target. It applies to fleet targets only.
	// +kubebuilder:validation:MaxLength=4
	// +kubebuilder:validation:Pattern=`^([1-9][0-9]?|100)%$`
	// +optional
	FleetShare string `json:"fleetShare,omitempty"`
}

// AgentBudgetExemption is a break-glass exception to an agent's budgets: one
// Pipeline environment, optionally one version, until it expires. The chart's
// <release>-agent-budgets admission policy verifies createdBy and the actor
// stamp, that its maker may exempt the agent and is not of the agent's
// party, and that an existing exemption never changes
// (docs/design/24-agent-budgets.md section 12).
type AgentBudgetExemption struct {
	// Name identifies the exemption (a DNS label), unique in the list.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Namespace and Pipeline are the Pipeline let through.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Pipeline string `json:"pipeline"`

	// Environment is the environment, or the fleet environment, let through.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Environment string `json:"environment"`

	// Bundle, when set, limits the exemption to that version.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Bundle string `json:"bundle,omitempty"`

	// Reason says why (recorded in the BudgetExempted audit record).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Reason string `json:"reason"`

	// ExpiresAt ends the exemption. The controller also ends it 24 hours
	// after it first sees it.
	ExpiresAt metav1.Time `json:"expiresAt"`

	// CreatedBy is who made the exception: admission verifies it is the
	// requester's username.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	CreatedBy string `json:"createdBy"`

	// Actor is who made the exception, stamped as on freeze exemptions. An
	// exemption whose actor is an agent, or that has none, is never
	// honoured: break-glass is for people (docs/design/24-agent-budgets.md
	// section 11.3). The chart's actor admission policies verify it from the
	// requester's username and groups, and the controller counts a stamped
	// exemption only while that check is installed
	// (--agent-budget-exemption-policies).
	// +optional
	Actor *Actor `json:"actor,omitempty"`
}

// AgentBudgetCount is one limit's room: the limit and what is used of it.
type AgentBudgetCount struct {
	// Limit is the spec's limit.
	Limit int32 `json:"limit"`
	// Used is what counts against it now.
	Used int32 `json:"used"`
	// WindowStart is the start of the rolling window (rate limits only).
	// +optional
	WindowStart *metav1.Time `json:"windowStart,omitempty"`
	// FreesAt is when Used next drops (rate limits only).
	// +optional
	FreesAt *metav1.Time `json:"freesAt,omitempty"`
}

// AgentBudgetRuleUsage is one rule's usage, with the rule's matching copied
// from the spec so a reader needs only the status. A count is absent when
// its limit is not set.
type AgentBudgetRuleUsage struct {
	// Name is the rule's name.
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// Environments and Namespaces are the rule's, as the usage was counted.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=63
	// +optional
	Environments []string `json:"environments,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=64
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`
	// Versions counts versionsPerDay.
	// +optional
	Versions *AgentBudgetCount `json:"versions,omitempty"`
	// Concurrent counts maxConcurrent.
	// +optional
	Concurrent *AgentBudgetCount `json:"concurrent,omitempty"`
	// FleetTargets counts fleetTargetsPerHour.
	// +optional
	FleetTargets *AgentBudgetCount `json:"fleetTargets,omitempty"`
	// Unavailable counts maxUnavailable.
	// +optional
	Unavailable *AgentBudgetCount `json:"unavailable,omitempty"`
	// FleetShare is the rule's fleetShare: it is counted per fleet when a
	// target is granted.
	// +kubebuilder:validation:MaxLength=4
	// +optional
	FleetShare string `json:"fleetShare,omitempty"`
	// Waiting is how many requests the rule holds.
	// +optional
	Waiting int32 `json:"waiting,omitempty"`
	// Exhausted are the limits with no room left.
	// +kubebuilder:validation:MaxItems=5
	// +kubebuilder:validation:items:Enum=versionsPerDay;maxConcurrent;fleetTargetsPerHour;maxUnavailable;fleetShare
	// +optional
	Exhausted []string `json:"exhausted,omitempty"`
}

// AgentBudgetGrant is one promotion the ledger let through: a version into
// an environment. It names the Bundle by UID only, not its namespace,
// Pipeline or name (docs/design/24-agent-budgets.md section 15.1).
type AgentBudgetGrant struct {
	// BundleUID is the Bundle's UID.
	// +kubebuilder:validation:MaxLength=64
	BundleUID string `json:"bundleUID"`
	// Environment is the step's environment (a fleet target's own name).
	// +kubebuilder:validation:MaxLength=63
	Environment string `json:"environment"`
	// Fleet is the fleet environment of a fleet target.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Fleet string `json:"fleet,omitempty"`
	// Rules are the rules the grant counts against.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=63
	// +optional
	Rules []string `json:"rules,omitempty"`
	// GrantedAt is when the ledger granted it.
	GrantedAt metav1.Time `json:"grantedAt"`
	// Version is true when the grant counts in versionsPerDay: the first of
	// its Bundle into its environment (a fleet's targets are one).
	// +optional
	Version bool `json:"version,omitempty"`
	// Settled is true once the promotion no longer counts as under way.
	// +optional
	Settled bool `json:"settled,omitempty"`
	// Unavailable is true while it counts in maxUnavailable.
	// +optional
	Unavailable bool `json:"unavailable,omitempty"`
	// Seeded is true for a grant recorded from a promotion that started
	// before the ledger did.
	// +optional
	Seeded bool `json:"seeded,omitempty"`
	// Exemption is the exemption the grant used, if any.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Exemption string `json:"exemption,omitempty"`
}

// Reasons an exemption is ignored (AgentBudgetExemptionState.Ignored, and
// the ledger's ExemptionIgnored condition).
const (
	// ExemptionMadeByAgent: its actor stamp is an agent's. Break-glass is
	// for people.
	ExemptionMadeByAgent = "MadeByAgent"
	// ExemptionUnverified: it has no actor stamp, so nothing verified that a
	// person made it.
	ExemptionUnverified = "Unverified"
)

// AgentBudgetExemptionState is one spec.budgetExemptions entry as the
// ledger's writer saw it: when it first saw it, and until when it counts. It
// stays listed after it expires, until it is removed from the spec, so the
// 24-hour cap cannot be restarted.
type AgentBudgetExemptionState struct {
	// Name is the exemption's name.
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// FirstSeenAt is when the controller first saw it.
	FirstSeenAt metav1.Time `json:"firstSeenAt"`
	// ExpiresAt is when it stops counting: its expiresAt, or earlier with
	// the controller's 24-hour cap. An ignored exemption never counts; its
	// expiresAt is its firstSeenAt.
	ExpiresAt metav1.Time `json:"expiresAt"`
	// Ignored, when set, says why the exemption never counts: MadeByAgent
	// (its actor stamp is an agent's) or Unverified (it has no stamp).
	// +kubebuilder:validation:Enum=MadeByAgent;Unverified
	// +optional
	Ignored string `json:"ignored,omitempty"`
}

// AgentUsage is one ledger: an installation's count of an agent's
// promotions against its budgets, written by that installation's budget
// reconciler only.
type AgentUsage struct {
	// ObservedGeneration is the spec generation this entry was computed for.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// EvaluatedAt is the writer's clock when it wrote the entry.
	// +optional
	EvaluatedAt *metav1.Time `json:"evaluatedAt,omitempty"`
	// NextEvaluationAt is when the writer evaluates the entry again: when a
	// count next drops.
	// +optional
	NextEvaluationAt *metav1.Time `json:"nextEvaluationAt,omitempty"`
	// Rules are the usage of each budget rule.
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	// +optional
	Rules []AgentBudgetRuleUsage `json:"rules,omitempty"`
	// Grants are the ledger: the promotions some window or state still
	// counts, at most 2,000.
	// +kubebuilder:validation:MaxItems=2000
	// +optional
	Grants []AgentBudgetGrant `json:"grants,omitempty"`
	// Exemptions are the spec's exemptions as the writer saw them: the ones
	// in effect, the expired ones and the ignored ones.
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=name
	// +optional
	Exemptions []AgentBudgetExemptionState `json:"exemptions,omitempty"`
	// Conditions of the ledger: Current, LedgerFull. Budget conditions are
	// kept here, never in the AgentIdentity's status.conditions.
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PromotionStepBudget is a step's place against its agent's budgets,
// written by the PromotionStep reconciler only.
type PromotionStepBudget struct {
	// Agent is the AgentIdentity whose budgets hold the step.
	// +kubebuilder:validation:MaxLength=253
	Agent string `json:"agent"`
	// State is Waiting (held for room) or Granted.
	// +kubebuilder:validation:Enum=Waiting;Granted
	State string `json:"state"`
	// Since is when the step started waiting: its place in the queue.
	// +optional
	Since *metav1.Time `json:"since,omitempty"`
	// Rules are the budget rules that match the step.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=63
	// +optional
	Rules []string `json:"rules,omitempty"`
	// Exemption is the exemption the grant used, if any.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Exemption string `json:"exemption,omitempty"`
}
