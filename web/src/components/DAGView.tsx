// components/DAGView.tsx — Promotion DAG visualization using dagre for layout.
// Uses a simple SVG-based rendering to avoid full reactflow complexity while
// still providing a clear visual DAG.
import { useState } from 'react'
import dagre from '@dagrejs/dagre'
import type { GraphNode, GraphEdge } from '../types'
import { NodeDetail } from './NodeDetail'

interface Props {
  nodes: GraphNode[]
  edges: GraphEdge[]
  loading?: boolean
  error?: string
}

const NODE_WIDTH = 180
const NODE_HEIGHT = 60
const MARGIN = 40

function nodeColor(node: GraphNode): string {
  if (node.type === 'PolicyGate') {
    switch (node.state) {
      case 'Pass': return '#166534'
      case 'Fail': return '#7f1d1d'
      default: return '#374151'
    }
  }
  switch (node.state) {
    case 'Succeeded': return '#14532d'
    case 'Running': case 'WaitingForMerge': case 'HealthChecking': return '#78350f'
    case 'Failed': return '#7f1d1d'
    default: return '#1e293b'
  }
}

function nodeBorderColor(node: GraphNode): string {
  if (node.type === 'PolicyGate') {
    switch (node.state) {
      case 'Pass': return '#22c55e'
      case 'Fail': return '#ef4444'
      default: return '#6b7280'
    }
  }
  switch (node.state) {
    case 'Succeeded': return '#22c55e'
    case 'Running': case 'WaitingForMerge': case 'HealthChecking': return '#f59e0b'
    case 'Failed': return '#ef4444'
    default: return '#475569'
  }
}

interface LayoutNode extends GraphNode {
  x: number
  y: number
}

function computeLayout(nodes: GraphNode[], edges: GraphEdge[]): LayoutNode[] {
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: 20, ranksep: 60, marginx: MARGIN, marginy: MARGIN })
  g.setDefaultEdgeLabel(() => ({}))

  for (const node of nodes) {
    g.setNode(node.id, { width: NODE_WIDTH, height: NODE_HEIGHT })
  }
  for (const edge of edges) {
    g.setEdge(edge.from, edge.to)
  }

  dagre.layout(g)

  return nodes.map(node => {
    const { x, y } = g.node(node.id) as { x: number; y: number }
    return { ...node, x, y }
  })
}

export function DAGView({ nodes, edges, loading, error }: Props) {
  const [selected, setSelected] = useState<GraphNode | null>(null)

  if (loading) {
    return <div style={{ padding: '2rem', color: '#94a3b8' }}>Loading DAG...</div>
  }
  if (error) {
    return <div style={{ padding: '2rem', color: '#ef4444' }}>Error: {error}</div>
  }
  if (nodes.length === 0) {
    return <div style={{ padding: '2rem', color: '#94a3b8' }}>No active promotion found.</div>
  }

  const layout = computeLayout(nodes, edges)
  const maxX = Math.max(...layout.map(n => n.x + NODE_WIDTH / 2)) + MARGIN
  const maxY = Math.max(...layout.map(n => n.y + NODE_HEIGHT / 2)) + MARGIN
  const svgW = Math.max(maxX, 400)
  const svgH = Math.max(maxY, 200)

  return (
    <div style={{ position: 'relative', overflow: 'auto' }}>
      <svg
        width={svgW}
        height={svgH}
        style={{ display: 'block' }}
        role="img"
        aria-label="Promotion DAG"
      >
        <defs>
          <marker id="arrowhead" markerWidth="8" markerHeight="6" refX="8" refY="3" orient="auto">
            <polygon points="0 0, 8 3, 0 6" fill="#475569" />
          </marker>
        </defs>

        {/* Edges */}
        {edges.map((edge, i) => {
          const from = layout.find(n => n.id === edge.from)
          const to = layout.find(n => n.id === edge.to)
          if (!from || !to) return null
          return (
            <line
              key={i}
              x1={from.x + NODE_WIDTH / 2}
              y1={from.y}
              x2={to.x - NODE_WIDTH / 2}
              y2={to.y}
              stroke="#475569"
              strokeWidth={1.5}
              markerEnd="url(#arrowhead)"
            />
          )
        })}

        {/* Nodes */}
        {layout.map(node => (
          <g
            key={node.id}
            transform={`translate(${node.x - NODE_WIDTH / 2}, ${node.y - NODE_HEIGHT / 2})`}
            onClick={() => setSelected(node)}
            style={{ cursor: 'pointer' }}
          >
            <rect
              width={NODE_WIDTH}
              height={NODE_HEIGHT}
              rx={6}
              fill={nodeColor(node)}
              stroke={nodeBorderColor(node)}
              strokeWidth={selected?.id === node.id ? 2.5 : 1.5}
            />
            <text
              x={NODE_WIDTH / 2}
              y={22}
              textAnchor="middle"
              fill="#e2e8f0"
              fontSize="11"
              fontWeight="600"
              style={{ pointerEvents: 'none' }}
            >
              {node.type === 'PolicyGate' ? '🔒 ' : ''}{node.environment}
            </text>
            <text
              x={NODE_WIDTH / 2}
              y={42}
              textAnchor="middle"
              fill="#94a3b8"
              fontSize="10"
              style={{ pointerEvents: 'none' }}
            >
              {node.state}
            </text>
          </g>
        ))}
      </svg>

      <NodeDetail node={selected} onClose={() => setSelected(null)} />
    </div>
  )
}
