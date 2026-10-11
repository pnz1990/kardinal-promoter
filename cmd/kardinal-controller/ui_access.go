// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	authzv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// maxAccessChecks bounds one POST /api/v1/ui/access: the UI asks for the
// actions of one screen at a time.
const maxAccessChecks = 32

// maxAccessBody bounds its body: 32 checks with DNS-1123 names fit in 16 KiB.
const maxAccessBody = 16 << 10

// accessActions are the only checks POST /access answers: the actions the UI
// offers ("verb resource[/subresource]"). Anything else is refused, so a
// caller cannot turn the endpoint into a SubjectAccessReview proxy.
var accessActions = map[string]bool{
	"create bundles":              true, // ship, promote, roll back
	"create approvals":            true, // approve, decline
	"update policygates/override": true,
	"update pipelines/pause":      true, // pause, resume
	"update pipelines/hold":       true, // release a hold, roll back and hold
	"update bundles/reject":       true,
}

// approvalNeedsIdentity is the answer for an approval without TokenReview: an
// approval names who decided, so POST /approvals refuses it (ui_lifecycle.go).
const approvalNeedsIdentity = "approving needs sign-in with your Kubernetes identity (the UI's TokenReview mode); kardinal approve works without it"

// accessChecker is the TokenReview-mode client: it answers a check for the
// request's user without recording a denial (uiauth.AuthorizingClient.Can).
type accessChecker interface {
	Can(ctx context.Context, attrs authzv1.ResourceAttributes) (bool, error)
}

// limitedChecker answers a check for the request's user with the /access
// reviewer (uiAuthConfig.accessCheck), as uiauth.AuthorizingClient.Can does
// with the unlimited one.
type limitedChecker struct{ access uiauth.AccessReviewer }

func (c limitedChecker) Can(ctx context.Context, attrs authzv1.ResourceAttributes) (bool, error) {
	user, ok := uiauth.UserFrom(ctx)
	if !ok || user.Username == "" {
		return false, apierrors.NewUnauthorized("no authenticated user")
	}
	allowed, _, err := c.access.Allowed(ctx, user, attrs)
	if err != nil {
		return false, fmt.Errorf("access review: %w", err)
	}
	return allowed, nil
}

// uiAccessCheck is one action the UI asks about: verb on a kardinal.io
// resource (or virtual subresource) in the request's namespace.
type uiAccessCheck struct {
	// Verb and Resource (with Subresource) name one of the UI's actions:
	// create bundles, create approvals, update policygates/override,
	// update pipelines/pause, update pipelines/hold or update bundles/reject.
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
	// Reason says why it is not allowed when that is not a missing role
	// (an approval without TokenReview).
	Reason string `json:"reason,omitempty"`
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
	// allowed (the UI API then acts as the controller for anyone who reaches
	// it) except create approvals, which needs the caller's identity.
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAccessBody)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Checks) == 0 || len(req.Checks) > maxAccessChecks {
		http.Error(w, fmt.Sprintf("checks must hold 1 to %d entries", maxAccessChecks), http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}
	if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
		http.Error(w, "namespace must be a DNS-1123 label", http.StatusBadRequest)
		return
	}
	if s.scopeNamespace != "" && ns != s.scopeNamespace {
		http.Error(w, fmt.Sprintf("the controller watches namespace %s only", s.scopeNamespace), http.StatusBadRequest)
		return
	}
	for i, c := range req.Checks {
		action := c.Verb + " " + c.Resource
		if c.Subresource != "" {
			action += "/" + c.Subresource
		}
		if !accessActions[action] {
			http.Error(w, fmt.Sprintf("checks[%d]: %q is not an action the UI offers", i, action), http.StatusBadRequest)
			return
		}
		if c.Name != "" {
			if errs := validation.IsDNS1123Subdomain(c.Name); len(errs) > 0 {
				http.Error(w, fmt.Sprintf("checks[%d]: name must be a DNS-1123 subdomain", i), http.StatusBadRequest)
				return
			}
		}
	}
	resp := uiAccessResponse{Results: make([]uiAccessResult, len(req.Checks))}
	checker, tokenReview := s.client.(accessChecker)
	if tokenReview && s.accessCheck != nil {
		checker = limitedChecker{s.accessCheck}
	}
	if tokenReview {
		if u, ok := uiauth.UserFrom(r.Context()); ok && u.Username != "" {
			resp.User = &uiAccessUser{Username: u.Username, Groups: u.Groups}
		}
	}
	// The same check twice is reviewed once.
	answered := map[uiAccessCheck]uiAccessResult{}
	for i, c := range req.Checks {
		if res, ok := answered[c]; ok {
			resp.Results[i] = res
			continue
		}
		res := uiAccessResult{Allowed: true, GrantedBy: uiauth.RolesFor(c.Verb, c.Resource, c.Subresource)}
		switch {
		case tokenReview:
			allowed, err := checker.Can(r.Context(), authzv1.ResourceAttributes{
				Namespace: ns, Verb: c.Verb, Group: "kardinal.io", Resource: c.Resource, Subresource: c.Subresource, Name: c.Name,
			})
			if errors.Is(err, uiauth.ErrAccessTotalRateLimited) {
				http.Error(w, "too many authorization checks from all users together; try again in a minute", http.StatusTooManyRequests)
				return
			}
			if errors.Is(err, uiauth.ErrAccessRateLimited) {
				http.Error(w, "too many authorization checks for this user; try again in a minute", http.StatusTooManyRequests)
				return
			}
			if err != nil {
				s.log.Error().Err(err).Msg("ui: access")
				http.Error(w, "access review unavailable", http.StatusServiceUnavailable)
				return
			}
			res.Allowed = allowed
		case c.Verb == "create" && c.Resource == "approvals":
			res.Allowed, res.Reason = false, approvalNeedsIdentity
		}
		answered[c] = res
		resp.Results[i] = res
	}
	writeJSON(w, resp)
}
