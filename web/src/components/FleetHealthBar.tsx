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
import { pipelineState, type PipelineCategory } from '../pipelineState'
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
  /** Paused pipelines. */
  paused: number
}

/**
 * Compute fleet health summary from the pipeline list: each pipeline in exactly one bucket,
 * its pipelineState category, so the counters agree with the sidebar badge and the header.
 * A pipeline with no version yet is in the total only: there is nothing to count it as.
 * Pure function — no side effects.
 */
export function computeFleetHealth(pipelines: Pipeline[]): FleetHealthSummary {
  const n = (c: PipelineCategory) => pipelines.filter(p => pipelineState(p).category === c).length
  return { total: pipelines.length, healthy: n('healthy'), blocked: n('blocked'), ciRed: n('failed'), promoting: n('promoting'), paused: n('paused') }
}

export type FleetFilter = 'all' | 'healthy' | 'blocked' | 'ci-red' | 'promoting' | 'paused'

const FLEET_FILTERS: readonly string[] = ['all', 'healthy', 'blocked', 'ci-red', 'promoting', 'paused']
const CATEGORY: Record<Exclude<FleetFilter, 'all'>, PipelineCategory> = { healthy: 'healthy', blocked: 'blocked', 'ci-red': 'failed', promoting: 'promoting', paused: 'paused' }

/** Whether value is a fleet filter (the URL's filter=). */
export function isFleetFilter(value: string | undefined): value is FleetFilter {
  return !!value && FLEET_FILTERS.includes(value)
}

/** The pipelines in a filter's bucket (pipelineState category). */
export function filterPipelines(
  pipelines: Pipeline[],
  filter: FleetFilter,
): Pipeline[] {
  return filter === 'all' ? pipelines : pipelines.filter(p => pipelineState(p).category === CATEGORY[filter])
}

/** The colour of a badge's count: a status bucket, or the code colour for promoting. */
type BadgeTone = 'verified' | 'blocked' | 'failed' | 'code' | 'paused'

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
          label="Failed"
          count={s.ciRed}
          tone="failed"
          active={activeFilter === 'ci-red'}
          onClick={toggle('ci-red')}
          aria-label={`${s.ciRed} pipeline${s.ciRed === 1 ? '' : 's'} with a failed promotion`}
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
      {s.paused > 0 && (
        <SummaryBadge
          label="Paused"
          count={s.paused}
          tone="paused"
          active={activeFilter === 'paused'}
          onClick={toggle('paused')}
          aria-label={`${s.paused} paused pipeline${s.paused === 1 ? '' : 's'}`}
        />
      )}
    </div>
  )
}
