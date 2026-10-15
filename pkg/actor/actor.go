// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package actor derives, compares and describes actors: who made a change,
// and for whom (docs/design/17-actors.md). The chart's <release>-actor
// ValidatingAdmissionPolicy is a transliteration of Derive and Scope; the
// controller (UI API, Bundle API) and the CLI call them directly, so all
// three agree on what a principal is.
package actor

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ServiceAccountPrefix starts the username of every ServiceAccount.
const ServiceAccountPrefix = "system:serviceaccount:"

// BundleAPIPrincipal is the principal of a Bundle created with the Bundle
// API's static token: no Kubernetes identity stands behind it.
const BundleAPIPrincipal = "bundle-api"

// Errors Derive and Scope return. Callers map them to 403.
var (
	// ErrConflict: the requester matches more than one agent.
	ErrConflict = errors.New("matches more than one AgentIdentity")
	// ErrBlocked: the requester is an agent that may not act (suspended or
	// not ready).
	ErrBlocked = errors.New("agent may not act")
	// ErrOutOfScope: the agent may not act here.
	ErrOutOfScope = errors.New("out of the agent's scope")
)

// User is the authenticated requester: what TokenReview, SelfSubjectReview
// and request.userInfo say.
type User struct {
	Username string
	Groups   []string
}

// Resolved is a derived actor and, for an agent, its index entry.
type Resolved struct {
	Actor v1alpha1.Actor
	Entry *v1alpha1.AgentIndexEntry
}

// Derive returns the actor for u according to idx (nil: no agents). It is
// what the <release>-actor policy computes: an agent when u's username or
// one of its groups is in the index, a bot for a ServiceAccount, else a
// human. It fails when u matches two agents, or an agent that is blocked.
func Derive(u User, idx *v1alpha1.AgentIndexStatus) (Resolved, error) {
	var matches []v1alpha1.AgentIndexEntry
	if idx != nil {
		if e, ok := idx.Users[u.Username]; ok {
			matches = append(matches, e)
		}
		for _, g := range u.Groups {
			if e, ok := idx.Groups[g]; ok {
				matches = append(matches, e)
			}
		}
	}
	if len(matches) == 0 {
		kind := v1alpha1.ActorHuman
		if strings.HasPrefix(u.Username, ServiceAccountPrefix) || u.Username == BundleAPIPrincipal {
			kind = v1alpha1.ActorBot
		}
		return Resolved{Actor: v1alpha1.Actor{Kind: kind, Principal: u.Username}}, nil
	}
	first := matches[0]
	for _, m := range matches[1:] {
		if m.Agent != first.Agent {
			return Resolved{}, fmt.Errorf("%s: %s and %s: %w", u.Username, first.Agent, m.Agent, ErrConflict)
		}
	}
	for _, m := range matches {
		if m.Blocked != "" {
			return Resolved{}, fmt.Errorf("agent %s: %s: %w", m.Agent, m.Blocked, ErrBlocked)
		}
	}
	e := first
	return Resolved{
		Actor: v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: u.Username, Agent: e.Agent, Sponsor: e.Sponsor},
		Entry: &e,
	}, nil
}

// Where is the target of a write, for an agent's scope: the namespace, and
// the Pipeline or environment when the write names one.
type Where struct {
	Namespace   string
	Pipeline    string
	Environment string
}

// Scope reports whether the agent entry e may act at w (nil e: not an
// agent, anything goes).
func Scope(e *v1alpha1.AgentIndexEntry, w Where) error {
	if e == nil {
		return nil
	}
	if !Matches(e.Namespaces, w.Namespace) {
		return fmt.Errorf("agent %s may not act in namespace %s (scope.namespaces): %w", e.Agent, w.Namespace, ErrOutOfScope)
	}
	if w.Pipeline != "" && !Matches(e.Pipelines, w.Pipeline) {
		return fmt.Errorf("agent %s may not act on pipeline %s (scope.pipelines): %w", e.Agent, w.Pipeline, ErrOutOfScope)
	}
	if w.Environment != "" && !Matches(e.Environments, w.Environment) {
		return fmt.Errorf("agent %s may not act in environment %s (scope.environments): %w", e.Agent, w.Environment, ErrOutOfScope)
	}
	return nil
}

// Matches reports whether value is in patterns: an exact name, or a prefix
// ending in "*" ("*" alone matches everything). An empty list matches nothing.
func Matches(patterns []string, value string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			return strings.HasPrefix(value, prefix)
		}
		return p == value
	})
}

// Stamp returns the stamp for r with the asserted fields a (nil: none).
func Stamp(r Resolved, a *v1alpha1.AssertedActor) *v1alpha1.Actor {
	out := r.Actor
	if a != nil && (a.Model != "" || a.Session != "" || a.Client != "") {
		cp := *a
		out.Asserted = &cp
	}
	return &out
}

// OnBehalf is the stamp the controller writes for a caller it authenticated
// (via ui or api).
func OnBehalf(r Resolved, via string) *v1alpha1.Actor {
	out := r.Actor
	out.Via = via
	return &out
}

// Controller is the stamp of a record kardinal writes on its own: principal
// is the controller's username, via subscription or auto-rollback.
func Controller(principal, via string) *v1alpha1.Actor {
	return &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: principal, Via: via}
}

// OrUnknown returns a, or for a record without a stamp the read-side actor
// {kind: unknown, principal: legacy} (legacy is the v0.11 principal:
// kardinal.io/created-by, spec.user or createdBy; "" when there is none).
func OrUnknown(a *v1alpha1.Actor, legacy string) v1alpha1.Actor {
	if a != nil {
		return *a
	}
	return v1alpha1.Actor{Kind: v1alpha1.ActorUnknown, Principal: legacy}
}

// Describe is the sentence that names a everywhere (PR evidence, UI, CLI).
func Describe(a v1alpha1.Actor) string {
	var s string
	switch a.Kind {
	case v1alpha1.ActorAgent:
		s = fmt.Sprintf("agent %s for %s", a.Agent, a.Sponsor)
	case v1alpha1.ActorBot:
		s = "bot " + a.Principal
	case v1alpha1.ActorController:
		s = "kardinal (" + a.Via + ")"
		return s
	case v1alpha1.ActorHuman:
		s = a.Principal
	default:
		// A record without a stamp: its v0.11 principal, when it has one.
		if a.Principal == "" {
			return "unknown"
		}
		return a.Principal
	}
	switch a.Via {
	case v1alpha1.ViaUI:
		s += " via the UI"
	case v1alpha1.ViaAPI:
		s += " via the API"
	}
	return s
}

// DescribeAsserted is the asserted part, labelled as such, or "".
func DescribeAsserted(a v1alpha1.Actor) string {
	if a.Asserted == nil {
		return ""
	}
	var parts []string
	if a.Asserted.Model != "" {
		parts = append(parts, "model "+a.Asserted.Model)
	}
	if a.Asserted.Session != "" {
		parts = append(parts, "session "+a.Asserted.Session)
	}
	if a.Asserted.Client != "" {
		parts = append(parts, "client "+a.Asserted.Client)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + " (asserted)"
}

// SubjectKey is the index key of subject s: a ServiceAccount's username, or
// the user or group name.
func SubjectKey(s v1alpha1.AgentSubject) string {
	if s.Kind == "ServiceAccount" {
		return ServiceAccountPrefix + s.Namespace + ":" + s.Name
	}
	return s.Name
}
