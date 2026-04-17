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

// PipelineList.test.tsx — Tests for the pipeline list sidebar (#533).
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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

  it('renders empty state when pipelines=[]', () => {
    render(<PipelineList pipelines={[]} onSelect={vi.fn()} />)
    expect(screen.getByText(/No pipelines found/i)).toBeInTheDocument()
  })

  it('shows kubectl command in empty state', () => {
    render(<PipelineList pipelines={[]} onSelect={vi.fn()} />)
    expect(screen.getByText(/kubectl apply/i)).toBeInTheDocument()
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
    // #762: click the invisible selection button (pointer-events pattern)
    await user.click(screen.getByRole('button', { name: /Select pipeline my-pipeline/i }))
    expect(onSelect).toHaveBeenCalledWith('my-pipeline')
  })

  it('calls onSelect on Enter key press', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const pipelines = [makePipeline({ name: 'kb-pipeline' })]
    render(<PipelineList pipelines={pipelines} onSelect={onSelect} />)
    const item = screen.getByRole('button', { name: /kb-pipeline/i })
    item.focus()
    await user.keyboard('{Enter}')
    expect(onSelect).toHaveBeenCalled()
  })

  it('highlights selected pipeline with aria-pressed=true', () => {
    const pipelines = [makePipeline({ name: 'selected-app' })]
    render(<PipelineList pipelines={pipelines} selected="selected-app" onSelect={vi.fn()} />)
    const item = screen.getByRole('button', { name: /selected-app/i })
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
})

describe('PipelineList — search filter (shown when > 3 pipelines)', () => {
  const manyPipelines = Array.from({ length: 5 }, (_, i) =>
    makePipeline({ name: `pipeline-${i + 1}` })
  )

  it('shows filter input when more than 3 pipelines', () => {
    render(<PipelineList pipelines={manyPipelines} onSelect={vi.fn()} />)
    expect(screen.getByRole('textbox', { name: /filter/i })).toBeInTheDocument()
  })

  it('filters pipelines by name', async () => {
    const user = userEvent.setup()
    render(<PipelineList pipelines={manyPipelines} onSelect={vi.fn()} />)
    const input = screen.getByRole('textbox')
    await user.type(input, 'pipeline-3')
    // After debounce (wait for state update)
    await new Promise(r => setTimeout(r, 200))
    expect(screen.getByText('pipeline-3')).toBeInTheDocument()
  })
})
