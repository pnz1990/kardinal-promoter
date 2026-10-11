// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/BlockedBanner.tsx — Banner shown when gates are holding a version back.
//
// Adapted from kro-ui's AnomalyBanner (compile-error banner pattern).
// Says how many gates block which environments ("1 gate is blocking prod"), with
// "Open gates" and a "Show blocked" toggle that highlights them on the map.

import '../styles/BlockedBanner.css'

interface BlockedBannerProps {
  /** Number of gates holding the version back (GraphNode.holding). */
  blockedCount: number
  /** The environments those gates guard, in pipeline order. */
  environments?: string[]
  /** Whether the highlight filter is currently active. */
  highlightActive: boolean
  /** Toggle the highlight filter. */
  onToggleHighlight: () => void
  /** Opens the gates (the pipeline page's Gates tab), when given. */
  onOpenGates?: () => void
}

/** "prod", "uat and prod", "test, uat and prod". */
function listOf(names: string[]): string {
  if (names.length <= 1) return names[0] ?? ''
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}

/** "1 gate is blocking prod", "2 gates are blocking uat and prod". */
export function blockedLabel(count: number, environments: string[] = []): string {
  const envs = [...new Set(environments.filter(Boolean))]
  const what = count === 1 ? '1 gate is blocking' : `${count} gates are blocking`
  return `${what} ${envs.length ? listOf(envs) : 'promotion'}`
}

/**
 * BlockedBanner renders an amber warning banner when one or more gates hold the
 * version back. It includes a toggle to highlight the blocked gates on the map.
 */
export function BlockedBanner({ blockedCount, environments, highlightActive, onToggleHighlight, onOpenGates }: BlockedBannerProps) {
  if (blockedCount === 0) return null

  const label = blockedLabel(blockedCount, environments)

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
      <div className="blocked-banner__actions">
        {onOpenGates && (
          <button type="button" onClick={onOpenGates} className="blocked-banner__toggle">
            Open gates
          </button>
        )}
        <button
          type="button"
          onClick={onToggleHighlight}
          aria-pressed={highlightActive}
          className="blocked-banner__toggle"
        >
          {highlightActive ? 'Show all' : 'Show blocked'}
        </button>
      </div>
    </div>
  )
}
