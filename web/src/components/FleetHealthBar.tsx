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
// FR-505-04: Empty state when zero pipelines loaded.
import type { Pipeline } from '../types'
import '../styles/FleetHealthBar.css'

/** Derived fleet health summary computed from the pipeline list. */
export interface FleetHealthSummary {
  total: number
  /** Pipelines with no active failures and not paused. */
  healthy: number
  /** Pipelines with blockerCount > 0. */
  blocked: number
  /** Pipelines with failedStepCount > 0. */
  ciRed: number
  /** Pipelines with at least one environment in flight (see isPromoting). */
  promoting: number
}

/**
 * PromotionStep states that mean a promotion is in flight. RollingBack is an
 * end state: the rollback Bundle's own step is the one in flight.
 */
const IN_FLIGHT_STATES = new Set(['Promoting', 'WaitingForMerge', 'HealthChecking', 'Verifying'])

/**
 * A pipeline is promoting when any environment of its active bundle is in
 * flight. Pipeline.phase is not used: it is also "Promoting" while a
 * PolicyGate holds a bundle before any environment starts, which the Blocked
 * count already shows.
 */
export function isPromoting(p: Pipeline): boolean {
  return Object.values(p.environmentStates ?? {}).some(s => IN_FLIGHT_STATES.has(s))
}

/** Healthy: nothing blocked, no failed or alarm-aborted step, not paused. */
function isHealthy(p: Pipeline): boolean {
  return (p.blockerCount ?? 0) === 0 && (p.failedStepCount ?? 0) === 0 && !p.paused
}

/**
 * Compute fleet health summary from the pipeline list.
 * Pure function — no side effects.
 */
export function computeFleetHealth(pipelines: Pipeline[]): FleetHealthSummary {
  let healthy = 0
  let blocked = 0
  let ciRed = 0
  let promoting = 0

  for (const p of pipelines) {
    if (isHealthy(p)) healthy++
    if ((p.blockerCount ?? 0) > 0) blocked++
    if ((p.failedStepCount ?? 0) > 0) ciRed++
    if (isPromoting(p)) promoting++
  }

  return {
    total: pipelines.length,
    healthy,
    blocked,
    ciRed,
    promoting,
  }
}

/** Filter category that maps to a UI badge. */
export type FleetFilter = 'all' | 'healthy' | 'blocked' | 'ci-red' | 'promoting'

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
      return pipelines.filter(isHealthy)
    case 'blocked':
      return pipelines.filter(p => (p.blockerCount ?? 0) > 0)
    case 'ci-red':
      return pipelines.filter(p => (p.failedStepCount ?? 0) > 0)
    case 'promoting':
      return pipelines.filter(isPromoting)
  }
}

/** The colour of a badge's count: a status bucket, or the code colour for promoting. */
type BadgeTone = 'verified' | 'blocked' | 'failed' | 'code'

interface SummaryBadgeProps {
  label: string
  count: number
  tone: BadgeTone
  active: boolean
  onClick: () => void
  'aria-label'?: string
}

function SummaryBadge({
  label,
  count,
  tone,
  active,
  onClick,
  'aria-label': ariaLabel,
}: SummaryBadgeProps) {
  return (
    <button
      onClick={onClick}
      aria-label={ariaLabel ?? `Filter by ${label}`}
      aria-pressed={active}
      className="fleet-health__badge"
      data-tone={tone}
    >
      <span className="fleet-health__count">
        {count}
      </span>
      <span className="fleet-health__badge-label">
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
      className="fleet-health"
    >
      {/* Total */}
      <button
        onClick={toggle('all')}
        aria-label="Show all pipelines"
        aria-pressed={activeFilter === 'all'}
        className="fleet-health__total"
      >
        <span className="fleet-health__total-label">
          Pipelines
        </span>
        <span className="fleet-health__count">
          {s.total}
        </span>
      </button>

      <span className="fleet-health__divider">|</span>

      <SummaryBadge
        label="Healthy"
        count={s.healthy}
        tone="verified"
        active={activeFilter === 'healthy'}
        onClick={toggle('healthy')}
        aria-label={`${s.healthy} healthy pipeline${s.healthy === 1 ? '' : 's'}`}
      />
      <SummaryBadge
        label="Blocked"
        count={s.blocked}
        tone="blocked"
        active={activeFilter === 'blocked'}
        onClick={toggle('blocked')}
        aria-label={`${s.blocked} blocked pipeline${s.blocked === 1 ? '' : 's'}`}
      />
      {s.ciRed > 0 && (
        <SummaryBadge
          label="CI Red"
          count={s.ciRed}
          tone="failed"
          active={activeFilter === 'ci-red'}
          onClick={toggle('ci-red')}
          aria-label={`${s.ciRed} pipeline${s.ciRed === 1 ? '' : 's'} with CI failures`}
        />
      )}

      <span className="fleet-health__divider">|</span>

      <SummaryBadge
        label="Promoting"
        count={s.promoting}
        tone="code"
        active={activeFilter === 'promoting'}
        onClick={toggle('promoting')}
        aria-label={`${s.promoting} pipeline${s.promoting === 1 ? '' : 's'} currently promoting`}
      />
    </div>
  )
}
