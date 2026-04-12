// components/NodeDetail.tsx — Detail panel shown when a DAG node is clicked.
// For PolicyGate nodes, shows CEL expression and last evaluated timestamp
// when provided by the graph API (expression / lastEvaluatedAt fields).
import type { GraphNode } from '../types'
import { HealthChip } from './HealthChip'

interface Props {
  node: GraphNode | null
  onClose: () => void
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

export function NodeDetail({ node, onClose }: Props) {
  if (!node) return null

  const isPolicyGate = node.type === 'PolicyGate'

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

      {node.outputs && Object.keys(node.outputs).length > 0 && (
        <div style={{ marginBottom: '0.75rem' }}>
          <h4 style={{ fontSize: '0.8rem', color: '#cbd5e1', marginBottom: '0.4rem' }}>Step Outputs</h4>
          {Object.entries(node.outputs).map(([k, v]) => (
            <div key={k} style={{ fontSize: '0.8rem', color: '#94a3b8' }}>
              <span style={{ color: '#7dd3fc' }}>{k}</span>: {v}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
