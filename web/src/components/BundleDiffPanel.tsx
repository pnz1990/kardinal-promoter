// components/BundleDiffPanel.tsx — Side-by-side bundle field comparison.
// #338: Shows key differences between two Bundle objects. Triggered by
// shift-clicking a bundle in BundleTimeline and pressing 'Compare'.
//
// Adapted from kro-ui InstanceTable.tsx SpecDiffPanel pattern.
import type { Bundle } from '../types'

interface Props {
  bundleA: Bundle
  bundleB: Bundle
  onClose: () => void
}

type FieldRow = {
  label: string
  valueA: string | null
  valueB: string | null
  /** If true, highlight this row as changed */
  changed: boolean
}

function truncate(s: string | undefined, max = 40): string {
  if (!s) return '—'
  return s.length > max ? s.slice(0, max - 1) + '…' : s
}

function buildRows(a: Bundle, b: Bundle): FieldRow[] {
  const rows: FieldRow[] = [
    {
      label: 'Phase',
      valueA: a.phase ?? null,
      valueB: b.phase ?? null,
      changed: a.phase !== b.phase,
    },
    {
      label: 'Type',
      valueA: a.type ?? null,
      valueB: b.type ?? null,
      changed: a.type !== b.type,
    },
    {
      label: 'Created',
      valueA: a.createdAt ? new Date(a.createdAt).toLocaleString() : null,
      valueB: b.createdAt ? new Date(b.createdAt).toLocaleString() : null,
      changed: a.createdAt !== b.createdAt,
    },
    {
      label: 'Author',
      valueA: a.provenance?.author ?? null,
      valueB: b.provenance?.author ?? null,
      changed: a.provenance?.author !== b.provenance?.author,
    },
    {
      label: 'Commit SHA',
      valueA: a.provenance?.commitSHA ? a.provenance.commitSHA.slice(0, 12) : null,
      valueB: b.provenance?.commitSHA ? b.provenance.commitSHA.slice(0, 12) : null,
      changed: a.provenance?.commitSHA !== b.provenance?.commitSHA,
    },
    {
      label: 'CI Run',
      valueA: a.provenance?.ciRunURL ?? null,
      valueB: b.provenance?.ciRunURL ?? null,
      changed: a.provenance?.ciRunURL !== b.provenance?.ciRunURL,
    },
  ]
  return rows
}

export function BundleDiffPanel({ bundleA, bundleB, onClose }: Props) {
  const rows = buildRows(bundleA, bundleB)
  const changedCount = rows.filter(r => r.changed).length

  return (
    <div style={{
      position: 'fixed',
      top: 0, left: 0, right: 0, bottom: 0,
      background: 'rgba(0,0,0,0.7)',
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'center',
      zIndex: 1000,
    }}
    onClick={onClose}
    role="dialog"
    aria-modal="true"
    aria-label="Bundle comparison"
    >
      <div
        onClick={e => e.stopPropagation()}
        style={{
          background: 'var(--color-bg)',
          border: '1px solid #334155',
          borderRadius: '8px',
          padding: '1.5rem',
          maxWidth: '700px',
          width: '95%',
          maxHeight: '80vh',
          overflowY: 'auto',
        }}
      >
        {/* Header */}
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '1.25rem' }}>
          <div>
            <h2 style={{ fontSize: '1rem', fontWeight: 700, color: 'var(--color-text)', margin: 0 }}>
              Bundle Comparison
            </h2>
            <p style={{ fontSize: '0.75rem', color: '#64748b', margin: '0.25rem 0 0' }}>
              {changedCount} field{changedCount !== 1 ? 's' : ''} differ between these bundles
            </p>
          </div>
          <button
            onClick={onClose}
            aria-label="Close comparison"
            style={{
              background: 'none',
              border: 'none',
              color: '#64748b',
              cursor: 'pointer',
              fontSize: '1.2rem',
              padding: '0 4px',
              lineHeight: 1,
            }}
          >×</button>
        </div>

        {/* Column headers */}
        <div style={{
          display: 'grid',
          gridTemplateColumns: '140px 1fr 1fr',
          gap: '0.5rem',
          marginBottom: '0.5rem',
        }}>
          <div />
          <div style={{ fontSize: '0.7rem', color: 'var(--color-text-faint)', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
            Bundle A
          </div>
          <div style={{ fontSize: '0.7rem', color: 'var(--color-text-faint)', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
            Bundle B
          </div>
        </div>

        {/* Bundle names */}
        <div style={{
          display: 'grid',
          gridTemplateColumns: '140px 1fr 1fr',
          gap: '0.5rem',
          marginBottom: '0.75rem',
          background: 'var(--color-surface)',
          borderRadius: '4px',
          padding: '0.4rem 0.6rem',
        }}>
          <span style={{ fontSize: '0.75rem', color: 'var(--color-text-muted)' }}>Name</span>
          <span style={{ fontFamily: 'monospace', fontSize: '0.75rem', color: 'var(--color-code-text)' }} title={bundleA.name}>
            {truncate(bundleA.name, 28)}
          </span>
          <span style={{ fontFamily: 'monospace', fontSize: '0.75rem', color: 'var(--color-code-text)' }} title={bundleB.name}>
            {truncate(bundleB.name, 28)}
          </span>
        </div>

        {/* Diff rows */}
        {rows.map(row => (
          <div key={row.label} style={{
            display: 'grid',
            gridTemplateColumns: '140px 1fr 1fr',
            gap: '0.5rem',
            marginBottom: '0.3rem',
            background: row.changed ? '#1a1000' : 'transparent',
            border: row.changed ? '1px solid #78350f' : '1px solid transparent',
            borderRadius: '4px',
            padding: '0.35rem 0.6rem',
          }}>
            <span style={{
              fontSize: '0.75rem',
              color: row.changed ? 'var(--color-warning)' : '#64748b',
              fontWeight: row.changed ? 600 : 400,
            }}>
              {row.changed ? '▸ ' : ''}{row.label}
            </span>
            <DiffCell value={row.valueA} changed={row.changed} isLink={row.label === 'CI Run'} />
            <DiffCell value={row.valueB} changed={row.changed} isLink={row.label === 'CI Run'} />
          </div>
        ))}

        {changedCount === 0 && (
          <p style={{ textAlign: 'center', color: 'var(--color-success)', fontSize: '0.85rem', marginTop: '0.5rem' }}>
            ✓ No differences found between these bundles.
          </p>
        )}

        <div style={{ marginTop: '1rem', textAlign: 'right' }}>
          <button
            onClick={onClose}
            style={{
              background: 'var(--color-surface)',
              border: '1px solid #334155',
              color: 'var(--color-text)',
              borderRadius: '4px',
              padding: '0.4rem 1rem',
              cursor: 'pointer',
              fontSize: '0.82rem',
            }}
          >
            Close
          </button>
        </div>
      </div>
    </div>
  )
}

function DiffCell({ value, changed, isLink }: { value: string | null; changed: boolean; isLink?: boolean }) {
  if (!value) {
    return <span style={{ fontSize: '0.75rem', color: 'var(--color-border)' }}>—</span>
  }
  if (isLink && value.startsWith('http')) {
    return (
      <a
        href={value}
        target="_blank"
        rel="noopener noreferrer"
        style={{ fontSize: '0.75rem', color: 'var(--color-accent)', fontFamily: 'monospace' }}
        title={value}
      >
        {truncate(value, 30)}
      </a>
    )
  }
  return (
    <span
      style={{
        fontSize: '0.75rem',
        color: changed ? 'var(--color-warning)' : 'var(--color-text-muted)',
        fontFamily: 'monospace',
      }}
      title={value}
    >
      {truncate(value, 30)}
    </span>
  )
}
