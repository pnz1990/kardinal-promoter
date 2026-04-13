// components/DAGView.tsx — Promotion DAG visualization using dagre for layout.
// Uses a simple SVG-based rendering to avoid full reactflow complexity while
// still providing a clear visual DAG.
import { useState } from 'react'
import dagre from '@dagrejs/dagre'
import type { GraphNode, GraphEdge, PromotionStep } from '../types'
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
  /** Pipeline name — passed to NodeDetail for the promote button. */
  pipelineName?: string
  /** Namespace of the pipeline. Defaults to 'default'. */
  namespace?: string
  /** Steps for the active bundle — avoids NodeDetail independent polling (#322). */
  steps?: PromotionStep[]
}

const NODE_WIDTH = 180
const NODE_HEIGHT = 64  // slightly taller to fit PR badge
const MARGIN = 40

/** Node label prefix for PolicyGate type. */
function nodeTypePrefix(node: GraphNode): string {
  return node.type === 'PolicyGate' ? '🔒 ' : ''
}

/**
 * Extract PR number from a GitHub PR URL.
 * "https://github.com/org/repo/pull/42" → "#42"
 */
function extractPRNumber(prURL: string): string | null {
  const m = prURL.match(/\/pull\/(\d+)$/)
  return m ? `#${m[1]}` : null
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

export function DAGView({ nodes, edges, loading, error, highlightNodeIds, bundleName, pipelineName, namespace, steps }: Props) {
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
        style={{ display: 'block', minWidth: '100%' }}
      >
        <defs>
          <marker id="arrowhead" markerWidth="8" markerHeight="6" refX="8" refY="3" orient="auto">
            <polygon points="0 0, 8 3, 0 6" fill="#475569" />
          </marker>
        </defs>

        {/* Edges — cubic bezier curves so they don't clip through nodes (#325) */}
        {layout.map((node, i) => {
          const nodeEdges = edges.filter(e => e.from === node.id)
          return nodeEdges.map((edge, j) => {
            const to = layout.find(n => n.id === edge.to)
            if (!to) return null
            const x1 = node.x + NODE_WIDTH / 2
            const y1 = node.y
            const x2 = to.x - NODE_WIDTH / 2
            const y2 = to.y
            // Control points: horizontal tension pulls the curve away from node edges.
            const cx1 = x1 + Math.abs(x2 - x1) * 0.5
            const cx2 = x2 - Math.abs(x2 - x1) * 0.5
            return (
              <path
                key={`${i}-${j}`}
                d={`M ${x1} ${y1} C ${cx1} ${y1}, ${cx2} ${y2}, ${x2} ${y2}`}
                fill="none"
                stroke="#475569"
                strokeWidth={1.5}
                markerEnd="url(#arrowhead)"
              />
            )
          })
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

          // PR badge: show PR number when WaitingForMerge or when prURL is available
          const prNumber = node.prURL ? extractPRNumber(node.prURL) : null
          const showPRBadge = prNumber !== null

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
                y={21}
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
                y={39}
                textAnchor="middle"
                fill={text}
                fontSize="10"
                style={{ pointerEvents: 'none' }}
              >
                {node.state}
              </text>
              {/* PR badge — shown when a PR exists (Kargo parity: stage node shows PR link) */}
              {showPRBadge && (
                <text
                  x={NODE_WIDTH / 2}
                  y={56}
                  textAnchor="middle"
                  fill="#818cf8"
                  fontSize="9"
                  style={{ pointerEvents: 'none' }}
                >
                  🔗 {prNumber}
                </text>
              )}
            </g>
          )
        })}
      </svg>

      <NodeDetail
        node={selected}
        onClose={() => setSelected(null)}
        bundleName={bundleName}
        pipelineName={pipelineName}
        namespace={namespace}
        steps={steps}
      />
    </div>
  )
}
