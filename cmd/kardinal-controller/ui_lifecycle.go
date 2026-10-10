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
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	switch {
	case errors.Is(err, lifecycle.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, lifecycle.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, lifecycle.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case apierrors.IsForbidden(err):
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		s.log.Error().Err(err).Msg("ui: " + action)
		http.Error(w, "failed to "+action, http.StatusInternalServerError)
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
	plan, err := lifecycle.PlanPromote(r.Context(), s.client, lifecycle.PromoteRequest{
		Namespace:   ns,
		Pipeline:    req.Pipeline,
		Environment: req.Environment,
		Actor:       requester,
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
	rollbackReq := lifecycle.RollbackRequest{
		Namespace:   ns,
		Pipeline:    req.Pipeline,
		Environment: req.Environment,
		ToBundle:    req.ToBundle,
		Actor:       requester,
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
	if req.Hold {
		msg += "; " + req.Environment + " is held on " + plan.Bundle.Name + " until the hold is released"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(uiRollbackResponse{
		Bundle:     plan.Bundle.Name,
		RollbackOf: plan.Target.Name,
		Message:    msg,
		Held:       req.Hold,
	})
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
	outcome, a, err := lifecycle.RecordApproval(r.Context(), s.client, lifecycle.ApprovalRequest{
		Namespace: ns, Bundle: req.Bundle, Environment: req.Environment, User: user.Username, Groups: user.Groups,
		Decision: req.Decision, Comment: req.Comment, Revoke: req.Revoke, Via: "ui",
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uiApprovalResponse{Outcome: string(outcome), Approval: a.Name, User: user.Username, Message: msg})
}

// handlePause handles POST /api/v1/ui/pause. It sets spec.paused
// (lifecycle.SetPaused); the Pipeline reconciler then creates the freeze gate,
// so no new step starts and in-flight steps hold at the next safe point. In
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
	// SetPaused retries a conflict, so a concurrent write to the Pipeline (for
	// example its status) does not fail the request.
	// In TokenReview mode the caller needs only the pause action
	// (pipelines/pause), not update on the Pipeline: the controller writes it.
	writer, err := s.actionClient(r.Context(), "pipelines", "pause", ns, req.Pipeline)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
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

// actionClient returns the client to write the named object with for an
// action the caller asked for. In TokenReview mode it checks that the
// caller may perform the action, the virtual subresource resource/action
// (pipelines/pause, policygates/override), and returns the controller's
// client: the caller does not need update on the whole object, and the
// handler records the caller as the requester. Otherwise (no auth mode, the
// shared token) it returns the handler's client.
func (s *uiAPIServer) actionClient(ctx context.Context, resource, action, namespace, name string) (client.Client, error) {
	az, ok := s.client.(actionAuthorizer)
	if !ok {
		return s.client, nil
	}
	if err := az.AuthorizeAction(ctx, "update", "kardinal.io", resource, action, namespace, name); err != nil {
		return nil, err
	}
	return az.Privileged(), nil
}
