// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// verifyAllowlistVersion is the release from which a verify gate's
// allowedAgents, allowedUsers and allowedGroups are one allowlist (#2411):
// a gate that lists only agents counts no person's verdict. Before it, such
// a gate also counted every person's.
const verifyAllowlistVersion = "v0.13.0"

// verifyGatesLabel is the label of the check.
const verifyGatesLabel = "Verify gates of agents only"

// verifyGatesHint is what to do for a gate the check names.
const verifyGatesHint = "To keep counting the people who verify through these gates, list them in verify.allowedUsers " +
	"or allowedGroups (docs/agentic/verify-with-agents.md#how-verdicts-count)"

// checkVerifyGates lists the verify gate templates that list only agents
// in verify.allowedAgents (no allowedUsers or allowedGroups): from
// verifyAllowlistVersion they count no person's verdict. Against an older
// controller, which still counts every person's verdict on them, it warns
// before the upgrade and names the gates on which a person's verdict counted
// on a gate instance still kept (its status.verdicts). From that release
// (and with a dev build) it passes and names them, so an operator sees which
// gates count agents only; an unknown controller warns. The gates of every
// namespace, or of ns when the user may not list them cluster-wide: then a
// passing result is a WARN, since the other namespaces were not checked.
func checkVerifyGates(ctx context.Context, client sigs_client.Reader, ns, controller string) doctorResult {
	r := doctorResult{label: verifyGatesLabel, icon: doctorPass}
	var list v1alpha1.PolicyGateList
	scope := "all namespaces"
	if err := client.List(ctx, &list); err != nil {
		scope = "namespace " + ns
		if err := client.List(ctx, &list, sigs_client.InNamespace(ns)); err != nil {
			r.icon, r.warned = doctorWarn, true
			r.detail = fmt.Sprintf("could not list PolicyGates: %v", err)
			return r
		}
	}
	// people counts, per template (namespace/name), the gate instances on
	// which a person's verdict counted.
	people := map[string]int{}
	var agentsOnly []string
	for i := range list.Items {
		g := &list.Items[i]
		if tmpl := g.Labels["kardinal.io/gate-template"]; tmpl != "" { // an instance
			tns := g.Labels[graph.LabelGateTemplateNamespace]
			if tns == "" {
				tns = g.Namespace
			}
			for _, v := range g.Status.Verdicts {
				if v.Counted && !strings.HasPrefix(v.Verifier, "agent ") {
					people[tns+"/"+tmpl]++
					break
				}
			}
			continue
		}
		if p := g.Spec.Verify; p != nil && len(p.AllowedAgents) > 0 && len(p.AllowedUsers) == 0 && len(p.AllowedGroups) == 0 {
			agentsOnly = append(agentsOnly, g.Namespace+"/"+g.Name)
		}
	}
	sort.Strings(agentsOnly)
	// A namespaced user's result is not the cluster's: the templates are
	// often in a platform namespace it cannot list.
	partial := func(r doctorResult) doctorResult {
		if scope == "all namespaces" || r.icon != doctorPass {
			return r
		}
		r.icon, r.warned = doctorWarn, true
		r.detail = "checked " + scope + " only: " + r.detail
		r.hint = "Run kardinal doctor as a user who may list PolicyGates in every namespace to check them all"
		return r
	}
	if len(agentsOnly) == 0 {
		r.detail = fmt.Sprintf("no verify gate lists only agents (%s)", scope)
		return partial(r)
	}
	// A dev build is main's: one allowlist. An unknown controller may be
	// older, so it warns as one.
	ver := releaseVersion(controller)
	if controller != "unknown" && (ver == "" || atLeast(ver, verifyAllowlistVersion)) {
		r.detail = fmt.Sprintf("count no person's verdict: %s (%s)", strings.Join(agentsOnly, ", "), scope)
		return partial(r)
	}
	named := make([]string, len(agentsOnly))
	for i, g := range agentsOnly {
		named[i] = g
		if n := people[g]; n > 0 {
			named[i] = fmt.Sprintf("%s (a person's verdict counted on %d gate instances)", g, n)
		}
	}
	r.icon, r.warned = doctorWarn, true
	r.detail = fmt.Sprintf("%s counts only their agents: %s (%s)", verifyAllowlistVersion, strings.Join(named, ", "), scope)
	r.hint = verifyGatesHint
	return r
}
