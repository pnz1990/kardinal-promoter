// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/accesslog"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// Freezes in the UI (docs/design/18-actor-policy.md section 9.5): the
// banner lists them, and Freeze agents, Stop the line and Unfreeze write
// them. A freeze names who froze, so the writes need the UI's TokenReview
// mode; the caller needs update on the virtual subresource
// changewindows/freeze (cluster-wide, or in the namespace the freeze is
// limited to), and the controller writes the window with the caller as
// spec.freeze.createdBy (the freezes admission policy lets only it name
// someone else).

// freezeAction is the virtual subresource the UI's freeze writes need.
const freezeAction = "freeze"

// freezeControlsOff answers a freeze write while --ui-freeze-controls is off.
const freezeControlsOff = "the UI's freeze controls are off (Helm ui.freezeControls); use kardinal freeze and kardinal unfreeze"

// maxFreezeMinutes bounds a UI freeze's duration (a week).
const maxFreezeMinutes = 7 * 24 * 60

// handleFreezes handles GET /api/v1/ui/freezes: every freeze, active first.
func (s *uiAPIServer) handleFreezes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	freezes, err := lifecycle.ListFreezes(r.Context(), s.client)
	if err != nil {
		s.writeLifecycleError(w, "list freezes", err)
		return
	}
	out := make([]uiFreeze, 0, len(freezes))
	for i := range freezes {
		out = append(out, toUIFreeze(&freezes[i]))
	}
	writeJSON(w, uiFreezesResponse{Freezes: out})
}

// toUIFreeze is cw as the banner shows it.
func toUIFreeze(cw *v1alpha1.ChangeWindow) uiFreeze {
	f := uiFreeze{Name: cw.Name, Actors: cw.Spec.Freeze.Actors, Namespaces: cw.Spec.Freeze.Namespaces,
		Active: changewindow.FreezeActive(cw), Since: cw.Spec.Start.UTC().Format(time.RFC3339),
		CreatedBy: cw.Spec.Freeze.CreatedBy, Reason: cw.Spec.Reason, Scope: changewindow.FreezeScope(cw.Spec.Freeze),
		Exemptions: len(cw.Status.Exemptions)}
	if f.Namespaces == nil {
		f.Namespaces = []string{}
	}
	if !cw.Spec.End.IsZero() {
		f.Until = cw.Spec.End.UTC().Format(time.RFC3339)
	}
	return f
}

// handleFreeze handles POST /api/v1/ui/freeze: kardinal freeze for the
// caller.
//
// Request body (JSON):
//
//	{"actors": "agents", "namespace": "", "reason": "INC-4211", "minutes": 0}
//
// 400 bad actors, no reason or a bad duration; 403 no verified identity or
// not allowed; 409 a same-named ChangeWindow that is not a freeze.
func (s *uiAPIServer) handleFreeze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.freezeControls {
		http.Error(w, freezeControlsOff, http.StatusForbidden)
		return
	}
	var req uiFreezeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Actors != v1alpha1.FreezeAgents && req.Actors != v1alpha1.FreezeEveryone {
		http.Error(w, "actors must be agents or everyone", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		http.Error(w, "reason is required: every held step shows it", http.StatusBadRequest)
		return
	}
	if req.Minutes < 0 || req.Minutes > maxFreezeMinutes {
		http.Error(w, fmt.Sprintf("minutes must be between 0 (until unfrozen) and %d", maxFreezeMinutes), http.StatusBadRequest)
		return
	}
	user, ok := uiauth.UserFrom(r.Context())
	if !ok || user.Username == "" {
		http.Error(w, "a freeze names who froze: it needs the UI's TokenReview mode (ui.auth.tokenReview), "+
			"or use kardinal freeze", http.StatusForbidden)
		return
	}
	if !s.inScope(w, req.Namespace) {
		return
	}
	name := lifecycle.FreezeName(req.Actors, req.Namespace)
	if _, ok := s.stampFor(w, r, actor.Where{Namespace: auditNamespace(req.Namespace)},
		accesslog.AuditTarget{Route: "/api/v1/ui/freeze", Environment: "*",
			Message: "freeze " + req.Actors + " (" + name + "): " + req.Reason}); !ok {
		return
	}
	writer, err := s.actionClient(r.Context(), "changewindows", freezeAction, req.Namespace, name)
	if err != nil {
		s.writeLifecycleError(w, "freeze", err)
		return
	}
	cw, _, err := lifecycle.Freeze(r.Context(), writer, lifecycle.FreezeRequest{Actors: req.Actors, Namespace: req.Namespace,
		Reason: req.Reason, For: time.Duration(req.Minutes) * time.Minute, CreatedBy: user.Username, Now: time.Now()})
	if err != nil {
		s.writeLifecycleError(w, "freeze", err)
		return
	}
	msg := fmt.Sprintf("frozen: %s (%s)", cw.Name, changewindow.FreezeScope(cw.Spec.Freeze))
	s.log.Info().Str("changewindow", cw.Name).Str("requestedBy", user.Username).Msg("ui: freeze")
	auditDone(r, "", msg)
	writeJSON(w, uiMessageResponse{Message: msg})
}

// handleUnfreeze handles POST /api/v1/ui/unfreeze: kardinal unfreeze <name>
// for the caller, authorized like the freeze it lifts.
//
// Request body (JSON):
//
//	{"name": "kardinal-freeze-agents"}
func (s *uiAPIServer) handleUnfreeze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.freezeControls {
		http.Error(w, freezeControlsOff, http.StatusForbidden)
		return
	}
	var req uiUnfreezeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	user, ok := uiauth.UserFrom(r.Context())
	if !ok || user.Username == "" {
		http.Error(w, "lifting a freeze needs the UI's TokenReview mode (ui.auth.tokenReview), or use kardinal unfreeze",
			http.StatusForbidden)
		return
	}
	var cw v1alpha1.ChangeWindow
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.Name}, &cw); err != nil {
		s.writeLifecycleError(w, "unfreeze", fmt.Errorf("no freeze %s: %w", req.Name, lifecycle.ErrNotFound))
		return
	}
	if cw.Spec.Freeze == nil {
		s.writeLifecycleError(w, "unfreeze", fmt.Errorf("ChangeWindow %s is not a freeze: %w", req.Name, lifecycle.ErrConflict))
		return
	}
	ns := ""
	if len(cw.Spec.Freeze.Namespaces) == 1 {
		ns = cw.Spec.Freeze.Namespaces[0] // a namespace freeze is lifted with that namespace's rights
	}
	if !s.inScope(w, ns) {
		return
	}
	if _, ok := s.stampFor(w, r, actor.Where{Namespace: auditNamespace(ns)},
		accesslog.AuditTarget{Route: "/api/v1/ui/unfreeze", Environment: "*", Message: "unfreeze " + req.Name}); !ok {
		return
	}
	writer, err := s.actionClient(r.Context(), "changewindows", freezeAction, ns, req.Name)
	if err != nil {
		s.writeLifecycleError(w, "unfreeze", err)
		return
	}
	if err := lifecycle.Unfreeze(r.Context(), writer, req.Name); err != nil {
		s.writeLifecycleError(w, "unfreeze", err)
		return
	}
	s.log.Info().Str("changewindow", req.Name).Str("requestedBy", user.Username).Msg("ui: unfreeze")
	msg := "lifted freeze " + req.Name
	auditDone(r, "", msg)
	writeJSON(w, uiMessageResponse{Message: msg})
}

// inScope refuses, with 403, a freeze write beyond the namespace this
// controller watches (--watch-namespace): a cluster-wide freeze, or another
// namespace's. It reports whether the write may go on.
func (s *uiAPIServer) inScope(w http.ResponseWriter, ns string) bool {
	if s.scopeNamespace == "" || ns == s.scopeNamespace {
		return true
	}
	where := "every namespace"
	if ns != "" {
		where = "namespace " + ns
	}
	http.Error(w, fmt.Sprintf("forbidden: this controller watches namespace %s only, so its UI cannot freeze %s",
		s.scopeNamespace, where), http.StatusForbidden)
	return false
}

// auditNamespace is where the API write AuditEvent of a freeze goes: its
// namespace, or the controller's for a cluster-wide freeze.
func auditNamespace(ns string) string {
	if ns != "" {
		return ns
	}
	if own := os.Getenv("POD_NAMESPACE"); own != "" {
		return own
	}
	return "kardinal-system"
}
