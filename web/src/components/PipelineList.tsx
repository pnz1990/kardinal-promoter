// components/PipelineList.tsx — Sidebar list of Pipelines with health chips,
// bundle name, environment count, and namespace indicator.
// Includes an onboarding empty state (Kargo parity).
// #345: debounced search/filter input at the top.
import { useState, useCallback, useRef } from 'react'
import type { Pipeline } from '../types'
import { HealthChip } from './HealthChip'

interface Props {
  pipelines: Pipeline[]
  selected?: string
  onSelect: (name: string) => void
  loading?: boolean
  error?: string
  /** Current namespace derived from loaded pipelines. Shown in header when set. */
  namespace?: string
}

/** Truncate a bundle name to a readable short form for the sidebar. */
function shortBundleName(name: string | undefined): string | null {
  if (!name) return null
  // Take last segment after last dash that looks like a version
  // e.g. "nginx-demo-v1-29-0-1712567890" → "nginx:v1.29"
  // Fallback: truncate to 14 chars
  if (name.length <= 14) return name
  return name.slice(0, 12) + '…'
}

/** Onboarding empty state shown when no pipelines have been created yet. */
function EmptyState() {
  return (
    <div style={{ padding: '1rem', color: '#94a3b8', fontSize: '0.8rem' }}>
      <p style={{ marginBottom: '0.75rem', fontStyle: 'italic' }}>No pipelines found.</p>
      <p style={{ marginBottom: '0.5rem', color: '#64748b' }}>Get started:</p>
      <code style={{
        display: 'block',
        background: '#0f172a',
        border: '1px solid #1e293b',
        borderRadius: '4px',
        padding: '0.4rem 0.5rem',
        fontSize: '0.72rem',
        color: '#7dd3fc',
        marginBottom: '0.5rem',
        whiteSpace: 'pre-wrap',
        wordBreak: 'break-all',
      }}>
        kubectl apply -f examples/quickstart/pipeline.yaml
      </code>
      <p style={{ marginBottom: '0.4rem', color: '#64748b' }}>Or use the wizard:</p>
      <code style={{
        display: 'block',
        background: '#0f172a',
        border: '1px solid #1e293b',
        borderRadius: '4px',
        padding: '0.4rem 0.5rem',
        fontSize: '0.72rem',
        color: '#7dd3fc',
        marginBottom: '0.75rem',
      }}>
        kardinal init
      </code>
      <a
        href="https://github.com/pnz1990/kardinal-promoter/blob/main/docs/quickstart.md"
        target="_blank"
        rel="noopener noreferrer"
        style={{ color: '#6366f1', fontSize: '0.75rem', textDecoration: 'none' }}
        aria-label="View quickstart documentation"
      >
        View quickstart docs ↗
      </a>
    </div>
  )
}

export function PipelineList({ pipelines, selected, onSelect, loading, error }: Props) {
  // #345: search/filter state with debounce
  const [searchQuery, setSearchQuery] = useState('')
  const debounceTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [debouncedQuery, setDebouncedQuery] = useState('')

  const handleSearchChange = useCallback((value: string) => {
    setSearchQuery(value)
    if (debounceTimer.current) clearTimeout(debounceTimer.current)
    debounceTimer.current = setTimeout(() => {
      setDebouncedQuery(value.trim().toLowerCase())
    }, 150)
  }, [])

  if (loading) {
    // #335: skeleton loading state — shimmer placeholders instead of "Loading pipelines..."
    return (
      <div style={{ padding: '0.5rem 0' }}>
        <style>{`
          @keyframes shimmer-pl {
            0% { background-position: 200% 0; }
            100% { background-position: -200% 0; }
          }
        `}</style>
        {[80, 65, 90, 70].map((w, i) => (
          <div
            key={i}
            style={{
              height: '42px',
              borderRadius: '4px',
              background: 'linear-gradient(90deg, #1e293b 25%, #293548 50%, #1e293b 75%)',
              backgroundSize: '200% 100%',
              animation: 'shimmer-pl 1.5s infinite',
              margin: '0.3rem 1rem',
              width: `${w}%`,
            }}
          />
        ))}
      </div>
    )
  }
  if (error) {
    return (
      <div style={{ padding: '1rem', color: '#ef4444', fontSize: '0.82rem' }}>
        Error: {error}
      </div>
    )
  }
  if (pipelines.length === 0) {
    return <EmptyState />
  }

  // #345: filter pipelines by search query
  const filteredPipelines = debouncedQuery
    ? pipelines.filter(p => p.name.toLowerCase().includes(debouncedQuery))
    : pipelines

  return (
    <div>
      {/* #345: search/filter input */}
      {pipelines.length > 3 && (
        <div style={{ padding: '0.5rem 1rem 0.25rem', position: 'relative' }}>
          <input
            type="text"
            placeholder="Filter pipelines…"
            value={searchQuery}
            onChange={e => handleSearchChange(e.target.value)}
            aria-label="Filter pipelines by name"
            style={{
              width: '100%',
              boxSizing: 'border-box',
              background: '#1e293b',
              border: '1px solid #334155',
              borderRadius: '4px',
              padding: '0.3rem 1.75rem 0.3rem 0.5rem',
              fontSize: '0.78rem',
              color: '#e2e8f0',
              outline: 'none',
            }}
          />
          {searchQuery && (
            <button
              onClick={() => handleSearchChange('')}
              aria-label="Clear filter"
              style={{
                position: 'absolute',
                right: '1.3rem',
                top: '50%',
                transform: 'translateY(-50%)',
                background: 'none',
                border: 'none',
                cursor: 'pointer',
                color: '#64748b',
                fontSize: '0.9rem',
                padding: '0 2px',
                lineHeight: 1,
              }}
            >×</button>
          )}
        </div>
      )}
      <ul style={{ listStyle: 'none', padding: 0, margin: 0 }}>
        {filteredPipelines.length === 0 && debouncedQuery && (
          <li style={{ padding: '0.75rem 1rem', color: '#64748b', fontSize: '0.8rem' }}>
            No pipelines match "{debouncedQuery}"
          </li>
        )}
        {filteredPipelines.map(p => {
        const bundle = shortBundleName(p.activeBundleName)
        const envCount = p.environmentCount

        return (
          <li
            key={p.name}
            onClick={() => onSelect(p.name)}
            role="button"
            aria-selected={selected === p.name}
            tabIndex={0}
            onKeyDown={e => (e.key === 'Enter' || e.key === ' ') && onSelect(p.name)}
            style={{
              padding: '0.6rem 1rem',
              cursor: 'pointer',
              background: selected === p.name ? '#1e293b' : 'transparent',
              borderLeft: selected === p.name ? '3px solid #6366f1' : '3px solid transparent',
            }}
          >
            {/* Pipeline name + phase badge */}
            <div style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              marginBottom: bundle || envCount ? '0.2rem' : 0,
            }}>
              <span style={{
                fontWeight: selected === p.name ? 600 : 400,
                fontSize: '0.85rem',
                color: '#e2e8f0',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
                maxWidth: '130px',
              }}>
                {p.name}
              </span>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.25rem' }}>
                {/* Paused badge — visible accent when pipeline is paused (#328) */}
                {p.paused && (
                  <span
                    title="Pipeline is paused — no new promotions will start"
                    style={{
                      fontSize: '0.6rem',
                      background: '#1e1b4b',
                      color: '#a5b4fc',
                      border: '1px solid #4338ca',
                      borderRadius: '3px',
                      padding: '0px 4px',
                      fontWeight: 700,
                      letterSpacing: '0.05em',
                    }}
                  >
                    PAUSED
                  </span>
                )}
                {p.phase && <HealthChip state={p.paused ? 'Paused' : p.phase} size="sm" />}
              </div>
            </div>

            {/* Sub-line: env count + active bundle */}
            {(bundle || envCount > 0) && (
              <div style={{ fontSize: '0.7rem', color: '#64748b', display: 'flex', gap: '0.4rem' }}>
                {envCount > 0 && (
                  <span>{envCount} env{envCount !== 1 ? 's' : ''}</span>
                )}
                {bundle && (
                  <>
                    {envCount > 0 && <span>·</span>}
                    <span
                      style={{ fontFamily: 'monospace', color: '#94a3b8' }}
                      title={p.activeBundleName}
                    >
                      {bundle}
                    </span>
                  </>
                )}
              </div>
            )}
          </li>
        )
      })}
      </ul>
    </div>
  )
}
