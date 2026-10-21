// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// unverifiedHeldLabel is the label of the check.
const unverifiedHeldLabel = "Versions held as unverified"

// unverifiedOverrideReason is the --reason of the override the check offers.
const unverifiedOverrideReason = "shipped while the agent index was not Ready"

// maxUnverifiedHeld is how many held versions the check names; the rest are
// counted.
const maxUnverifiedHeld = 20

// unverifiedHeld is one version an agent gate holds because its stamp is
// unverified.
type unverifiedHeld struct {
	ns, pipeline, env, bundle, gate, by string
	// others are the other gates that hold it in env.
	others []string
}

// checkUnverifiedHeld lists the versions that a kardinal-agents-* gate holds
// because their actor stamp is unverified (#2451): written while the
// AgentIndex was not Ready, a person's version counts as an agent's, and
// stays held after the index recovers until it is shipped again or a person
// overrides the gate. Each comes with the one command that lets it through.
// A version is left out once it is done (Verified, Failed, Superseded or
// Rejected), and when the Ready index now names its writer as an agent by
// user: it is that agent's, and the agent's rules hold it. A version other
// gates hold too says so, since the override lets it past the agent gate
// only. While no AgentIndex is Ready the result is an INFO: fix the index
// first. The gates of every namespace, or of ns when the user may not list
// them cluster-wide: then a passing result is a WARN.
func checkUnverifiedHeld(ctx context.Context, c sigs_client.Reader, ns string) doctorResult {
	r := doctorResult{label: unverifiedHeldLabel, icon: doctorPass}
	var list v1alpha1.PolicyGateList
	scope := "all namespaces"
	if err := c.List(ctx, &list); err != nil {
		scope = "namespace " + ns
		if err := c.List(ctx, &list, sigs_client.InNamespace(ns)); err != nil {
			r.icon, r.warned = doctorWarn, true
			r.detail = fmt.Sprintf("could not list PolicyGates: %v", err)
			return r
		}
	}
	// held gate instances by namespace/bundle/environment.
	type key struct{ ns, bundle, env string }
	agentGates := map[key]*v1alpha1.PolicyGate{}
	blocking := map[key][]string{}
	for i := range list.Items {
		g := &list.Items[i]
		k := key{g.Namespace, g.Labels["kardinal.io/bundle"], g.Labels["kardinal.io/environment"]}
		if k.bundle == "" || g.Status.Ready || g.Status.LastEvaluatedAt == nil {
			continue
		}
		tmpl := g.Labels["kardinal.io/gate-template"]
		if strings.HasPrefix(tmpl, graph.AgentGatePrefix) {
			agentGates[k] = g
			continue
		}
		blocking[k] = append(blocking[k], gateDisplayName(*g))
	}
	var idx *v1alpha1.AgentIndexStatus
	idxRead, idxErr := false, error(nil)
	var held []unverifiedHeld
	for k, g := range agentGates {
		var b v1alpha1.Bundle
		if err := c.Get(ctx, types.NamespacedName{Namespace: k.ns, Name: k.bundle}, &b); err != nil {
			continue
		}
		if b.Spec.Actor == nil || b.Spec.Actor.Kind != v1alpha1.ActorUnverified {
			continue
		}
		switch b.Status.Phase {
		case "Verified", "Failed", "Superseded", "Rejected":
			continue
		}
		if !idxRead {
			idx, idxErr = (&actor.Source{Reader: c}).Index(ctx)
			idxRead = true
		}
		if idxErr == nil && actor.Reverify(*b.Spec.Actor, idx).Kind == v1alpha1.ActorAgent {
			continue // the agent's: its rules hold it
		}
		others := append([]string(nil), blocking[k]...)
		sort.Strings(others)
		held = append(held, unverifiedHeld{ns: k.ns, pipeline: g.Labels["kardinal.io/pipeline"], env: k.env,
			bundle: k.bundle, gate: g.Labels["kardinal.io/gate-template"], by: b.Spec.Actor.Principal, others: others})
	}
	sort.Slice(held, func(i, j int) bool {
		a, b := held[i], held[j]
		if a.ns != b.ns {
			return a.ns < b.ns
		}
		if a.pipeline != b.pipeline {
			return a.pipeline < b.pipeline
		}
		if a.env != b.env {
			return a.env < b.env
		}
		return a.bundle < b.bundle
	})
	if len(held) == 0 {
		r.detail = fmt.Sprintf("no version is held because of an unverified stamp (%s)", scope)
		if scope != "all namespaces" {
			r.icon, r.warned = doctorWarn, true
			r.detail = "checked " + scope + " only: " + r.detail
			r.hint = "Run kardinal doctor as a user who may list PolicyGates in every namespace to check them all"
		}
		return r
	}
	if errors.Is(idxErr, actor.ErrIndexNotReady) {
		r.icon, r.noted = doctorInfo, true
		r.detail = fmt.Sprintf("%d version(s) stamped unverified are held, and the agent index is not Ready yet (%s)",
			len(held), scope)
		r.hint = "Make the index Ready first (kubectl get agentindex; docs/actors.md#while-the-index-isnt-ready), then run doctor again"
		return r
	}
	names := make([]string, 0, len(held))
	fixes := make([]string, 0, len(held))
	for i, h := range held {
		if i == maxUnverifiedHeld {
			names = append(names, fmt.Sprintf("and %d more", len(held)-maxUnverifiedHeld))
			break
		}
		name := fmt.Sprintf("%s/%s %s: %s by %s", h.ns, h.pipeline, h.env, h.bundle, h.by)
		fix := fmt.Sprintf("kardinal override %s %s --gate %s --reason %q -n %s", h.pipeline, h.env, h.gate,
			unverifiedOverrideReason, h.ns)
		if len(h.others) > 0 {
			name += " (also held by " + strings.Join(h.others, ", ") + ")"
			fix += "   # also held by " + strings.Join(h.others, ", ")
		}
		names = append(names, name)
		fixes = append(fixes, fix)
	}
	r.icon, r.warned = doctorWarn, true
	r.detail = fmt.Sprintf("%d version(s) shipped while the agent index was not Ready are held: %s (%s)",
		len(held), strings.Join(names, "; "), scope)
	r.hint = "Override each one, or ship it again:\n" + strings.Join(fixes, "\n")
	return r
}
