// components/DAGView.tsx — Promotion DAG visualization using dagre for layout.
// Uses a simple SVG-based rendering to avoid full reactflow complexity while
// still providing a clear visual DAG.
import { useState } from 'react'
import dagre from '@dagrejs/dagre'
import type { GraphNode, GraphEdge } from '../types'
import { NodeDetail } from './NodeDetail'
import { kardinalStateToHealth, healthChipColors } from './HealthChip'

interface Props {
  nodes: GraphNode[]
  edges: GraphEdge[]
  loading?: boolean
  error?: string
  /** Optional set of node IDs to highlight (e.g. blocked gates). */
  highlightNodeIds?: Set<string>
  /** Bundle name — passed to NodeDetail to fetch step detail. */
  bundleName?: string
}

const NODE_WIDTH = 180
const NODE_HEIGHT = 60
const MARGIN = 40

/** Node label prefix for PolicyGate type. */
function nodeTypePrefix(node: GraphNode): string {
  return node.type === 'PolicyGate' ? '🔒 ' : ''
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

export function DAGView({ nodes, edges, loading, error, highlightNodeIds, bundleName }: Props) {
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
        {layout.map(node => {
          const health = kardinalStateToHealth(node.state, node.type)
          const { bg, border, text } = healthChipColors(health)
          const isSelected = selected?.id === node.id
          const isHighlighted = highlightNodeIds?.has(node.id) ?? false

          // Highlighted nodes get a brighter stroke to stand out.
          const strokeColor = isHighlighted ? '#fbbf24' : border
          const strokeWidth = isSelected ? 2.5 : isHighlighted ? 2.5 : 1.5

          return (
            <g
              key={node.id}
              transform={`translate(${node.x - NODE_WIDTH / 2}, ${node.y - NODE_HEIGHT / 2})`}
              onClick={() => setSelected(node)}
              style={{ cursor: 'pointer' }}
              role="button"
              aria-label={`${node.environment} — ${node.state}`}
            >
              <rect
                width={NODE_WIDTH}
                height={NODE_HEIGHT}
                rx={6}
                fill={bg}
                stroke={strokeColor}
                strokeWidth={strokeWidth}
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
                {nodeTypePrefix(node)}{node.environment}
              </text>
              <text
                x={NODE_WIDTH / 2}
                y={42}
                textAnchor="middle"
                fill={text}
                fontSize="10"
                style={{ pointerEvents: 'none' }}
              >
                {node.state}
              </text>
            </g>
          )
        })}
      </svg>

      <NodeDetail node={selected} onClose={() => setSelected(null)} bundleName={bundleName} />
    </div>
  )
}
