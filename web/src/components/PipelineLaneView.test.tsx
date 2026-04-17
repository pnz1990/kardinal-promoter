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

// PipelineLaneView.test.tsx — Tests for the pipeline stage lane view (#533).
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { PipelineLaneView } from './PipelineLaneView'
import type { GraphNode } from '../types'

const makeNode = (overrides: Partial<GraphNode> = {}): GraphNode => ({
  id: 'step-test',
  type: 'PromotionStep',
  label: 'test',
  environment: 'test',
  state: 'Pending',
  ...overrides,
})

describe('PipelineLaneView — empty/loading states', () => {
  it('renders nothing when nodes=[]', () => {
    const { container } = render(<PipelineLaneView nodes={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders nothing when loading=true', () => {
    const nodes = [makeNode()]
    const { container } = render(<PipelineLaneView nodes={nodes} loading />)
    expect(container.firstChild).toBeNull()
  })

  it('filters out PolicyGate nodes, shows only PromotionStep', () => {
    const nodes: GraphNode[] = [
      makeNode({ id: 'step-test', type: 'PromotionStep', environment: 'test' }),
      {
        id: 'gate-no-weekend',
        type: 'PolicyGate',
        label: 'no-weekend',
        environment: 'no-weekend',
        state: 'Block',
      },
    ]
    render(<PipelineLaneView nodes={nodes} />)
    expect(screen.getByText('test')).toBeInTheDocument()
    expect(screen.queryByText('no-weekend')).not.toBeInTheDocument()
  })
})

describe('PipelineLaneView — stage cards', () => {
  it('renders stage card for each PromotionStep', () => {
    const nodes = [
      makeNode({ id: 'step-test', environment: 'test', state: 'Verified' }),
      makeNode({ id: 'step-uat', environment: 'uat', state: 'Promoting' }),
      makeNode({ id: 'step-prod', environment: 'prod', state: 'Pending' }),
    ]
    render(<PipelineLaneView nodes={nodes} />)
    expect(screen.getByText('test')).toBeInTheDocument()
    expect(screen.getByText('uat')).toBeInTheDocument()
    expect(screen.getByText('prod')).toBeInTheDocument()
  })

  it('renders HealthChip for each stage', () => {
    const nodes = [makeNode({ environment: 'env1', state: 'Verified' })]
    render(<PipelineLaneView nodes={nodes} />)
    // HealthChip renders the state as text (or label)
    expect(screen.getByText('Verified')).toBeInTheDocument()
  })

  it('calls onSelectNode when stage card is clicked', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const node = makeNode({ id: 'step-env', environment: 'env' })
    render(<PipelineLaneView nodes={[node]} onSelectNode={onSelect} />)
    await user.click(screen.getByRole('button', { name: /env/i }))
    expect(onSelect).toHaveBeenCalledWith(node)
  })

  it('deselects when selected card is clicked again', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const node = makeNode({ id: 'step-env', environment: 'env' })
    render(<PipelineLaneView nodes={[node]} selectedNode={node} onSelectNode={onSelect} />)
    await user.click(screen.getByRole('button', { name: /env/i }))
    expect(onSelect).toHaveBeenCalledWith(null)
  })

  it('selected card has stage-card--selected CSS class', () => {
    const node = makeNode({ id: 'step-env', environment: 'env' })
    render(<PipelineLaneView nodes={[node]} selectedNode={node} />)
    const card = screen.getByRole('button', { name: /env/i })
    expect(card.classList.contains('stage-card--selected')).toBe(true)
  })
})
