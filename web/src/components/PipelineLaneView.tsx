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
// Each card represents a PromotionStep DAG node.
import { useState, type CSSProperties } from 'react'
import type { EnvironmentHold, GraphEdge, GraphNode } from '../types'
import { HealthChip, kardinalStateToHealth } from './HealthChip'
import { PipelineActionDialog, type PipelineActionKind } from './PipelineActionDialog'
import { canPromote, canRollback } from '../pipelineActions'
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

  // Only show PromotionStep nodes (not PolicyGates) in the lane view.
  const stageNodes = nodes.filter(n => n.type === 'PromotionStep')

  if (loading || stageNodes.length === 0) {
    return null
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
    }}>
      {stageNodes.map((node, idx) => {
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

        return (
          <div key={node.id} style={{ display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
            {/* Connector line between stages */}
            {idx > 0 && (
              <div style={{
                width: '20px',
                height: '2px',
                background: 'var(--color-surface)',
                flexShrink: 0,
              }} />
            )}

            {/* Stage card — uses CSS class for state-driven colors.
                #758: Card is a non-interactive div (mouse click only).
                A visually-hidden button provides keyboard selection. */}
            <div

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
              <div style={{ position: 'relative', zIndex: 1 }}>
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
                <div style={{ display: 'flex', gap: '0.25rem', marginTop: '0.1rem' }}>
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
