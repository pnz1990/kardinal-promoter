// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// components/HealthChip.tsx — Reusable health state chip with 7-state color coding.
//
// #532: State-driven visual properties are now CSS classes (health-chip--{state})
// so tests can assert class names, not hex color strings.
// healthChipColors() is retained for SVG contexts (DAGView) that require inline colors.

import '../styles/HealthChip.css'

/** The 7 canonical health chip states. */
export type HealthState =
  | 'Ready'         // Verified / Pass / Pipeline Ready — green
  | 'Reconciling'   // Promoting / WaitingForMerge / HealthChecking — amber
  | 'Error'         // Failed / AbortedByAlarm / Block / Bundle Rejected — red
  | 'Pending'       // Pending / Available / NotStarted / gate Waiting — slate
  | 'Unknown'       // Superseded / unknown — gray
  | 'Degraded'      // Pipeline phase Degraded — orange
  | 'Paused'        // Pipeline paused (spec.paused=true) — indigo

/**
 * Maps a kardinal state string to a HealthState. Handles PromotionStep states
 * (api/v1alpha1/promotionstep_types.go), the graph API's synthetic NotStarted,
 * Bundle phases, Pipeline phases (Ready/Degraded/Promoting/Unknown) and gate states.
 */
export function kardinalStateToHealth(state: string, nodeType?: string): HealthState {
  if (nodeType === 'PolicyGate') {
    switch (state) {
      case 'Pass':   return 'Ready'
      case 'Block':
      case 'Fail':   return 'Error'
      case 'Pending':
      case 'Waiting': return 'Pending' // not ready, not holding the bundle
      case 'Superseded': return 'Unknown' // bundle superseded; not evaluated again
      case 'Rejected': return 'Unknown'   // bundle rejected; not evaluated again
      default:       return 'Unknown'
    }
  }
  switch (state) {
    case 'Verified':
    case 'Pass':
    case 'Ready':
      return 'Ready'
    case 'Promoting':
    case 'WaitingForMerge':
    case 'HealthChecking':
    case 'Verifying':       // post-deploy hooks and analyses running
      return 'Reconciling'
    case 'Failed':
    case 'AbortedByAlarm':  // terminal: an alarm stopped the promotion; a human must act
    case 'RollingBack':     // terminal: health failed and a rollback Bundle took over (as the CLI shows it)
    case 'Rejected':        // Bundle phase: kardinal reject; never promoted again
    case 'Block':
      return 'Error'
    case 'Degraded':
      return 'Degraded'
    case 'Pending':
    case 'Available':
    case 'NotStarted':      // graph API: environment not reached yet
      return 'Pending'
    case 'Paused':
      return 'Paused'
    case 'Superseded':
    default:
      return 'Unknown'
  }
}

/**
 * Returns the CSS class modifier for a given HealthState.
 * Used to apply health-chip--{state} class to the chip element.
 */
export function healthStateClass(state: HealthState): string {
  return `health-chip--${state.toLowerCase()}`
}

/**
 * Returns the background and text colors for a given HealthState.
 * Retained for SVG rendering contexts (DAGView) that cannot use CSS classes.
 * All values are theme tokens, so dark and light mode both work.
 */
export function healthChipColors(state: HealthState): { bg: string; text: string; border: string } {
  switch (state) {
    case 'Ready':
      return { bg: 'var(--color-success-bg)', text: 'var(--color-success)', border: 'var(--color-success)' }
    case 'Reconciling':
      return { bg: 'var(--color-warning-bg)', text: 'var(--color-warning)', border: 'var(--color-warning)' }
    case 'Error':
      return { bg: 'var(--color-error-bg)', text: 'var(--color-error)', border: 'var(--color-error)' }
    case 'Pending':
      return { bg: 'var(--color-surface)', text: 'var(--color-text-muted)', border: 'var(--color-text-faint)' }
    case 'Degraded':
      return { bg: 'var(--color-degraded-bg)', text: 'var(--color-degraded)', border: 'var(--color-degraded)' }
    case 'Paused':
      return { bg: 'var(--color-accent-bg)', text: 'var(--color-accent)', border: 'var(--color-accent)' }
    case 'Unknown':
    default:
      return { bg: 'var(--color-surface)', text: 'var(--color-text-faint)', border: 'var(--color-border)' }
  }
}

/**
 * What a PolicyGate state means, for states whose health word would mislead a
 * screen reader ("Waiting — Pending"). Read instead of the health word.
 */
const GATE_STATE_MEANING: Record<string, string> = {
  Waiting: 'not ready, not holding the bundle',
  Superseded: 'bundle superseded, not evaluated again',
  Rejected: 'bundle rejected, not evaluated again',
}

interface HealthChipProps {
  /** Raw kardinal state string (e.g. 'Verified', 'WaitingForMerge', 'Pass', 'Block'). */
  state: string
  /** Optional node type for context-aware mapping ('PolicyGate' vs default). */
  nodeType?: string
  /** Optional display label override (defaults to the raw state string). */
  label?: string
  /** Size variant: 'sm' (default) or 'md'. */
  size?: 'sm' | 'md'
}

/**
 * HealthChip renders a pill badge with health state color coding.
 *
 * #532: Uses CSS classes (health-chip--{state}) instead of inline styles.
 * Tests should assert className, not background-color.
 */
export function HealthChip({ state, nodeType, label, size = 'sm' }: HealthChipProps) {
  const health = kardinalStateToHealth(state, nodeType)
  const stateClass = healthStateClass(health)
  const sizeClass = `health-chip--${size}`
  const meaning = nodeType === 'PolicyGate' ? GATE_STATE_MEANING[state] : undefined

  return (
    <span
      className={`health-chip ${stateClass} ${sizeClass}`}
      title={`${state} (${meaning ?? health})`}
      aria-label={`${label ?? state} — ${meaning ?? health}`}
      data-health-state={health}
    >
      {label ?? state}
      {/* aria-label on a span is not read by every screen reader. */}
      {meaning && <span className="sr-only">, {meaning}</span>}
    </span>
  )
}
