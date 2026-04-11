// components/PipelineList.tsx — Sidebar list of Pipelines with active Bundle phase badges.
import type { Pipeline } from '../types'

interface Props {
  pipelines: Pipeline[]
  selected?: string
  onSelect: (name: string) => void
  loading?: boolean
  error?: string
}

function phaseBadgeColor(phase: string): string {
  switch (phase) {
    case 'Verified': return '#22c55e'
    case 'Promoting': return '#f59e0b'
    case 'Failed': return '#ef4444'
    case 'Superseded': return '#94a3b8'
    default: return '#64748b'
  }
}

export function PipelineList({ pipelines, selected, onSelect, loading, error }: Props) {
  if (loading) {
    return (
      <div style={{ padding: '1rem', color: '#94a3b8' }}>
        Loading pipelines...
      </div>
    )
  }
  if (error) {
    return (
      <div style={{ padding: '1rem', color: '#ef4444' }}>
        Error: {error}
      </div>
    )
  }
  if (pipelines.length === 0) {
    return (
      <div style={{ padding: '1rem', color: '#94a3b8' }}>
        No pipelines found.
      </div>
    )
  }

  return (
    <ul style={{ listStyle: 'none', padding: 0 }}>
      {pipelines.map(p => (
        <li
          key={p.name}
          onClick={() => onSelect(p.name)}
          style={{
            padding: '0.75rem 1rem',
            cursor: 'pointer',
            background: selected === p.name ? '#1e293b' : 'transparent',
            borderLeft: selected === p.name ? '3px solid #6366f1' : '3px solid transparent',
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
          }}
        >
          <span style={{ fontWeight: selected === p.name ? 600 : 400 }}>{p.name}</span>
          {p.phase && (
            <span style={{
              background: phaseBadgeColor(p.phase),
              color: '#fff',
              fontSize: '0.7rem',
              padding: '2px 6px',
              borderRadius: '9999px',
              fontWeight: 600,
            }}>
              {p.phase}
            </span>
          )}
        </li>
      ))}
    </ul>
  )
}
