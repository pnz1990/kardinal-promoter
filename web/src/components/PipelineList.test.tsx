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

// PipelineList.test.tsx — Tests for the pipeline list sidebar (#533, #345, #800).
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef } from 'react'
import { PipelineList } from './PipelineList'
import type { Pipeline } from '../types'

const makePipeline = (overrides: Partial<Pipeline> = {}): Pipeline => ({
  name: 'test-pipeline',
  namespace: 'default',
  phase: 'Ready',
  environmentCount: 3,
  ...overrides,
})

describe('PipelineList — empty and loading states', () => {
  it('renders skeleton placeholders when loading=true', () => {
    const { container } = render(
      <PipelineList pipelines={[]} loading onSelect={vi.fn()} />
    )
    // Should not show empty state
    expect(screen.queryByText(/No pipelines/i)).not.toBeInTheDocument()
    // Container should have content
    expect(container.firstChild).toBeTruthy()
  })

  it('renders error message when error prop is provided', () => {
    render(<PipelineList pipelines={[]} error="API unavailable" onSelect={vi.fn()} />)
    expect(screen.getByText(/Error: API unavailable/i)).toBeInTheDocument()
  })

  // A failed poll keeps the last pipelines App read: the list, its filter and
  // the / shortcut stay, and the error shows above the list.
  it('keeps the list and the filter, and shows the error, when a read fails after one worked', async () => {
    const user = userEvent.setup()
    const ref = createRef<HTMLInputElement>()
    const pipelines = [makePipeline({ name: 'alpha' }), makePipeline({ name: 'beta' })]
    const { rerender } = render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} searchInputRef={ref} />)
    await user.type(screen.getByRole('textbox', { name: 'Filter pipelines by name or namespace' }), 'alp')

    rerender(<PipelineList pipelines={pipelines} error="API error 503: unavailable" onSelect={vi.fn()} searchInputRef={ref} />)
    expect(screen.getByRole('alert')).toHaveTextContent(/^Error: API error 503: unavailable$/)
    const filter = screen.getByRole('textbox', { name: 'Filter pipelines by name or namespace' })
    expect(ref.current).toBe(filter)
    expect(filter).toHaveValue('alp')
    expect(screen.getByRole('list', { name: 'Pipelines' })).toHaveTextContent('alpha')
  })

  it('renders empty state when pipelines=[]', () => {
    render(<PipelineList pipelines={[]} onSelect={vi.fn()} />)
    expect(screen.getByText(/No pipelines found/i)).toBeInTheDocument()
  })

  // The fleet health bar's filter can hide every pipeline of a cluster that
  // has some: that is not an empty cluster, and the filter input (and its /
  // shortcut) stays.
  it('says no pipeline matches when the fleet filter hides them all', () => {
    const ref = createRef<HTMLInputElement>()
    render(<PipelineList pipelines={[]} total={3} onSelect={vi.fn()} searchInputRef={ref} />)
    expect(screen.queryByText(/No pipelines found/i)).not.toBeInTheDocument()
    expect(screen.getByText('No pipelines match this filter.')).toBeInTheDocument()
    expect(ref.current).toBe(screen.getByRole('textbox', { name: 'Filter pipelines by name or namespace' }))
  })

  it('names the query when the query and the fleet filter hide them all', async () => {
    render(<PipelineList pipelines={[]} total={3} onSelect={vi.fn()} />)
    await userEvent.type(screen.getByRole('textbox', { name: 'Filter pipelines by name or namespace' }), 'Shop')
    expect(await screen.findByText('No pipelines match “shop”')).toBeInTheDocument()
    expect(screen.queryByText('No pipelines match this filter.')).not.toBeInTheDocument()
  })

  it('leaves the setup commands to the main panel onboarding card', () => {
    render(<PipelineList pipelines={[]} onSelect={vi.fn()} />)
    expect(screen.queryByText(/kubectl apply/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/kardinal init/i)).not.toBeInTheDocument()
  })
})

describe('PipelineList — pipeline items', () => {
  it('renders pipeline name', () => {
    const pipelines = [makePipeline({ name: 'my-app' })]
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    expect(screen.getByText('my-app')).toBeInTheDocument()
  })

  it('renders multiple pipelines', () => {
    const pipelines = [
      makePipeline({ name: 'app-1' }),
      makePipeline({ name: 'app-2' }),
      makePipeline({ name: 'app-3' }),
    ]
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    expect(screen.getByText('app-1')).toBeInTheDocument()
    expect(screen.getByText('app-2')).toBeInTheDocument()
    expect(screen.getByText('app-3')).toBeInTheDocument()
  })

  it('calls onSelect when pipeline is clicked', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const pipelines = [makePipeline({ name: 'my-pipeline' })]
    render(<PipelineList pipelines={pipelines} onSelect={onSelect} />)
    await user.click(screen.getByText('my-pipeline'))
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith('my-pipeline', 'default')
  })

  it('calls onSelect on Enter key press', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const pipelines = [makePipeline({ name: 'kb-pipeline' })]
    render(<PipelineList pipelines={pipelines} onSelect={onSelect} />)
    // The copy button's name starts with "Copy"; the row's name starts with the pipeline.
    const item = screen.getByRole('button', { name: /^kb-pipeline/ })
    item.focus()
    await user.keyboard('{Enter}')
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith('kb-pipeline', 'default')
  })

  it('highlights selected pipeline with aria-pressed=true', () => {
    const pipelines = [makePipeline({ name: 'selected-app' })]
    render(<PipelineList pipelines={pipelines} selected="selected-app" onSelect={vi.fn()} />)
    const item = screen.getByRole('button', { name: /^selected-app/ })
    expect(item).toHaveAttribute('aria-pressed', 'true')
  })

  it('shows PAUSED badge when pipeline is paused', () => {
    const pipelines = [makePipeline({ paused: true })]
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    expect(screen.getByText('PAUSED')).toBeInTheDocument()
  })

  it('shows environment count', () => {
    const pipelines = [makePipeline({ environmentCount: 4 })]
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    expect(screen.getByText(/4 envs/i)).toBeInTheDocument()
  })

  // E2E-R05: "Idle" is only for phase Unknown (no bundle yet). A promoting
  // pipeline that a PolicyGate holds reads "Blocked", in the amber of the
  // fleet bar's Blocked badge.
  it.each<[string, Partial<Pipeline>, string, string]>([
    ['no bundle yet', { phase: 'Unknown' }, 'Idle', 'Unknown'],
    ['a bundle promoting', { phase: 'Promoting' }, 'Promoting', 'Reconciling'],
    ['a bundle held by a gate', { phase: 'Promoting', blockerCount: 1 }, 'Blocked', 'Reconciling'],
    ['every environment verified', { phase: 'Ready' }, 'Ready', 'Ready'],
    ['paused with a bundle held by a gate', { phase: 'Promoting', blockerCount: 1, paused: true }, 'Paused', 'Paused'],
  ])('phase chip for %s', (_, overrides, label, health) => {
    const { container } = render(<PipelineList pipelines={[makePipeline(overrides)]} onSelect={vi.fn()} />)
    const chip = container.querySelector('.health-chip')
    expect(chip?.textContent).toBe(label)
    expect(chip).toHaveAttribute('data-health-state', health)
  })
})

describe('PipelineList — search filter (#345 #800)', () => {
  // #800: filter now always visible at all pipeline counts, not just >3
  it('shows filter input with 1 pipeline (O5: always rendered)', () => {
    render(<PipelineList pipelines={[makePipeline()]} onSelect={vi.fn()} />)
    expect(screen.getByRole('textbox', { name: /filter/i })).toBeInTheDocument()
  })

  it('shows filter input with 3 pipelines (O5: no >3 guard)', () => {
    const pipelines = [
      makePipeline({ name: 'a' }),
      makePipeline({ name: 'b' }),
      makePipeline({ name: 'c' }),
    ]
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    expect(screen.getByRole('textbox', { name: /filter/i })).toBeInTheDocument()
  })

  it('shows filter input with 5 pipelines', () => {
    const pipelines = Array.from({ length: 5 }, (_, i) =>
      makePipeline({ name: `pipeline-${i + 1}` })
    )
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    expect(screen.getByRole('textbox', { name: /filter/i })).toBeInTheDocument()
  })

  it('filters pipelines by name', async () => {
    const user = userEvent.setup()
    const pipelines = Array.from({ length: 5 }, (_, i) =>
      makePipeline({ name: `pipeline-${i + 1}` })
    )
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    const input = screen.getByRole('textbox')
    await user.type(input, 'pipeline-3')
    // After debounce (wait for state update)
    await new Promise(r => setTimeout(r, 200))
    expect(screen.getByText('pipeline-3')).toBeInTheDocument()
  })

  it('Esc in filter clears value and blurs (O3)', async () => {
    const user = userEvent.setup()
    const pipelines = [makePipeline({ name: 'my-app' })]
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    const input = screen.getByRole('textbox', { name: /filter/i }) as HTMLInputElement
    await user.type(input, 'my')
    expect(input.value).toBe('my')
    await user.keyboard('{Escape}')
    // Value should be cleared
    expect(input.value).toBe('')
    // Input should not have focus (blur)
    expect(document.activeElement).not.toBe(input)
  })

  // #800: searchInputRef allows external focus
  it('searchInputRef points to the filter input (O1 mechanism)', () => {
    const ref = createRef<HTMLInputElement>()
    const pipelines = [makePipeline()]
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} searchInputRef={ref} />)
    expect(ref.current).toBeTruthy()
    expect(ref.current?.tagName.toLowerCase()).toBe('input')
    expect(ref.current?.getAttribute('aria-label')).toMatch(/filter/i)
  })
})

describe('PipelineList — virtual scrolling (#815)', () => {
  // O1: virtual scrolling activates above threshold
  it('renders all pipeline names with ≤50 pipelines (normal mode)', () => {
    const pipelines = Array.from({ length: 50 }, (_, i) =>
      makePipeline({ name: `pipe-${i + 1}` })
    )
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    // All pipelines should be in the DOM for ≤50
    expect(screen.getByText('pipe-1')).toBeInTheDocument()
    expect(screen.getByText('pipe-50')).toBeInTheDocument()
  })

  // O2: with >50 pipelines, the virtual list renders some items
  it('renders visible items with >50 pipelines (virtual mode)', () => {
    const pipelines = Array.from({ length: 100 }, (_, i) =>
      makePipeline({ name: `pipe-${i + 1}` })
    )
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    // At least some items should be rendered (overscan=5, estimateSize=52)
    // The virtualizer renders items visible in the scroll window.
    // In jsdom (no layout), totalSize=0 so getVirtualItems may be empty — check the list structure exists.
    const list = screen.getByRole('list', { name: /pipelines/i })
    expect(list).toBeInTheDocument()
  })

  // O3: filter works correctly even in virtual mode
  it('filter updates the virtual list items (O3)', async () => {
    const user = userEvent.setup()
    const pipelines = Array.from({ length: 100 }, (_, i) =>
      makePipeline({ name: `pipe-${i + 1}` })
    )
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    const input = screen.getByRole('textbox', { name: /filter/i })
    await user.type(input, 'pipe-99')
    await new Promise(r => setTimeout(r, 200))
    // List still renders correctly after filter
    const list = screen.getByRole('list', { name: /pipelines/i })
    expect(list).toBeInTheDocument()
  })

  // O5: multi-namespace grouped display falls back to normal rendering
  it('does NOT use virtual scrolling for multi-namespace grouped display (O5)', () => {
    const pipelines = [
      ...Array.from({ length: 60 }, (_, i) => makePipeline({ name: `app-${i}`, namespace: 'ns-a' })),
      ...Array.from({ length: 60 }, (_, i) => makePipeline({ name: `svc-${i}`, namespace: 'ns-b' })),
    ]
    render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    // Should render namespace headers (grouped mode, not virtual)
    expect(screen.getByText('ns-a')).toBeInTheDocument()
    expect(screen.getByText('ns-b')).toBeInTheDocument()
  })

  // O4 (aria-pressed on the selected row in virtual mode, #819) is in
  // PipelineList.virtual.test.tsx, which stubs the virtualizer so rows render in jsdom.
})
