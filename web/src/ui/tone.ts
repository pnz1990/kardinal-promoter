// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// tone.ts — the status buckets of the display-state contract (pkg/display, #1700) and the
// design tokens each one uses (DESIGN-SYSTEM.md §2.2).

export type Tone =
  | 'verified' | 'promoting' | 'waiting' | 'blocked' | 'failed'
  | 'superseded' | 'paused' | 'held' | 'pending' | 'degraded' | 'unknown'

export const TONES: readonly Tone[] = [
  'verified', 'promoting', 'waiting', 'blocked', 'failed',
  'superseded', 'paused', 'held', 'pending', 'degraded', 'unknown',
]

/** The --k-* custom properties of a tone: its text colour, and its chip background (or none). */
export function toneVars(tone: Tone): { fg: string; bg: string } {
  switch (tone) {
    case 'verified': return { fg: 'var(--k-status-verified-fg)', bg: 'var(--k-status-verified-wash)' }
    case 'promoting': return { fg: 'var(--k-status-promoting-fg)', bg: 'var(--k-status-promoting-wash)' }
    case 'waiting': return { fg: 'var(--k-status-waiting-fg)', bg: 'var(--k-status-waiting-wash)' }
    case 'blocked': return { fg: 'var(--k-status-blocked-fg)', bg: 'var(--k-status-blocked-wash)' }
    case 'failed': return { fg: 'var(--k-status-failed-on-fill)', bg: 'var(--k-status-failed-fill)' }
    case 'superseded': return { fg: 'var(--k-status-superseded-fg)', bg: 'transparent' }
    case 'paused': return { fg: 'var(--k-status-paused-fg)', bg: 'var(--k-hatch)' }
    case 'held': return { fg: 'var(--k-status-held-fg)', bg: 'var(--k-hatch)' }
    case 'degraded': return { fg: 'var(--k-status-failed-fg)', bg: 'transparent' }
    case 'pending':
    case 'unknown': return { fg: 'var(--k-ink-3)', bg: 'transparent' }
  }
}
