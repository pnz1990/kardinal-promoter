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
  | 'Ready'         // Succeeded / Verified / Pass — green
  | 'Reconciling'   // Running / WaitingForMerge / HealthChecking / Promoting — amber
  | 'Error'         // Failed / Block — red
  | 'Pending'       // Pending / Available (not yet started) — slate
  | 'Unknown'       // Superseded / unknown — gray
  | 'Degraded'      // Partial failure (reserved for future use) — orange
  | 'Paused'        // Pipeline paused (spec.paused=true) — indigo

/** Maps a kardinal promotion/gate state string to a HealthState. */
export function kardinalStateToHealth(state: string, nodeType?: string): HealthState {
  if (nodeType === 'PolicyGate') {
    switch (state) {
      case 'Pass':   return 'Ready'
      case 'Block':
      case 'Fail':   return 'Error'
      case 'Pending': return 'Pending'
      default:       return 'Unknown'
    }
  }
  switch (state) {
    case 'Succeeded':
    case 'Verified':
    case 'Pass':
      return 'Ready'
    case 'Running':
    case 'Promoting':
    case 'WaitingForMerge':
    case 'HealthChecking':
      return 'Reconciling'
    case 'Failed':
    case 'Block':
      return 'Error'
    case 'Pending':
    case 'Available':
      return 'Pending'
    case 'Superseded':
      return 'Unknown'
    case 'Paused':
      return 'Paused'
    case 'Idle':
      return 'Unknown'  // Idle environments shown as gray (not yet started)
    default:
      return 'Unknown'
  }
}

/**
 * #523: pipelinePhaseLabel translates the backend Pipeline.phase into a
 * display-friendly string for the sidebar chip.
 *
 * The backend DerivePhase() returns "Unknown" as the default (non-Ready,
 * non-Degraded) state, which renders as "Unknown — Unknown" in the chip
 * and confuses users. This function provides context-aware translation:
 *
 * - "Unknown" + no active bundle/environmentStates → "Idle"
 * - "Unknown" + environmentStates present (active bundle) → "Promoting"
 * - All other phases → pass through unchanged
 */
export function pipelinePhaseLabel(pipeline: {
  phase: string
  environmentCount?: number
  environmentStates?: Record<string, string>
  activeBundleName?: string
}): string {
  if (pipeline.phase !== 'Unknown') return pipeline.phase

  // Any environmentStates means there's an active bundle — show "Promoting"
  const hasActiveBundleData = pipeline.environmentStates &&
    Object.keys(pipeline.environmentStates).length > 0
  if (hasActiveBundleData) return 'Promoting'

  // active bundle name also indicates activity
  if (pipeline.activeBundleName) return 'Promoting'

  return 'Idle'
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
 */
export function healthChipColors(state: HealthState): { bg: string; text: string; border: string } {
  switch (state) {
    case 'Ready':
      return { bg: '#14532d', text: '#4ade80', border: '#22c55e' }
    case 'Reconciling':
      return { bg: '#78350f', text: '#fbbf24', border: '#f59e0b' }
    case 'Error':
      return { bg: '#7f1d1d', text: '#f87171', border: '#ef4444' }
    case 'Pending':
      return { bg: '#1e293b', text: '#94a3b8', border: '#475569' }
    case 'Degraded':
      return { bg: '#7c2d12', text: '#fb923c', border: '#f97316' }
    case 'Paused':
      return { bg: '#1e1b4b', text: '#a5b4fc', border: '#6366f1' }
    case 'Unknown':
    default:
      return { bg: '#1e293b', text: '#64748b', border: '#334155' }
  }
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

  return (
    <span
      className={`health-chip ${stateClass} ${sizeClass}`}
      title={`${state} (${health})`}
      aria-label={`${label ?? state} — ${health}`}
      data-health-state={health}
    >
      {label ?? state}
    </span>
  )
}
