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

// components/FleetHealthBar.tsx — Fleet-wide health summary bar (#467).
// FR-505-01: Aggregate pipeline health across the fleet.
// FR-505-02: Clickable filter badges that drive the pipeline list filter.
// FR-505-03: Full-CD counter (pipelines with cdLevel=full-cd).
// FR-505-04: Empty state when zero pipelines loaded.
import type { Pipeline } from '../types'

/** Derived fleet health summary computed from the pipeline list. */
export interface FleetHealthSummary {
  total: number
  /** Pipelines with no active failures and not paused. */
  healthy: number
  /** Pipelines with blockerCount > 0. */
  blocked: number
  /** Pipelines with failedStepCount > 0. */
  ciRed: number
  /** Pipelines with cdLevel = 'full-cd'. */
  fullCD: number
  /** Pipelines with activeBundleName set and phase Promoting. */
  promoting: number
}

/**
 * Compute fleet health summary from the pipeline list.
 * Pure function — no side effects.
 */
export function computeFleetHealth(pipelines: Pipeline[]): FleetHealthSummary {
  let healthy = 0
  let blocked = 0
  let ciRed = 0
  let fullCD = 0
  let promoting = 0

  for (const p of pipelines) {
    const isBlocked = (p.blockerCount ?? 0) > 0
    const isCiRed = (p.failedStepCount ?? 0) > 0
    const isProblematic = isBlocked || isCiRed || p.paused

    if (!isProblematic) healthy++
    if (isBlocked) blocked++
    if (isCiRed) ciRed++
    if (p.cdLevel === 'full-cd') fullCD++
    if (p.phase === 'Promoting') promoting++
  }

  return {
    total: pipelines.length,
    healthy,
    blocked,
    ciRed,
    fullCD,
    promoting,
  }
}

/** Filter category that maps to a UI badge. */
export type FleetFilter = 'all' | 'healthy' | 'blocked' | 'ci-red' | 'full-cd' | 'promoting'

/**
 * Filter a pipeline list by the given fleet filter.
 * Returns a new array — does not mutate input.
 */
export function filterPipelines(
  pipelines: Pipeline[],
  filter: FleetFilter,
): Pipeline[] {
  switch (filter) {
    case 'all':
      return pipelines
    case 'healthy':
      return pipelines.filter(p => (p.blockerCount ?? 0) === 0 && (p.failedStepCount ?? 0) === 0 && !p.paused)
    case 'blocked':
      return pipelines.filter(p => (p.blockerCount ?? 0) > 0)
    case 'ci-red':
      return pipelines.filter(p => (p.failedStepCount ?? 0) > 0)
    case 'full-cd':
      return pipelines.filter(p => p.cdLevel === 'full-cd')
    case 'promoting':
      return pipelines.filter(p => p.phase === 'Promoting')
  }
}

interface SummaryBadgeProps {
  label: string
  count: number
  color: string
  bgColor: string
  borderColor: string
  active: boolean
  onClick: () => void
  'aria-label'?: string
}

function SummaryBadge({
  label,
  count,
  color,
  bgColor,
  borderColor,
  active,
  onClick,
  'aria-label': ariaLabel,
}: SummaryBadgeProps) {
  return (
    <button
      onClick={onClick}
      aria-label={ariaLabel ?? `Filter by ${label}`}
      aria-pressed={active}
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: '0.4rem',
        padding: '0.35rem 0.65rem',
        background: active ? bgColor : 'transparent',
        border: `1px solid ${active ? borderColor : 'var(--color-surface)'}`,
        borderRadius: '6px',
        cursor: 'pointer',
        transition: 'all 0.1s',
      }}
    >
      <span style={{
        fontSize: '1.1rem',
        fontWeight: 700,
        color,
        fontVariantNumeric: 'tabular-nums',
        lineHeight: 1,
      }}>
        {count}
      </span>
      <span style={{
        fontSize: '0.7rem',
        color: active ? color : 'var(--color-text-muted)',  /* CSS var adapts to both dark and light theme — #94a3b8 was dark-only (#762 contrast fix) */
        fontWeight: active ? 600 : 400,
        whiteSpace: 'nowrap',
      }}>
        {label}
      </span>
    </button>
  )
}

interface FleetHealthBarProps {
  pipelines: Pipeline[]
  activeFilter: FleetFilter
  onFilterChange: (filter: FleetFilter) => void
}

/**
 * FleetHealthBar renders a summary bar with clickable filter badges.
 * Computed client-side from the pipeline list — no new API needed.
 */
export function FleetHealthBar({ pipelines, activeFilter, onFilterChange }: FleetHealthBarProps) {
  if (pipelines.length === 0) {
    return null
  }

  const s = computeFleetHealth(pipelines)

  const toggle = (filter: FleetFilter) => () =>
    onFilterChange(activeFilter === filter ? 'all' : filter)

  return (
    <div
      aria-label="Fleet health summary"
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: '0.5rem',
        padding: '0.5rem 0.75rem',
        background: 'var(--color-bg-deep)',
        border: '1px solid var(--color-border-muted)',
        borderRadius: '6px',
        flexWrap: 'wrap',
        marginBottom: '0.75rem',
      }}
    >
      {/* Total */}
      <button
        onClick={toggle('all')}
        aria-label="Show all pipelines"
        aria-pressed={activeFilter === 'all'}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: '0.35rem',
          padding: '0.3rem 0.55rem',
          background: activeFilter === 'all' ? 'var(--color-surface)' : 'transparent',
          border: `1px solid ${activeFilter === 'all' ? 'var(--color-text-faint)' : 'var(--color-surface)'}`,
          borderRadius: '6px',
          cursor: 'pointer',
        }}
      >
        <span style={{ fontSize: '0.65rem', color: 'var(--color-text-muted)', fontWeight: 600 }}>
          Pipelines
        </span>
        <span style={{ fontSize: '1.1rem', fontWeight: 700, color: 'var(--color-text)', fontVariantNumeric: 'tabular-nums' }}>
          {s.total}
        </span>
      </button>

      <span style={{ color: 'var(--color-surface)', fontSize: '1.2rem', userSelect: 'none' }} aria-hidden="true">|</span>

      <SummaryBadge
        label="Healthy"
        count={s.healthy}
        color="var(--color-success)"
        bgColor="#052e16"
        borderColor="#166534"
        active={activeFilter === 'healthy'}
        onClick={toggle('healthy')}
        aria-label={`${s.healthy} healthy pipelines`}
      />
      <SummaryBadge
        label="Blocked"
        count={s.blocked}
        color="#f59e0b"
        bgColor="#1c1507"
        borderColor="#92400e"
        active={activeFilter === 'blocked'}
        onClick={toggle('blocked')}
        aria-label={`${s.blocked} blocked pipelines`}
      />
      {s.ciRed > 0 && (
        <SummaryBadge
          label="CI Red"
          count={s.ciRed}
          color="#ef4444"
          bgColor="#1e0c0c"
          borderColor="#7f1d1d"
          active={activeFilter === 'ci-red'}
          onClick={toggle('ci-red')}
          aria-label={`${s.ciRed} pipelines with CI failures`}
        />
      )}

      <span style={{ color: 'var(--color-surface)', fontSize: '1.2rem', userSelect: 'none' }} aria-hidden="true">|</span>

      <SummaryBadge
        label="Promoting"
        count={s.promoting}
        color="#7dd3fc"
        bgColor="#0c1a2e"
        borderColor="#075985"
        active={activeFilter === 'promoting'}
        onClick={toggle('promoting')}
        aria-label={`${s.promoting} pipelines currently promoting`}
      />
      <SummaryBadge
        label="Full CD"
        count={s.fullCD}
        color="var(--color-accent)"
        bgColor="#12103a"
        borderColor="#3730a3"
        active={activeFilter === 'full-cd'}
        onClick={toggle('full-cd')}
        aria-label={`${s.fullCD} fully automated pipelines`}
      />
    </div>
  )
}
