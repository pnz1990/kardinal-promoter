// components/HealthChip.tsx — Reusable health state chip with 7-state color coding.
//
// Adapts the kro-ui health chip pattern (Ready/Degraded/Reconciling/Pending/Error/Unknown)
// to kardinal's promotion states, plus a Paused state for suspended pipelines.
// All ad-hoc phaseBadgeColor and nodeColor functions should be replaced with
// HealthChip or healthChipColors() calls.

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

/** Returns the background and text colors for a given HealthState. */
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
 * Replaces the ad-hoc phaseBadgeColor / nodeColor / stateColor inline functions
 * across DAGView, NodeDetail, and PipelineList.
 */
export function HealthChip({ state, nodeType, label, size = 'sm' }: HealthChipProps) {
  const health = kardinalStateToHealth(state, nodeType)
  const { bg, text, border } = healthChipColors(health)

  const fontSize = size === 'md' ? '0.75rem' : '0.65rem'
  const padding = size === 'md' ? '2px 8px' : '1px 5px'

  return (
    <span
      style={{
        display: 'inline-block',
        background: bg,
        color: text,
        border: `1px solid ${border}`,
        fontSize,
        padding,
        borderRadius: '9999px',
        fontWeight: 600,
        whiteSpace: 'nowrap',
        lineHeight: '1.4',
      }}
      title={`${state} (${health})`}
      aria-label={`${label ?? state} — ${health}`}
    >
      {label ?? state}
    </span>
  )
}
