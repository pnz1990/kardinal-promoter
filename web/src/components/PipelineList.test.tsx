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

// PipelineList.test.tsx — Tests for #462 pipeline operations view:
// sortPipelines, pipelineRowHealth, formatAge.
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import type { Pipeline } from '../types'
import {
  sortPipelines,
  pipelineRowHealth,
  formatAge,
  PipelineList,
} from './PipelineList'

// ─── Fixtures ─────────────────────────────────────────────────────────────────

function makePipeline(overrides: Partial<Pipeline> = {}): Pipeline {
  return {
    name: 'my-app',
    namespace: 'default',
    phase: 'Promoting',
    environmentCount: 3,
    ...overrides,
  }
}

// ─── formatAge ────────────────────────────────────────────────────────────────

describe('formatAge', () => {
  it('returns — for undefined or zero', () => {
    expect(formatAge(undefined)).toBe('—')
    expect(formatAge(0)).toBe('—')
  })

  it('formats seconds', () => {
    expect(formatAge(45)).toBe('45s')
    expect(formatAge(59)).toBe('59s')
  })

  it('formats minutes', () => {
    expect(formatAge(60)).toBe('1m')
    expect(formatAge(90)).toBe('1m')
    expect(formatAge(3599)).toBe('59m')
  })

  it('formats hours', () => {
    expect(formatAge(3600)).toBe('1h')
    expect(formatAge(7200)).toBe('2h')
    expect(formatAge(86399)).toBe('23h')
  })

  it('formats days', () => {
    expect(formatAge(86400)).toBe('1d')
    expect(formatAge(172800)).toBe('2d')
  })
})

// ─── pipelineRowHealth ────────────────────────────────────────────────────────

describe('pipelineRowHealth', () => {
  it('returns red when blockedCount > 0', () => {
    expect(pipelineRowHealth(makePipeline({ blockedCount: 2 }))).toBe('red')
  })

  it('returns yellow when paused', () => {
    expect(pipelineRowHealth(makePipeline({ paused: true, blockedCount: 0 }))).toBe('yellow')
  })

  it('returns green when phase is Verified', () => {
    expect(pipelineRowHealth(makePipeline({ phase: 'Verified', blockedCount: 0 }))).toBe('green')
  })

  it('returns yellow when phase is Promoting', () => {
    expect(pipelineRowHealth(makePipeline({ phase: 'Promoting', blockedCount: 0 }))).toBe('yellow')
  })

  it('returns yellow when phase is WaitingForMerge', () => {
    expect(pipelineRowHealth(makePipeline({ phase: 'WaitingForMerge', blockedCount: 0 }))).toBe('yellow')
  })

  it('returns red when phase is Failed and no blockedCount', () => {
    expect(pipelineRowHealth(makePipeline({ phase: 'Failed', blockedCount: 0 }))).toBe('red')
  })

  it('returns gray when no active bundle', () => {
    expect(pipelineRowHealth(makePipeline({ phase: 'Ready', blockedCount: 0, activeBundleName: undefined }))).toBe('gray')
  })

  it('red takes precedence over paused', () => {
    expect(pipelineRowHealth(makePipeline({ paused: true, blockedCount: 1 }))).toBe('red')
  })
})

// ─── sortPipelines ────────────────────────────────────────────────────────────

describe('sortPipelines', () => {
  const pipelines: Pipeline[] = [
    makePipeline({ name: 'c-app', blockedCount: 0, lastBundleAgeSeconds: 100, phase: 'Verified' }),
    makePipeline({ name: 'a-app', blockedCount: 2, lastBundleAgeSeconds: 300, phase: 'Failed' }),
    makePipeline({ name: 'b-app', blockedCount: 0, lastBundleAgeSeconds: 50,  phase: 'Promoting' }),
  ]

  it('sorts by name ascending', () => {
    const sorted = sortPipelines(pipelines, 'name', 'asc')
    expect(sorted.map(p => p.name)).toEqual(['a-app', 'b-app', 'c-app'])
  })

  it('sorts by name descending', () => {
    const sorted = sortPipelines(pipelines, 'name', 'desc')
    expect(sorted.map(p => p.name)).toEqual(['c-app', 'b-app', 'a-app'])
  })

  it('sorts by blocked desc: most blocked first', () => {
    const sorted = sortPipelines(pipelines, 'blocked', 'desc')
    expect(sorted[0].name).toBe('a-app') // blockedCount=2 floats first
  })

  it('sorts by age asc: newest first (smallest age in seconds)', () => {
    const sorted = sortPipelines(pipelines, 'age', 'asc')
    expect(sorted.map(p => p.lastBundleAgeSeconds)).toEqual([50, 100, 300])
  })

  it('sorts by age desc: oldest first', () => {
    const sorted = sortPipelines(pipelines, 'age', 'desc')
    expect(sorted.map(p => p.lastBundleAgeSeconds)).toEqual([300, 100, 50])
  })

  it('sorts by status desc: worst health first', () => {
    // a-app is Failed (red), b-app is Promoting (yellow), c-app is Verified (green)
    const sorted = sortPipelines(pipelines, 'status', 'desc')
    expect(sorted[0].name).toBe('a-app') // red
    expect(sorted[sorted.length - 1].name).toBe('c-app') // green
  })

  it('does not mutate the original array', () => {
    const original = [...pipelines]
    sortPipelines(pipelines, 'name', 'asc')
    expect(pipelines.map(p => p.name)).toEqual(original.map(p => p.name))
  })

  it('default blocked sort: pipeline with blockedCount=2 appears first', () => {
    // This is the default sort used on initial render
    const sorted = sortPipelines(pipelines, 'blocked', 'desc')
    expect(sorted[0].blockedCount).toBe(2)
  })
})

// ─── PipelineList component ───────────────────────────────────────────────────

describe('PipelineList', () => {
  it('renders empty state when no pipelines', () => {
    render(<PipelineList pipelines={[]} onSelect={() => {}} />)
    expect(screen.getByText('No pipelines found.')).toBeInTheDocument()
  })

  it('renders pipeline name', () => {
    const p = makePipeline({ name: 'my-pipeline', phase: 'Verified', activeBundleName: 'v1.0' })
    render(<PipelineList pipelines={[p]} onSelect={() => {}} />)
    expect(screen.getByText('my-pipeline')).toBeInTheDocument()
  })

  it('shows blocked badge when blockedCount > 0', () => {
    const p = makePipeline({ name: 'bad-pipeline', blockedCount: 2, phase: 'Failed' })
    render(<PipelineList pipelines={[p]} onSelect={() => {}} />)
    expect(screen.getByTitle('2 environments failed')).toBeInTheDocument()
  })

  it('shows PAUSED badge when paused', () => {
    const p = makePipeline({ paused: true, name: 'paused-pipeline' })
    render(<PipelineList pipelines={[p]} onSelect={() => {}} />)
    expect(screen.getByTitle('Pipeline is paused — no new promotions will start')).toBeInTheDocument()
  })

  it('shows sort controls when more than 1 pipeline', () => {
    const p1 = makePipeline({ name: 'app-1' })
    const p2 = makePipeline({ name: 'app-2', namespace: 'default' })
    render(<PipelineList pipelines={[p1, p2]} onSelect={() => {}} />)
    expect(screen.getByLabelText(/Sort by Name/)).toBeInTheDocument()
    expect(screen.getByLabelText(/Sort by Blocked/)).toBeInTheDocument()
    expect(screen.getByLabelText(/Sort by Age/)).toBeInTheDocument()
  })

  it('does not show sort controls when only 1 pipeline', () => {
    const p = makePipeline({ name: 'app-1' })
    render(<PipelineList pipelines={[p]} onSelect={() => {}} />)
    expect(screen.queryByLabelText(/Sort by Name/)).not.toBeInTheDocument()
  })

  it('shows loading skeleton when loading=true', () => {
    const { container } = render(<PipelineList pipelines={[]} onSelect={() => {}} loading={true} />)
    // Skeleton is rendered as divs with animation
    const skeletons = container.querySelectorAll('div[style*="shimmer-pl"]')
    expect(skeletons.length).toBeGreaterThan(0)
  })

  it('shows error message when error prop is set', () => {
    render(<PipelineList pipelines={[]} onSelect={() => {}} error="network error" />)
    expect(screen.getByText(/network error/)).toBeInTheDocument()
  })

  it('calls onSelect with pipeline name on click', async () => {
    const onSelect = vi.fn()
    const p = makePipeline({ name: 'click-me', phase: 'Verified' })
    render(<PipelineList pipelines={[p]} onSelect={onSelect} />)
    screen.getByText('click-me').click()
    expect(onSelect).toHaveBeenCalledWith('click-me')
  })

  it('shows age when lastBundleAgeSeconds is set', () => {
    const p = makePipeline({
      name: 'aged',
      activeBundleName: 'v1',
      lastBundleAgeSeconds: 7200,
      environmentStates: { test: 'Verified' },
    })
    render(<PipelineList pipelines={[p]} onSelect={() => {}} />)
    expect(screen.getByTitle('Age of active bundle')).toBeInTheDocument()
    expect(screen.getByTitle('Age of active bundle').textContent).toBe('2h')
  })
})
