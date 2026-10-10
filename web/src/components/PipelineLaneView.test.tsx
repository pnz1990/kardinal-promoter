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
  releaseHold: vi.fn(),
}))
vi.mock('../api/client', () => ({ api }))

import { laneStages, PipelineLaneView } from './PipelineLaneView'

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

// The rule itself is tested in pipelineActions.test.ts; these check the lane uses it.
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

// #1528: roll back and hold, and release the hold.
describe('PipelineLaneView — rollback hold (#1528)', () => {
  beforeEach(() => {
    api.rollback.mockReset()
    api.releaseHold.mockReset()
  })

  it('holds the environment with a reason, and refuses a hold without one', async () => {
    const user = userEvent.setup()
    api.rollback.mockResolvedValue({ bundle: 'app-rollback-abc123', message: 'ok', held: true })
    const { nodes, edges } = lane({ test: 'Verified', uat: 'Verified', prod: 'Verified' })
    render(<PipelineLaneView nodes={nodes} edges={edges} pipelineName="app" namespace="team-a" />)

    await user.click(screen.getByRole('button', { name: 'Roll back prod' }))
    const dialog = screen.getByRole('dialog', { name: 'Roll back prod?' })
    await user.click(within(dialog).getByRole('checkbox', { name: 'Hold prod on this rollback' }))
    expect(within(dialog).getByText(/would block the rollback pass as EXEMPT/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Roll back and hold prod' }))
    expect(within(dialog).getByRole('alert')).toHaveTextContent('Say why prod is held')
    expect(api.rollback).not.toHaveBeenCalled()

    await user.type(within(dialog).getByLabelText('Reason (required)'), '  INC-42: leaks connections ')
    await user.click(within(dialog).getByRole('button', { name: 'Roll back and hold prod' }))
    expect(api.rollback).toHaveBeenCalledWith('app', 'prod', 'team-a', undefined, 'INC-42: leaks connections')
    await user.click(screen.getByRole('button', { name: 'Roll back prod' }))
    const again = screen.getByRole('dialog', { name: 'Roll back prod?' })
    await user.click(within(again).getByRole('checkbox', { name: 'Hold prod on this rollback' }))
    await user.type(within(again).getByLabelText('Reason (required)'), 'INC-43')
    await user.selectOptions(within(again).getByLabelText('Hold ends'), '24h')
    await user.click(within(again).getByRole('button', { name: 'Roll back and hold prod' }))
    expect(api.rollback).toHaveBeenLastCalledWith('app', 'prod', 'team-a', undefined, 'INC-43', '24h')
    expect(await screen.findByRole('status')).toHaveTextContent('Rollback started: bundle app-rollback-abc123; prod is held on it')
  })

  it('a plain rollback sends no hold', async () => {
    const user = userEvent.setup()
    api.rollback.mockResolvedValue({ bundle: 'app-rb', message: 'ok' })
    const { nodes, edges } = lane({ test: 'Verified', uat: 'Verified', prod: 'Verified' })
    render(<PipelineLaneView nodes={nodes} edges={edges} pipelineName="app" namespace="team-a" />)
    await user.click(screen.getByRole('button', { name: 'Roll back prod' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Roll back prod' }))
    expect(api.rollback).toHaveBeenCalledWith('app', 'prod', 'team-a')
  })

  it('shows a held environment with its reason and releases it on confirm', async () => {
    const user = userEvent.setup()
    api.releaseHold.mockResolvedValue({ message: 'ok' })
    const onActionDone = vi.fn()
    const { nodes, edges } = lane({ test: 'Verified', uat: 'Verified', prod: 'Verified' })
    render(<PipelineLaneView nodes={nodes} edges={edges} pipelineName="app" namespace="team-a" onActionDone={onActionDone}
      holds={{ prod: { bundle: 'app-rollback-abc123', reason: 'INC-42', createdBy: 'alice' } }} />)

    expect(screen.getByText('Held')).toBeInTheDocument()
    expect(screen.getByTitle('Held on app-rollback-abc123 by alice: INC-42')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Roll back prod' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Release the hold on prod' }))
    const dialog = screen.getByRole('dialog', { name: 'Release the hold on prod?' })
    expect(api.releaseHold).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'Release hold' }))
    expect(api.releaseHold).toHaveBeenCalledWith('app', 'prod', 'team-a')
    expect(await screen.findByRole('status')).toHaveTextContent('Hold on prod released')
    expect(onActionDone).toHaveBeenCalledOnce()
  })
})

describe('PipelineLaneView — waves and parallel environments (#1580)', () => {
  const step = (env: string, state = 'Pending'): GraphNode => makeNode({ id: `step-${env}`, label: env, environment: env, state })
  const fan = (root: string, envs: string[], states: Record<string, string> = {}) => ({
    nodes: [step(root, 'Verified'), ...envs.map(e => step(e, states[e]))],
    edges: envs.map(e => ({ from: `step-${root}`, to: `step-${e}` })),
  })

  it('groups steps by depth through gates, in graph order', () => {
    const nodes = [step('test'), step('eu'), step('us'), step('global'),
      { id: 'gate-g', type: 'PolicyGate' as const, label: 'g', environment: 'us', state: 'Pass' }]
    const edges: GraphEdge[] = [
      { from: 'step-test', to: 'step-eu' }, { from: 'step-test', to: 'gate-g' }, { from: 'gate-g', to: 'step-us' },
      { from: 'step-eu', to: 'step-global' }, { from: 'step-us', to: 'step-global' },
    ]
    expect(laneStages(nodes, edges).map(g => g.map(n => n.environment))).toEqual([['test'], ['eu', 'us'], ['global']])
    expect(laneStages(nodes, []).map(g => g.map(n => n.environment))).toEqual([['test'], ['eu'], ['us'], ['global']])
  })

  it('stacks a few parallel environments in one column', () => {
    const { nodes, edges } = fan('test', ['eu', 'us'])
    render(<PipelineLaneView nodes={nodes} edges={edges} />)
    const column = screen.getByRole('group', { name: 'Parallel: eu, us' })
    expect(within(column).getByText('eu')).toBeInTheDocument()
    expect(within(column).getByText('us')).toBeInTheDocument()
  })

  it('draws a wave as one card that counts its states and expands to the list', async () => {
    const envs = Array.from({ length: 149 }, (_, i) => `env-${String(i + 1).padStart(3, '0')}`)
    const states = Object.fromEntries(envs.map((e, i) => [e, i < 120 ? 'Verified' : i < 148 ? 'Promoting' : 'Failed']))
    const { nodes, edges } = fan('env-000', envs, states)
    const onSelect = vi.fn()
    render(<PipelineLaneView nodes={nodes} edges={edges} onSelectNode={onSelect} />)
    const wave = screen.getByRole('group', { name: '149 environments: env-001 to env-149' })
    expect(within(wave).getByText('env-001 … env-149')).toBeInTheDocument()
    expect(wave).toHaveTextContent('1 failed28 in progress120 verified')
    // Collapsed: no card per environment.
    expect(screen.queryByRole('button', { name: 'Select env-042' })).toBeNull()

    await userEvent.click(within(wave).getByRole('button', { name: 'Show environments' }))
    expect(within(wave).getAllByRole('button', { name: /^Select env-/ })).toHaveLength(149)
    await userEvent.click(within(wave).getByRole('button', { name: 'Select env-042' }))
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ environment: 'env-042' }))
    await userEvent.click(within(wave).getByRole('button', { name: 'Hide environments' }))
    expect(screen.queryByRole('button', { name: 'Select env-042' })).toBeNull()
  })

  it('the toggle controls the list, and a collapsed wave names its selected environment', async () => {
    const envs = Array.from({ length: 6 }, (_, i) => `w${i + 1}`)
    const { nodes, edges } = fan('test', envs)
    const { rerender } = render(<PipelineLaneView nodes={nodes} edges={edges} />)
    const wave = screen.getByRole('group', { name: '6 environments: w1 to w6' })
    const toggle = within(wave).getByRole('button', { name: 'Show environments' })
    expect(wave).not.toHaveTextContent('Selected:')
    rerender(<PipelineLaneView nodes={nodes} edges={edges} selectedNode={nodes[3]} />)
    expect(wave).toHaveTextContent('Selected: w3')
    expect(wave).toHaveClass('stage-wave--selected')
    await userEvent.click(toggle)
    const list = document.getElementById(toggle.getAttribute('aria-controls')!)
    expect(list).not.toBeNull()
    expect(within(list!).getAllByRole('button', { name: /^(Des|S)elect w/ })).toHaveLength(6)
    expect(wave).not.toHaveTextContent('Selected:')
  })
})
