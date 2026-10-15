// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"regexp"
	"strings"
	"time"

	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
)

// windowGateExpr is a gate expression that is one ChangeWindow read and
// nothing else: changewindow.isAllowed("name") or
// !changewindow.isBlocked("name"), as a soak comparison is recognised
// (soak.go). Such a gate opens when the window stops blocking.
var windowGateExpr = regexp.MustCompile(`^\(?\s*(?:changewindow\.isAllowed\(\s*"([^"\\]+)"\s*\)|!\s*changewindow\.isBlocked\(\s*"([^"\\]+)"\s*\))\s*\)?$`)

// windowOfGate is the ChangeWindow a gate's whole expression reads, or "".
func windowOfGate(g *v1alpha1.PolicyGate) string {
	if g == nil || g.Spec.Approval != nil {
		return ""
	}
	m := windowGateExpr.FindStringSubmatch(strings.TrimSpace(g.Spec.Expression))
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}

// changeWindows reads the ChangeWindows (cluster-scoped) when one of gates
// is a window gate; nil when none is, or the caller may not read them (then
// nothing is said about when such a gate opens).
func changeWindows(ctx context.Context, c sigs_client.Client, gates []v1alpha1.PolicyGate) []v1alpha1.ChangeWindow {
	for i := range gates {
		if windowOfGate(&gates[i]) != "" {
			var list v1alpha1.ChangeWindowList
			if err := c.List(ctx, &list); err != nil {
				return nil
			}
			return list.Items
		}
	}
	return nil
}

// windowOpens is when gate g, a window gate, opens by itself after now, in
// words ("at Thursday 04:25 UTC, in about 18m"), or "" when that is not
// known: g is not a window gate, the window is not among windows, or it
// blocks until someone changes it.
func windowOpens(g *v1alpha1.PolicyGate, windows []v1alpha1.ChangeWindow, now time.Time) string {
	name := windowOfGate(g)
	if name == "" {
		return ""
	}
	for i := range windows {
		if windows[i].Name != name {
			continue
		}
		at, ok := changewindow.OpensAt(windows[i].Spec, now)
		if !ok || !at.After(now) {
			return ""
		}
		return "at " + at.UTC().Format("Monday 15:04 UTC") + ", in about " + durationText(at.Sub(now).Round(time.Minute))
	}
	return ""
}
