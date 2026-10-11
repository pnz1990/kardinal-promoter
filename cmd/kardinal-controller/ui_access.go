// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	authzv1 "k8s.io/api/authorization/v1"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// maxAccessChecks bounds one POST /api/v1/ui/access: the UI asks for the
// actions of one screen at a time.
const maxAccessChecks = 32

// accessVerbs are the verbs a check may name.
var accessVerbs = map[string]bool{"get": true, "list": true, "create": true, "update": true, "patch": true, "delete": true}

// accessChecker is the TokenReview-mode client: it answers a check for the
// request's user without recording a denial (uiauth.AuthorizingClient.Can).
type accessChecker interface {
	Can(ctx context.Context, attrs authzv1.ResourceAttributes) (bool, error)
}

// uiAccessCheck is one action the UI asks about: verb on a kardinal.io
// resource (or virtual subresource) in the request's namespace.
type uiAccessCheck struct {
	// Verb is get, list, create, update, patch or delete.
	Verb string `json:"verb"`
	// Resource is a kardinal.io resource, plural: bundles, pipelines, approvals, policygates.
	Resource string `json:"resource"`
	// Subresource is a virtual subresource such as pause, hold, reject or override.
	Subresource string `json:"subresource,omitempty"`
	// Name is the object, when the check is for one.
	Name string `json:"name,omitempty"`
}

// uiAccessRequest is the body of POST /api/v1/ui/access.
type uiAccessRequest struct {
	// Namespace the checks are in. Defaults to "default".
	Namespace string `json:"namespace,omitempty"`
	// Checks are answered in order, at most 32.
	Checks []uiAccessCheck `json:"checks"`
}

// uiAccessResult answers one check.
type uiAccessResult struct {
	// Allowed is whether the caller may do it.
	Allowed bool `json:"allowed"`
	// GrantedBy names the chart's user roles that grant it ("approver or
	// admin"), when the chart grants it to particular roles.
	GrantedBy string `json:"grantedBy,omitempty"`
}

// uiAccessUser is who the caller is, as TokenReview verified.
type uiAccessUser struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups,omitempty"`
}

// uiAccessResponse is the body of a POST /api/v1/ui/access answer.
type uiAccessResponse struct {
	// User is the verified caller; absent when the UI API does not identify
	// callers (no TokenReview), so an action that names who decided (an
	// approval) cannot be taken from the UI.
	User *uiAccessUser `json:"user,omitempty"`
	// Results answer Checks, in order. Without TokenReview every check is
	// allowed: the UI API then acts as the controller for anyone who reaches it.
	Results []uiAccessResult `json:"results"`
}

// handleAccess handles POST /api/v1/ui/access: which of the given actions
// the caller may take, so the UI hides or disables the others and names the
// role that grants them. In TokenReview mode each check is a
// SubjectAccessReview for the verified user (cached for
// uiauth.DefaultCacheTTL, like every other check); a "no" is an answer, not a
// 403.
func (s *uiAPIServer) handleAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req uiAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Checks) == 0 || len(req.Checks) > maxAccessChecks {
		http.Error(w, fmt.Sprintf("checks must hold 1 to %d entries", maxAccessChecks), http.StatusBadRequest)
		return
	}
	for i, c := range req.Checks {
		if !accessVerbs[c.Verb] || c.Resource == "" {
			http.Error(w, fmt.Sprintf("checks[%d]: verb must be one of get, list, create, update, patch, delete, and resource is required", i),
				http.StatusBadRequest)
			return
		}
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}
	resp := uiAccessResponse{Results: make([]uiAccessResult, len(req.Checks))}
	checker, tokenReview := s.client.(accessChecker)
	if tokenReview {
		if u, ok := uiauth.UserFrom(r.Context()); ok && u.Username != "" {
			resp.User = &uiAccessUser{Username: u.Username, Groups: u.Groups}
		}
	}
	for i, c := range req.Checks {
		res := uiAccessResult{Allowed: true, GrantedBy: uiauth.RolesFor(c.Verb, c.Resource, c.Subresource)}
		if tokenReview {
			allowed, err := checker.Can(r.Context(), authzv1.ResourceAttributes{
				Namespace: ns, Verb: c.Verb, Group: "kardinal.io", Resource: c.Resource, Subresource: c.Subresource, Name: c.Name,
			})
			if err != nil {
				s.log.Error().Err(err).Msg("ui: access")
				http.Error(w, "access review unavailable", http.StatusServiceUnavailable)
				return
			}
			res.Allowed = allowed
		}
		resp.Results[i] = res
	}
	writeJSON(w, resp)
}
