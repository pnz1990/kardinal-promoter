// components/PipelineLaneView.tsx — Horizontal pipeline stage lane view.
// Shows environments as cards in a horizontal strip: env name, state chip,
// bundle info, and promote/rollback quick actions.
// Part of #332 (complete visual redesign — Kargo-parity pipeline lane view).
//
// Adapted from Kargo's horizontal stage cards pattern.
// Each card represents a PromotionStep DAG node.
import type { GraphNode } from '../types'
import { HealthChip, kardinalStateToHealth } from './HealthChip'

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

/** Color scheme for a given stage state. */
function stageColors(state: string): { bg: string; border: string; accent: string } {
  const health = kardinalStateToHealth(state)
  switch (health) {
    case 'Ready':
      return { bg: '#052e16', border: '#16a34a', accent: '#22c55e' }
    case 'Error':
    case 'Degraded':
      return { bg: '#2d0b0b', border: '#b91c1c', accent: '#ef4444' }
    case 'Reconciling':
      return { bg: '#1e1b4b', border: '#4338ca', accent: '#6366f1' }
    case 'Pending':
      return { bg: '#0f172a', border: '#334155', accent: '#94a3b8' }
    default:
      return { bg: '#0f172a', border: '#1e293b', accent: '#475569' }
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
    <div style={{
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
        const colors = stageColors(node.state)
        const isPending = node.state === 'Pending' || node.state === 'Unknown'
        const hasAction = !isPending && onPromote
        const showPRLink = node.prURL && node.state === 'WaitingForMerge'

        return (
          <div key={node.id} style={{ display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
            {/* Connector line between stages */}
            {idx > 0 && (
              <div style={{
                width: '20px',
                height: '2px',
                background: '#1e293b',
                flexShrink: 0,
              }} />
            )}

            {/* Stage card */}
            <div
              role="button"
              tabIndex={0}
              aria-selected={isSelected}
              onClick={() => onSelectNode?.(isSelected ? null : node)}
              onKeyDown={e => (e.key === 'Enter' || e.key === ' ') && onSelectNode?.(isSelected ? null : node)}
              style={{
                background: colors.bg,
                border: `1.5px solid ${isSelected ? colors.accent : colors.border}`,
                borderRadius: '6px',
                padding: '0.6rem 0.75rem',
                cursor: 'pointer',
                minWidth: '140px',
                maxWidth: '180px',
                display: 'flex',
                flexDirection: 'column',
                gap: '0.3rem',
                transition: 'border-color 0.15s',
                boxShadow: isSelected ? `0 0 0 1px ${colors.accent}` : 'none',
                outline: 'none',
              }}
            >
              {/* Environment name + state chip */}
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.3rem' }}>
                <span style={{
                  fontSize: '0.78rem',
                  fontWeight: 700,
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
                  color: '#64748b',
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
                    color: '#6366f1',
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
                  color: '#94a3b8',
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
                      background: '#1e293b',
                      color: '#6366f1',
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
                        background: '#1e293b',
                        color: '#94a3b8',
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
        )
      })}
    </div>
  )
}
