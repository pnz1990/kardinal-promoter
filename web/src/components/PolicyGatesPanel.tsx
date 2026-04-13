// components/PolicyGatesPanel.tsx — Collapsible panel showing all active PolicyGates.
// Wires up api.listGates() to display gate state with CEL expressions (#340).
import { useState } from 'react'
import type { PolicyGate } from '../types'
import { HealthChip } from './HealthChip'

interface Props {
  gates: PolicyGate[]
  loading?: boolean
}

function formatAge(iso: string | undefined): string {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return '—'
    const diffSec = Math.floor((Date.now() - d.getTime()) / 1000)
    if (diffSec < 60) return `${diffSec}s ago`
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
    return `${Math.floor(diffSec / 3600)}h ago`
  } catch {
    return '—'
  }
}

/** Collapsed summary chip: X blocked / Y total */
function GateSummaryChip({ gates }: { gates: PolicyGate[] }) {
  const blocked = gates.filter(g => !g.ready).length
  const total = gates.length
  if (total === 0) return null
  return (
    <span style={{
      fontSize: '0.65rem',
      background: blocked > 0 ? '#7f1d1d' : '#14532d',
      color: blocked > 0 ? '#fca5a5' : '#86efac',
      border: `1px solid ${blocked > 0 ? '#dc2626' : '#16a34a'}`,
      borderRadius: '4px',
      padding: '1px 6px',
      marginLeft: '0.5rem',
    }}>
      {blocked > 0 ? `${blocked} blocked` : `${total} passing`}
    </span>
  )
}

export function PolicyGatesPanel({ gates, loading }: Props) {
  const [open, setOpen] = useState(false)

  if (loading) {
    return (
      <div style={{ marginBottom: '0.75rem' }}>
        <button
          style={{
            background: 'none', border: 'none', color: '#64748b',
            cursor: 'default', fontSize: '0.8rem', padding: '0.25rem 0',
          }}
          disabled
        >
          ▸ Policy Gates (loading…)
        </button>
      </div>
    )
  }

  if (gates.length === 0) return null

  return (
    <div style={{ marginBottom: '0.75rem' }}>
      <button
        onClick={() => setOpen(o => !o)}
        style={{
          background: 'none',
          border: 'none',
          color: '#6366f1',
          cursor: 'pointer',
          fontSize: '0.8rem',
          padding: '0.25rem 0',
          fontWeight: 600,
          display: 'flex',
          alignItems: 'center',
        }}
        aria-expanded={open}
      >
        {open ? '▾' : '▸'} Policy Gates ({gates.length})
        <GateSummaryChip gates={gates} />
      </button>

      {open && (
        <div style={{
          borderLeft: '2px solid #1e293b',
          paddingLeft: '0.75rem',
          marginTop: '0.25rem',
        }}>
          {gates.map(gate => (
            <div
              key={`${gate.namespace}/${gate.name}`}
              style={{
                display: 'flex',
                flexDirection: 'column',
                gap: '0.2rem',
                padding: '0.4rem 0',
                borderBottom: '1px solid #1e293b',
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                <HealthChip
                  state={gate.ready ? 'Pass' : 'Block'}
                  nodeType="PolicyGate"
                  size="sm"
                />
                <span style={{ fontSize: '0.8rem', color: '#e2e8f0', fontWeight: 500 }}>
                  {gate.name}
                </span>
                <span style={{ fontSize: '0.65rem', color: '#475569', marginLeft: 'auto' }}>
                  {gate.namespace} · {formatAge(gate.lastEvaluatedAt)}
                </span>
              </div>
              {gate.expression && (
                <code style={{
                  fontSize: '0.72rem',
                  color: '#7dd3fc',
                  background: '#0f172a',
                  border: '1px solid #1e293b',
                  borderRadius: '3px',
                  padding: '2px 6px',
                  fontFamily: 'monospace',
                  wordBreak: 'break-all',
                }}>
                  {gate.expression}
                </code>
              )}
              {!gate.ready && gate.reason && (
                <div style={{ fontSize: '0.7rem', color: '#f87171' }}>
                  {gate.reason}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
