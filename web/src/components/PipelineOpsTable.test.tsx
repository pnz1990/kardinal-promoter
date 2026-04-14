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

import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { PipelineOpsTable } from './PipelineOpsTable'
import type { Pipeline } from '../types'

function makePipeline(overrides: Partial<Pipeline> = {}): Pipeline {
  return {
    name: 'test-pipeline',
    namespace: 'default',
    phase: 'Ready',
    environmentCount: 3,
    activeBundleName: 'test-app-v1',
    blockerCount: 0,
    failedStepCount: 0,
    inventoryAgeDays: 2,
    lastMergedAt: new Date(Date.now() - 86400000).toISOString(), // 1 day ago
    cdLevel: 'full-cd',
    ...overrides,
  }
}

describe('PipelineOpsTable', () => {
  it('renders column headers', () => {
    render(
      <PipelineOpsTable
        pipelines={[makePipeline()]}
        onSelect={() => {}}
      />
    )
    expect(screen.getByText('Pipeline')).toBeTruthy()
    expect(screen.getByText('Status')).toBeTruthy()
    expect(screen.getByText('Blockers')).toBeTruthy()
    expect(screen.getByText('Failed Steps')).toBeTruthy()
    expect(screen.getByText('Inventory Age')).toBeTruthy()
    expect(screen.getByText('Last Merge')).toBeTruthy()
    expect(screen.getByText('CD Level')).toBeTruthy()
  })

  it('renders pipeline name and bundle', () => {
    render(
      <PipelineOpsTable
        pipelines={[makePipeline({ name: 'my-pipeline', activeBundleName: 'my-bundle-v2' })]}
        onSelect={() => {}}
      />
    )
    expect(screen.getByText('my-pipeline')).toBeTruthy()
    expect(screen.getByText('my-bundle-v2')).toBeTruthy()
  })

  it('shows blocker count in red when > 0', () => {
    const { container } = render(
      <PipelineOpsTable
        pipelines={[makePipeline({ blockerCount: 3 })]}
        onSelect={() => {}}
      />
    )
    const blockerCell = container.querySelector('td:nth-child(3)')
    expect(blockerCell?.textContent).toContain('3')
    // JSDOM normalizes hex to rgb — check for the red color in either format
    const style = blockerCell?.getAttribute('style') ?? ''
    expect(
      style.includes('#ef4444') || style.includes('rgb(239, 68, 68)')
    ).toBe(true)
  })

  it('shows green when no blockers', () => {
    const { container } = render(
      <PipelineOpsTable
        pipelines={[makePipeline({ blockerCount: 0 })]}
        onSelect={() => {}}
      />
    )
    const blockerCell = container.querySelector('td:nth-child(3)')
    const style = blockerCell?.getAttribute('style') ?? ''
    expect(
      style.includes('#22c55e') || style.includes('rgb(34, 197, 94)')
    ).toBe(true)
  })

  it('shows stale inventory warning when > 14 days', () => {
    render(
      <PipelineOpsTable
        pipelines={[makePipeline({ inventoryAgeDays: 20 })]}
        onSelect={() => {}}
      />
    )
    // Warning icon should be present in the inventory age cell
    expect(screen.getByTitle('20 days since last bundle')).toBeTruthy()
  })

  it('calls onSelect when row is clicked', () => {
    const onSelect = vi.fn()
    render(
      <PipelineOpsTable
        pipelines={[makePipeline({ name: 'click-me' })]}
        onSelect={onSelect}
      />
    )
    const row = screen.getByRole('button')
    fireEvent.click(row)
    expect(onSelect).toHaveBeenCalledWith('click-me')
  })

  it('calls onSelect on Enter key', () => {
    const onSelect = vi.fn()
    render(
      <PipelineOpsTable
        pipelines={[makePipeline({ name: 'keyboard-select' })]}
        onSelect={onSelect}
      />
    )
    const row = screen.getByRole('button')
    fireEvent.keyDown(row, { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledWith('keyboard-select')
  })

  it('filters pipelines by name substring', () => {
    const pipelines = [
      makePipeline({ name: 'frontend-pipeline' }),
      makePipeline({ name: 'backend-pipeline' }),
    ]
    render(<PipelineOpsTable pipelines={pipelines} onSelect={() => {}} />)
    const filterInput = screen.getByRole('textbox')
    fireEvent.change(filterInput, { target: { value: 'front' } })
    expect(screen.getByText('frontend-pipeline')).toBeTruthy()
    expect(screen.queryByText('backend-pipeline')).toBeNull()
  })

  it('shows empty state when filter matches nothing', () => {
    render(
      <PipelineOpsTable
        pipelines={[makePipeline({ name: 'nginx' })]}
        onSelect={() => {}}
      />
    )
    const filterInput = screen.getByRole('textbox')
    fireEvent.change(filterInput, { target: { value: 'xyznotfound' } })
    expect(screen.getByText('No pipelines match "xyznotfound"')).toBeTruthy()
  })

  it('sorts by blockerCount descending by default', () => {
    const pipelines = [
      makePipeline({ name: 'no-blockers', blockerCount: 0 }),
      makePipeline({ name: 'two-blockers', blockerCount: 2 }),
      makePipeline({ name: 'one-blocker', blockerCount: 1 }),
    ]
    const { container } = render(
      <PipelineOpsTable pipelines={pipelines} onSelect={() => {}} />
    )
    const rows = container.querySelectorAll('tbody tr')
    // Default sort is blockerCount desc → two-blockers first
    expect(rows[0].textContent).toContain('two-blockers')
  })

  it('shows PAUSED badge for paused pipelines', () => {
    render(
      <PipelineOpsTable
        pipelines={[makePipeline({ paused: true, name: 'paused-pipe' })]}
        onSelect={() => {}}
      />
    )
    expect(screen.getByText('PAUSED')).toBeTruthy()
  })

  it('renders loading state', () => {
    render(<PipelineOpsTable pipelines={[]} onSelect={() => {}} loading />)
    expect(screen.getByText('Loading pipelines…')).toBeTruthy()
  })

  it('renders error state', () => {
    render(<PipelineOpsTable pipelines={[]} onSelect={() => {}} error="connection refused" />)
    expect(screen.getByText(/connection refused/)).toBeTruthy()
  })

  it('shows Full CD badge for full-cd pipeline', () => {
    render(
      <PipelineOpsTable
        pipelines={[makePipeline({ cdLevel: 'full-cd' })]}
        onSelect={() => {}}
      />
    )
    expect(screen.getByText('Full CD')).toBeTruthy()
  })

  it('shows Manual badge for manual pipeline', () => {
    render(
      <PipelineOpsTable
        pipelines={[makePipeline({ cdLevel: 'manual' })]}
        onSelect={() => {}}
      />
    )
    expect(screen.getByText('Manual')).toBeTruthy()
  })
})
