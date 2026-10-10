// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/PipelineLaneView.tsx — Horizontal pipeline stage lane view.
// Shows environments as cards in a horizontal strip: env name, state chip,
// bundle info, and promote/rollback quick actions.
// Part of #332 (complete visual redesign — Kargo-parity pipeline lane view).
//
// #532: State-driven visual properties use CSS classes (stage-card--{state}).
//
// Promote shows only where it can do something: every upstream environment is
// Verified and this one is not reached yet, failed, or stopped by an alarm.
// Rollback shows on Verified environments. The rule lives in pipelineActions.ts
// so NodeDetail offers the same actions. Both ask first (PipelineActionDialog),
// show the result in the lane, and ask the parent to refresh.
//
// Adapted from Kargo's horizontal stage cards pattern.
// Each card represents a PromotionStep DAG node. Environments at the same depth
// (a wave, or parallel dependsOn) share one column, so the lane does not read
// as a chain (#1580); a column of WAVE_PLATE_MIN or more is one wave card that
// counts its states and expands to the full list.
import { useId, useMemo, useState, type CSSProperties } from 'react'
import type { EnvironmentHold, GraphEdge, GraphNode } from '../types'
import { HealthChip, kardinalStateToHealth } from './HealthChip'
import { PipelineActionDialog, type PipelineActionKind } from './PipelineActionDialog'
import { canPromote, canRollback, upstreamSteps } from '../pipelineActions'
import { groupByDepth, WAVE_PLATE_MIN } from '../fleetModel'
import '../styles/PipelineLaneView.css'

interface Props {
  /** DAG nodes — only PromotionStep nodes are rendered as stage cards. */
  nodes: GraphNode[]
  /** DAG edges — used to find each environment's upstream environments. */
  edges?: GraphEdge[]
  /** Currently selected node (highlighted card). */
  selectedNode?: GraphNode | null
  /** Called when a stage card is clicked. */
  onSelectNode?: (node: GraphNode | null) => void
  /** Active bundle name for display. */
  activeBundleName?: string
  /** Pipeline the actions apply to. Without it the lane shows no actions. */
  pipelineName?: string
  /** Namespace of the pipeline. */
  namespace?: string
  /** Called after a promote or rollback request succeeds, so the parent can refresh. */
  onActionDone?: () => void
  /** Held environments (spec.holds, #1528) by name: shown as a Held badge with a release action. */
  holds?: Record<string, EnvironmentHold>
  loading?: boolean
}

/** CSS class modifier for a given stage state. */
function stageStateClass(state: string): string {
  const health = kardinalStateToHealth(state)
  return `stage-card--${health.toLowerCase()}`
}

/** Color scheme kept for inline uses that can't use CSS (e.g. box-shadow). */
function stageAccentColor(state: string): string {
  const health = kardinalStateToHealth(state)
  switch (health) {
    case 'Ready':       return 'var(--color-success)'
    case 'Error':       return 'var(--color-error)'
    case 'Degraded':    return 'var(--color-degraded)'
    case 'Reconciling': return 'var(--color-warning)'
    default:            return 'var(--color-text-muted)'
  }
}

/**
 * The PromotionStep nodes grouped by depth (longest path from a root through
 * the graph's edges, gates looked through), in the graph's order. Without
 * edges, one stage per node in the graph's order, as before.
 */
export function laneStages(nodes: GraphNode[], edges: GraphEdge[]): GraphNode[][] {
  const steps = nodes.filter(n => n.type === 'PromotionStep')
  if (edges.length === 0) return steps.map(n => [n])
  const byId = new Map(steps.map(n => [n.id, n]))
  const ups = new Map(steps.map(n => [n.id, upstreamSteps(n, nodes, edges).map(u => u.id)]))
  return groupByDepth(steps.map(n => n.id), ups).map(g => g.map(id => byId.get(id)!))
}

/** Health states in the order a wave card lists them, most urgent first. */
const WAVE_HEALTH_ORDER = ['Error', 'Degraded', 'Reconciling', 'Pending', 'Ready', 'Unknown'] as const
const WAVE_HEALTH_LABEL: Record<string, string> = {
  Error: 'failed', Degraded: 'degraded', Reconciling: 'in progress', Pending: 'not started', Ready: 'verified', Unknown: 'unknown',
}

/** Counts of a wave's nodes by health state, most urgent first. */
export function waveCounts(nodes: GraphNode[]): Array<{ health: string; count: number }> {
  const by = new Map<string, number>()
  for (const n of nodes) {
    const h = kardinalStateToHealth(n.state)
    by.set(h, (by.get(h) ?? 0) + 1)
  }
  return WAVE_HEALTH_ORDER.filter(h => by.has(h)).map(h => ({ health: h, count: by.get(h)! }))
}

const ACTION_BUTTON: CSSProperties = {
  fontSize: '0.65rem',
  background: 'var(--color-surface-2)',
  border: '1px solid var(--color-border)',
  borderRadius: '3px',
  padding: '1px 5px',
  cursor: 'pointer',
}

export function PipelineLaneView({
  nodes,
  edges = [],
  selectedNode,
  onSelectNode,
  activeBundleName,
  pipelineName,
  namespace = 'default',
  onActionDone,
  holds = {},
  loading,
}: Props) {
  const [pending, setPending] = useState<{ kind: PipelineActionKind; environment: string } | null>(null)
  const [result, setResult] = useState<string | null>(null)

  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())

  const waveId = useId()
  // Only PromotionStep nodes (not PolicyGates) are stages, one column per
  // depth. Polls bring new arrays, so key on the topology and states.
  const stages = useMemo(() => laneStages(nodes, edges), [nodes, edges])

  if (loading || stages.length === 0) {
    return null
  }

  const renderCard = (node: GraphNode) => {
    const isSelected = selectedNode?.id === node.id
    const accent = stageAccentColor(node.state)
    const showPromote = !!pipelineName && canPromote(node, nodes, edges)
    const hold = holds[node.environment]
    // A held environment is already on a rollback: release it first.
    const showRollback = !!pipelineName && canRollback(node) && !hold
    const showPRLink = node.prURL && node.state === 'WaitingForMerge'
    const cardClass = [
      'stage-card',
      stageStateClass(node.state),
      isSelected ? 'stage-card--selected' : '',
    ].filter(Boolean).join(' ')

    // Stage card — uses CSS class for state-driven colors.
    // #758: Card is a non-interactive div (mouse click only).
    // A visually-hidden button provides keyboard selection.
    return (
      <div
        key={node.id}
        data-health-state={kardinalStateToHealth(node.state)}
        onClick={() => onSelectNode?.(isSelected ? null : node)}
        className={cardClass}
        style={{
          display: 'flex',
          flexDirection: 'column',
          gap: '0.3rem',
          boxShadow: isSelected ? `0 0 0 1px ${accent}` : 'none',
          outline: 'none',
          position: 'relative',
        }}
      >
        {/* Visually-hidden select button — keyboard accessibility (#758).
            Position absolute, covers the card, z-index 0 so visible content renders above. */}
        <button
          aria-label={isSelected ? `Deselect ${node.environment}` : `Select ${node.environment}`}
          title={[node.environment, activeBundleName, node.message,
            hold && `Held on ${hold.bundle} by ${hold.createdBy || 'unknown'}: ${hold.reason}` +
              (hold.expiresAt ? ` (until ${hold.expiresAt})` : '')].filter(Boolean).join('\n')}
          aria-pressed={isSelected}
          onClick={e => { e.stopPropagation(); onSelectNode?.(isSelected ? null : node) }}
          style={{
            position: 'absolute',
            inset: 0,
            opacity: 0,
            cursor: 'pointer',
            border: 'none',
            background: 'none',
            zIndex: 0,
          }}
        />
        {/* Card content sits above the invisible button */}
        {/* The content is drawn over the select button but lets the pointer
            through to it (#1580 QA: it intercepted real clicks), except the
            PR link and the action buttons, which take their own clicks. The
            button's title carries the tooltips of the truncated lines. */}
        <div className="stage-card__content" style={{ position: 'relative', zIndex: 1, pointerEvents: 'none' }}>
        {/* Environment name + state chip */}
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.3rem' }}>
          <span style={{
            fontSize: '0.78rem',
            fontWeight: 700,
            color: 'var(--color-text)',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}>
            {node.environment}
          </span>
          <HealthChip state={node.state} size="sm" />
        </div>

        {/* Active bundle name (truncated) */}
        {activeBundleName && (
          <div style={{
            fontSize: '0.65rem',
            fontFamily: 'monospace',
            color: 'var(--color-text-muted)',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
          title={activeBundleName}>
            {activeBundleName.length > 18 ? activeBundleName.slice(-16) : activeBundleName}
          </div>
        )}

        {/* PR link when waiting for merge */}
        {showPRLink && (
          <a
            href={node.prURL!}
            target="_blank"
            rel="noopener noreferrer"
            onClick={e => e.stopPropagation()}
            style={{
              pointerEvents: 'auto',
              fontSize: '0.65rem',
              color: 'var(--color-accent)',
              textDecoration: 'none',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}
          >
            PR → merge to deploy ↗
          </a>
        )}

        {/* Step message (truncated) */}
        {node.message && !showPRLink && (
          <div style={{
            fontSize: '0.65rem',
            color: 'var(--color-text-muted)',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
          title={node.message}>
            {node.message.length > 30 ? node.message.slice(0, 28) + '…' : node.message}
          </div>
        )}

        {/* Held on a rollback (#1528): who, why, and the release action */}
        {hold && (
          <div className="stage-card__hold"
            title={`Held on ${hold.bundle} by ${hold.createdBy || 'unknown'}: ${hold.reason}` +
              (hold.expiresAt ? ` (until ${hold.expiresAt})` : '')}>
            <span className="stage-card__hold-badge">Held</span>
            <span className="stage-card__hold-reason">{hold.reason}</span>
            {hold.bundleMissing && (
              <span className="stage-card__hold-missing" role="alert">
                Rollback Bundle {hold.bundle} does not exist; the hold stays in effect.
                {hold.releaseCommand && <> Release with <code>{hold.releaseCommand}</code>.</>}
              </span>
            )}
          </div>
        )}

        {/* Action buttons row */}
        {(showPromote || showRollback || (hold && !!pipelineName)) && (
          <div style={{ display: 'flex', gap: '0.25rem', marginTop: '0.1rem', pointerEvents: 'auto' }}>
            {showPromote && (
              <button
                type="button"
                title={`Promote ${pipelineName} to ${node.environment}`}
                aria-label={`Promote to ${node.environment}`}
                onClick={e => { e.stopPropagation(); setResult(null); setPending({ kind: 'promote', environment: node.environment }) }}
                style={{ ...ACTION_BUTTON, color: 'var(--color-accent)' }}
              >
                <span aria-hidden="true">▶</span> Promote
              </button>
            )}
            {showRollback && (
              <button
                type="button"
                title={`Roll back ${node.environment} to the previous verified version`}
                aria-label={`Roll back ${node.environment}`}
                onClick={e => { e.stopPropagation(); setResult(null); setPending({ kind: 'rollback', environment: node.environment }) }}
                style={{ ...ACTION_BUTTON, color: 'var(--color-error)' }}
              >
                <span aria-hidden="true">↩</span> Roll back
              </button>
            )}
            {hold && pipelineName && (
              <button
                type="button"
                title={`Release the hold on ${node.environment}`}
                aria-label={`Release the hold on ${node.environment}`}
                onClick={e => { e.stopPropagation(); setResult(null); setPending({ kind: 'release-hold', environment: node.environment }) }}
                style={{ ...ACTION_BUTTON, color: 'var(--color-warning)' }}
              >
                Release hold
              </button>
            )}
          </div>
        )}
      </div>
      </div>
    )
  }


  return (
    <>
    <div
      role="group"
      aria-label="Pipeline stages"
      style={{
      display: 'flex',
      gap: '0.5rem',
      padding: '0.75rem 1.5rem',
      overflowX: 'auto',
      background: 'var(--color-bg-deep)',
      borderBottom: '1px solid var(--color-border-muted)',
      alignItems: 'stretch',
      minHeight: '100px',
      // Keep the lane its own height: an expanded wave must not be squeezed
      // (and clipped) by the DAG below it.
      flexShrink: 0,
    }}>
      {stages.map((stage, idx) => {
        const key = stage.map(n => n.id).join(',')
        const wave = stage.length >= WAVE_PLATE_MIN
        const open = expanded.has(key)
        // A collapsed wave says which of its environments is selected.
        const selectedHere = wave && !open ? stage.find(n => n.id === selectedNode?.id) : undefined
        const listId = `${waveId}-wave-${idx}`
        return (
          <div key={key} style={{ display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
            {/* Connector line between stages */}
            {idx > 0 && (
              <div style={{
                width: '20px',
                height: '2px',
                background: 'var(--color-surface)',
                flexShrink: 0,
              }} />
            )}
            {stage.length === 1 ? renderCard(stage[0]) : (
              <div
                role="group"
                aria-label={wave ? `${stage.length} environments: ${stage[0].environment} to ${stage[stage.length - 1].environment}` : `Parallel: ${stage.map(n => n.environment).join(', ')}`}
                className={[wave ? 'stage-wave' : 'stage-parallel', selectedHere ? 'stage-wave--selected' : ''].filter(Boolean).join(' ')}
              >
                {wave && (
                  <div className="stage-wave__head">
                    <span className="stage-wave__title">{stage.length} environments</span>
                    <span className="stage-wave__range">{stage[0].environment} … {stage[stage.length - 1].environment}</span>
                    <span className="stage-wave__counts">
                      {waveCounts(stage).map(c => (
                        <span key={c.health} className={`stage-wave__count stage-card--${c.health.toLowerCase()}`}>
                          {c.count} {WAVE_HEALTH_LABEL[c.health]}
                        </span>
                      ))}
                    </span>
                    <button
                      type="button"
                      className="stage-wave__toggle"
                      aria-expanded={open}
                      aria-controls={listId}
                      onClick={() => setExpanded(prev => {
                        const next = new Set(prev)
                        if (next.has(key)) next.delete(key)
                        else next.add(key)
                        return next
                      })}
                    >
                      {open ? 'Hide environments' : 'Show environments'}
                    </button>
                    {selectedHere && (
                      <span className="stage-wave__selected">Selected: {selectedHere.environment}</span>
                    )}
                  </div>
                )}
                {(!wave || open) && (
                  <div id={wave ? listId : undefined} className={wave ? 'stage-wave__list' : 'stage-parallel__list'}>
                    {stage.map(renderCard)}
                  </div>
                )}
              </div>
            )}
          </div>
        )
      })}
    </div>
    {result && (
      <div role="status" style={{ padding: '0.35rem 1.5rem', fontSize: '0.75rem', color: 'var(--color-success)' }}>
        {result}
      </div>
    )}
    {pending && pipelineName && (
      <PipelineActionDialog
        kind={pending.kind}
        pipelineName={pipelineName}
        environment={pending.environment}
        namespace={namespace}
        onDone={message => {
          setPending(null)
          setResult(message)
          onActionDone?.()
        }}
        onCancel={() => setPending(null)}
      />
    )}
    </>
  )
}
