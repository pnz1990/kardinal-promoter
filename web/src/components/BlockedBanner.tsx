// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/BlockedBanner.tsx — Banner shown when PolicyGates are blocking promotion.
//
// Adapted from kro-ui's AnomalyBanner (compile-error banner pattern).
// Shows count of blocked PolicyGates with a "Show blocked" button to highlight them.

import '../styles/BlockedBanner.css'

interface BlockedBannerProps {
  /** Number of PolicyGate nodes holding the bundle back (GraphNode.holding). */
  blockedCount: number
  /** Whether the highlight filter is currently active. */
  highlightActive: boolean
  /** Toggle the highlight filter. */
  onToggleHighlight: () => void
}

/**
 * BlockedBanner renders an amber warning banner when one or more PolicyGates
 * are in a blocked state. It includes a toggle to highlight the blocked nodes in the DAG.
 */
export function BlockedBanner({ blockedCount, highlightActive, onToggleHighlight }: BlockedBannerProps) {
  if (blockedCount === 0) return null

  const label = blockedCount === 1
    ? '1 PolicyGate blocking promotion'
    : `${blockedCount} PolicyGates blocking promotion`

  return (
    <div
      role="alert"
      className="blocked-banner"
    >
      <div className="blocked-banner__message">
        <span className="blocked-banner__icon" aria-hidden="true">⚠</span>
        <span className="blocked-banner__label">
          {label}
        </span>
      </div>
      <button
        onClick={onToggleHighlight}
        aria-pressed={highlightActive}
        className="blocked-banner__toggle"
      >
        {highlightActive ? 'Show all' : 'Show blocked'}
      </button>
    </div>
  )
}
