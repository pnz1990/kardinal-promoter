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
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { GraphEdge, GraphNode } from '../types'

const api = vi.hoisted(() => ({
  promote: vi.fn(),
  rollback: vi.fn(),
}))
vi.mock('../api/client', () => ({ api }))

import { PipelineLaneView, canPromote } from './PipelineLaneView'

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

  it('aria-pressed=true on selected card', () => {
    const node = makeNode({ id: 'step-env', environment: 'env' })
    render(<PipelineLaneView nodes={[node]} selectedNode={node} />)
    const card = screen.getByRole('button', { name: /env/i })
    expect(card).toHaveAttribute('aria-pressed', 'true')
  })
})

// test → gate → prod, and test → uat (the shape Go sends: gates sit between steps).
function lane(states: { test: string; uat: string; prod: string }) {
  const nodes: GraphNode[] = [
    makeNode({ id: 'step-test', environment: 'test', state: states.test }),
    makeNode({ id: 'step-uat', environment: 'uat', state: states.uat }),
    { id: 'gate-prod', type: 'PolicyGate', label: 'no-weekend', environment: 'prod', state: 'Pass' },
    makeNode({ id: 'step-prod', environment: 'prod', state: states.prod }),
  ]
  const edges: GraphEdge[] = [
    { from: 'step-test', to: 'step-uat' },
    { from: 'step-uat', to: 'gate-prod' },
    { from: 'gate-prod', to: 'step-prod' },
  ]
  return { nodes, edges }
}

describe('canPromote (C10b-web-08)', () => {
  it.each([
    { name: 'not reached yet, upstream verified (through a gate)', env: 'prod', states: { test: 'Verified', uat: 'Verified', prod: 'NotStarted' }, want: true },
    { name: 'failed, upstream verified', env: 'prod', states: { test: 'Verified', uat: 'Verified', prod: 'Failed' }, want: true },
    { name: 'stopped by an alarm, upstream verified', env: 'uat', states: { test: 'Verified', uat: 'AbortedByAlarm', prod: 'NotStarted' }, want: true },
    { name: 'not reached yet, upstream still promoting', env: 'prod', states: { test: 'Verified', uat: 'Promoting', prod: 'NotStarted' }, want: false },
    { name: 'in flight', env: 'uat', states: { test: 'Verified', uat: 'Promoting', prod: 'NotStarted' }, want: false },
    { name: 'waiting for merge', env: 'uat', states: { test: 'Verified', uat: 'WaitingForMerge', prod: 'NotStarted' }, want: false },
    { name: 'already verified', env: 'uat', states: { test: 'Verified', uat: 'Verified', prod: 'NotStarted' }, want: false },
    { name: 'first environment (nothing upstream)', env: 'test', states: { test: 'Failed', uat: 'NotStarted', prod: 'NotStarted' }, want: false },
  ])('$name → $want', ({ env, states, want }) => {
    const { nodes, edges } = lane(states)
    const node = nodes.find(n => n.environment === env && n.type === 'PromotionStep')!
    expect(canPromote(node, nodes, edges)).toBe(want)
  })
})

describe('PipelineLaneView — promote and roll back (C10b-web-08)', () => {
  beforeEach(() => {
    api.promote.mockReset()
    api.rollback.mockReset()
  })

  function renderLane(states: { test: string; uat: string; prod: string }, onActionDone = vi.fn()) {
    const { nodes, edges } = lane(states)
    render(<PipelineLaneView nodes={nodes} edges={edges} pipelineName="app" namespace="team-a" onActionDone={onActionDone} />)
    return onActionDone
  }

  it('shows Promote only where a promotion can start, and Roll back only on verified environments', () => {
    renderLane({ test: 'Verified', uat: 'Promoting', prod: 'NotStarted' })
    expect(screen.queryByRole('button', { name: /^Promote to/ })).not.toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /^Roll back/ }).map(b => b.getAttribute('aria-label'))).toEqual(['Roll back test'])
  })

  it('shows no actions without a pipeline', () => {
    const { nodes, edges } = lane({ test: 'Verified', uat: 'Verified', prod: 'NotStarted' })
    render(<PipelineLaneView nodes={nodes} edges={edges} />)
    expect(screen.queryByRole('button', { name: /^(Promote to|Roll back)/ })).not.toBeInTheDocument()
  })

  it('asks first, calls the API only on confirm, then reports and refreshes', async () => {
    const user = userEvent.setup()
    api.promote.mockResolvedValue({ bundle: 'app-xyz', message: 'ok' })
    const onActionDone = renderLane({ test: 'Verified', uat: 'Verified', prod: 'NotStarted' })

    await user.click(screen.getByRole('button', { name: 'Promote to prod' }))
    const dialog = screen.getByRole('dialog', { name: 'Promote app to prod?' })
    expect(api.promote).not.toHaveBeenCalled()

    await user.click(within(dialog).getByRole('button', { name: 'Promote to prod' }))
    expect(api.promote).toHaveBeenCalledWith('app', 'prod', 'team-a')
    expect(await screen.findByRole('status')).toHaveTextContent('Promotion started: bundle app-xyz')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(onActionDone).toHaveBeenCalledOnce()
  })

  it('Cancel closes the dialog without calling the API', async () => {
    const user = userEvent.setup()
    renderLane({ test: 'Verified', uat: 'NotStarted', prod: 'NotStarted' })
    await user.click(screen.getByRole('button', { name: 'Roll back test' }))
    await user.click(within(screen.getByRole('dialog', { name: 'Roll back test?' })).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(api.rollback).not.toHaveBeenCalled()
  })

  it('keeps the dialog open and says what failed', async () => {
    const user = userEvent.setup()
    api.rollback.mockRejectedValue(new Error('API error 404: pipeline not found'))
    const onActionDone = renderLane({ test: 'Verified', uat: 'NotStarted', prod: 'NotStarted' })
    await user.click(screen.getByRole('button', { name: 'Roll back test' }))
    const dialog = screen.getByRole('dialog', { name: 'Roll back test?' })
    await user.click(within(dialog).getByRole('button', { name: 'Roll back test' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Could not roll back test: API error 404: pipeline not found')
    expect(onActionDone).not.toHaveBeenCalled()
  })
})
