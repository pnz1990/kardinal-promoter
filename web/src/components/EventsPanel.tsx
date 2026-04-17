// components/EventsPanel.tsx — Kubernetes events stream for a PromotionStep.
// Adapted from kro-ui EventsPanel.tsx — simplified for kardinal's dark theme.
// Displays events newest-first, with Warning type styled differently from Normal.
// #527

export interface StepEvent {
  type: string          // "Normal" | "Warning"
  reason: string        // short CamelCase reason
  message: string       // human-readable message
  count: number         // number of occurrences
  firstTimestamp: string  // RFC3339
  lastTimestamp: string   // RFC3339
}

interface EventsPanelProps {
  events: StepEvent[] | null
  /** Step name — shown in the empty state kubectl command hint. */
  stepName?: string
  /** Namespace — shown in the empty state kubectl command hint. */
  namespace?: string
}

/** Format an RFC3339 timestamp as a relative human-readable string. */
function relativeTime(iso: string): string {
  if (!iso) return ''
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return iso
    const diffSec = Math.floor((Date.now() - d.getTime()) / 1000)
    if (diffSec < 60) return `${diffSec}s ago`
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  } catch {
    return iso
  }
}

export default function EventsPanel({ events, stepName, namespace }: EventsPanelProps) {
  const items = events ?? []
  const kubectlNs = namespace ? `-n ${namespace}` : ''
  const kubectlResource = stepName ? `--field-selector involvedObject.name=${stepName}` : ''
  const kubectlCmd = `kubectl get events ${kubectlNs} ${kubectlResource}`.trim().replace(/\s+/g, ' ')

  return (
    <div data-testid="events-panel" style={{ marginBottom: '0.75rem' }}>
      <h4 style={{ fontSize: '0.8rem', color: '#cbd5e1', marginBottom: '0.4rem' }}>
        Events {items.length > 0 && <span style={{ color: 'var(--color-text-muted)', fontWeight: 400 }}>({items.length})</span>}
      </h4>
      {items.length === 0 ? (
        <div
          data-testid="events-panel-empty"
          style={{
            fontSize: '0.75rem',
            color: 'var(--color-text-faint)',
            background: 'var(--color-bg)',
            border: '1px solid #1e293b',
            borderRadius: '4px',
            padding: '0.5rem 0.75rem',
            fontStyle: 'italic',
          }}
        >
          No events recorded yet.{' '}
          <code style={{ fontStyle: 'normal', color: 'var(--color-text-muted)', fontFamily: 'monospace' }}>{kubectlCmd}</code>
        </div>
      ) : (
        <div style={{
          background: 'var(--color-bg)',
          border: '1px solid #1e293b',
          borderRadius: '4px',
          overflow: 'hidden',
        }}>
          {items.map((ev, i) => (
            <div
              key={`${ev.reason}-${i}`}
              data-testid="event-row"
              style={{
                padding: '8px 12px',
                borderBottom: i < items.length - 1 ? '1px solid #1e293b' : undefined,
                borderLeft: ev.type === 'Warning' ? '2px solid #f59e0b' : '2px solid transparent',
              }}
            >
              {/* Header row: type badge + reason + count + timestamp */}
              <div style={{ display: 'flex', alignItems: 'center', gap: '6px', flexWrap: 'wrap' }}>
                <span
                  data-testid="event-type"
                  style={{
                    fontSize: '10px',
                    fontWeight: 600,
                    padding: '1px 5px',
                    borderRadius: '3px',
                    border: '1px solid',
                    background: ev.type === 'Warning' ? '#451a03' : '#0c2a1a',
                    borderColor: ev.type === 'Warning' ? '#92400e' : '#166534',
                    color: ev.type === 'Warning' ? 'var(--color-warning)' : 'var(--color-success)',
                  }}
                >
                  {ev.type}
                </span>
                <span style={{ fontSize: '12px', fontWeight: 600, color: 'var(--color-text)' }}>
                  {ev.reason}
                </span>
                {ev.count > 1 && (
                  <span style={{ fontSize: '11px', color: 'var(--color-text-muted)' }}>×{ev.count}</span>
                )}
                <span style={{ fontSize: '11px', color: 'var(--color-text-faint)', fontFamily: 'monospace', marginLeft: 'auto' }}>
                  {relativeTime(ev.lastTimestamp)}
                </span>
              </div>
              {/* Message row */}
              {ev.message && (
                <div style={{ fontSize: '12px', color: 'var(--color-text-muted)', marginTop: '3px', lineHeight: 1.4 }}>
                  {ev.message}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
