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
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import dagre from '@dagrejs/dagre'
import { DAGView, orderRanks } from './DAGView'
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
      expression: '!schedule.isWeekend',
    }]
    render(<DAGView nodes={nodes} edges={[]} />)
    const elements = screen.getAllByText(/weekend-gate/i)
    expect(elements.length).toBeGreaterThanOrEqual(1)
  })

  it('names a PolicyGate node by its gate, not its environment', () => {
    // The API sets environment to the guarded environment (C07-controller-13).
    const nodes: GraphNode[] = [{
      id: 'gate-app-v1-no-weekend-prod',
      type: 'PolicyGate',
      label: 'no-weekend',
      environment: 'prod',
      state: 'Block',
    }]
    render(<DAGView nodes={nodes} edges={[]} />)
    expect(screen.getByRole('button', { name: 'no-weekend — Block' })).toBeInTheDocument()
    expect(screen.getByText(/no-weekend/, { selector: 'text' })).toBeInTheDocument()
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

// The PR badge text is "🔗 #N" (aria-hidden SVG text, found by text content).
function prBadge(): Element | null {
  return Array.from(document.querySelectorAll('svg text')).find(t => t.textContent?.startsWith('🔗')) ?? null
}

describe('DAGView — PR badge (C10a-web-13, C10a-web-14)', () => {
  afterEach(() => vi.restoreAllMocks())

  it.each([
    { scm: 'GitHub', url: 'https://github.com/o/r/pull/42', want: '🔗 #42' },
    { scm: 'GitLab', url: 'https://gitlab.com/o/r/-/merge_requests/7', want: '🔗 #7' },
    { scm: 'Forgejo', url: 'https://code.example.com/o/r/pulls/9', want: '🔗 #9' },
    { scm: 'Bitbucket', url: 'https://bitbucket.example.com/projects/P/repos/r/pull-requests/3/', want: '🔗 #3' },
    { scm: 'Azure DevOps', url: 'https://dev.azure.com/o/p/_git/r/pullrequest/12', want: '🔗 #12' },
  ])('shows the PR number for a $scm PR', ({ url, want }) => {
    render(<DAGView nodes={[makeNode({ state: 'WaitingForMerge', prURL: url })]} edges={[]} />)
    expect(prBadge()).toHaveTextContent(want)
  })

  it('opens an https PR in a new tab without selecting the node', () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const onSelect = vi.fn()
    render(<DAGView nodes={[makeNode({ prURL: 'https://github.com/o/r/pull/42' })]} edges={[]} onSelectNode={onSelect} />)
    fireEvent.click(prBadge()!)
    expect(open).toHaveBeenCalledWith('https://github.com/o/r/pull/42', '_blank', 'noopener,noreferrer')
    expect(onSelect).not.toHaveBeenCalled()
  })

  it.each([
    'javascript:alert(document.domain)//x/pull/1',
    'data:text/html,x/pull/1',
  ])('shows no badge and opens nothing for %s', url => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    render(<DAGView nodes={[makeNode({ prURL: url })]} edges={[]} />)
    expect(prBadge()).toBeNull()
    expect(open).not.toHaveBeenCalled()
  })
})

describe('DAGView — layout only re-runs when the topology changes (C10a-web-15)', () => {
  const topology = () => ({
    nodes: [makeNode({ id: 'a', environment: 'a', state: 'Promoting' }), makeNode({ id: 'b', environment: 'b' })],
    edges: [makeEdge('a', 'b')],
  })

  it('reuses the layout for polls that return the same nodes and edges', () => {
    vi.mocked(dagre.layout).mockClear()
    const first = topology()
    const { rerender } = render(<DAGView nodes={first.nodes} edges={first.edges} />)
    for (let i = 0; i < 3; i++) {
      const next = topology() // fresh arrays, as each poll returns
      rerender(<DAGView nodes={next.nodes} edges={next.edges} />)
    }
    expect(dagre.layout).toHaveBeenCalledTimes(1)
  })

  it('shows the new state from a poll even though the layout is reused', () => {
    const first = topology()
    const { rerender } = render(<DAGView nodes={first.nodes} edges={first.edges} />)
    const next = topology()
    next.nodes[0] = { ...next.nodes[0], state: 'Verified' }
    rerender(<DAGView nodes={next.nodes} edges={next.edges} />)
    expect(screen.getByRole('button', { name: 'a — Verified' })).toBeInTheDocument()
  })

  it('re-runs the layout when a node is added', () => {
    vi.mocked(dagre.layout).mockClear()
    const first = topology()
    const { rerender } = render(<DAGView nodes={first.nodes} edges={first.edges} />)
    const next = topology()
    next.nodes.push(makeNode({ id: 'c', environment: 'c' }))
    next.edges.push(makeEdge('b', 'c'))
    rerender(<DAGView nodes={next.nodes} edges={next.edges} />)
    expect(dagre.layout).toHaveBeenCalledTimes(2)
  })
})

describe('DAGView — tooltip on keyboard focus (C10a-web-18)', () => {
  it('opens on focus with the PR link and closes on blur', () => {
    vi.useFakeTimers()
    try {
      render(<DAGView nodes={[makeNode({ environment: 'uat', state: 'WaitingForMerge', prURL: 'https://github.com/o/r/pull/5' })]} edges={[]} />)
      const node = screen.getByRole('button', { name: 'uat — WaitingForMerge' })
      act(() => { node.focus() })
      const tooltip = screen.getByRole('tooltip')
      expect(tooltip).toHaveTextContent('uat')
      expect(tooltip.querySelector('a')).toHaveAttribute('href', 'https://github.com/o/r/pull/5')
      act(() => { node.blur() })
      act(() => { vi.advanceTimersByTime(200) })
      expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('orderRanks (#1580)', () => {
  const n = (id: string, type: GraphNode['type'] = 'PromotionStep'): GraphNode => makeNode({ id, environment: id, label: id, type })

  it('puts a wave top to bottom in list order, keeping the column and its slots', () => {
    const nodes = [n('root'), n('w1'), n('w2'), n('w3')]
    const edges: GraphEdge[] = [{ from: 'root', to: 'w1' }, { from: 'root', to: 'w2' }, { from: 'root', to: 'w3' }]
    // dagre's tie-break drew the wave bottom-up.
    const pos = new Map([['root', { x: 100, y: 200 }], ['w1', { x: 340, y: 300 }], ['w2', { x: 340, y: 200 }], ['w3', { x: 340, y: 100 }]])
    orderRanks(nodes, edges, pos)
    expect(['w1', 'w2', 'w3'].map(id => pos.get(id))).toEqual([{ x: 340, y: 100 }, { x: 340, y: 200 }, { x: 340, y: 300 }])
    expect(pos.get('root')).toEqual({ x: 100, y: 200 })
  })

  it('follows the upstream order through gates, so edges do not cross', () => {
    // a → gate-b → b and a → gate-c → c: the gates and their steps keep the same order.
    const nodes = [n('a'), n('b'), n('gate-b', 'PolicyGate'), n('c'), n('gate-c', 'PolicyGate')]
    const edges: GraphEdge[] = [{ from: 'a', to: 'gate-b' }, { from: 'gate-b', to: 'b' }, { from: 'a', to: 'gate-c' }, { from: 'gate-c', to: 'c' }]
    const pos = new Map([
      ['a', { x: 0, y: 50 }], ['gate-b', { x: 1, y: 90 }], ['gate-c', { x: 1, y: 10 }], ['b', { x: 2, y: 10 }], ['c', { x: 2, y: 90 }],
    ])
    orderRanks(nodes, edges, pos)
    expect(pos.get('gate-b')!.y).toBe(10)
    expect(pos.get('gate-c')!.y).toBe(90)
    expect(pos.get('b')!.y).toBe(10)
    expect(pos.get('c')!.y).toBe(90)
  })
})
