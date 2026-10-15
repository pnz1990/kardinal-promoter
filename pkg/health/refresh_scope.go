// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health

import (
	"fmt"
	"regexp"
	"strings"
)

// RefreshScope decides which GitOps objects a Pipeline may have the
// controller refresh (health.refresh, #1781). A refresh is a write in the
// controller's name, so a Pipeline refreshes only what belongs to its own
// namespace: an object in the Pipeline's namespace, or one that deploys
// there (Application spec.destination.namespace, Kustomization
// spec.targetNamespace). Rules (--health-refresh-namespaces) admit more,
// per Pipeline namespace. The zero value admits only the Pipeline's own
// namespace.
type RefreshScope struct {
	rules []refreshRule
}

// refreshRule admits, for Pipelines in a namespace matching pipeline, the
// objects whose namespace matches target: the destination when the object
// has one, else the object's own namespace.
type refreshRule struct {
	pipeline, target string
}

// ParseRefreshScope reads rules of the form pipelineNamespace=namespace. Each
// side is a namespace, a prefix ending in "*" ("team-a-*"), or "*" (any);
// "*=*" restores the refresh of any object.
func ParseRefreshScope(entries []string) (RefreshScope, error) {
	var s RefreshScope
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		p, t, ok := strings.Cut(e, "=")
		p, t = strings.TrimSpace(p), strings.TrimSpace(t)
		if !ok || p == "" || t == "" || !validPattern(p) || !validPattern(t) {
			return RefreshScope{}, fmt.Errorf("refresh namespace rule %q: want pipelineNamespace=namespace, each a namespace, a prefix ending in * or *", e)
		}
		s.rules = append(s.rules, refreshRule{pipeline: p, target: t})
	}
	return s, nil
}

// namespacePattern is a namespace name, a prefix of one ending in "*", or "*".
var namespacePattern = regexp.MustCompile(`^(\*|[a-z0-9]([-a-z0-9]*[a-z0-9])?|[a-z0-9][-a-z0-9]*\*)$`)

func validPattern(p string) bool { return namespacePattern.MatchString(p) }

func matchNamespace(pattern, ns string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(ns, prefix)
	}
	return pattern == ns
}

// Rules lists the rules as written, for the controller's start-up log.
func (s RefreshScope) Rules() []string {
	out := make([]string, len(s.rules))
	for i, r := range s.rules {
		out[i] = r.pipeline + "=" + r.target
	}
	return out
}

// Allows reports whether a Pipeline in pipelineNS may refresh t.
func (s RefreshScope) Allows(pipelineNS string, t RefreshTarget) bool {
	if t.Namespace == pipelineNS || t.Destination == pipelineNS {
		return true
	}
	ns := t.Destination
	if ns == "" {
		ns = t.Namespace
	}
	for _, r := range s.rules {
		if matchNamespace(r.pipeline, pipelineNS) && matchNamespace(r.target, ns) {
			return true
		}
	}
	return false
}

// OutOfScope lists the targets a Pipeline in pipelineNS may not refresh, as
// "Kind namespace/name (deploys to dest)".
func (s RefreshScope) OutOfScope(pipelineNS string, targets []RefreshTarget) []string {
	var out []string
	for _, t := range targets {
		if s.Allows(pipelineNS, t) {
			continue
		}
		d := t.String()
		if t.Destination != "" && t.Kind != "GitRepository" {
			d += " (deploys to " + t.Destination + ")"
		}
		out = append(out, d)
	}
	return out
}
