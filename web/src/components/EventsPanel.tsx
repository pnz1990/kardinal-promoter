// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// components/EventsPanel.tsx — Kubernetes Events stream for a PromotionStep node (#527).
//
// Adapts the kro-ui EventsPanel pattern: grouped by reason, relative timestamps,
// Warning events styled differently from Normal events.
// Empty state: "No events recorded" — not an error.

import { useEffect, useState } from 'react'
import { api } from '../api/client'
import type { StepEvent } from '../types'

interface Props {
  /** Kubernetes namespace of the PromotionStep. */
  namespace: string
  /** Name of the PromotionStep object. */
  stepName: string
}

/** Format an ISO timestamp to a human-readable relative string. */
function relativeTime(iso: string | undefined): string {
  if (!iso) return ''
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return iso
    const diffSec = Math.floor((Date.now() - d.getTime()) / 1000)
    if (diffSec < 0) return 'just now'
    if (diffSec < 60) return `${diffSec}s ago`
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`
    return d.toLocaleDateString()
  } catch {
    return iso
  }
}

/**
 * EventsPanel shows the last 20 Kubernetes Events for a PromotionStep.
 * Fetches once on mount; does not poll (events are historical, not real-time).
 * Warning events are highlighted in amber.
 */
export function EventsPanel({ namespace, stepName }: Props) {
  const [events, setEvents] = useState<StepEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | undefined>()

  useEffect(() => {
    if (!stepName || !namespace) return
    setLoading(true)
    setError(undefined)
    api.getStepEvents(namespace, stepName)
      .then(evts => {
        setEvents(evts ?? [])
        setLoading(false)
      })
      .catch(e => {
        setError(String(e))
        setLoading(false)
      })
  }, [namespace, stepName])

  if (loading) {
    return (
      <div style={{ padding: '0.5rem 0', color: '#475569', fontSize: '0.72rem' }}>
        Loading events…
      </div>
    )
  }

  if (error) {
    // Silently degrade — events are a nice-to-have, not critical
    return null
  }

  if (events.length === 0) {
    return (
      <div style={{
        padding: '0.5rem 0.75rem',
        color: '#475569',
        fontSize: '0.72rem',
        fontStyle: 'italic',
      }}>
        No events recorded
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.15rem' }}>
      {events.map((ev, i) => (
        <EventRow key={`${ev.reason}-${i}`} event={ev} />
      ))}
    </div>
  )
}

/** A single event row. Warning events are amber; Normal events are muted. */
function EventRow({ event }: { event: StepEvent }) {
  const isWarning = event.type === 'Warning'
  return (
    <div style={{
      display: 'grid',
      gridTemplateColumns: 'auto 1fr auto',
      gap: '0.4rem',
      alignItems: 'baseline',
      padding: '0.2rem 0.5rem',
      borderRadius: '4px',
      background: isWarning ? 'rgba(245,158,11,0.06)' : 'transparent',
    }}>
      {/* Reason badge */}
      <span style={{
        fontSize: '0.62rem',
        fontWeight: 700,
        color: isWarning ? '#fbbf24' : '#94a3b8',
        whiteSpace: 'nowrap',
        fontFamily: 'monospace',
      }}>
        {event.reason}
        {event.count > 1 && (
          <span style={{ fontWeight: 400, marginLeft: '0.25rem' }}>×{event.count}</span>
        )}
      </span>

      {/* Message */}
      <span style={{
        fontSize: '0.7rem',
        color: isWarning ? '#fde68a' : '#cbd5e1',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
      }} title={event.message}>
        {event.message}
      </span>

      {/* Timestamp */}
      <span style={{
        fontSize: '0.62rem',
        color: '#475569',
        whiteSpace: 'nowrap',
        textAlign: 'right',
      }}>
        {relativeTime(event.lastTimestamp)}
      </span>
    </div>
  )
}
