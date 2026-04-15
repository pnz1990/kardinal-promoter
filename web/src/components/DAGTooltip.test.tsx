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

// components/DAGTooltip.test.tsx — Unit tests for #526 portal tooltip.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import DAGTooltip, { type DAGTooltipTarget } from './DAGTooltip'
import type { GraphNode } from '../types'

function makeRect(overrides: Partial<DOMRect> = {}): DOMRect {
  return {
    left: 100,
    top: 50,
    right: 280,
    bottom: 126,
    width: 180,
    height: 76,
    x: 100,
    y: 50,
    toJSON: () => ({}),
    ...overrides,
  } as DOMRect
}

const promotionStepNode: GraphNode = {
  id: 'step-test',
  type: 'PromotionStep',
  label: 'test',
  environment: 'test',
  state: 'Promoting',
  message: 'git-push: in progress',
  prURL: 'https://github.com/pnz1990/kardinal-demo/pull/42',
}

const policyGateNode: GraphNode = {
  id: 'gate-wk',
  type: 'PolicyGate',
  label: 'no-weekend-deploys',
  environment: 'prod',
  state: 'Blocked',
  expression: '!schedule.isWeekend()',
  lastEvaluatedAt: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
  message: 'Weekend: gate is blocking',
}

const makeTarget = (node: GraphNode): DAGTooltipTarget => ({
  node,
  rect: makeRect(),
})

describe('DAGTooltip', () => {
  it('renders nothing when target is null', () => {
    const { container } = render(<DAGTooltip target={null} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders tooltip when target is provided', () => {
    render(<DAGTooltip target={makeTarget(promotionStepNode)} />)
    expect(screen.getByTestId('dag-tooltip')).toBeInTheDocument()
  })

  it('shows environment name for PromotionStep', () => {
    render(<DAGTooltip target={makeTarget(promotionStepNode)} />)
    expect(screen.getByText('test')).toBeInTheDocument()
  })

  it('shows state for PromotionStep', () => {
    render(<DAGTooltip target={makeTarget(promotionStepNode)} />)
    expect(screen.getByText('Promoting')).toBeInTheDocument()
  })

  it('shows message for PromotionStep', () => {
    render(<DAGTooltip target={makeTarget(promotionStepNode)} />)
    expect(screen.getByText(/git-push/)).toBeInTheDocument()
  })

  it('shows PR link for PromotionStep with prURL', () => {
    render(<DAGTooltip target={makeTarget(promotionStepNode)} />)
    const link = screen.getByRole('link', { name: /View Pull Request/ })
    expect(link).toHaveAttribute('href', promotionStepNode.prURL)
  })

  it('shows 🔒 prefix and gate name for PolicyGate', () => {
    render(<DAGTooltip target={makeTarget(policyGateNode)} />)
    expect(screen.getByText(/no-weekend-deploys/)).toBeInTheDocument()
  })

  it('shows CEL expression for PolicyGate', () => {
    render(<DAGTooltip target={makeTarget(policyGateNode)} />)
    expect(screen.getByText('!schedule.isWeekend()')).toBeInTheDocument()
  })

  it('shows last evaluated timestamp for PolicyGate', () => {
    render(<DAGTooltip target={makeTarget(policyGateNode)} />)
    expect(screen.getByText(/Evaluated/)).toBeInTheDocument()
  })

  it('calls onMouseEnter when cursor enters tooltip', () => {
    const onEnter = vi.fn()
    render(<DAGTooltip target={makeTarget(promotionStepNode)} onMouseEnter={onEnter} />)
    const tooltip = screen.getByTestId('dag-tooltip')
    fireEvent.mouseEnter(tooltip)
    expect(onEnter).toHaveBeenCalledOnce()
  })

  it('calls onMouseLeave when cursor leaves tooltip', () => {
    const onLeave = vi.fn()
    render(<DAGTooltip target={makeTarget(promotionStepNode)} onMouseLeave={onLeave} />)
    const tooltip = screen.getByTestId('dag-tooltip')
    fireEvent.mouseLeave(tooltip)
    expect(onLeave).toHaveBeenCalledOnce()
  })
})
