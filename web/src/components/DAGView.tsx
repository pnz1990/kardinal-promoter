// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/DAGView.tsx — Promotion DAG visualization: dagre lays the graph
// out and plain SVG draws it (no graph-rendering library).
//
// #326: selectedNode is lifted to the parent (App) so NodeDetail can be rendered
// as a proper split panel sibling rather than a position:fixed overlay.
// #334: DAG legend strip explains node shapes and state colors.
// #521: dagre only re-runs when the topology (node IDs + edges) changes; a poll
//       that only changes node states reuses the positions.
// #532: DAG node state is expressed via CSS classes (dag-node--{state}).
// #526: Portal tooltip replaces native SVG <title> tooltips. It opens on hover
//       and on keyboard focus.
import { displayState } from '../displayStates.gen'
import { useState, useEffect, useMemo, useRef, useCallback } from 'react'
import dagre from '@dagrejs/dagre'
import type { GraphNode, GraphEdge } from '../types'
import { kardinalStateToHealth, healthStateClass } from './HealthChip'
import DAGTooltip, { type DAGTooltipTarget } from './DAGTooltip'
import { formatElapsedSince } from '../timeFormat'
import { isHttpURL, prNumberFromURL } from '../prLink'
import { Icon } from '../ui/Icon'
import { ErrorNotice } from '../ui/ErrorNotice'
import '../styles/DAGView.css'

/** Active states that warrant an elapsed-time display (#330). */
const ACTIVE_STATES = new Set(['Promoting', 'WaitingForMerge', 'HealthChecking', 'Verifying'])

/** Hook that ticks every second and returns elapsed string for an active PromotionStep node. */
function useElapsedTick(startedAt: string | undefined, active: boolean): string {
  const [elapsed, setElapsed] = useState(() => formatElapsedSince(startedAt))
  useEffect(() => {
    if (!active || !startedAt) return
    setElapsed(formatElapsedSince(startedAt))
    const id = setInterval(() => setElapsed(formatElapsedSince(startedAt)), 1000)
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
  /** Read the graph again (the error's Retry). */
  onRetry?: () => void
}

const NODE_WIDTH = 180
const NODE_HEIGHT = 76  // taller to fit PR badge + elapsed timer row (#330)
/** Where a gate node's icon sits, from its top-left corner. */
const GATE_ICON_INSET = 6
const MARGIN = 40

/** Text baselines inside a node, as fractions of its height: name, state, elapsed
 * timer, and the PR badge (below the timer when one shows). */
const LINE = {
  name: NODE_HEIGHT * 0.28,
  state: NODE_HEIGHT * 0.51,
  elapsed: NODE_HEIGHT * 0.71,
  prUnderTimer: NODE_HEIGHT * 0.89,
  pr: NODE_HEIGHT * 0.74,
}

/** The display-state label of a node (P2): the CLI's words, never the raw value. */
function stateLabel(node: GraphNode): string {
  return displayState(node.type === 'PolicyGate' ? 'gate' : 'step', node.state).label
}

/** Display name: the gate name for PolicyGate nodes, else the environment. */
function nodeName(node: GraphNode): string {
  return node.type === 'PolicyGate' ? node.label : node.environment
}

/**
 * PR number for the badge, from any supported SCM ("#42"), or null. Null also
 * for a non-http(s) URL, so the badge never opens one.
 */
function extractPRNumber(prURL: string): string | null {
  return prNumberFromURL(prURL)
}

interface LayoutNode extends GraphNode {
  x: number
  y: number
}


/** Node centre positions from dagre, keyed by node ID. */
function computePositions(nodes: GraphNode[], edges: GraphEdge[]): Map<string, { x: number; y: number }> {
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

  const positions = new Map<string, { x: number; y: number }>()
  for (const node of nodes) {
    const { x, y } = g.node(node.id) as { x: number; y: number }
    positions.set(node.id, { x, y })
  }
  orderRanks(nodes, edges, positions)
  return positions
}

/**
 * orderRanks puts the nodes of each rank (column) top to bottom in the graph's
 * order, which is the Pipeline's environment order (#1580): dagre breaks ties
 * its own way and drew a wave of env-001…env-149 from env-149 down. Columns
 * are visited left to right; a node sorts by the mean y of its upstream nodes
 * (already placed), then by its position in nodes, and takes one of the
 * column's existing y slots, so x and spacing stay dagre's.
 */
export function orderRanks(nodes: GraphNode[], edges: GraphEdge[], positions: Map<string, { x: number; y: number }>): void {
  const index = new Map(nodes.map((n, i) => [n.id, i]))
  const ups = new Map<string, string[]>()
  for (const e of edges) {
    if (index.has(e.from) && index.has(e.to)) ups.set(e.to, [...(ups.get(e.to) ?? []), e.from])
  }
  const columns = new Map<number, string[]>()
  for (const n of nodes) {
    const x = Math.round(positions.get(n.id)!.x)
    columns.set(x, [...(columns.get(x) ?? []), n.id])
  }
  for (const x of [...columns.keys()].sort((a, b) => a - b)) {
    const ids = columns.get(x)!
    if (ids.length < 2) continue
    const slots = ids.map(id => positions.get(id)!.y).sort((a, b) => a - b)
    const key = (id: string): number => {
      const from = ups.get(id) ?? []
      if (from.length === 0) return 0
      return from.reduce((sum, u) => sum + positions.get(u)!.y, 0) / from.length
    }
    const keys = new Map(ids.map(id => [id, key(id)]))
    const sorted = [...ids].sort((a, b) => keys.get(a)! - keys.get(b)! || index.get(a)! - index.get(b)!)
    sorted.forEach((id, i) => { positions.get(id)!.y = slots[i] })
  }
}

/** A string that changes only when node IDs or edges change. */
function topologyKey(nodes: GraphNode[], edges: GraphEdge[]): string {
  return nodes.map(n => n.id).join('|') + '#' + edges.map(e => `${e.from}>${e.to}`).join('|')
}

/**
 * #334: DAG legend strip — explains node shapes and state colors.
 * Shown at the bottom of the DAG view so new users understand the visualization.
 */
function DAGLegend() {
  // Colors come from the same per-health rules as the nodes (DAGView.css), so the
  // legend always matches what the graph shows.
  const legendItems: Array<{ label: string; state: string; desc: string }> = [
    { label: 'Verified', state: 'Verified', desc: 'Deployed and healthy' },
    { label: 'In progress', state: 'Promoting', desc: 'Promoting, waiting for the PR to merge, or checking health' },
    { label: 'Failed', state: 'Failed', desc: 'Failed, or stopped by an alarm' },
    { label: 'Queued', state: 'Pending', desc: 'Not reached yet, or waiting its turn' },
  ]

  return (
    <div className="dag-legend">
      <span className="dag-legend__title">Legend:</span>
      {/* Node type icons */}
      <span title="A stage: one environment's promotion (rounded box)">
        <span className="dag-legend__shape">▭</span>
        Stage
      </span>
      <span title="A gate: a policy check before an environment (gate icon)">
        <Icon name="gate" className="dag-legend__icon" />
        Gate
      </span>
      <span aria-hidden="true" className="dag-legend__divider">│</span>
      {/* State color chips */}
      {legendItems.map(item => (
        <span
          key={item.label}
          title={item.desc}
          className="dag-legend__chip"
          data-health={kardinalStateToHealth(item.state).toLowerCase()}
        >
          {item.label}
        </span>
      ))}
      <span title="With Show blocked on, the gates blocking a version are highlighted" className="dag-legend__highlight">
        <span className="dag-legend__highlight-mark">◆</span>highlighted = blocked gate
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
  const isActive = node.type === 'PromotionStep' && ACTIVE_STATES.has(node.state)
  const elapsed = useElapsedTick(node.startedAt, isActive)

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
      className={nodeClass}
      data-node-id={node.id}
      data-health-state={health}
      role="button"
      tabIndex={0}
      aria-label={`${nodeName(node)} — ${stateLabel(node)}`}
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
        // #346: the focus ring is the box's stroke (DAGView.css, .dag-node:focus)
        // Keyboard users get the same tooltip as mouse users.
        onHoverStart?.(node, (e.currentTarget as SVGGElement).getBoundingClientRect())
      }}
      onBlur={() => onHoverEnd?.()}
    >
      {/* Portal tooltip replaces native SVG <title> (#526). 
          See DAGTooltip rendered by DAGView parent. */}
      <rect
        className="dag-node__box"
        width={NODE_WIDTH}
        height={NODE_HEIGHT}
      />
      <text
        className="dag-node__name"
        x={NODE_WIDTH / 2}
        y={LINE.name}
        textAnchor="middle"
      >
        {nodeName(node)}
      </text>
      {/* A gate carries the gate icon in its corner (inline SVG renders on every OS). */}
      {node.type === 'PolicyGate' && <Icon name="gate" className="dag-node__icon" x={GATE_ICON_INSET} y={GATE_ICON_INSET} />}
      <text
        className="dag-node__state"
        x={NODE_WIDTH / 2}
        y={LINE.state}
        textAnchor="middle"
      >
        {stateLabel(node)}
      </text>
      {/* #330: Elapsed timer — shown for active PromotionStep nodes */}
      {isActive && elapsed && (
        <text
          className="dag-node__elapsed"
          x={NODE_WIDTH / 2}
          y={LINE.elapsed}
          textAnchor="middle"
        >
          {elapsed}
        </text>
      )}
      {/* PR badge — shown when a PR exists; clicking opens the PR in a new tab (#361)
          #748: Use text+onClick instead of <a> to avoid nested-interactive (a[href] inside g[role=button]).
          #762: role="link" removed — it made axe fire nested-interactive (interactive inside interactive).
          The <g> carries aria-label describing the node; PR number is visible in text.
          Keyboard and screen-reader users reach the PR link in the node detail panel
          (Enter on the node) and in the tooltip, which opens on focus. */}
      {showPRBadge && node.prURL && (
        <text
          className="dag-node__pr"
          x={NODE_WIDTH / 2}
          y={isActive && elapsed ? LINE.prUnderTimer : LINE.pr}
          textAnchor="middle"
          onClick={e => {
            e.stopPropagation()
            if (isHttpURL(node.prURL)) window.open(node.prURL, '_blank', 'noopener,noreferrer')
          }}
          aria-hidden="true"
        >
          PR {prNumber}
        </text>
      )}
    </g>
  )
}

export function DAGView({ nodes, edges, loading, error, onRetry, highlightNodeIds, selectedNode, onSelectNode, isStaticTopology }: Props) {
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

  // #521: dagre only re-runs when the topology changes. Each 5-second poll hands
  // us fresh arrays, so the memo is keyed on the node IDs and edges, not on the
  // arrays. The current node data (state, message, PR) is merged in on every render.
  // IMPORTANT: this useMemo must come BEFORE any early returns so that React's
  // hook call count is consistent across all render paths (Rules of Hooks).
  const topoKey = topologyKey(nodes, edges)
  const positions = useMemo(() => computePositions(nodes, edges), [topoKey]) // keyed on topology on purpose
  const layout: LayoutNode[] = nodes.map(node => ({ ...node, ...(positions.get(node.id) ?? { x: 0, y: 0 }) }))

  if (loading) {
    return (
      <div>
        {/* #335: skeleton loading state */}
        <div className="dag-view__skeleton">
          {[1, 2, 3].map(i => (
            <div key={i} className="dag-view__skeleton-row" />
          ))}
        </div>
      </div>
    )
  }
  if (error) {
    return <div className="dag-view__message"><ErrorNotice error={error} onRetry={onRetry} /></div>
  }
  if (nodes.length === 0) {
    return <div className="dag-view__message">No active promotion found.</div>
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
          <span className="dag-static-banner__mark">◦</span>
          The pipeline's environments. No version has shipped yet.
        </div>
      )}
      <div className="dag-view__scroll">
        <svg
          className="dag-view__svg"
          width={svgW}
          height={svgH}
        >
          <defs>
            <marker id="arrowhead" markerWidth="8" markerHeight="6" refX="8" refY="3" orient="auto">
              <polygon className="dag-view__arrowhead" points="0 0, 8 3, 0 6" />
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
                  className="dag-view__edge"
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
