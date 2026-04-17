// components/PipelineLaneView.tsx — Horizontal pipeline stage lane view.
// Shows environments as cards in a horizontal strip: env name, state chip,
// bundle info, and promote/rollback quick actions.
// Part of #332 (complete visual redesign — Kargo-parity pipeline lane view).
//
// #532: State-driven visual properties use CSS classes (stage-card--{state}).
//
// Adapted from Kargo's horizontal stage cards pattern.
// Each card represents a PromotionStep DAG node.
import type { GraphNode } from '../types'
import { HealthChip, kardinalStateToHealth } from './HealthChip'
import '../styles/PipelineLaneView.css'

interface Props {
  /** DAG nodes — only PromotionStep nodes are rendered as stage cards. */
  nodes: GraphNode[]
  /** Currently selected node (highlighted card). */
  selectedNode?: GraphNode | null
  /** Called when a stage card is clicked. */
  onSelectNode?: (node: GraphNode | null) => void
  /** Active bundle name for display. */
  activeBundleName?: string
  /** Pipeline name for action buttons. */
  pipelineName?: string
  /** Called when Promote button is clicked for an environment. */
  onPromote?: (environment: string) => void
  /** Called when Rollback button is clicked for an environment. */
  onRollback?: (environment: string) => void
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
    case 'Error':
    case 'Degraded':    return '#ef4444'
    case 'Reconciling': return 'var(--color-accent)'
    default:            return 'var(--color-text-muted)'
  }
}

export function PipelineLaneView({
  nodes,
  selectedNode,
  onSelectNode,
  activeBundleName,
  onPromote,
  onRollback,
  loading,
}: Props) {
  // Only show PromotionStep nodes (not PolicyGates) in the lane view.
  const stageNodes = nodes.filter(n => n.type === 'PromotionStep')

  if (loading || stageNodes.length === 0) {
    return null
  }

  return (
    <div
      role="group"
      aria-label="Pipeline stages"
      style={{
      display: 'flex',
      gap: '0.5rem',
      padding: '0.75rem 1.5rem',
      overflowX: 'auto',
      background: '#070f1b',
      borderBottom: '1px solid #1e293b',
      alignItems: 'stretch',
      minHeight: '100px',
    }}>
      {stageNodes.map((node, idx) => {
        const isSelected = selectedNode?.id === node.id
        const accent = stageAccentColor(node.state)
        const isPending = node.state === 'Pending' || node.state === 'Unknown'
        const hasAction = !isPending && onPromote
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
                  // Stage cards always have dark backgrounds — var(--color-text) flips to
                  // dark (#1e293b) in light mode causing contrast failure. Use hardcoded
                  // light color. #e2e8f0 matches the CSS .stage-card base rule.
                  color: '#e2e8f0',
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
                  // Stage card backgrounds are always dark — #64748b (slate-500) fails
                  // on #052e16 (ready bg). Use #94a3b8 (slate-400, 7.1:1 on dark bg) ✓
                  color: '#94a3b8',
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
                    // Stage card backgrounds are always dark — var(--color-accent) flips to
                    // #4f46e5 (indigo-600) in light mode which fails on dark card bg.
                    // Use hardcoded #a5b4fc (indigo-300, 9.5:1 on #1e1b4b) ✓
                    color: '#a5b4fc',
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

              {/* Action buttons row */}
              {hasAction && (
                <div style={{ display: 'flex', gap: '0.25rem', marginTop: '0.1rem' }}>
                  <button
                    title={`Promote ${node.environment}`}
                    onClick={e => { e.stopPropagation(); onPromote?.(node.environment) }}
                    style={{
                      fontSize: '0.6rem',
                      background: 'var(--color-surface)',
                      color: 'var(--color-accent)',
                      border: '1px solid #334155',
                      borderRadius: '3px',
                      padding: '1px 5px',
                      cursor: 'pointer',
                    }}
                  >
                    ▶ Promote
                  </button>
                  {onRollback && node.state === 'Verified' && (
                    <button
                      title={`Rollback ${node.environment}`}
                      onClick={e => { e.stopPropagation(); onRollback?.(node.environment) }}
                      style={{
                        fontSize: '0.6rem',
                        background: 'var(--color-surface)',
                        color: 'var(--color-text-muted)',
                        border: '1px solid #334155',
                        borderRadius: '3px',
                        padding: '1px 5px',
                        cursor: 'pointer',
                      }}
                    >
                      ↩ Rollback
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
  )
}
