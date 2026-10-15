// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package main (ui_lifecycle.go) implements the UI promote, rollback, pause
// and resume actions. Each one calls the pkg/lifecycle function the CLI uses,
// so the UI and the CLI pick the same Bundle and build the same objects.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/accesslog"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// uiActor is the requester the UI records when it does not know who the
// caller is: with the static UI token (--ui-auth-token) or with no UI auth.
const uiActor = lifecycle.UICreator

// uiRequester is who asked for a UI action: a promote, a rollback, a new
// Bundle, a gate approval, a pause or a resume. With --ui-tokenreview-auth it
// is the username the TokenReview middleware stored in the request context, as
// the API server returned it (for example
// system:serviceaccount:team-a:deployer); otherwise it is uiActor.
//
// The username is recorded as is. It only goes into the kardinal.io/requested-by
// annotation (never a label, so the 63-character label limit does not apply),
// a gate override's createdBy and the gate reason built from it, the PR body
// (escaped by mdcell) and JSON log fields, and the API server already bounds
// it: by default it refuses objects whose annotations exceed 256 KiB and
// request bodies over 3 MiB.
func uiRequester(ctx context.Context) string {
	if u, ok := uiauth.UserFrom(ctx); ok && u.Username != "" {
		return u.Username
	}
	return uiActor
}

// writeLifecycleError maps a pkg/lifecycle error to an HTTP status. Planner
// errors (not found, invalid, conflict) carry a message meant for the user;
// anything else is logged and reported as an internal error.
func (s *uiAPIServer) writeLifecycleError(w http.ResponseWriter, action string, err error) {
	code, msg := s.lifecycleStatus(action, err)
	http.Error(w, msg, code)
}

// lifecycleStatus is the HTTP status and message writeLifecycleError sends
// for err. A refused action's message names what is missing and the role
// that grants it (uiauth.AuthorizeAction).
func (s *uiAPIServer) lifecycleStatus(action string, err error) (int, string) {
	switch {
	case errors.Is(err, lifecycle.ErrNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, lifecycle.ErrInvalid):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, lifecycle.ErrConflict):
		return http.StatusConflict, err.Error()
	case apierrors.IsConflict(err):
		return http.StatusConflict, err.Error()
	case apierrors.IsForbidden(err):
		return http.StatusForbidden, err.Error()
	default:
		s.log.Error().Err(err).Msg("ui: " + action)
		return http.StatusInternalServerError, "failed to " + action
	}
}

// handlePromote handles POST /api/v1/ui/promote. It promotes the newest Bundle
// Verified in every upstream of the environment by creating a Bundle that
// copies its artifacts (lifecycle.PlanPromote). UI equivalent of
// `kardinal promote <pipeline> --env <env>`.
//
// Request body (JSON):
//
//	{"pipeline": "nginx-demo", "environment": "prod", "namespace": "default"}
//
// Response (JSON on success, HTTP 201):
//
//	{"bundle": "nginx-demo-abc123", "source": "nginx-demo-v2", "message": "..."}
//
// 404 unknown pipeline; 400 unknown or first environment; 409 nothing to
// promote (nothing verified upstream, already there, or a newer Bundle is
// still promoting).
func (s *uiAPIServer) handlePromote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req uiPromoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Pipeline == "" || req.Environment == "" {
		http.Error(w, "pipeline and environment are required", http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}

	requester := uiRequester(r.Context())
	stamp, ok := s.stampFor(w, r, actor.Where{Namespace: ns, Pipeline: req.Pipeline},
		accesslog.AuditTarget{Route: "/api/v1/ui/promote", Environment: req.Environment,
			Message: "promote " + req.Pipeline + " to " + req.Environment})
	if !ok {
		return
	}
	plan, err := lifecycle.PlanPromote(r.Context(), s.client, lifecycle.PromoteRequest{
		Namespace:   ns,
		Pipeline:    req.Pipeline,
		Environment: req.Environment,
		Actor:       requester,
		Stamp:       stamp,
		Now:         time.Now(),
	})
	if err != nil {
		s.writeLifecycleError(w, "promote", err)
		return
	}
	if err := lifecycle.CreateBundleAs(r.Context(), s.client, plan.Bundle, requester); err != nil {
		s.writeLifecycleError(w, "create promote bundle", err)
		return
	}
	auditDone(r, plan.Bundle.Name, "promoted "+plan.Source.Name+" to "+req.Environment+" as "+plan.Bundle.Name)

	s.log.Info().
		Str("bundle", plan.Bundle.Name).
		Str("source", plan.Source.Name).
		Str("pipeline", req.Pipeline).
		Str("env", req.Environment).
		Str("requestedBy", requester).
		Msg("ui: promote triggered")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(uiPromoteResponse{
		Bundle: plan.Bundle.Name,
		Source: plan.Source.Name,
		Message: "promoting " + plan.Source.Name + " (Verified in " + strings.Join(plan.Upstreams, ", ") +
			") to " + req.Environment + " — track with kardinal get bundles " + req.Pipeline,
	})
}

// handleRollback handles POST /api/v1/ui/rollback. It creates a rollback
// Bundle with the artifacts of the most recent Bundle, other than the one
// deployed now, that was Verified in the environment, or of toBundle when set
// (lifecycle.PlanRollback). UI equivalent of `kardinal rollback`.
//
// Request body (JSON):
//
//	{"pipeline": "nginx-demo", "environment": "prod", "namespace": "default", "toBundle": "nginx-demo-abc"}
//
// Response (JSON on success, HTTP 201):
//
//	{"bundle": "nginx-demo-rollback-xyz", "rollbackOf": "nginx-demo-abc", "message": "..."}
//
// 404 unknown pipeline or toBundle; 400 missing environment, or a toBundle of
// another pipeline or without artifacts; 409 nothing earlier to roll back to.
func (s *uiAPIServer) handleRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req uiRollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Pipeline == "" || req.Environment == "" {
		http.Error(w, "pipeline and environment are required", http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}

	requester := uiRequester(r.Context())
	stamp, ok := s.stampFor(w, r, actor.Where{Namespace: ns, Pipeline: req.Pipeline},
		accesslog.AuditTarget{Route: "/api/v1/ui/rollback", Environment: req.Environment,
			Message: "roll back " + req.Pipeline + " in " + req.Environment})
	if !ok {
		return
	}
	rollbackReq := lifecycle.RollbackRequest{
		Namespace:   ns,
		Pipeline:    req.Pipeline,
		Environment: req.Environment,
		ToBundle:    req.ToBundle,
		Actor:       requester,
		Stamp:       stamp,
		Now:         time.Now(),
	}
	if !req.Hold && req.HoldReason != "" {
		http.Error(w, "holdReason is the reason of a hold: set hold", http.StatusBadRequest)
		return
	}
	var plan *lifecycle.RollbackPlan
	var err error
	if req.Hold {
		if strings.TrimSpace(req.HoldReason) == "" {
			http.Error(w, "a hold needs a holdReason", http.StatusBadRequest)
			return
		}
		var expiresIn time.Duration
		if req.HoldExpiresIn != "" {
			if expiresIn, err = time.ParseDuration(req.HoldExpiresIn); err != nil || expiresIn <= 0 {
				http.Error(w, "holdExpiresIn must be a positive Go duration (24h)", http.StatusBadRequest)
				return
			}
		}
		// The caller must be able to create the rollback Bundle before the
		// hold is written: one who cannot never gets a hold that is removed
		// again a moment later.
		if az, ok := s.client.(actionAuthorizer); ok {
			if err = az.AuthorizeAction(r.Context(), "create", "kardinal.io", "bundles", "", ns, ""); err != nil {
				s.writeLifecycleError(w, "rollback", err)
				return
			}
		}
		// The hold needs pipelines/hold, not update on the Pipeline: the
		// controller writes it. The plan's reads and the Bundle create stay
		// on the caller's client.
		var holdWriter client.Client
		if holdWriter, err = s.actionClient(r.Context(), "pipelines", "hold", ns, req.Pipeline); err != nil {
			s.writeLifecycleError(w, "hold", err)
			return
		}
		plan, _, err = lifecycle.RollbackAndHold(r.Context(), s.client,
			lifecycle.HoldRequest{RollbackRequest: rollbackReq, HoldReason: req.HoldReason, ExpiresIn: expiresIn,
				Creator: requester, HoldWriter: holdWriter})
	} else {
		plan, err = lifecycle.PlanRollback(r.Context(), s.client, rollbackReq)
		if err == nil {
			if createErr := lifecycle.CreateBundleAs(r.Context(), s.client, plan.Bundle, requester); createErr != nil {
				s.writeLifecycleError(w, "create rollback bundle", createErr)
				return
			}
		}
	}
	if err != nil {
		s.writeLifecycleError(w, "rollback", err)
		return
	}

	s.log.Info().
		Str("bundle", plan.Bundle.Name).
		Str("pipeline", req.Pipeline).
		Str("env", req.Environment).
		Str("rollbackOf", plan.Target.Name).
		Str("rollbackFrom", plan.CurrentName).
		Bool("hold", req.Hold).
		Str("requestedBy", requester).
		Msg("ui: rollback triggered")

	msg := "rollback started — rolling " + req.Environment + " back to " + plan.Target.Name
	if plan.Fleet != nil {
		changes := 0
		for _, t := range plan.Fleet {
			if t.Changes {
				changes++
			}
		}
		msg = fmt.Sprintf("rollback started — rolling fleet %s back to %s: %d of %d targets go back, paced by maxConcurrent",
			req.Environment, plan.Target.Name, changes, len(plan.Fleet))
		if paused, pErr := s.fleetPaused(r.Context(), ns, req.Pipeline, req.Environment); pErr == nil && paused {
			msg += "; the fleet is paused: no target starts until it is resumed"
		}
	}
	if req.Hold {
		msg += "; " + req.Environment + " is held on " + plan.Bundle.Name + " until the hold is released"
	}
	auditDone(r, plan.Bundle.Name, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(uiRollbackResponse{
		Bundle:     plan.Bundle.Name,
		RollbackOf: plan.Target.Name,
		Message:    msg,
		Held:       req.Hold,
		Fleet:      fleetRollbackResponse(plan.Fleet),
	})
}

// fleetRollbackResponse is plan.Fleet for the UI API.
func fleetRollbackResponse(in []lifecycle.FleetTargetRollback) []uiFleetTargetRollback {
	if in == nil {
		return nil
	}
	out := make([]uiFleetTargetRollback, len(in))
	for i, t := range in {
		out[i] = uiFleetTargetRollback{Environment: t.Environment, Runs: t.Runs, Changes: t.Changes}
	}
	return out
}

// fleetPaused reports whether fleet is in the Pipeline's spec.pausedFleets.
func (s *uiAPIServer) fleetPaused(ctx context.Context, ns, pipeline, fleet string) (bool, error) {
	var p v1alpha1.Pipeline
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: pipeline}, &p); err != nil {
		return false, fmt.Errorf("get pipeline %s/%s: %w", ns, pipeline, err)
	}
	return lifecycle.FleetPaused(&p, fleet), nil
}

// handleReleaseHold handles POST /api/v1/ui/release-hold: it removes the hold
// of an environment (lifecycle.ReleaseHold). UI equivalent of
// `kardinal release-hold`. 404 when the environment is not held.
func (s *uiAPIServer) handleReleaseHold(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req uiReleaseHoldRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Pipeline == "" || req.Environment == "" {
		http.Error(w, "pipeline and environment are required", http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}
	if _, ok := s.stampFor(w, r, actor.Where{Namespace: ns, Pipeline: req.Pipeline},
		accesslog.AuditTarget{Route: "/api/v1/ui/release-hold", Environment: req.Environment,
			Message: "release the hold of " + req.Pipeline + " in " + req.Environment}); !ok {
		return
	}
	// pipelines/hold, not update on the Pipeline: the controller writes it.
	writer, err := s.actionClient(r.Context(), "pipelines", "hold", ns, req.Pipeline)
	if err != nil {
		s.writeLifecycleError(w, "release hold", err)
		return
	}
	h, err := lifecycle.ReleaseHold(r.Context(), writer, ns, req.Pipeline, req.Environment)
	if err != nil {
		s.writeLifecycleError(w, "release hold", err)
		return
	}
	s.log.Info().Str("pipeline", req.Pipeline).Str("env", req.Environment).Str("bundle", h.Bundle).
		Str("requestedBy", uiRequester(r.Context())).Msg("ui: hold released")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uiMessageResponse{
		Message: "released the hold of " + req.Environment + " (rollback " + h.Bundle + ")"})
}

// handleApproval handles POST /api/v1/ui/approvals: the UI's kardinal
// approve. It records the authenticated UI user's decision (approve or
// reject) on a Bundle for an environment's approval gates, replaces it, or
// revokes it (lifecycle.RecordApproval). An Approval names a person, so it
// needs a verified identity: only TokenReview mode (ui.auth.tokenReview) has
// one; with a static UI token or no UI auth the request is refused (403).
// The user's groups are the ones TokenReview returned, which the gate's
// allowedGroups are matched against. The controller writes the Approval
// after a SubjectAccessReview of the user (create/delete approvals); the
// chart's approvals policy admits the controller's write only with
// kardinal.io/recorded-via: ui, and lets it revoke only such Approvals.
//
// Request body (JSON):
//
//	{"bundle": "app-v2", "environment": "prod", "namespace": "default", "decision": "approve", "comment": "LGTM"}
//
// 400 bad decision or unknown environment; 403 no verified identity or not
// allowed; 404 unknown Bundle or nothing to revoke; 409 a halted Bundle.
func (s *uiAPIServer) handleApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req uiApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Bundle == "" || req.Environment == "" {
		http.Error(w, "bundle and environment are required", http.StatusBadRequest)
		return
	}
	user, ok := uiauth.UserFrom(r.Context())
	if !ok || user.Username == "" {
		http.Error(w, "an approval names who decided: it needs the UI's TokenReview mode (ui.auth.tokenReview), "+
			"or use kardinal approve", http.StatusForbidden)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}
	stamp, ok := s.stampFor(w, r, actor.Where{Namespace: ns, Environment: req.Environment},
		accesslog.AuditTarget{Route: "/api/v1/ui/approvals", Bundle: req.Bundle,
			Message: "record a decision on " + req.Bundle + " for " + req.Environment})
	if !ok {
		return
	}
	outcome, a, err := lifecycle.RecordApproval(r.Context(), s.client, lifecycle.ApprovalRequest{
		Namespace: ns, Bundle: req.Bundle, Environment: req.Environment, User: user.Username, Groups: user.Groups,
		Decision: req.Decision, Comment: req.Comment, Revoke: req.Revoke, Via: "ui", Actor: stamp,
	})
	if err != nil {
		s.writeLifecycleError(w, "approval", err)
		return
	}
	s.log.Info().Str("bundle", req.Bundle).Str("env", req.Environment).Str("decision", a.Spec.Decision).
		Str("outcome", string(outcome)).Str("requestedBy", user.Username).Msg("ui: approval")
	verb := a.Spec.Decision + "s"
	msg := fmt.Sprintf("%s: %s %s %s for %s", outcome, user.Username, verb, req.Bundle, req.Environment)
	if outcome == lifecycle.ApprovalRevoked {
		msg = fmt.Sprintf("Revoked: %s no longer %s %s for %s", user.Username, verb, req.Bundle, req.Environment)
	}
	auditDone(r, "", msg)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uiApprovalResponse{Outcome: string(outcome), Approval: a.Name, User: user.Username, Message: msg})
}

// handlePause handles POST /api/v1/ui/pause. It sets spec.paused
// (lifecycle.SetPaused); the Pipeline reconciler then creates the freeze gate,
// so no new step starts and in-flight steps hold at the next safe point.
// With "environment" it pauses only that fleet (lifecycle.SetFleetPaused,
// spec.pausedFleets): no further target starts, the ones in flight finish. In
// TokenReview mode the caller needs update on pipelines/pause, not on the
// Pipeline: the controller writes it (actionClient).
//
// Request body (JSON):
//
//	{"pipeline": "nginx-demo", "namespace": "default"}
//
// Response (JSON on success):
//
//	{"message": "pipeline nginx-demo paused"}
func (s *uiAPIServer) handlePause(w http.ResponseWriter, r *http.Request) {
	s.handlePauseResume(w, r, true)
}

// handleResume handles POST /api/v1/ui/resume. It clears spec.paused; the
// Pipeline reconciler then deletes the freeze gate.
func (s *uiAPIServer) handleResume(w http.ResponseWriter, r *http.Request) {
	s.handlePauseResume(w, r, false)
}

func (s *uiAPIServer) handlePauseResume(w http.ResponseWriter, r *http.Request, pause bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req uiPipelineActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Pipeline == "" {
		http.Error(w, "pipeline is required", http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}

	action, done := "resume", "resumed"
	if pause {
		action, done = "pause", "paused"
	}
	if _, ok := s.stampFor(w, r, actor.Where{Namespace: ns, Pipeline: req.Pipeline},
		accesslog.AuditTarget{Route: "/api/v1/ui/" + action, Message: done + " pipeline " + req.Pipeline}); !ok {
		return
	}
	// SetPaused retries a conflict, so a concurrent write to the Pipeline (for
	// example its status) does not fail the request.
	// In TokenReview mode the caller needs only the pause action
	// (pipelines/pause), not update on the Pipeline: the controller writes it.
	writer, err := s.actionClient(r.Context(), "pipelines", "pause", ns, req.Pipeline)
	if err != nil {
		s.writeLifecycleError(w, action, err)
		return
	}
	if req.Environment != "" {
		// One fleet (spec.pausedFleets): the same pipelines/pause right.
		if err := lifecycle.SetFleetPaused(r.Context(), writer, ns, req.Pipeline, req.Environment, pause); err != nil {
			s.writeLifecycleError(w, action+" fleet", err)
			return
		}
		s.log.Info().Str("pipeline", req.Pipeline).Str("env", req.Environment).Bool("paused", pause).
			Str("requestedBy", uiRequester(r.Context())).Msg("ui: fleet " + done)
		msg := "fleet " + req.Environment + " of " + req.Pipeline + " resumed: its targets start again"
		if pause {
			msg = "fleet " + req.Environment + " of " + req.Pipeline + " paused: no further target starts, the targets in flight finish"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(uiMessageResponse{Message: msg})
		return
	}
	if err := lifecycle.SetPaused(r.Context(), writer, ns, req.Pipeline, pause); err != nil {
		s.writeLifecycleError(w, action+" pipeline", err)
		return
	}

	s.log.Info().Str("pipeline", req.Pipeline).Bool("paused", pause).
		Str("requestedBy", uiRequester(r.Context())).Msg("ui: pipeline " + done)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uiMessageResponse{Message: "pipeline " + req.Pipeline + " " + done})
}

// actionAuthorizer is the AuthorizingClient of TokenReview mode.
type actionAuthorizer interface {
	AuthorizeAction(ctx context.Context, verb, group, resource, subresource, namespace, name string) error
	Privileged() client.Client
}

// actionClient returns the client, with fresh Gets (freshGets), to write the named object with for an
// action the caller asked for. In TokenReview mode it checks that the
// caller may perform the action, the virtual subresource resource/action
// (pipelines/pause, policygates/override), and returns the controller's
// client: the caller does not need update on the whole object, and the
// handler records the caller as the requester. Otherwise (no auth mode, the
// shared token) it returns the handler's client.
//
// With --watch-namespace an action outside that namespace is refused before
// any read: the client returned reads the API server uncached and, in
// TokenReview mode, as the controller, so it must not reach beyond the
// namespace the controller watches.
func (s *uiAPIServer) actionClient(ctx context.Context, resource, action, namespace, name string) (client.Client, error) {
	if s.scopeNamespace != "" && namespace != s.scopeNamespace {
		return nil, fmt.Errorf("the controller watches namespace %s only, not %q: %w", s.scopeNamespace, namespace, lifecycle.ErrInvalid)
	}
	az, ok := s.client.(actionAuthorizer)
	if !ok {
		return s.fresh(s.client), nil
	}
	if err := az.AuthorizeAction(ctx, "update", "kardinal.io", resource, action, namespace, name); err != nil {
		return nil, err
	}
	return s.fresh(az.Privileged()), nil
}

// handleReject handles POST /api/v1/ui/reject: it sets spec.rejected on the
// Bundle, as kardinal reject does, with the caller as the rejecting user.
// In TokenReview mode the caller needs the reject action (bundles/reject),
// not update on the Bundle: the controller writes it, recording the
// requester the review verified (the bundle-rejection admission policy lets
// only the controller write a rejection in another user's name). The Bundle
// reconciler then records the BundleRejected AuditEvent.
func (s *uiAPIServer) handleReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req uiRejectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Bundle == "" {
		http.Error(w, "bundle is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		http.Error(w, "reason is required", http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}
	if _, ok := s.stampFor(w, r, actor.Where{Namespace: ns},
		accesslog.AuditTarget{Route: "/api/v1/ui/reject", Bundle: req.Bundle,
			Message: "reject bundle " + req.Bundle + ": " + req.Reason}); !ok {
		return
	}
	writer, err := s.actionClient(r.Context(), "bundles", "reject", ns, req.Bundle)
	if err != nil {
		s.writeLifecycleError(w, "reject bundle", err)
		return
	}
	by := uiRequester(r.Context())
	b, err := lifecycle.Reject(r.Context(), writer, ns, req.Bundle, req.Reason, by, time.Now())
	if err != nil {
		s.writeLifecycleError(w, "reject bundle", err)
		return
	}
	if t := accesslog.FromContext(r.Context()).Audit; t != nil {
		t.Pipeline = b.Spec.Pipeline
	}
	s.log.Info().Str("bundle", req.Bundle).Str("namespace", ns).Str("reason", req.Reason).
		Str("requestedBy", by).Msg("ui: bundle rejected")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uiMessageResponse{Message: fmt.Sprintf(
		"bundle %s rejected by %s: it is never promoted again; an environment that runs it keeps it until rolled back",
		b.Name, by)})
}

// handlePipelineOverride handles POST /api/v1/ui/override: it overrides a
// gate template in one environment of a Pipeline, as kardinal override
// --stage does, on the gate instances its in-progress Bundles have
// (lifecycle.OverrideTargets). In TokenReview mode the caller needs the
// override action (policygates/override) on every one of them, checked
// before any is written; the controller writes the overrides, recording the
// requester the review verified. The PolicyGate reconciler records one
// GateOverridden AuditEvent per override.
func (s *uiAPIServer) handlePipelineOverride(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req uiPipelineOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	switch {
	case req.Pipeline == "":
		http.Error(w, "pipeline is required", http.StatusBadRequest)
		return
	case req.Gate == "":
		http.Error(w, "gate is required", http.StatusBadRequest)
		return
	case req.Environment == "":
		http.Error(w, "environment is required", http.StatusBadRequest)
		return
	case strings.TrimSpace(req.Reason) == "":
		http.Error(w, "reason is required", http.StatusBadRequest)
		return
	}
	expiresMins := req.ExpiresInMinutes
	if expiresMins == 0 {
		expiresMins = 60
	}
	if expiresMins < 1 || expiresMins > maxGateOverrideMinutes {
		http.Error(w, fmt.Sprintf("expiresInMinutes must be between 1 and %d", maxGateOverrideMinutes), http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}
	var pl v1alpha1.Pipeline
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: req.Pipeline}, &pl); err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("pipeline %s not found in namespace %s", req.Pipeline, ns), http.StatusNotFound)
			return
		}
		s.writeLifecycleError(w, "override gate", err)
		return
	}
	targets, err := lifecycle.OverrideTargets(r.Context(), s.client, ns, req.Pipeline, req.Environment, req.Gate)
	if err != nil {
		s.writeLifecycleError(w, "override gate", err)
		return
	}
	stamp, ok := s.stampFor(w, r, actor.Where{Namespace: ns, Pipeline: req.Pipeline, Environment: req.Environment},
		accesslog.AuditTarget{Route: "/api/v1/ui/override",
			Message: fmt.Sprintf("override gate %s in %s for %d minutes: %s", req.Gate, req.Environment, expiresMins, req.Reason)})
	if !ok {
		return
	}
	// Every instance is authorized before any is written.
	writers := make([]client.Client, len(targets))
	for i := range targets {
		if writers[i], err = s.actionClient(r.Context(), "policygates", "override", ns, targets[i].Name); err != nil {
			s.writeLifecycleError(w, "override gate "+targets[i].Name, err)
			return
		}
	}
	by := uiRequester(r.Context())
	now := time.Now().UTC()
	createdAt := metav1.NewTime(now)
	override := v1alpha1.PolicyGateOverride{Reason: req.Reason, Stage: req.Environment,
		ExpiresAt: metav1.NewTime(now.Add(time.Duration(expiresMins) * time.Minute)), CreatedAt: &createdAt, CreatedBy: by,
		Actor: stamp}
	// Every instance is tried: a failure on one does not leave the others
	// unwritten, and the answer names each gate that was not overridden.
	names := make([]string, 0, len(targets))
	var failed []string
	code := 0
	for i := range targets {
		if err := lifecycle.AppendOverride(r.Context(), writers[i], &targets[i], override); err != nil {
			c, msg := s.lifecycleStatus("override gate "+targets[i].Name, err)
			if code == 0 {
				code = c
			}
			failed = append(failed, fmt.Sprintf("%s (%s)", targets[i].Name, msg))
			continue
		}
		names = append(names, targets[i].Name)
	}
	if len(failed) > 0 {
		overridden := "none"
		if len(names) > 0 {
			overridden = strings.Join(names, ", ")
		}
		s.log.Warn().Str("pipeline", req.Pipeline).Str("gate", req.Gate).Str("environment", req.Environment).
			Strs("overridden", names).Strs("failed", failed).Msg("ui: gate override partly failed")
		http.Error(w, fmt.Sprintf("gate %s in %s: overridden on %s; not overridden on %s",
			req.Gate, req.Environment, overridden, strings.Join(failed, "; ")), code)
		return
	}
	s.log.Info().Str("pipeline", req.Pipeline).Str("gate", req.Gate).Str("environment", req.Environment).
		Strs("instances", names).Str("reason", req.Reason).Str("createdBy", by).
		Time("expiresAt", override.ExpiresAt.Time).Msg("ui: gate overridden")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uiPipelineOverrideResponse{
		Message: fmt.Sprintf("gate %s overridden in %s for %s until %s", req.Gate,
			req.Environment, versionsInProgress(len(names)), override.ExpiresAt.UTC().Format(time.RFC3339)),
		Gates:     names,
		ExpiresAt: override.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// versionsInProgress says how many in-progress Bundles an override covers, in
// the UI's words: "1 version in progress", "2 versions in progress".
func versionsInProgress(n int) string {
	if n == 1 {
		return "1 version in progress"
	}
	return fmt.Sprintf("%d versions in progress", n)
}
