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

// DAGView.test.tsx — Tests for DAG memoization (#521) and basic rendering.
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { DAGView } from './DAGView'
import type { GraphNode, GraphEdge } from '../types'

// Mock dagre to count layout calls
let layoutCallCount = 0

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
      graphlib: { Graph: MockGraph },
      layout: vi.fn(() => { layoutCallCount++ }),
    },
  }
})

const makeNode = (id: string, state = 'NotStarted'): GraphNode => ({
  id,
  type: 'PromotionStep',
  label: id,
  environment: id,
  state,
})

const makeEdge = (from: string, to: string): GraphEdge => ({ from, to })

const BASE_NODES: GraphNode[] = [
  makeNode('step-test', 'Promoting'),
  makeNode('step-uat', 'Pending'),
  makeNode('step-prod', 'Pending'),
]
const BASE_EDGES: GraphEdge[] = [
  makeEdge('step-test', 'step-uat'),
  makeEdge('step-uat', 'step-prod'),
]

describe('DAGView — #521 layout memoization', () => {
  it('renders nodes and does not crash', () => {
    layoutCallCount = 0
    render(<DAGView nodes={BASE_NODES} edges={BASE_EDGES} />)
    expect(document.querySelector('svg')).toBeInTheDocument()
  })

  it('re-renders with same topology but different states — layout should not re-run', () => {
    layoutCallCount = 0
    const { rerender } = render(<DAGView nodes={BASE_NODES} edges={BASE_EDGES} />)
    const callsAfterFirstRender = layoutCallCount

    // Same IDs + edges, but node states changed (simulates poll update)
    const updatedNodes: GraphNode[] = [
      makeNode('step-test', 'Verified'),   // state changed
      makeNode('step-uat', 'Promoting'),   // state changed
      makeNode('step-prod', 'Pending'),
    ]
    rerender(<DAGView nodes={updatedNodes} edges={BASE_EDGES} />)

    // Layout should NOT have been called again (topology unchanged)
    expect(layoutCallCount).toBe(callsAfterFirstRender)
  })

  it('re-renders with new node added — layout DOES re-run', () => {
    layoutCallCount = 0
    const { rerender } = render(<DAGView nodes={BASE_NODES} edges={BASE_EDGES} />)
    const callsAfterFirstRender = layoutCallCount

    // New node added — topology changed
    const expandedNodes: GraphNode[] = [
      ...BASE_NODES,
      makeNode('step-newenv', 'Pending'),
    ]
    const expandedEdges: GraphEdge[] = [
      ...BASE_EDGES,
      makeEdge('step-prod', 'step-newenv'),
    ]
    rerender(<DAGView nodes={expandedNodes} edges={expandedEdges} />)

    // Layout SHOULD have been called again (topology changed)
    expect(layoutCallCount).toBeGreaterThan(callsAfterFirstRender)
  })
})

describe('DAGView — empty/loading/error states', () => {
  it('shows loading skeleton when loading=true', () => {
    render(<DAGView nodes={[]} edges={[]} loading />)
    expect(screen.queryByText(/No active promotion/i)).not.toBeInTheDocument()
  })

  it('shows error message when error prop given', () => {
    render(<DAGView nodes={[]} edges={[]} error="cluster unreachable" />)
    expect(screen.getByText(/Error: cluster unreachable/i)).toBeInTheDocument()
  })

  it('shows empty message when nodes is empty (no loading, no error)', () => {
    render(<DAGView nodes={[]} edges={[]} />)
    expect(screen.getByText(/No active promotion found/i)).toBeInTheDocument()
  })
})
