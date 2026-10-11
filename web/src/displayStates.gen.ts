// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Code generated from pkg/display (TestTypeScriptTable). DO NOT EDIT.
// Regenerate: KARDINAL_UPDATE_DISPLAY=1 go test ./pkg/display -run TestTypeScriptTable
//
// The display-state table (P2): the label, glyph and tone of every API state,
// the same words as the CLI. The API values are unchanged; this is display only.

/** The design-system status bucket of a state: picks colour and shape. */
export type Tone =
  | 'verified' | 'promoting' | 'waiting' | 'blocked' | 'failed' | 'superseded'
  | 'paused' | 'held' | 'pending' | 'degraded' | 'unknown'

/** The object a state belongs to. */
export type DisplayKind = 'pipeline' | 'bundle' | 'step' | 'substep' | 'gate'

/** How one API value is shown. */
export interface DisplayState {
  /** The API value, unchanged (e.g. 'WaitingForMerge'). */
  value: string
  /** What a person reads (e.g. 'Waiting for PR merge'). */
  label: string
  /** A one-character Unicode symbol. */
  glyph: string
  /** The glyph without Unicode. */
  ascii: string
  tone: Tone
}

export const DISPLAY_STATES: Record<DisplayKind, Record<string, DisplayState>> = {
  pipeline: {
    Degraded: { value: "Degraded", label: "A version failed", glyph: "!", ascii: "!", tone: "degraded" },
    Paused: { value: "Paused", label: "Paused", glyph: "‖", ascii: "||", tone: "paused" },
    Promoting: { value: "Promoting", label: "Promoting", glyph: "◐", ascii: ">", tone: "promoting" },
    Ready: { value: "Ready", label: "Up to date", glyph: "●", ascii: "*", tone: "verified" },
    Unknown: { value: "Unknown", label: "No version yet", glyph: "○", ascii: "o", tone: "pending" },
  },
  bundle: {
    Available: { value: "Available", label: "Ready to promote", glyph: "○", ascii: "o", tone: "pending" },
    Failed: { value: "Failed", label: "Failed", glyph: "✕", ascii: "x", tone: "failed" },
    Promoting: { value: "Promoting", label: "Promoting", glyph: "◐", ascii: ">", tone: "promoting" },
    Rejected: { value: "Rejected", label: "Rejected", glyph: "⊘", ascii: "x", tone: "failed" },
    Superseded: { value: "Superseded", label: "Replaced by newer version", glyph: "↷", ascii: "-", tone: "superseded" },
    Verified: { value: "Verified", label: "Verified", glyph: "●", ascii: "*", tone: "verified" },
  },
  step: {
    AbortedByAlarm: { value: "AbortedByAlarm", label: "Stopped by alarm", glyph: "✕", ascii: "x", tone: "failed" },
    Failed: { value: "Failed", label: "Failed", glyph: "✕", ascii: "x", tone: "failed" },
    HealthChecking: { value: "HealthChecking", label: "Checking health", glyph: "◌", ascii: "~", tone: "promoting" },
    NotStarted: { value: "NotStarted", label: "Not reached", glyph: "○", ascii: "o", tone: "pending" },
    Pending: { value: "Pending", label: "Queued", glyph: "○", ascii: "o", tone: "pending" },
    Promoting: { value: "Promoting", label: "Promoting", glyph: "◐", ascii: ">", tone: "promoting" },
    RollingBack: { value: "RollingBack", label: "Rolling back", glyph: "↺", ascii: "<", tone: "failed" },
    Superseded: { value: "Superseded", label: "Replaced by newer version", glyph: "↷", ascii: "-", tone: "superseded" },
    Verified: { value: "Verified", label: "Live", glyph: "●", ascii: "*", tone: "verified" },
    Verifying: { value: "Verifying", label: "Verifying", glyph: "◎", ascii: "~", tone: "promoting" },
    WaitingForMerge: { value: "WaitingForMerge", label: "Waiting for PR merge", glyph: "◷", ascii: "~", tone: "waiting" },
  },
  substep: {
    Completed: { value: "Completed", label: "Done", glyph: "✓", ascii: "+", tone: "verified" },
    Failed: { value: "Failed", label: "Failed", glyph: "✕", ascii: "x", tone: "failed" },
    InProgress: { value: "InProgress", label: "In progress", glyph: "◐", ascii: ">", tone: "promoting" },
    Pending: { value: "Pending", label: "Not started", glyph: "○", ascii: "o", tone: "pending" },
  },
  gate: {
    Block: { value: "Block", label: "Blocking", glyph: "▮", ascii: "#", tone: "blocked" },
    Pass: { value: "Pass", label: "Passing", glyph: "✓", ascii: "+", tone: "verified" },
    Pending: { value: "Pending", label: "Not evaluated yet", glyph: "○", ascii: "o", tone: "waiting" },
    Rejected: { value: "Rejected", label: "Version rejected", glyph: "⊘", ascii: "-", tone: "superseded" },
    Superseded: { value: "Superseded", label: "Replaced by newer version", glyph: "↷", ascii: "-", tone: "superseded" },
    Waiting: { value: "Waiting", label: "Not ready", glyph: "◷", ascii: "~", tone: "waiting" },
  },
}

/** Splits a CamelCase value into words, like Go's display.Words ('WaitingForMerge' → 'Waiting for merge'). */
export function words(value: string): string {
  if (/[ \-_]/.test(value)) return value
  let out = ''
  for (let i = 0; i < value.length; i++) {
    const c = value[i]
    const upper = c >= 'A' && c <= 'Z'
    const prevLower = i > 0 && value[i - 1] >= 'a' && value[i - 1] <= 'z'
    const nextLower = i + 1 < value.length && value[i + 1] >= 'a' && value[i + 1] <= 'z'
    if (i > 0 && upper && (prevLower || nextLower)) {
      out += ' ' + c.toLowerCase()
    } else {
      out += c
    }
  }
  return out
}

/** How value of kind is shown; an unknown value reads as words, tone 'unknown' (Go: display.Of). */
export function displayState(kind: DisplayKind, value: string): DisplayState {
  const known = DISPLAY_STATES[kind][value]
  if (known) return known
  return { value, label: words(value) || 'Unknown', glyph: '?', ascii: '?', tone: 'unknown' }
}

/** An environment whose step a gate holds (Go: display.HeldByGate). */
export function heldByGate(stepState: string, gate: string): DisplayState {
  return { value: stepState, label: "Held by gate {gate}".replace("{gate}", gate), glyph: "⏸", ascii: "||", tone: "blocked" }
}

/** An environment held by a rollback (Go: display.Held). */
export function held(stepState: string): DisplayState {
  const h: DisplayState = { value: "", label: "Held by rollback", glyph: "⊙", ascii: "@", tone: "held" }
  return { ...h, value: stepState }
}
