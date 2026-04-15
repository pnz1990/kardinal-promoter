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

// NodeDetail.test.tsx — Tests for the node detail panel (#533).
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { NodeDetail } from './NodeDetail'
import type { GraphNode, PromotionStep } from '../types'

// Mock the API client — NodeDetail calls validateCEL for PolicyGate nodes
vi.mock('../api/client', () => ({
  api: {
    validateCEL: vi.fn().mockResolvedValue({ valid: true }),
    promote: vi.fn().mockResolvedValue({ bundle: 'b', message: 'ok' }),
    rollback: vi.fn().mockResolvedValue({ bundle: 'b', message: 'ok' }),
    getSteps: vi.fn().mockResolvedValue([]),
    getStepEvents: vi.fn().mockResolvedValue([]),
  },
}))

const makePromotionStepNode = (overrides: Partial<GraphNode> = {}): GraphNode => ({
  id: 'step-test',
  type: 'PromotionStep',
  label: 'test',
  environment: 'test',
  state: 'Promoting',
  ...overrides,
})

const makePolicyGateNode = (overrides: Partial<GraphNode> = {}): GraphNode => ({
  id: 'gate-wk',
  type: 'PolicyGate',
  label: 'no-weekend',
  environment: 'no-weekend',
  state: 'Block',
  expression: '!schedule.isWeekend()',
  ...overrides,
})

const makeStep = (overrides: Partial<PromotionStep> = {}): PromotionStep => ({
  name: 'step-test-bundle',
  namespace: 'default',
  pipeline: 'my-app',
  bundle: 'bundle-abc',
  environment: 'test',
  stepType: 'standard',
  state: 'Promoting',
  ...overrides,
})

describe('NodeDetail — null node', () => {
  it('renders nothing when node=null', () => {
    const { container } = render(<NodeDetail node={null} onClose={vi.fn()} />)
    expect(container.firstChild).toBeNull()
  })
})

describe('NodeDetail — close button', () => {
  it('calls onClose when close button is clicked', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    render(
      <NodeDetail node={makePromotionStepNode()} onClose={onClose} />
    )
    const closeBtn = screen.getByLabelText('Close')
    await user.click(closeBtn)
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

describe('NodeDetail — PromotionStep node', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders environment name', () => {
    render(
      <NodeDetail
        node={makePromotionStepNode({ environment: 'production', state: 'Verified' })}
        onClose={vi.fn()}
      />
    )
    expect(screen.getByText('production')).toBeInTheDocument()
  })

  it('renders HealthChip with node state', () => {
    render(
      <NodeDetail
        node={makePromotionStepNode({ state: 'Verified' })}
        onClose={vi.fn()}
      />
    )
    // HealthChip renders the state label
    expect(screen.getByText('Verified')).toBeInTheDocument()
  })

  it('shows PR link when prURL is provided', () => {
    render(
      <NodeDetail
        node={makePromotionStepNode({ prURL: 'https://github.com/org/repo/pull/42' })}
        onClose={vi.fn()}
      />
    )
    // PR link text is "View Pull Request ↗" for non-WaitingForMerge states
    expect(screen.getByRole('link', { name: /View Pull Request/i })).toBeInTheDocument()
  })

  it('shows conditions section when step has conditions', () => {
    const steps = [makeStep({
      environment: 'test',
      conditions: [
        { type: 'Ready', status: 'True', message: 'Step complete' },
      ],
    })]
    render(
      <NodeDetail
        node={makePromotionStepNode({ id: 'step-test', environment: 'test' })}
        onClose={vi.fn()}
        steps={steps}
      />
    )
    expect(screen.getByText('Ready')).toBeInTheDocument()
  })
})

describe('NodeDetail — PolicyGate node', () => {
  it('renders gate name', () => {
    render(
      <NodeDetail
        node={makePolicyGateNode({ label: 'no-weekend-deploys' })}
        onClose={vi.fn()}
      />
    )
    expect(screen.getByText(/no-weekend-deploys/i)).toBeInTheDocument()
  })

  it('renders CEL expression', () => {
    render(
      <NodeDetail
        node={makePolicyGateNode({ expression: '!schedule.isWeekend()' })}
        onClose={vi.fn()}
      />
    )
    // Expression appears in the syntax-highlighted code block
    expect(screen.getByText(/isWeekend/i)).toBeInTheDocument()
  })
})
