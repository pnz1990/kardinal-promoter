// components/DAGView.tsx — Promotion DAG visualization using dagre for layout.
// Uses a simple SVG-based rendering to avoid full reactflow complexity while
// still providing a clear visual DAG.
//
// #326: selectedNode is lifted to the parent (App) so NodeDetail can be rendered
// as a proper split panel sibling rather than a position:fixed overlay.
// #334: DAG legend strip explains node shapes and state colors.
// #521: computeLayout is memoized — only re-runs when topology (IDs + edges) changes.
// #532: DAG node state is expressed via CSS classes (dag-node--{state}).
// #526: Portal tooltip replaces native SVG <title> tooltips.
import { useState, useEffect, useMemo, useRef, useCallback } from 'react'
import dagre from '@dagrejs/dagre'
import type { GraphNode, GraphEdge } from '../types'
import { kardinalStateToHealth, healthChipColors, healthStateClass } from './HealthChip'
import DAGTooltip, { type DAGTooltipTarget } from './DAGTooltip'
import '../styles/DAGView.css'

/** Active states that warrant an elapsed-time display (#330). */
const ACTIVE_STATES = new Set(['Promoting', 'WaitingForMerge', 'HealthChecking'])

/** Elapsed time formatter: "4m 12s", "1h 23m", etc. (#330) */
function formatElapsed(startedAt: string | undefined): string {
  if (!startedAt) return ''
  const startMs = new Date(startedAt).getTime()
  if (isNaN(startMs)) return ''
  const elapsed = Math.floor((Date.now() - startMs) / 1000)
  if (elapsed <= 0) return ''
  if (elapsed < 60) return `${elapsed}s`
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m ${elapsed % 60}s`
  return `${Math.floor(elapsed / 3600)}h ${Math.floor((elapsed % 3600) / 60)}m`
}

/** Hook that ticks every second and returns elapsed string for an active PromotionStep node. */
function useElapsedTick(startedAt: string | undefined, active: boolean): string {
  const [elapsed, setElapsed] = useState(() => formatElapsed(startedAt))
  useEffect(() => {
    if (!active || !startedAt) return
    setElapsed(formatElapsed(startedAt))
    const id = setInterval(() => setElapsed(formatElapsed(startedAt)), 1000)
    return () => clearInterval(id)
  }, [startedAt, active])
  return elapsed
}

interface Props {
  nodes: GraphNode[]
  edges: GraphEdge[]
  loading?: boolean
  error?: string
  /** Optional set of node IDs to highlight (e.g. blocked gates). */
  highlightNodeIds?: Set<string>
  /** Currently selected node — controlled by parent (#326). */
  selectedNode?: GraphNode | null
  /** Called when a node is clicked — parent updates selected state (#326). */
  onSelectNode?: (node: GraphNode | null) => void
  /** #532: When true, shows a .dag-static-banner indicating no active bundle. */
  isStaticTopology?: boolean
}

const NODE_WIDTH = 180
const NODE_HEIGHT = 76  // taller to fit PR badge + elapsed timer row (#330)
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

/**
 * #334: DAG legend strip — explains node shapes and state colors.
 * Shown at the bottom of the DAG view so new users understand the visualization.
 */
function DAGLegend() {
  const legendItems: Array<{ label: string; bg: string; border: string; text: string; desc: string }> = [
    { label: 'Verified', bg: '#14532d', border: '#16a34a', text: '#86efac', desc: 'Passed' },
    { label: 'Promoting', bg: '#1e1b4b', border: 'var(--color-accent)', text: 'var(--color-accent)', desc: 'Running' },
    { label: 'Waiting', bg: '#1c2c50', border: '#3b82f6', text: '#93c5fd', desc: 'Awaiting merge' },
    { label: 'Failed', bg: '#450a0a', border: '#dc2626', text: '#fca5a5', desc: 'Error' },
    { label: 'Pending', bg: 'var(--color-surface)', border: 'var(--color-text-faint)', text: 'var(--color-text-muted)', desc: 'Not started' },
  ]

  return (
    <div style={{
      display: 'flex',
      alignItems: 'center',
      gap: '0.75rem',
      flexWrap: 'wrap',
      marginTop: '0.75rem',
      padding: '0.5rem 0.25rem',
      borderTop: '1px solid #1e293b',
      fontSize: '0.65rem',
      color: 'var(--color-text-faint)',
    }}>
      <span style={{ fontWeight: 600, color: 'var(--color-text-faint)', marginRight: '0.25rem' }}>Legend:</span>
      {/* Node type icons */}
      <span title="PromotionStep — environment node (rounded rect)">
        <span style={{ fontSize: '0.6rem', border: '1px solid #475569', borderRadius: '2px', padding: '0 3px', marginRight: '3px', color: '#64748b' }}>▭</span>
        Environment step
      </span>
      <span title="PolicyGate — CEL policy check (🔒 prefix)">
        <span style={{ marginRight: '3px' }}>🔒</span>
        Policy gate
      </span>
      <span style={{ color: 'var(--color-surface)' }}>│</span>
      {/* State color chips */}
      {legendItems.map(item => (
        <span
          key={item.label}
          title={item.desc}
          style={{
            background: item.bg,
            border: `1px solid ${item.border}`,
            borderRadius: '3px',
            padding: '1px 5px',
            color: item.text,
            fontSize: '0.6rem',
            cursor: 'default',
          }}
        >
          {item.label}
        </span>
      ))}
      <span title="Highlighted nodes indicate blocked PolicyGates when filter is active" style={{ marginLeft: 'auto', color: 'var(--color-text-faint)' }}>
        <span style={{ color: 'var(--color-warning)', marginRight: '3px' }}>◆</span>highlighted = blocked gate
      </span>
    </div>
  )
}

/** A single DAG node rendered as SVG — extracted to enable the elapsed timer hook. */
function DAGNode({
  node,
  isSelected,
  isHighlighted,
  onSelectNode,
  onHoverStart,
  onHoverEnd,
}: {
  node: LayoutNode
  isSelected: boolean
  isHighlighted: boolean
  onSelectNode?: (node: GraphNode | null) => void
  onHoverStart?: (node: GraphNode, rect: DOMRect) => void
  onHoverEnd?: () => void
}) {
  const health = kardinalStateToHealth(node.state, node.type)
  const { bg, border, text } = healthChipColors(health)
  const isActive = node.type === 'PromotionStep' && ACTIVE_STATES.has(node.state)
  const elapsed = useElapsedTick(node.startedAt, isActive)

  const strokeColor = isHighlighted ? 'var(--color-warning)' : border
  const strokeWidth = isSelected ? 2.5 : isHighlighted ? 2.5 : 1.5

  const prNumber = node.prURL ? extractPRNumber(node.prURL) : null
  const showPRBadge = prNumber !== null

  // #532: compose CSS class list for the node group — enables class-based assertions in tests
  const nodeClass = [
    'dag-node',
    `dag-node--${healthStateClass(health).replace('health-chip--', '')}`,
    isSelected ? 'dag-node--selected' : '',
    isHighlighted ? 'dag-node--highlighted' : '',
  ].filter(Boolean).join(' ')

  return (
    <g
      transform={`translate(${node.x - NODE_WIDTH / 2}, ${node.y - NODE_HEIGHT / 2})`}
      onClick={() => onSelectNode?.(isSelected ? null : node)}
      style={{ cursor: 'pointer', outline: 'none' }}
      className={nodeClass}
      data-node-id={node.id}
      data-health-state={health}
      role="button"
      tabIndex={0}
      aria-label={`${node.environment} — ${node.state}`}
      aria-pressed={isSelected}
      onMouseEnter={e => {
        const rect = (e.currentTarget as SVGGElement).getBoundingClientRect()
        onHoverStart?.(node, rect)
      }}
      onMouseLeave={onHoverEnd}
      onKeyDown={e => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onSelectNode?.(isSelected ? null : node)
        }
      }}
      onFocus={e => {
        // #346: visible focus ring for keyboard navigation
        const rect = (e.currentTarget as SVGGElement).querySelector('rect')
        if (rect) {
          rect.setAttribute('stroke', 'var(--color-accent)')
          rect.setAttribute('stroke-width', '2.5')
        }
      }}
      onBlur={e => {
        // #346: restore normal stroke on blur
        const rect = (e.currentTarget as SVGGElement).querySelector('rect')
        if (rect) {
          rect.setAttribute('stroke', strokeColor)
          rect.setAttribute('stroke-width', String(strokeWidth))
        }
      }}
    >
      {/* Portal tooltip replaces native SVG <title> (#526). 
          See DAGTooltip rendered by DAGView parent. */}
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
        fill="var(--color-text)"
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
      {/* #330: Elapsed timer — shown for active PromotionStep nodes */}
      {isActive && elapsed && (
        <text
          x={NODE_WIDTH / 2}
          y={54}
          textAnchor="middle"
          fill="#f59e0b"
          fontSize="9"
          style={{ pointerEvents: 'none' }}
        >
          ⏱ {elapsed}
        </text>
      )}
      {/* PR badge — shown when a PR exists; clicking opens the PR in a new tab (#361)
          #748: Use text+onClick instead of <a> to avoid nested-interactive (a[href] inside g[role=button]).
          #762: role="link" removed — it made axe fire nested-interactive (interactive inside interactive).
          The <g> carries aria-label describing the node; PR number is visible in text. */}
      {showPRBadge && node.prURL && (
        <text
          x={NODE_WIDTH / 2}
          y={isActive && elapsed ? 68 : 56}
          textAnchor="middle"
          fill="#818cf8"
          fontSize="9"
          style={{ cursor: 'pointer', textDecoration: 'underline' }}
          onClick={e => { e.stopPropagation(); window.open(node.prURL, '_blank', 'noopener,noreferrer') }}
          aria-hidden="true"
        >
          🔗 {prNumber}
        </text>
      )}
    </g>
  )
}

export function DAGView({ nodes, edges, loading, error, highlightNodeIds, selectedNode, onSelectNode, isStaticTopology }: Props) {
  // #526: Portal tooltip state — shared across all DAGNode instances.
  const [tooltipTarget, setTooltipTarget] = useState<DAGTooltipTarget | null>(null)
  const hideTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const handleHoverStart = useCallback((node: GraphNode, rect: DOMRect) => {
    if (hideTimerRef.current !== null) {
      clearTimeout(hideTimerRef.current)
      hideTimerRef.current = null
    }
    setTooltipTarget({ node, rect })
  }, [])

  const handleHoverEnd = useCallback(() => {
    hideTimerRef.current = setTimeout(() => {
      setTooltipTarget(null)
      hideTimerRef.current = null
    }, 150)
  }, [])

  // Clean up hide timer on unmount
  useEffect(() => {
    return () => {
      if (hideTimerRef.current !== null) clearTimeout(hideTimerRef.current)
    }
  }, [])

  // #521: memoize dagre layout so it only recomputes when nodes/edges change,
  // not on every 5-second poll when only node states (colors) change.
  // IMPORTANT: this useMemo must come BEFORE any early returns so that React's
  // hook call count is consistent across all render paths (Rules of Hooks).
  const layout = useMemo(() => computeLayout(nodes, edges), [nodes, edges])

  if (loading) {
    return (
      <div>
        {/* #335: skeleton loading state */}
        <div style={{ padding: '2rem' }}>
          {[1, 2, 3].map(i => (
            <div
              key={i}
              style={{
                height: '64px',
                borderRadius: '6px',
                background: 'linear-gradient(90deg, #1e293b 25%, #293548 50%, #1e293b 75%)',
                backgroundSize: '200% 100%',
                animation: 'shimmer 1.5s infinite',
                marginBottom: '0.75rem',
                width: `${60 + i * 15}%`,
              }}
            />
          ))}
        </div>
        <style>{`
          @keyframes shimmer {
            0% { background-position: 200% 0; }
            100% { background-position: -200% 0; }
          }
        `}</style>
      </div>
    )
  }
  if (error) {
    return <div style={{ padding: '2rem', color: '#ef4444' }}>Error: {error}</div>
  }
  if (nodes.length === 0) {
    return <div style={{ padding: '2rem', color: 'var(--color-text-muted)' }}>No active promotion found.</div>
  }
  const maxX = Math.max(...layout.map(n => n.x + NODE_WIDTH / 2)) + MARGIN
  const maxY = Math.max(...layout.map(n => n.y + NODE_HEIGHT / 2)) + MARGIN
  const svgW = Math.max(maxX, 400)
  const svgH = Math.max(maxY, 200)

  return (
    <div>
      {/* #532: Static topology banner — uses .dag-static-banner CSS class */}
      {isStaticTopology && (
        <div className="dag-static-banner" data-testid="dag-static-banner">
          <span style={{ color: 'var(--color-text-faint)' }}>◦</span>
          Pipeline topology — no active bundle. Create one to start promoting.
        </div>
      )}
      <div style={{ overflow: 'auto' }}>
        <svg
          width={svgW}
          height={svgH}
          style={{ display: 'block', minWidth: '100%' }}
        >
          <defs>
            <marker id="arrowhead" markerWidth="8" markerHeight="6" refX="8" refY="3" orient="auto">
              <polygon points="0 0, 8 3, 0 6" fill="var(--color-text-faint)" />
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
              const cx1 = x1 + Math.abs(x2 - x1) * 0.5
              const cx2 = x2 - Math.abs(x2 - x1) * 0.5
              return (
                <path
                  key={`${i}-${j}`}
                  d={`M ${x1} ${y1} C ${cx1} ${y1}, ${cx2} ${y2}, ${x2} ${y2}`}
                  fill="none"
                  stroke="var(--color-text-faint)"
                  strokeWidth={1.5}
                  markerEnd="url(#arrowhead)"
                />
              )
            })
          })}

          {/* Nodes — rendered via component to support per-node hooks (elapsed timer) */}
          {layout.map(node => (
            <DAGNode
              key={node.id}
              node={node}
              isSelected={selectedNode?.id === node.id}
              isHighlighted={highlightNodeIds?.has(node.id) ?? false}
              onSelectNode={onSelectNode}
              onHoverStart={handleHoverStart}
              onHoverEnd={handleHoverEnd}
            />
          ))}
        </svg>
      </div>

      {/* #334: DAG legend strip */}
      <DAGLegend />

      {/* #526: Portal tooltip — rendered outside SVG to avoid clipping */}
      <DAGTooltip
        target={tooltipTarget}
        onMouseEnter={() => {
          if (hideTimerRef.current !== null) {
            clearTimeout(hideTimerRef.current)
            hideTimerRef.current = null
          }
        }}
        onMouseLeave={handleHoverEnd}
      />
    </div>
  )
}
