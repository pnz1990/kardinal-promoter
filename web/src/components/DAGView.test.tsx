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

// DAGView.test.tsx — Tests for the DAG visualization component (#533).
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DAGView } from './DAGView'
import type { GraphNode, GraphEdge } from '../types'

// Mock dagre to avoid layout computations in jsdom
vi.mock('@dagrejs/dagre', () => {
  function MockGraph(this: Record<string, unknown>) {
    this.setGraph = vi.fn()
    this.setDefaultEdgeLabel = vi.fn()
    this.setNode = vi.fn()
    this.setEdge = vi.fn()
    this.node = vi.fn().mockReturnValue({ x: 100, y: 80 })
  }
  return {
    default: {
      graphlib: { Graph: MockGraph },
      layout: vi.fn(),
    },
  }
})

const makeNode = (overrides: Partial<GraphNode> = {}): GraphNode => ({
  id: 'step-test',
  type: 'PromotionStep',
  label: 'test',
  environment: 'test',
  state: 'NotStarted',
  ...overrides,
})

const makeEdge = (from: string, to: string): GraphEdge => ({ from, to })

describe('DAGView — loading state', () => {
  it('shows skeleton when loading=true, not "No active promotion"', () => {
    render(<DAGView nodes={[]} edges={[]} loading />)
    expect(screen.queryByText(/No active promotion/i)).not.toBeInTheDocument()
  })
})

describe('DAGView — error state', () => {
  it('shows error message', () => {
    render(<DAGView nodes={[]} edges={[]} error="connection refused" />)
    expect(screen.getByText(/Error: connection refused/i)).toBeInTheDocument()
  })
})

describe('DAGView — empty state', () => {
  it('shows "No active promotion found" when nodes=[]', () => {
    render(<DAGView nodes={[]} edges={[]} />)
    expect(screen.getByText(/No active promotion found/i)).toBeInTheDocument()
  })
})

describe('DAGView — node rendering', () => {
  it('renders SVG when nodes are provided', () => {
    const nodes = [makeNode({ environment: 'production', state: 'Verified' })]
    render(<DAGView nodes={nodes} edges={[]} />)
    expect(document.querySelector('svg')).toBeInTheDocument()
  })

  it('renders environment name in node', () => {
    const nodes = [makeNode({ environment: 'staging', state: 'Promoting' })]
    render(<DAGView nodes={nodes} edges={[]} />)
    expect(screen.getByText('staging')).toBeInTheDocument()
  })

  it('renders node state text', () => {
    const nodes = [makeNode({ state: 'Verified' })]
    render(<DAGView nodes={nodes} edges={[]} />)
    // State text appears in SVG text elements (may appear multiple times due to legend)
    const elements = screen.getAllByText('Verified')
    expect(elements.length).toBeGreaterThanOrEqual(1)
  })

  it('renders PolicyGate node', () => {
    const nodes: GraphNode[] = [{
      id: 'gate-wk',
      type: 'PolicyGate',
      label: 'weekend-gate',
      environment: 'weekend-gate',
      state: 'Block',
      expression: '!schedule.isWeekend()',
    }]
    render(<DAGView nodes={nodes} edges={[]} />)
    const elements = screen.getAllByText(/weekend-gate/i)
    expect(elements.length).toBeGreaterThanOrEqual(1)
  })
})

describe('DAGView — node interaction', () => {
  it('calls onSelectNode when a node is clicked', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const nodes = [makeNode({ id: 'step-env', environment: 'my-env', state: 'Pending' })]
    render(<DAGView nodes={nodes} edges={[]} onSelectNode={onSelect} />)
    const btn = screen.getByRole('button', { name: /my-env — Pending/i })
    await user.click(btn)
    expect(onSelect).toHaveBeenCalledTimes(1)
  })

  it('passes null to onSelectNode when selected node is clicked again', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const node = makeNode({ id: 'step-env', environment: 'my-env', state: 'Pending' })
    render(<DAGView nodes={[node]} edges={[]} selectedNode={node} onSelectNode={onSelect} />)
    const btn = screen.getByRole('button', { name: /my-env — Pending/i })
    await user.click(btn)
    expect(onSelect).toHaveBeenCalledWith(null)
  })

  it('sets aria-pressed=true on selected node', () => {
    const node = makeNode({ id: 'step-env', environment: 'my-env', state: 'Pending' })
    render(<DAGView nodes={[node]} edges={[]} selectedNode={node} />)
    const btn = screen.getByRole('button', { name: /my-env — Pending/i })
    expect(btn).toHaveAttribute('aria-pressed', 'true')
  })
})

describe('DAGView — edge rendering', () => {
  it('renders SVG path for each edge', () => {
    const nodes = [
      makeNode({ id: 'step-a', environment: 'a' }),
      makeNode({ id: 'step-b', environment: 'b' }),
    ]
    const edges = [makeEdge('step-a', 'step-b')]
    render(<DAGView nodes={nodes} edges={edges} />)
    const paths = document.querySelectorAll('path')
    expect(paths.length).toBeGreaterThan(0)
  })
})

describe('DAGView — legend', () => {
  it('renders the DAG legend', () => {
    const nodes = [makeNode()]
    render(<DAGView nodes={nodes} edges={[]} />)
    expect(screen.getByText('Legend:')).toBeInTheDocument()
  })
})
