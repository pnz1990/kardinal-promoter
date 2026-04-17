// components/PipelineList.tsx — Sidebar list of Pipelines with health chips,
// bundle name, environment count, and namespace indicator.
// Includes an onboarding empty state (Kargo parity).
// #345: debounced search/filter input at the top.
import { useState, useCallback, useRef } from 'react'
import type { Pipeline } from '../types'
import { HealthChip } from './HealthChip'
import CopyButton from './CopyButton'

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
    <div style={{ padding: '1rem', color: 'var(--color-text-muted)', fontSize: '0.8rem' }}>
      <p style={{ marginBottom: '0.75rem', fontStyle: 'italic' }}>No pipelines found.</p>
      <p style={{ marginBottom: '0.5rem', color: 'var(--color-text-muted)' }}>Get started:</p>
      <code style={{
        display: 'block',
        background: 'var(--color-bg)',
        border: '1px solid #1e293b',
        borderRadius: '4px',
        padding: '0.4rem 0.5rem',
        fontSize: '0.72rem',
        color: 'var(--color-code)',
        marginBottom: '0.5rem',
        whiteSpace: 'pre-wrap',
        wordBreak: 'break-all',
      }}>
        kubectl apply -f examples/quickstart/pipeline.yaml
      </code>
      <p style={{ marginBottom: '0.4rem', color: 'var(--color-text-muted)' }}>Or use the wizard:</p>
      <code style={{
        display: 'block',
        background: 'var(--color-bg)',
        border: '1px solid #1e293b',
        borderRadius: '4px',
        padding: '0.4rem 0.5rem',
        fontSize: '0.72rem',
        color: 'var(--color-code)',
        marginBottom: '0.75rem',
      }}>
        kardinal init
      </code>
      <a
        href="https://github.com/pnz1990/kardinal-promoter/blob/main/docs/quickstart.md"
        target="_blank"
        rel="noopener noreferrer"
        style={{ color: 'var(--color-accent)', fontSize: '0.75rem', textDecoration: 'none' }}
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

  // #345: filter pipelines by search query (includes namespace prefix search)
  const filteredPipelines = debouncedQuery
    ? pipelines.filter(p =>
        p.name.toLowerCase().includes(debouncedQuery) ||
        p.namespace.toLowerCase().includes(debouncedQuery) ||
        `${p.namespace}/${p.name}`.toLowerCase().includes(debouncedQuery)
      )
    : pipelines

  // #358: detect multi-namespace setup — show namespace prefix when needed
  const uniqueNamespaces = new Set(pipelines.map(p => p.namespace))
  const isMultiNamespace = uniqueNamespaces.size > 1

  // Group by namespace for multi-namespace display
  const pipelinesByNamespace: Record<string, typeof filteredPipelines> = {}
  for (const p of filteredPipelines) {
    if (!pipelinesByNamespace[p.namespace]) pipelinesByNamespace[p.namespace] = []
    pipelinesByNamespace[p.namespace].push(p)
  }
  const namespaceOrder = Array.from(uniqueNamespaces).sort()

  return (
    <div>
      {/* #345: search/filter input */}
      {pipelines.length > 3 && (
         <div style={{ padding: '0.5rem 1rem 0.25rem', position: 'relative' }}>
           <input
             type="text"
             placeholder={isMultiNamespace ? "Filter by name or namespace…" : "Filter pipelines…"}
             value={searchQuery}
             onChange={e => handleSearchChange(e.target.value)}
             aria-label="Filter pipelines by name or namespace"
             style={{
               width: '100%',
               boxSizing: 'border-box',
              background: 'var(--color-surface)',
              border: '1px solid #334155',
              borderRadius: '4px',
              padding: '0.3rem 1.75rem 0.3rem 0.5rem',
              fontSize: '0.78rem',
              color: 'var(--color-text)',
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
      <ul role="list" aria-label="Pipelines" style={{ listStyle: 'none', padding: 0, margin: 0 }}>
        {filteredPipelines.length === 0 && debouncedQuery && (
          <li role="presentation" style={{ padding: '0.75rem 1rem', color: 'var(--color-text-muted)', fontSize: '0.8rem' }}>
            No pipelines match "{debouncedQuery}"
          </li>
        )}
        {/* #358: multi-namespace grouped display */}
        {isMultiNamespace ? (
          namespaceOrder.map(ns => {
            const nsPipelines = pipelinesByNamespace[ns]
            if (!nsPipelines || nsPipelines.length === 0) return null
            return (
              <li key={ns}>
                {/* Namespace header */}
                <div style={{
                  padding: '0.3rem 1rem 0.15rem',
                  fontSize: '0.65rem',
                  color: 'var(--color-text-faint)',
                  textTransform: 'uppercase',
                  letterSpacing: '0.05em',
                  borderTop: '1px solid #1e293b',
                  fontFamily: 'monospace',
                  background: '#070f1b',
                }}>
                  {ns}
                </div>
      <ul role="group" style={{ listStyle: 'none', padding: 0, margin: 0 }}>
                  {nsPipelines.map(p => renderPipelineItem(p))}
                </ul>
              </li>
            )
          })
        ) : (
          filteredPipelines.map(p => renderPipelineItem(p))
        )}
      </ul>
    </div>
  )

  // #358: renderPipelineItem as inner function for reuse in grouped and flat display
  function renderPipelineItem(p: Pipeline) {
        const bundle = shortBundleName(p.activeBundleName)
        const envCount = p.environmentCount

        return (
          <li
            key={`${p.namespace}/${p.name}`}
            style={{ listStyle: 'none' }}
          >
          {/* #762: Outer <li> is non-interactive. A <button> handles selection to avoid
              nested-interactive axe violation (interactive inside interactive). */}
          <button
            onClick={() => onSelect(p.name)}
            aria-pressed={selected === p.name}
            onKeyDown={e => (e.key === 'Enter' || e.key === ' ') && onSelect(p.name)}
            style={{
              width: '100%',
              textAlign: 'left',
              padding: '0.6rem 1rem',
              cursor: 'pointer',
              background: selected === p.name ? 'var(--color-surface)' : 'transparent',
              borderLeft: selected === p.name ? '3px solid #6366f1' : '3px solid transparent',
              borderTop: 'none',
              borderRight: 'none',
              borderBottom: 'none',
              display: 'block',
            }}
          >
            {/* Pipeline name + phase badge */}
            <div style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              marginBottom: bundle || envCount ? '0.2rem' : 0,
            }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.25rem', minWidth: 0 }}>
                <span style={{
                  fontWeight: selected === p.name ? 600 : 400,
                  fontSize: '0.85rem',
                  color: 'var(--color-text)',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                  maxWidth: '105px',
                }}>
                  {p.name}
                </span>
                {/* #763: copy pipeline name to clipboard
                    tabIndex={-1} prevents nested-interactive axe violation — CopyButton
                    is inside the selection <button> and must not be independently focusable. */}
                <CopyButton text={p.name} title={`Copy pipeline name "${p.name}"`} tabIndex={-1} />
              </div>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.25rem' }}>
                {/* Paused badge — visible accent when pipeline is paused (#328) */}
                {p.paused && (
                  <span
                    title="Pipeline is paused — no new promotions will start"
                    style={{
                      fontSize: '0.6rem',
                      background: '#1e1b4b',
                      color: 'var(--color-accent)',
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
                {p.phase && (
                  // #523: pipelines with phase "Unknown" have no active promotion — show
                  // "Idle" as a more descriptive label than "Unknown" to users.
                  <HealthChip
                    state={p.paused ? 'Paused' : p.phase}
                    label={p.paused ? undefined : p.phase === 'Unknown' ? 'Idle' : undefined}
                    size="sm"
                  />
                )}
              </div>
            </div>

             {/* Sub-line: env count + health bar + active bundle (#342) */}
            {(bundle || envCount > 0) && (
              <div style={{ fontSize: '0.7rem', color: 'var(--color-text-muted)', display: 'flex', flexDirection: 'column', gap: '0.2rem' }}>
                {/* Multi-segment health bar when env states are available (#342) */}
                {p.environmentStates && Object.keys(p.environmentStates).length > 0 ? (
                  <div style={{ display: 'flex', gap: '0.3rem', alignItems: 'center', flexWrap: 'wrap' }}>
                    <span>{envCount} env{envCount !== 1 ? 's' : ''}</span>
                    <span style={{ color: 'var(--color-surface)' }}>·</span>
                    {/* State badges: count per phase */}
                    {(() => {
                      const counts: Record<string, number> = {}
                      for (const phase of Object.values(p.environmentStates!)) {
                        counts[phase] = (counts[phase] ?? 0) + 1
                      }
                      const phaseColor: Record<string, string> = {
                        Verified: 'var(--color-success)', Promoting: 'var(--color-accent)', WaitingForMerge: 'var(--color-accent)',
                        HealthChecking: '#a78bfa', Failed: 'var(--color-error)', Pending: 'var(--color-text-faint)',
                      }
                      return Object.entries(counts).map(([phase, count]) => (
                        <span key={phase} style={{
                          fontSize: '0.6rem',
                          color: phaseColor[phase] ?? 'var(--color-text-muted)',
                          fontWeight: 600,
                        }} title={`${count} env${count !== 1 ? 's' : ''} in ${phase}`}>
                          {count} {phase === 'WaitingForMerge' ? 'PR' : phase === 'HealthChecking' ? 'health' : phase.toLowerCase()}
                        </span>
                      ))
                    })()}
                  </div>
                ) : (
                  <div style={{ display: 'flex', gap: '0.4rem' }}>
                    {envCount > 0 && (
                      <span>{envCount} env{envCount !== 1 ? 's' : ''}</span>
                    )}
                    {bundle && (
                      <>
                        {envCount > 0 && <span>·</span>}
                        <span
                          style={{ fontFamily: 'monospace', color: 'var(--color-text-muted)' }}
                          title={p.activeBundleName}
                        >
                          {bundle}
                        </span>
                      </>
                    )}
                  </div>
                )}
                {/* Bundle name shown below the health bar when env states are shown */}
                {p.environmentStates && bundle && (
                  <span style={{ fontFamily: 'monospace', color: 'var(--color-text-muted)' }} title={p.activeBundleName}>
                    {bundle}
                  </span>
                )}
              </div>
            )}
          </button>
          </li>
        )
  }
}
