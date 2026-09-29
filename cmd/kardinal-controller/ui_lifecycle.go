// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package main (ui_lifecycle.go) implements the UI promote, rollback, pause
// and resume actions. Each one calls the pkg/lifecycle function the CLI uses,
// so the UI and the CLI pick the same Bundle and build the same objects.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// uiActor is recorded as the requester of Bundles the UI creates.
const uiActor = "kardinal-ui"

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
	var req struct {
		Pipeline    string `json:"pipeline"`
		Environment string `json:"environment"`
		Namespace   string `json:"namespace"`
	}
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

	plan, err := lifecycle.PlanPromote(r.Context(), s.client, lifecycle.PromoteRequest{
		Namespace:   ns,
		Pipeline:    req.Pipeline,
		Environment: req.Environment,
		Actor:       uiActor,
		Now:         time.Now(),
	})
	if err != nil {
		s.writeLifecycleError(w, "promote", err)
		return
	}
	if err := s.client.Create(r.Context(), plan.Bundle); err != nil {
		s.writeLifecycleError(w, "create promote bundle", err)
		return
	}

	s.log.Info().
		Str("bundle", plan.Bundle.Name).
		Str("source", plan.Source.Name).
		Str("pipeline", req.Pipeline).
		Str("env", req.Environment).
		Msg("ui: promote triggered")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"bundle": plan.Bundle.Name,
		"source": plan.Source.Name,
		"message": "promoting " + plan.Source.Name + " (Verified in " + strings.Join(plan.Upstreams, ", ") +
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
	var req struct {
		Pipeline    string `json:"pipeline"`
		Environment string `json:"environment"`
		Namespace   string `json:"namespace"`
		ToBundle    string `json:"toBundle"`
	}
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

	plan, err := lifecycle.PlanRollback(r.Context(), s.client, lifecycle.RollbackRequest{
		Namespace:   ns,
		Pipeline:    req.Pipeline,
		Environment: req.Environment,
		ToBundle:    req.ToBundle,
		Actor:       uiActor,
		Now:         time.Now(),
	})
	if err != nil {
		s.writeLifecycleError(w, "rollback", err)
		return
	}
	if err := s.client.Create(r.Context(), plan.Bundle); err != nil {
		s.writeLifecycleError(w, "create rollback bundle", err)
		return
	}

	s.log.Info().
		Str("bundle", plan.Bundle.Name).
		Str("pipeline", req.Pipeline).
		Str("env", req.Environment).
		Str("rollbackOf", plan.Target.Name).
		Str("rollbackFrom", plan.CurrentName).
		Msg("ui: rollback triggered")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"bundle":     plan.Bundle.Name,
		"rollbackOf": plan.Target.Name,
		"message":    "rollback started — rolling " + req.Environment + " back to " + plan.Target.Name,
	})
}

// handlePause handles POST /api/v1/ui/pause. It sets spec.paused
// (lifecycle.SetPaused); the Pipeline reconciler then creates the freeze gate,
// so no new step starts and in-flight steps hold at the next safe point. The
// caller needs only get and update on the Pipeline.
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
	var req struct {
		Pipeline  string `json:"pipeline"`
		Namespace string `json:"namespace"`
	}
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
	if err := lifecycle.SetPaused(r.Context(), s.client, ns, req.Pipeline, pause); err != nil {
		s.writeLifecycleError(w, action+" pipeline", err)
		return
	}

	s.log.Info().Str("pipeline", req.Pipeline).Bool("paused", pause).Msg("ui: pipeline " + done)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": "pipeline " + req.Pipeline + " " + done,
	})
}
