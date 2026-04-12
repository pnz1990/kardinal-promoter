// components/NodeDetail.tsx — Detail panel shown when a DAG node is clicked.
// For PolicyGate nodes: shows CEL expression and last evaluated timestamp.
// For PromotionStep nodes: shows step-by-step execution context (Kargo parity).
import { useState, useEffect } from 'react'
import type { GraphNode, PromotionStep } from '../types'
import { HealthChip, kardinalStateToHealth } from './HealthChip'
import { api } from '../api/client'

interface Props {
  node: GraphNode | null
  onClose: () => void
  /** Bundle name — needed to fetch detailed step data. */
  bundleName?: string
  /** Pipeline name — needed for the promote action. */
  pipelineName?: string
  /** Namespace of the pipeline. Defaults to 'default'. */
  namespace?: string
}

/** Format an ISO timestamp to a human-readable string. */
function formatTimestamp(iso: string): string {
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return iso
    const now = Date.now()
    const diffMs = now - d.getTime()
    const diffSec = Math.floor(diffMs / 1000)
    if (diffSec < 60) return `${diffSec}s ago`
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`
    return d.toLocaleString()
  } catch {
    return iso
  }
}

/** List of promotion sub-step types in execution order. */
const STEP_SEQUENCE = [
  'git-clone',
  'kustomize-set-image',
  'helm-set-image',
  'git-commit',
  'git-push',
  'open-pr',
  'wait-for-merge',
  'health-check',
]

/** Returns an icon for a sub-step given the current state and currentStepIndex. */
function stepIcon(index: number, currentIndex: number, stepState: string): string {
  if (index < currentIndex) return '✓'
  if (index === currentIndex) {
    const health = kardinalStateToHealth(stepState)
    if (health === 'Error') return '✗'
    if (health === 'Reconciling') return '▶'
    return '○'
  }
  return '○'
}

function stepIconColor(index: number, currentIndex: number, stepState: string): string {
  if (index < currentIndex) return '#22c55e'
  if (index === currentIndex) {
    const health = kardinalStateToHealth(stepState)
    if (health === 'Error') return '#ef4444'
    if (health === 'Reconciling') return '#f59e0b'
    return '#94a3b8'
  }
  return '#334155'
}

/** Step progress panel for PromotionStep nodes. */
function StepProgress({ step }: { step: PromotionStep }) {
  const currentIndex = step.currentStepIndex ?? 0
  const state = step.state

  return (
    <div style={{ marginBottom: '0.75rem' }}>
      <h4 style={{ fontSize: '0.8rem', color: '#cbd5e1', marginBottom: '0.5rem' }}>
        Promotion Steps
      </h4>
      <div style={{
        background: '#0f172a',
        border: '1px solid #1e293b',
        borderRadius: '4px',
        padding: '0.5rem 0.75rem',
      }}>
        {STEP_SEQUENCE.map((stepType, i) => (
          <div
            key={stepType}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '0.5rem',
              marginBottom: i < STEP_SEQUENCE.length - 1 ? '0.3rem' : 0,
              opacity: i > currentIndex ? 0.4 : 1,
            }}
          >
            <span style={{
              fontSize: '0.7rem',
              color: stepIconColor(i, currentIndex, state),
              width: '12px',
              flexShrink: 0,
            }}>
              {stepIcon(i, currentIndex, state)}
            </span>
            <span style={{
              fontSize: '0.75rem',
              color: i === currentIndex ? '#e2e8f0' : '#64748b',
              fontFamily: 'monospace',
              fontWeight: i === currentIndex ? 600 : 400,
            }}>
              {stepType}
            </span>
            {i === currentIndex && state === 'WaitingForMerge' && (
              <span style={{ fontSize: '0.65rem', color: '#f59e0b' }}>waiting</span>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

export function NodeDetail({ node, onClose, bundleName, pipelineName, namespace = 'default' }: Props) {
  const [stepDetail, setStepDetail] = useState<PromotionStep | null>(null)
  const [stepLoading, setStepLoading] = useState(false)
  const [promoteState, setPromoteState] = useState<'idle' | 'loading' | 'success' | 'error'>('idle')
  const [promoteMessage, setPromoteMessage] = useState<string | null>(null)

  const isPolicyGate = node?.type === 'PolicyGate'
  const isPromotionStep = node?.type === 'PromotionStep'
  const isActiveState = node && ['Running', 'Promoting', 'WaitingForMerge', 'HealthChecking'].includes(node.state)

  /** Trigger a new promotion for this environment. */
  function handlePromote() {
    if (!pipelineName || !node?.environment) return
    setPromoteState('loading')
    setPromoteMessage(null)
    api.promote(pipelineName, node.environment, namespace)
      .then(res => {
        setPromoteState('success')
        setPromoteMessage(`Bundle ${res.bundle} created`)
      })
      .catch((err: unknown) => {
        setPromoteState('error')
        setPromoteMessage(err instanceof Error ? err.message : 'Promote failed')
      })
  }

  // Fetch step detail when a PromotionStep node is selected and bundle is known.
  useEffect(() => {
    if (!node || !isPromotionStep || !bundleName) {
      setStepDetail(null)
      return
    }

    setStepLoading(true)
    api.getSteps(bundleName)
      .then(steps => {
        const match = steps.find(s => s.environment === node.environment)
        setStepDetail(match ?? null)
      })
      .catch(() => setStepDetail(null))
      .finally(() => setStepLoading(false))
  }, [node?.id, bundleName, isPromotionStep])

  // Auto-refresh step detail when node is in active state.
  useEffect(() => {
    if (!isActiveState || !bundleName || !isPromotionStep) return
    const id = setInterval(() => {
      api.getSteps(bundleName)
        .then(steps => {
          const match = steps.find(s => s.environment === node!.environment)
          setStepDetail(match ?? null)
        })
        .catch(() => {/* ignore */})
    }, 3000)
    return () => clearInterval(id)
  }, [isActiveState, bundleName, isPromotionStep, node?.id])

  if (!node) return null

  return (
    <div style={{
      position: 'fixed',
      right: 0,
      top: 0,
      bottom: 0,
      width: '340px',
      background: '#1e293b',
      borderLeft: '1px solid #334155',
      padding: '1.5rem',
      overflowY: 'auto',
      zIndex: 1000,
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '1rem' }}>
        <h3 style={{ fontSize: '1rem', fontWeight: 600 }}>{node.label}</h3>
        <button
          onClick={onClose}
          style={{
            background: 'none',
            border: 'none',
            color: '#94a3b8',
            cursor: 'pointer',
            fontSize: '1.25rem',
            lineHeight: 1,
          }}
          aria-label="Close"
        >
          ×
        </button>
      </div>

      <div style={{ marginBottom: '0.75rem' }}>
        <HealthChip state={node.state} nodeType={node.type} size="md" />
      </div>

      <div style={{ fontSize: '0.85rem', color: '#94a3b8', marginBottom: '0.5rem' }}>
        <strong style={{ color: '#cbd5e1' }}>Type:</strong> {node.type}
      </div>
      <div style={{ fontSize: '0.85rem', color: '#94a3b8', marginBottom: '0.5rem' }}>
        <strong style={{ color: '#cbd5e1' }}>Environment:</strong> {node.environment}
      </div>

      {/* Promote button — shown on PromotionStep nodes when a pipeline is known */}
      {isPromotionStep && pipelineName && node.environment && (
        <div style={{ marginBottom: '0.75rem' }}>
          <button
            onClick={handlePromote}
            disabled={promoteState === 'loading'}
            title={`Promote ${pipelineName} to ${node.environment}`}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '0.4rem',
              padding: '0.35rem 0.8rem',
              background: promoteState === 'success' ? '#166534' : promoteState === 'error' ? '#7f1d1d' : '#4f46e5',
              color: '#fff',
              border: 'none',
              borderRadius: '4px',
              cursor: promoteState === 'loading' ? 'wait' : 'pointer',
              fontSize: '0.8rem',
              fontWeight: 500,
              opacity: promoteState === 'loading' ? 0.7 : 1,
            }}
          >
            <span>▶</span>
            <span>
              {promoteState === 'loading' ? 'Promoting…'
                : promoteState === 'success' ? 'Promoted!'
                : promoteState === 'error' ? 'Failed'
                : `Promote to ${node.environment}`}
            </span>
          </button>
          {promoteMessage && (
            <div style={{
              marginTop: '0.3rem',
              fontSize: '0.7rem',
              color: promoteState === 'error' ? '#fca5a5' : '#86efac',
            }}>
              {promoteMessage}
            </div>
          )}
        </div>
      )}

      {/* PromotionStep: step progress log */}
      {isPromotionStep && stepLoading && (
        <div style={{ fontSize: '0.8rem', color: '#475569', marginBottom: '0.75rem', fontStyle: 'italic' }}>
          Loading step details...
        </div>
      )}
      {isPromotionStep && !stepLoading && stepDetail && (
        <StepProgress step={stepDetail} />
      )}
      {isPromotionStep && !stepLoading && !stepDetail && !bundleName && (
        <div style={{ fontSize: '0.8rem', color: '#475569', marginBottom: '0.75rem', fontStyle: 'italic' }}>
          Step sequence available when promotion is active.
        </div>
      )}

      {/* PolicyGate: CEL expression display */}
      {isPolicyGate && node.expression && (
        <div style={{ marginBottom: '0.75rem' }}>
          <h4 style={{ fontSize: '0.8rem', color: '#cbd5e1', marginBottom: '0.4rem' }}>
            CEL Expression
          </h4>
          <code style={{
            display: 'block',
            background: '#0f172a',
            border: '1px solid #334155',
            borderRadius: '4px',
            padding: '0.5rem 0.75rem',
            fontSize: '0.8rem',
            color: '#7dd3fc',
            fontFamily: 'monospace',
            wordBreak: 'break-all',
            whiteSpace: 'pre-wrap',
          }}>
            {node.expression}
          </code>
        </div>
      )}

      {/* PolicyGate: last evaluated timestamp */}
      {isPolicyGate && node.lastEvaluatedAt && (
        <div style={{ fontSize: '0.8rem', color: '#94a3b8', marginBottom: '0.5rem' }}>
          <strong style={{ color: '#cbd5e1' }}>Last evaluated:</strong>{' '}
          <span title={node.lastEvaluatedAt}>
            {formatTimestamp(node.lastEvaluatedAt)}
          </span>
        </div>
      )}

      {/* PolicyGate: placeholder when expression is not yet populated */}
      {isPolicyGate && !node.expression && (
        <div style={{ fontSize: '0.8rem', color: '#475569', marginBottom: '0.75rem', fontStyle: 'italic' }}>
          CEL expression will appear here when the graph API populates it.
        </div>
      )}

      {node.message && (
        <div style={{ fontSize: '0.85rem', color: '#94a3b8', marginBottom: '0.75rem' }}>
          <strong style={{ color: '#cbd5e1' }}>Message:</strong> {node.message}
        </div>
      )}

      {node.prURL && (
        <div style={{ marginBottom: '0.75rem' }}>
          <a
            href={node.prURL}
            target="_blank"
            rel="noopener noreferrer"
            style={{ color: '#6366f1', fontSize: '0.85rem' }}
          >
            View Pull Request ↗
          </a>
        </div>
      )}

      {/* PromotionStep outputs from graph node */}
      {node.outputs && Object.keys(node.outputs).length > 0 && (
        <div style={{ marginBottom: '0.75rem' }}>
          <h4 style={{ fontSize: '0.8rem', color: '#cbd5e1', marginBottom: '0.4rem' }}>Step Outputs</h4>
          {Object.entries(node.outputs).map(([k, v]) => (
            <div key={k} style={{ fontSize: '0.8rem', color: '#94a3b8', marginBottom: '0.2rem' }}>
              <span style={{ color: '#7dd3fc' }}>{k}</span>:{' '}
              {k.toLowerCase().includes('url') ? (
                <a href={v} target="_blank" rel="noopener noreferrer" style={{ color: '#6366f1' }}>
                  {v.length > 40 ? v.slice(0, 37) + '…' : v}
                </a>
              ) : (
                <span style={{ fontFamily: 'monospace' }}>{v}</span>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
