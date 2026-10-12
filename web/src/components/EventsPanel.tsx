// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/EventsPanel.tsx — Kubernetes events stream for a PromotionStep.
// Adapted from kro-ui EventsPanel.tsx; colors come from the theme tokens.
// Displays events newest-first, with Warning type styled differently from Normal.
// #527

import type { StepEvent } from '../types'
import '../styles/EventsPanel.css'
import { humanDuration } from '../timeFormat'

export type { StepEvent }

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
    if (diffSec < 86400) return `${humanDuration(diffSec)} ago`
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
    <div data-testid="events-panel" className="events-panel">
      <h4 className="events-panel__title">
        Events {items.length > 0 && <span className="events-panel__count">({items.length})</span>}
      </h4>
      {items.length === 0 ? (
        <div data-testid="events-panel-empty" className="events-panel__empty">
          No events recorded yet.{' '}
          <code className="events-panel__cmd">{kubectlCmd}</code>
        </div>
      ) : (
        <div className="events-panel__list">
          {items.map((ev, i) => (
            <div
              key={`${ev.reason}-${i}`}
              data-testid="event-row"
              className="events-panel__row"
              data-type={ev.type === 'Warning' ? 'warning' : 'normal'}
            >
              {/* Header row: type badge + reason + count + timestamp */}
              <div className="events-panel__head">
                <span data-testid="event-type" className="events-panel__type">
                  {ev.type}
                </span>
                <span className="events-panel__reason">
                  {ev.reason}
                </span>
                {ev.count > 1 && (
                  <span className="events-panel__repeat">×{ev.count}</span>
                )}
                <span className="events-panel__time">
                  {relativeTime(ev.lastTimestamp)}
                </span>
              </div>
              {/* Message row */}
              {ev.message && (
                <div className="events-panel__message">
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
