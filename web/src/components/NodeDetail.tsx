// components/NodeDetail.tsx — Detail panel shown when a DAG node is clicked.
import type { GraphNode } from '../types'
import { HealthChip } from './HealthChip'

interface Props {
  node: GraphNode | null
  onClose: () => void
}

export function NodeDetail({ node, onClose }: Props) {
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

      {node.message && (
        <div style={{ fontSize: '0.85rem', color: '#94a3b8', marginBottom: '0.75rem' }}>
          <strong style={{ color: '#cbd5e1' }}>Message:</strong> {node.message}
        </div>
      )}

      {/* PolicyGate: CEL expression section */}
      {node.type === 'PolicyGate' && node.expression && (
        <div style={{ marginBottom: '0.75rem' }}>
          <h4 style={{ fontSize: '0.8rem', color: '#cbd5e1', marginBottom: '0.4rem' }}>CEL Expression</h4>
          <pre style={{
            fontSize: '0.8rem',
            color: '#7dd3fc',
            background: '#0f172a',
            borderRadius: '4px',
            padding: '0.5rem 0.75rem',
            margin: 0,
            overflowX: 'auto',
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-all',
          }}>
            {node.expression}
          </pre>
          <div style={{ marginTop: '0.4rem', fontSize: '0.78rem', color: '#94a3b8' }}>
            Result: <span style={{ color: node.state === 'Pass' ? '#22c55e' : node.state === 'Fail' ? '#ef4444' : '#94a3b8', fontWeight: 600 }}>
              {node.state}
            </span>
          </div>
        </div>
      )}

      {/* PolicyGate: last evaluated timestamp */}
      {node.type === 'PolicyGate' && node.lastEvaluatedAt && (
        <div style={{ fontSize: '0.78rem', color: '#64748b', marginBottom: '0.75rem' }}>
          Last evaluated: {new Date(node.lastEvaluatedAt).toLocaleString()}
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
