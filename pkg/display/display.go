// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package display maps kardinal's API states to what a person reads: a label,
// a glyph (with an ASCII fallback for terminals without Unicode) and a tone,
// the status bucket of the design system (kardinal-parity/DESIGN-SYSTEM.md,
// "Status"). It is the one table the CLI, the UI API and the UI use, so the
// three surfaces say the same words for the same state.
//
// It is display only: the API values (Pipeline and Bundle phase, PromotionStep
// and sub-step state, PolicyGate state) are unchanged, and no decision reads a
// label. A value the table does not know still gets a readable label (its
// CamelCase split into words) and ToneUnknown.
package display

import (
	"strings"
	"unicode"
)

// Tone is the status bucket a state belongs to: it picks the colour, the
// shape and the glyph family. Hue is never the only cue.
type Tone string

const (
	ToneVerified   Tone = "verified"   // live, passing: healthy is quiet
	TonePromoting  Tone = "promoting"  // kardinal is acting (writing, checking health, verifying)
	ToneWaiting    Tone = "waiting"    // needs a person or an external event (PR review, a gate, a time)
	ToneBlocked    Tone = "blocked"    // a gate holds it back
	ToneFailed     Tone = "failed"     // failed, stopped, rolling back, rejected
	ToneSuperseded Tone = "superseded" // replaced by a newer version: final, not a failure
	TonePaused     Tone = "paused"     // the Pipeline is paused
	ToneHeld       Tone = "held"       // an environment is held (rollback --hold)
	TonePending    Tone = "pending"    // not reached yet
	ToneDegraded   Tone = "degraded"   // something failed somewhere below (Pipeline)
	ToneUnknown    Tone = "unknown"    // a value this table does not know
)

// State is how one API value is shown.
type State struct {
	// Value is the API value, unchanged (for example "WaitingForMerge").
	Value string `json:"value"`
	// Label is the human text (for example "Waiting for PR merge").
	Label string `json:"label"`
	// Glyph is a one-character Unicode symbol for the state.
	Glyph string `json:"glyph"`
	// ASCII is Glyph for a terminal without Unicode (NO_COLOR, TERM=dumb).
	ASCII string `json:"ascii"`
	// Tone is the status bucket.
	Tone Tone `json:"tone"`
}

// Kind is the object a state belongs to: the same value means different
// things on different kinds (a Verified Bundle is verified everywhere, a
// Verified step is live in its environment).
type Kind string

const (
	KindPipeline Kind = "pipeline" // Pipeline status.phase, and Paused
	KindBundle   Kind = "bundle"   // Bundle status.phase
	KindStep     Kind = "step"     // PromotionStep status.state, and NotStarted (not reached)
	KindSubStep  Kind = "substep"  // a PromotionStep's status.steps[].state
	KindGate     Kind = "gate"     // the state of a gate instance (graph.GateState)
)

func s(value, label, glyph, ascii string, tone Tone) State {
	return State{Value: value, Label: label, Glyph: glyph, ASCII: ascii, Tone: tone}
}

// table is every known value of every kind.
var table = map[Kind]map[string]State{
	KindPipeline: index(
		s("Ready", "Up to date", "●", "*", ToneVerified),
		s("Promoting", "Promoting", "◐", ">", TonePromoting),
		s("Degraded", "A version failed", "!", "!", ToneDegraded),
		s("Unknown", "No version yet", "○", "o", TonePending),
		s("Paused", "Paused", "‖", "||", TonePaused),
	),
	KindBundle: index(
		s("Available", "Ready to promote", "○", "o", TonePending),
		s("Promoting", "Promoting", "◐", ">", TonePromoting),
		s("Verified", "Verified", "●", "*", ToneVerified),
		s("Failed", "Failed", "✕", "x", ToneFailed),
		s("Superseded", "Replaced by newer version", "↷", "-", ToneSuperseded),
		s("Rejected", "Rejected", "⊘", "x", ToneFailed),
	),
	KindStep: index(
		s("NotStarted", "Not reached", "○", "o", TonePending),
		s("Pending", "Queued", "○", "o", TonePending),
		s("Promoting", "Promoting", "◐", ">", TonePromoting),
		s("WaitingForMerge", "Waiting for PR merge", "◷", "~", ToneWaiting),
		s("HealthChecking", "Checking health", "◌", "~", TonePromoting),
		s("Verifying", "Verifying", "◎", "~", TonePromoting),
		s("Verified", "Live", "●", "*", ToneVerified),
		s("Failed", "Failed", "✕", "x", ToneFailed),
		s("AbortedByAlarm", "Stopped by alarm", "✕", "x", ToneFailed),
		s("RollingBack", "Rolling back", "↺", "<", ToneFailed),
		s("Superseded", "Replaced by newer version", "↷", "-", ToneSuperseded),
	),
	KindSubStep: index(
		s("Pending", "Not started", "○", "o", TonePending),
		s("InProgress", "In progress", "◐", ">", TonePromoting),
		s("Completed", "Done", "✓", "+", ToneVerified),
		s("Failed", "Failed", "✕", "x", ToneFailed),
	),
	KindGate: index(
		s("Pass", "Passing", "✓", "+", ToneVerified),
		s("Block", "Blocking", "▮", "#", ToneBlocked),
		s("Waiting", "Not ready", "◷", "~", ToneWaiting),
		s("Pending", "Not evaluated yet", "○", "o", ToneWaiting),
		s("Superseded", "Replaced by newer version", "↷", "-", ToneSuperseded),
		s("Rejected", "Version rejected", "⊘", "-", ToneSuperseded),
	),
}

func index(states ...State) map[string]State {
	m := make(map[string]State, len(states))
	for _, st := range states {
		m[st.Value] = st
	}
	return m
}

// Of returns how value of kind is shown. An unknown value is labelled by
// splitting its CamelCase into words, with ToneUnknown; "" is "Unknown".
func Of(kind Kind, value string) State {
	if st, ok := table[kind][value]; ok {
		return st
	}
	label := Words(value)
	if label == "" {
		label = "Unknown"
	}
	return State{Value: value, Label: label, Glyph: "?", ASCII: "?", Tone: ToneUnknown}
}

// Pipeline is Of(KindPipeline, phase), or Paused when the Pipeline is paused.
func Pipeline(phase string, paused bool) State {
	if paused {
		return Of(KindPipeline, "Paused")
	}
	return Of(KindPipeline, phase)
}

// Bundle is Of(KindBundle, phase).
func Bundle(phase string) State { return Of(KindBundle, phase) }

// Step is Of(KindStep, state).
func Step(state string) State { return Of(KindStep, state) }

// SubStep is Of(KindSubStep, state).
func SubStep(state string) State { return Of(KindSubStep, state) }

// Gate is Of(KindGate, state).
func Gate(state string) State { return Of(KindGate, state) }

// HeldByGate is an environment whose step has not started because gate
// holds it: "Held by gate <gate>", ToneBlocked. Value stays the step state.
func HeldByGate(stepState, gate string) State {
	return State{Value: stepState, Label: "Held by gate " + gate, Glyph: "⏸", ASCII: "||", Tone: ToneBlocked}
}

// Held is an environment held by a rollback (kardinal rollback --hold).
func Held(stepState string) State {
	return State{Value: stepState, Label: "Held by rollback", Glyph: "⊙", ASCII: "@", Tone: ToneHeld}
}

// Table returns every known state of every kind: what the UI API serves at
// /api/v1/ui/display-states. The maps are copies.
func Table() map[Kind]map[string]State {
	out := make(map[Kind]map[string]State, len(table))
	for k, m := range table {
		c := make(map[string]State, len(m))
		for v, st := range m {
			c[v] = st
		}
		out[k] = c
	}
	return out
}

// Text is the state for a person: the glyph and the label ("◷ Waiting for PR
// merge"), or with ascii the ASCII glyph ("~ Waiting for PR merge").
func (st State) Text(ascii bool) string {
	g := st.Glyph
	if ascii {
		g = st.ASCII
	}
	return g + " " + st.Label
}

// Words splits a CamelCase value into lower-case words after the first
// ("WaitingForMerge" → "Waiting for merge"); a value with spaces, or one
// word, is returned as is.
func Words(value string) string {
	if strings.ContainsAny(value, " -_") {
		return value
	}
	var b strings.Builder
	rs := []rune(value)
	for i, r := range rs {
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
			b.WriteRune(' ')
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ValueOf is the API value whose label of kind is label, for a reader of the
// CLI's table output (tests, scripts that cannot use -o json); "" when no
// known state has it.
func ValueOf(kind Kind, label string) string {
	for v, st := range table[kind] {
		if st.Label == label {
			return v
		}
	}
	return ""
}
