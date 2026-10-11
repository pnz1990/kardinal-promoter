// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

// Request and response bodies of the UI API's write endpoints. They are named
// types, not anonymous structs, so the OpenAPI spec (openapi.json,
// docs/reference/rest-api.md) is generated from them.

// uiPromoteRequest is the body of POST /api/v1/ui/promote.
type uiPromoteRequest struct {
	// Pipeline is the Pipeline to promote in.
	Pipeline string `json:"pipeline"`
	// Environment is the environment to promote to; its upstreams must have
	// a Verified Bundle.
	Environment string `json:"environment"`
	// Namespace of the Pipeline. Defaults to "default".
	Namespace string `json:"namespace,omitempty"`
}

// uiPromoteResponse is the 201 response of POST /api/v1/ui/promote.
type uiPromoteResponse struct {
	// Bundle is the promotion Bundle created.
	Bundle string `json:"bundle"`
	// Source is the Verified Bundle whose artifacts it promotes.
	Source string `json:"source"`
	// Message describes the promotion.
	Message string `json:"message"`
}

// uiApprovalRequest is the body of POST /api/v1/ui/approvals.
type uiApprovalRequest struct {
	// Bundle is the Bundle decided on.
	Bundle string `json:"bundle"`
	// Environment is the environment whose approval gates count the decision.
	Environment string `json:"environment"`
	// Namespace of the Bundle. Defaults to "default".
	Namespace string `json:"namespace,omitempty"`
	// Decision is "approve" or "reject". Ignored with revoke.
	Decision string `json:"decision,omitempty"`
	// Comment is a note shown with the decision.
	Comment string `json:"comment,omitempty"`
	// Revoke deletes the user's decision instead of recording one.
	Revoke bool `json:"revoke,omitempty"`
}

// uiApprovalResponse is the response of POST /api/v1/ui/approvals.
type uiApprovalResponse struct {
	// Outcome is "Recorded", "Already recorded" or "Revoked".
	Outcome string `json:"outcome"`
	// Approval is the name of the Approval recorded or deleted.
	Approval string `json:"approval"`
	// User is who the decision is recorded for: the authenticated UI user.
	User string `json:"user"`
	// Message describes what was done.
	Message string `json:"message"`
}

// uiRollbackRequest is the body of POST /api/v1/ui/rollback.
type uiRollbackRequest struct {
	// Pipeline is the Pipeline to roll back.
	Pipeline string `json:"pipeline"`
	// Environment is the environment to roll back.
	Environment string `json:"environment"`
	// Namespace of the Pipeline. Defaults to "default".
	Namespace string `json:"namespace,omitempty"`
	// ToBundle is the Bundle to go back to. Empty means the most recent Bundle,
	// other than the one deployed now, that was Verified in the environment.
	ToBundle string `json:"toBundle,omitempty"`
	// Hold keeps the environment on the rollback until it is released
	// (kardinal rollback --hold): no other Bundle promotes into it, and the
	// rollback passes the gates that would block it, each pass audited.
	Hold bool `json:"hold,omitempty"`
	// HoldReason says why the environment is held. Required with hold.
	HoldReason string `json:"holdReason,omitempty"`
	// HoldExpiresIn, a Go duration such as 24h, ends the hold that long after
	// it is made. Empty: the hold lasts until it is released.
	HoldExpiresIn string `json:"holdExpiresIn,omitempty"`
}

// uiReleaseHoldRequest is the body of POST /api/v1/ui/release-hold.
type uiReleaseHoldRequest struct {
	// Pipeline is the Pipeline whose environment is held.
	Pipeline string `json:"pipeline"`
	// Environment is the held environment.
	Environment string `json:"environment"`
	// Namespace of the Pipeline. Defaults to "default".
	Namespace string `json:"namespace,omitempty"`
}

// uiRollbackResponse is the 201 response of POST /api/v1/ui/rollback.
type uiRollbackResponse struct {
	// Bundle is the rollback Bundle created.
	Bundle string `json:"bundle"`
	// RollbackOf is the Bundle whose artifacts it restores.
	RollbackOf string `json:"rollbackOf"`
	// Message describes the rollback.
	Message string `json:"message"`
	// Held is true when the environment is held on the rollback.
	Held bool `json:"held,omitempty"`
}

// uiPipelineActionRequest is the body of POST /api/v1/ui/pause and
// POST /api/v1/ui/resume.
type uiPipelineActionRequest struct {
	// Pipeline is the Pipeline to pause or resume.
	Pipeline string `json:"pipeline"`
	// Namespace of the Pipeline. Defaults to "default".
	Namespace string `json:"namespace,omitempty"`
}

// uiRejectRequest is the body of POST /api/v1/ui/reject.
type uiRejectRequest struct {
	// Bundle is the Bundle to reject.
	Bundle string `json:"bundle"`
	// Namespace of the Bundle. Defaults to "default".
	Namespace string `json:"namespace,omitempty"`
	// Reason is recorded in spec.rejected and the audit trail. Required.
	Reason string `json:"reason"`
}

// uiPipelineOverrideRequest is the body of POST /api/v1/ui/override: the
// override of a gate template in one environment of a Pipeline, recorded on
// the instances its in-progress Bundles have (kardinal override).
type uiPipelineOverrideRequest struct {
	// Pipeline whose in-progress Bundles' gate instances get the override.
	Pipeline string `json:"pipeline"`
	// Namespace of the Pipeline. Defaults to "default".
	Namespace string `json:"namespace,omitempty"`
	// Gate is the gate template name (or the name of one gate instance).
	Gate string `json:"gate"`
	// Environment is the environment the override applies to. Required.
	Environment string `json:"environment"`
	// Reason is recorded on the override and in the audit trail. Required.
	Reason string `json:"reason"`
	// ExpiresInMinutes is how long the override lasts, 1 to the cap
	// (--gate-override-max-minutes). Defaults to 60.
	ExpiresInMinutes int `json:"expiresInMinutes,omitempty"`
}

// uiPipelineOverrideResponse is the response of POST /api/v1/ui/override.
type uiPipelineOverrideResponse struct {
	// Message describes what was done.
	Message string `json:"message"`
	// Gates are the gate instances that got the override.
	Gates []string `json:"gates"`
	// ExpiresAt is when the override expires (RFC 3339).
	ExpiresAt string `json:"expiresAt"`
}

// uiMessageResponse is the response of an action that creates nothing.
type uiMessageResponse struct {
	// Message describes what was done.
	Message string `json:"message"`
}

// uiValidateCELRequest is the body of POST /api/v1/ui/validate-cel.
type uiValidateCELRequest struct {
	// Expression is the PolicyGate CEL expression to compile.
	Expression string `json:"expression"`
}

// uiValidateCELResponse is the response of POST /api/v1/ui/validate-cel.
type uiValidateCELResponse struct {
	// Valid is true when the expression compiles in the PolicyGate CEL environment.
	Valid bool `json:"valid"`
	// Error is the compile error when Valid is false.
	Error string `json:"error,omitempty"`
}

// uiGateOverrideRequest is the body of POST /api/v1/ui/gates/{name}/approve.
type uiGateOverrideRequest struct {
	// Reason is recorded on the override and in the audit trail. Required.
	Reason string `json:"reason"`
	// Namespace of the gate, used only with the {name}/approve form; the
	// {namespace}/{name}/approve form takes it from the path.
	Namespace string `json:"namespace,omitempty"`
	// Stage limits the override to one environment. Empty overrides the gate
	// in every environment it applies to.
	Stage string `json:"stage,omitempty"`
	// ExpiresInMinutes is how long the override lasts, 1 to 1440. Defaults to 60.
	ExpiresInMinutes int `json:"expiresInMinutes,omitempty"`
}

// uiCreateBundleRequest is the body of POST /api/v1/ui/bundles.
type uiCreateBundleRequest struct {
	// Pipeline is the Pipeline the Bundle promotes through.
	Pipeline string `json:"pipeline"`
	// Image is the image reference: repo:tag, repo@sha256:digest or repo.
	Image string `json:"image"`
	// CommitSHA is the source commit (spec.provenance.commitSHA).
	CommitSHA string `json:"commitSHA,omitempty"`
	// Author is the build's author (spec.provenance.author).
	Author string `json:"author,omitempty"`
	// Namespace of the Pipeline. Defaults to "default".
	Namespace string `json:"namespace,omitempty"`
}

// uiCreateBundleResponse is the 201 response of POST /api/v1/ui/bundles.
type uiCreateBundleResponse struct {
	// Bundle is the name of the created Bundle.
	Bundle string `json:"bundle"`
	// Message describes how to follow it.
	Message string `json:"message"`
}

// uiDisplayState is how one API state is shown (pkg/display): the same
// labels as the CLI.
type uiDisplayState struct {
	// Value is the API value, unchanged, for example WaitingForMerge.
	Value string `json:"value"`
	// Label is the text a person reads, for example "Waiting for PR merge".
	Label string `json:"label"`
	// Glyph is a one-character Unicode symbol for the state.
	Glyph string `json:"glyph"`
	// ASCII is the glyph for a surface without Unicode.
	ASCII string `json:"ascii"`
	// Tone is the status bucket that picks colour and shape: verified,
	// promoting, waiting, blocked, failed, superseded, paused, held,
	// pending, degraded or unknown.
	Tone string `json:"tone"`
}

// uiDisplayStates is the response of GET /api/v1/ui/display-states: every
// known state of each kind, keyed by API value. A value not listed is shown
// with its CamelCase split into words and tone unknown.
type uiDisplayStates struct {
	// Pipeline maps Pipeline status.phase values, and Paused.
	Pipeline map[string]uiDisplayState `json:"pipeline"`
	// Bundle maps Bundle status.phase values.
	Bundle map[string]uiDisplayState `json:"bundle"`
	// Step maps PromotionStep status.state values, and NotStarted (an
	// environment not reached yet).
	Step map[string]uiDisplayState `json:"step"`
	// SubStep maps the state of a PromotionStep's status.steps[].
	SubStep map[string]uiDisplayState `json:"substep"`
	// Gate maps the state of a gate instance (Pass, Block, Waiting, Pending,
	// Superseded, Rejected).
	Gate map[string]uiDisplayState `json:"gate"`
	// HeldByGate is the label of an environment a gate holds; {gate} is the
	// gate's name. Its tone is blocked.
	HeldByGate uiDisplayState `json:"heldByGate"`
	// Held is the label of an environment held by a rollback (rollback --hold).
	Held uiDisplayState `json:"held"`
}
