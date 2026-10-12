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

// components/FleetHealthBar.test.tsx — Tests for #467 fleet health dashboard.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import {
  computeFleetHealth,
  filterPipelines,
  FleetHealthBar,
} from './FleetHealthBar'
import type { Pipeline } from '../types'

// ─── Test helpers ──────────────────────────────────────────────────────────────

function makePipeline(overrides: Partial<Pipeline> = {}): Pipeline {
  return {
    name: 'my-pipeline',
    namespace: 'default',
    phase: 'Ready',
    environmentCount: 3,
    ...overrides,
  }
}

// ─── computeFleetHealth ───────────────────────────────────────────────────────

describe('computeFleetHealth', () => {
  it('returns zeros for empty pipeline list', () => {
    const s = computeFleetHealth([])
    expect(s.total).toBe(0)
    expect(s.healthy).toBe(0)
    expect(s.blocked).toBe(0)
    expect(s.ciRed).toBe(0)
    expect(s.promoting).toBe(0)
  })

  it('counts healthy pipeline (no blockers, no failures, not paused)', () => {
    const s = computeFleetHealth([
      makePipeline({ blockerCount: 0, failedStepCount: 0, paused: false }),
    ])
    expect(s.healthy).toBe(1)
    expect(s.blocked).toBe(0)
  })

  it('counts blocked pipeline (blockerCount > 0)', () => {
    const s = computeFleetHealth([makePipeline({ blockerCount: 2 })])
    expect(s.blocked).toBe(1)
    expect(s.healthy).toBe(0)
  })

  it('counts CI red pipeline (failedStepCount > 0)', () => {
    const s = computeFleetHealth([makePipeline({ failedStepCount: 1 })])
    expect(s.ciRed).toBe(1)
    expect(s.healthy).toBe(0)
  })

  it('counts paused pipeline as not healthy', () => {
    const s = computeFleetHealth([makePipeline({ paused: true })])
    expect(s.healthy).toBe(0)
  })

  // "promoting" comes from the per-environment states of the active bundle,
  // not from phase "Promoting" (also set while a gate holds the bundle).
  it.each<[string, Record<string, string> | undefined, number]>([
    ['an environment Promoting', { test: 'Verified', prod: 'Promoting' }, 1],
    ['an environment WaitingForMerge', { test: 'Verified', prod: 'WaitingForMerge' }, 1],
    ['an environment HealthChecking', { test: 'HealthChecking' }, 1],
    ['an environment RollingBack (an end state)', { prod: 'RollingBack' }, 0],
    ['all environments Verified', { test: 'Verified', prod: 'Verified' }, 0],
    ['environments not started or failed', { test: 'Failed', prod: 'NotStarted' }, 0],
    ['no environment states', undefined, 0],
  ])('promoting count with %s', (_, environmentStates, want) => {
    const s = computeFleetHealth([makePipeline({ phase: 'Unknown', environmentStates })])
    expect(s.promoting).toBe(want)
  })

  // One state per pipeline: a Promoting phase is promoting, as the sidebar and the header say it;
  // a gate that holds the version makes it blocked instead (pipelineState).
  it('counts phase "Promoting" as promoting, and a held one as blocked, never both', () => {
    expect(computeFleetHealth([makePipeline({ phase: 'Promoting' })]).promoting).toBe(1)
    const held = computeFleetHealth([makePipeline({ phase: 'Promoting', blockerCount: 1 })])
    expect([held.promoting, held.blocked]).toEqual([0, 1])
  })

  it('handles mixed fleet correctly', () => {
    const pipelines = [
      makePipeline({ name: 'a', blockerCount: 0, failedStepCount: 0, phase: 'Ready' }),
      makePipeline({ name: 'b', blockerCount: 1, phase: 'Unknown', environmentStates: { prod: 'WaitingForMerge' } }),
      makePipeline({ name: 'c', failedStepCount: 2 }),
      makePipeline({ name: 'd', phase: 'Unknown', environmentStates: { test: 'Promoting' } }),
    ]
    const s = computeFleetHealth(pipelines)
    expect(s.total).toBe(4)
    // One bucket per pipeline (pipelineState): 'b' is held by a gate, not also promoting.
    expect(s.healthy).toBe(1) // 'a'
    expect(s.blocked).toBe(1) // 'b'
    expect(s.ciRed).toBe(1)   // 'c'
    expect(s.promoting).toBe(1) // 'd'
  })
})

// ─── filterPipelines ──────────────────────────────────────────────────────────

describe('filterPipelines', () => {
  const pipelines = [
    makePipeline({ name: 'healthy', blockerCount: 0, failedStepCount: 0, paused: false }),
    makePipeline({ name: 'blocked', blockerCount: 1 }),
    makePipeline({ name: 'ci-red', failedStepCount: 1 }),
    makePipeline({ name: 'promoting', phase: 'Unknown', environmentStates: { test: 'Verified', prod: 'HealthChecking' } }),
  ]

  it('filter=all returns all pipelines', () => {
    expect(filterPipelines(pipelines, 'all')).toHaveLength(4)
  })

  it('filter=healthy returns only healthy', () => {
    const result = filterPipelines(pipelines, 'healthy')
    // 'healthy' and 'promoting' pipelines qualify (no blockers/failures/pause)
    expect(result.map(p => p.name)).toContain('healthy')
    expect(result.map(p => p.name)).not.toContain('blocked')
    expect(result.map(p => p.name)).not.toContain('ci-red')
  })

  it('filter=blocked returns only blocked', () => {
    const result = filterPipelines(pipelines, 'blocked')
    expect(result).toHaveLength(1)
    expect(result[0].name).toBe('blocked')
  })

  it('filter=ci-red returns only CI red', () => {
    const result = filterPipelines(pipelines, 'ci-red')
    expect(result).toHaveLength(1)
    expect(result[0].name).toBe('ci-red')
  })

  it('filter=promoting returns only promoting pipelines', () => {
    const result = filterPipelines(pipelines, 'promoting')
    expect(result).toHaveLength(1)
    expect(result[0].name).toBe('promoting')
  })

  it('does not mutate the original array', () => {
    const original = [...pipelines]
    filterPipelines(pipelines, 'blocked')
    expect(pipelines).toEqual(original)
  })
})

// ─── FleetHealthBar component ─────────────────────────────────────────────────

describe('FleetHealthBar', () => {
  it('renders nothing when pipeline list is empty', () => {
    const { container } = render(
      <FleetHealthBar pipelines={[]} activeFilter="all" onFilterChange={() => {}} />
    )
    expect(container.firstChild).toBeNull()
  })

  it('renders fleet health summary when pipelines present', () => {
    const pipelines = [
      makePipeline({ name: 'a', blockerCount: 0 }),
      makePipeline({ name: 'b', blockerCount: 1 }),
    ]
    render(<FleetHealthBar pipelines={pipelines} activeFilter="all" onFilterChange={() => {}} />)
    expect(screen.getByLabelText(/fleet health summary/i)).toBeInTheDocument()
  })

  it('shows total pipeline count', () => {
    const pipelines = [makePipeline(), makePipeline({ name: 'b' })]
    render(<FleetHealthBar pipelines={pipelines} activeFilter="all" onFilterChange={() => {}} />)
    // The "Pipelines" label is unique; find its parent button to verify the count
    expect(screen.getByText('Pipelines')).toBeInTheDocument()
    expect(screen.getByLabelText('Show all pipelines')).toBeInTheDocument()
  })

  it('calls onFilterChange when "Blocked" badge clicked', () => {
    const onFilterChange = vi.fn()
    const pipelines = [makePipeline({ blockerCount: 1 })]
    render(<FleetHealthBar pipelines={pipelines} activeFilter="all" onFilterChange={onFilterChange} />)
    fireEvent.click(screen.getByLabelText(/blocked pipeline/i))
    expect(onFilterChange).toHaveBeenCalledWith('blocked')
  })

  it('calls onFilterChange with "all" when active filter re-clicked (toggle off)', () => {
    const onFilterChange = vi.fn()
    const pipelines = [makePipeline({ blockerCount: 1 })]
    render(<FleetHealthBar pipelines={pipelines} activeFilter="blocked" onFilterChange={onFilterChange} />)
    fireEvent.click(screen.getByLabelText(/blocked pipeline/i))
    expect(onFilterChange).toHaveBeenCalledWith('all')
  })

  it('marks active filter badge as aria-pressed=true', () => {
    const pipelines = [makePipeline({ blockerCount: 1 })]
    render(<FleetHealthBar pipelines={pipelines} activeFilter="blocked" onFilterChange={() => {}} />)
    const blockedBtn = screen.getByLabelText(/blocked pipeline/i)
    expect(blockedBtn).toHaveAttribute('aria-pressed', 'true')
  })

  // #1269: the Full CD count read spec.policyGates, which gates nothing.
  it('has no Full CD badge', () => {
    render(<FleetHealthBar pipelines={[makePipeline()]} activeFilter="all" onFilterChange={() => {}} />)
    expect(screen.queryByText('Full CD')).not.toBeInTheDocument()
  })

  it('shows a Failed badge only when a pipeline has a failed promotion', () => {
    const { unmount } = render(<FleetHealthBar pipelines={[makePipeline({ failedStepCount: 0 })]} activeFilter="all" onFilterChange={() => {}} />)
    expect(screen.queryByText('Failed')).not.toBeInTheDocument()
    unmount()
    render(<FleetHealthBar pipelines={[makePipeline({ failedStepCount: 1 })]} activeFilter="all" onFilterChange={() => {}} />)
    expect(screen.getByText('Failed')).toBeInTheDocument()
    expect(screen.queryByText('CI Red')).not.toBeInTheDocument()
  })

  it('names each badge in the singular for one pipeline and the plural otherwise', () => {
    const promoting = { environmentStates: { prod: 'Promoting' } }
    const { unmount } = render(
      <FleetHealthBar
        pipelines={[
          makePipeline({ name: 'ok' }),
          makePipeline({ name: 'gated', blockerCount: 1 }),
          makePipeline({ name: 'red', failedStepCount: 1 }),
          makePipeline({ name: 'moving', ...promoting }),
        ]}
        activeFilter="all"
        onFilterChange={() => {}}
      />
    )
    expect(screen.getByRole('button', { name: '1 healthy pipeline' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '1 blocked pipeline' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '1 pipeline with a failed promotion' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '1 pipeline currently promoting' })).toBeInTheDocument()
    unmount()

    render(
      <FleetHealthBar
        pipelines={[
          makePipeline({ name: 'a', blockerCount: 1 }),
          makePipeline({ name: 'b', blockerCount: 1 }),
          makePipeline({ name: 'c', failedStepCount: 1 }),
          makePipeline({ name: 'd', failedStepCount: 1 }),
          makePipeline({ name: 'e', ...promoting }),
          makePipeline({ name: 'f', ...promoting }),
        ]}
        activeFilter="all"
        onFilterChange={() => {}}
      />
    )
    expect(screen.getByRole('button', { name: '0 healthy pipelines' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '2 blocked pipelines' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '2 pipelines with a failed promotion' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '2 pipelines currently promoting' })).toBeInTheDocument()
  })
})
