// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow

import (
	"fmt"
	"sort"
	"strings"
	"time"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// A freeze is a ChangeWindow with spec.freeze: while it is active it holds
// the promotions it selects by itself, without a gate naming it
// (docs/design/18-actor-policy.md section 9). The PromotionStep reconciler
// holds a step with FreezesFor; the CLI and the UI API list and write them.

// FreezeTarget is the promotion a freeze may hold: one environment of one
// version of a pipeline, and who created the version.
type FreezeTarget struct {
	Namespace, Pipeline, Environment, Bundle string
	// Author is the version's actor (lifecycle.BundleAuthor); only its kind
	// matters: an agent freeze holds versions whose stamp says agent.
	Author kardinalv1alpha1.Actor
}

// FreezeMatch is an active freeze that selects a target.
type FreezeMatch struct {
	// Window is the freezing ChangeWindow.
	Window *kardinalv1alpha1.ChangeWindow
	// Exemption is the exemption that lets the target through, nil when the
	// freeze holds it.
	Exemption *kardinalv1alpha1.FreezeExemption
	// Since is when the freeze started.
	Since time.Time
}

// FreezeSelects reports whether active freeze spec f selects target t by
// namespace and actor (exemptions aside).
func FreezeSelects(f *kardinalv1alpha1.ChangeWindowFreeze, t FreezeTarget) bool {
	if f == nil {
		return false
	}
	if len(f.Namespaces) > 0 && !actor.Matches(f.Namespaces, t.Namespace) {
		return false
	}
	switch f.Actors {
	case kardinalv1alpha1.FreezeEveryone:
		return true
	case kardinalv1alpha1.FreezeAgents:
		return t.Author.Kind == kardinalv1alpha1.ActorAgent
	}
	return true // an unknown selector holds: a freeze must fail closed
}

// exemptionFor is the first exemption of f in effect at now for t, or nil.
func exemptionFor(f *kardinalv1alpha1.ChangeWindowFreeze, t FreezeTarget, now time.Time) *kardinalv1alpha1.FreezeExemption {
	for i := range f.Exemptions {
		e := &f.Exemptions[i]
		if e.Namespace != t.Namespace || e.Pipeline != t.Pipeline {
			continue
		}
		if (e.Environment != "" && e.Environment != t.Environment) || (e.Bundle != "" && e.Bundle != t.Bundle) {
			continue
		}
		if !now.Before(e.ExpiresAt.Time) {
			continue
		}
		return e
	}
	return nil
}

// FreezesFor returns the freezes among windows that are active at now and
// select t, sorted by name, and the next instant the answer can change (a
// window boundary or an exemption's expiry; zero when none is known). A
// freeze whose spec cannot be evaluated is active: a freeze fails closed.
func FreezesFor(windows []kardinalv1alpha1.ChangeWindow, t FreezeTarget, now time.Time) ([]FreezeMatch, time.Time) {
	var out []FreezeMatch
	var next time.Time
	earliest := func(at time.Time) {
		if !at.IsZero() && at.After(now) && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	for i := range windows {
		w := &windows[i]
		if w.Spec.Freeze == nil || !w.DeletionTimestamp.IsZero() {
			continue
		}
		res := Evaluate(w.Spec, now)
		earliest(res.Next)
		if !res.Active || !FreezeSelects(w.Spec.Freeze, t) {
			continue
		}
		m := FreezeMatch{Window: w, Since: w.Spec.Start.Time}
		if e := exemptionFor(w.Spec.Freeze, t, now); e != nil {
			m.Exemption = e
			earliest(e.ExpiresAt.Time)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Window.Name < out[j].Window.Name })
	return out, next
}

// Holding returns the matches that hold the target (no exemption).
func Holding(matches []FreezeMatch) []FreezeMatch {
	var out []FreezeMatch
	for _, m := range matches {
		if m.Exemption == nil {
			out = append(out, m)
		}
	}
	return out
}

// FreezeScope describes whom and where a freeze holds, for messages:
// "agents, every namespace", "everyone, namespaces payments-*".
func FreezeScope(f *kardinalv1alpha1.ChangeWindowFreeze) string {
	who := "everyone"
	if f.Actors == kardinalv1alpha1.FreezeAgents {
		who = "agents"
	}
	where := "every namespace"
	if len(f.Namespaces) > 0 {
		where = "namespaces " + strings.Join(f.Namespaces, ", ")
	}
	return who + ", " + where
}

// FrozenMessage is the step message of a step m holds. It names the
// freeze, who froze and why, and the two ways on.
func FrozenMessage(m FreezeMatch, pipeline, env string) string {
	f := m.Window.Spec.Freeze
	by := f.CreatedBy
	if by == "" {
		by = "(unknown)"
	}
	msg := fmt.Sprintf("frozen by %s (%s) since %s by %s", m.Window.Name, FreezeScope(f),
		m.Since.UTC().Format(time.RFC3339), by)
	if r := m.Window.Spec.Reason; r != "" {
		msg += ": " + r
	}
	return msg + fmt.Sprintf(" — lift it with: kardinal unfreeze %s, or let this through with: kardinal override %s %s --freeze --reason <why>",
		m.Window.Name, pipeline, env)
}
