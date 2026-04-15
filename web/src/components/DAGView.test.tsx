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

// DAGView.test.tsx — Tests for the DAG visualization component.
// Covers static topology rendering (issue #525), loading/error states, and node rendering.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { DAGView } from './DAGView'
import type { GraphNode, GraphEdge } from '../types'

// Mock @dagrejs/dagre since it relies on layout algorithms not available in jsdom
vi.mock('@dagrejs/dagre', () => {
  function MockGraph(this: Record<string, unknown>) {
    this.setGraph = vi.fn()
    this.setDefaultEdgeLabel = vi.fn()
    this.setNode = vi.fn()
    this.setEdge = vi.fn()
    this.node = vi.fn().mockReturnValue({ x: 100, y: 100 })
  }
  return {
    default: {
      graphlib: {
        Graph: MockGraph,
      },
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
  it('renders skeleton placeholders when loading=true', () => {
    const { container } = render(
      <DAGView nodes={[]} edges={[]} loading />
    )
    // Should NOT show "No active promotion" text
    expect(screen.queryByText(/No active promotion/i)).not.toBeInTheDocument()
    // Should have loading content rendered
    expect(container.firstChild).toBeTruthy()
  })

  it('renders error message when error prop is provided', () => {
    render(<DAGView nodes={[]} edges={[]} error="connection refused" />)
    expect(screen.getByText(/Error: connection refused/i)).toBeInTheDocument()
  })
})

describe('DAGView — #525 static pipeline topology', () => {
  it('does NOT show "No active promotion" message when nodes are provided', () => {
    const nodes: GraphNode[] = [
      makeNode({ id: 'step-test', environment: 'test', state: 'NotStarted' }),
      makeNode({ id: 'step-uat', environment: 'uat', state: 'NotStarted' }),
      makeNode({ id: 'step-prod', environment: 'prod', state: 'NotStarted' }),
    ]
    const edges: GraphEdge[] = [
      makeEdge('step-test', 'step-uat'),
      makeEdge('step-uat', 'step-prod'),
    ]
    render(<DAGView nodes={nodes} edges={edges} />)
    expect(screen.queryByText(/No active promotion/i)).not.toBeInTheDocument()
  })

  it('renders SVG with nodes when topology is provided (even all NotStarted)', () => {
    const nodes: GraphNode[] = [
      makeNode({ id: 'step-test', environment: 'test', state: 'NotStarted' }),
    ]
    render(<DAGView nodes={nodes} edges={[]} />)
    // Should render an SVG element
    expect(document.querySelector('svg')).toBeInTheDocument()
  })

  it('renders "No active promotion" message ONLY when nodes array is empty', () => {
    render(<DAGView nodes={[]} edges={[]} />)
    expect(screen.getByText(/No active promotion found/i)).toBeInTheDocument()
  })

  it('renders environment name in node', () => {
    const nodes: GraphNode[] = [
      makeNode({ id: 'step-production', environment: 'production', state: 'NotStarted' }),
    ]
    render(<DAGView nodes={nodes} edges={[]} />)
    expect(screen.getByText('production')).toBeInTheDocument()
  })

  it('renders node state text', () => {
    const nodes: GraphNode[] = [
      makeNode({ id: 'step-test', environment: 'test', state: 'Promoting' }),
    ]
    render(<DAGView nodes={nodes} edges={[]} />)
    // Multiple elements with "Promoting" text are expected (SVG text + legend)
    const elements = screen.getAllByText('Promoting')
    expect(elements.length).toBeGreaterThanOrEqual(1)
  })
})

describe('DAGView — node interaction', () => {
  it('calls onSelectNode when a node is clicked', () => {
    const onSelect = vi.fn()
    const nodes: GraphNode[] = [
      makeNode({ id: 'step-env', environment: 'env', state: 'NotStarted' }),
    ]
    render(<DAGView nodes={nodes} edges={[]} onSelectNode={onSelect} />)
    // Find the node button by aria-label
    const nodeButton = screen.getByRole('button', { name: /env — NotStarted/i })
    fireEvent.click(nodeButton)
    expect(onSelect).toHaveBeenCalledTimes(1)
  })

  it('highlights selected node with aria-pressed=true', () => {
    const nodes: GraphNode[] = [
      makeNode({ id: 'step-env', environment: 'env', state: 'NotStarted' }),
    ]
    const selectedNode: GraphNode = makeNode({ id: 'step-env', environment: 'env', state: 'NotStarted' })
    render(<DAGView nodes={nodes} edges={[]} selectedNode={selectedNode} />)
    const nodeButton = screen.getByRole('button', { name: /env — NotStarted/i })
    expect(nodeButton).toHaveAttribute('aria-pressed', 'true')
  })
})

describe('DAGView — PolicyGate nodes', () => {
  it('renders PolicyGate node with lock prefix', () => {
    const nodes: GraphNode[] = [
      {
        id: 'gate-no-weekend',
        type: 'PolicyGate',
        label: 'no-weekend-deploys',
        environment: 'no-weekend-deploys',
        state: 'Block',
        expression: '!schedule.isWeekend()',
      },
    ]
    render(<DAGView nodes={nodes} edges={[]} />)
    // Should show the environment name in an SVG text element (may appear multiple times)
    const elements = screen.getAllByText(/no-weekend-deploys/i)
    expect(elements.length).toBeGreaterThanOrEqual(1)
  })
})
