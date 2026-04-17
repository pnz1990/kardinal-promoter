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

// components/GateDetailPanel.tsx — Policy gate detail panel (#468).
// Shows CEL expression (syntax highlighted), status, lastEvaluatedAt relative time,
// blocking duration, and override history with audit records.
// Opened by clicking a PolicyGate node in the DAG. Closes on outside click or Escape.
import { useEffect, useRef, useCallback } from 'react'
import type { GraphNode, PolicyGate } from '../types'
import { HealthChip } from './HealthChip'

interface Props {
  /** The DAG node that was clicked — provides gate id for lookup. */
  node: GraphNode
  /** All gates from api.listGates() — used to find the matching gate. */
  gates: PolicyGate[]
  /** Called when the panel should close. */
  onClose: () => void
}

/** #502: Simple CEL syntax tokenizer for lightweight highlighting.
 * Returns spans with different colors for keywords, strings, identifiers, operators. */
export function tokenizeCEL(expr: string): Array<{ text: string; type: 'keyword' | 'string' | 'number' | 'operator' | 'function' | 'plain' }> {
  const tokens: Array<{ text: string; type: 'keyword' | 'string' | 'number' | 'operator' | 'function' | 'plain' }> = []
  const KEYWORDS = new Set(['true', 'false', 'null', 'in', 'has', 'all', 'exists', 'exists_one', 'map', 'filter'])
  const TOKEN_RE = /("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|\d+(?:\.\d+)?|[a-zA-Z_][a-zA-Z0-9_.]*(?=\s*\()|[a-zA-Z_][a-zA-Z0-9_]*|[+\-*/<>=!&|?:,\[\]{}()]+|\s+|.)/g
  let match: RegExpExecArray | null
  while ((match = TOKEN_RE.exec(expr)) !== null) {
    const text = match[0]
    if (/^\s+$/.test(text)) {
      tokens.push({ text, type: 'plain' })
    } else if (text.startsWith('"') || text.startsWith("'")) {
      tokens.push({ text, type: 'string' })
    } else if (/^\d/.test(text)) {
      tokens.push({ text, type: 'number' })
    } else if (KEYWORDS.has(text)) {
      tokens.push({ text, type: 'keyword' })
    } else if (/^[a-zA-Z_][a-zA-Z0-9_.]*\(/.test(text + '(') && /^[a-zA-Z_][a-zA-Z0-9_.]*$/.test(text) && !KEYWORDS.has(text)) {
      // Function name (followed by opening paren)
      tokens.push({ text, type: 'function' })
    } else if (/^[+\-*/<>=!&|?:,\[\]{}()]+$/.test(text)) {
      tokens.push({ text, type: 'operator' })
    } else {
      tokens.push({ text, type: 'plain' })
    }
  }
  return tokens
}

const TOKEN_COLORS: Record<string, string> = {
  keyword: '#f59e0b',   // amber — true/false/null/in
  string: 'var(--color-success)',    // green — string literals
  number: '#60a5fa',    // blue — numbers
  operator: 'var(--color-text)',  // white — operators
  function: '#22d3ee',  // cyan — function calls
  plain: 'var(--color-text-muted)',     // gray — identifiers
}

/** #502: Format relative time from ISO string. */
export function relativeTime(iso: string | undefined): string {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return '—'
    const diffSec = Math.floor((Date.now() - d.getTime()) / 1000)
    if (diffSec < 0) return 'future'
    if (diffSec < 60) return `${diffSec}s ago`
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`
    return `${Math.floor(diffSec / 86400)}d ago`
  } catch { return '—' }
}

/** #502: Format blocking duration: "blocking for X minutes" since last evaluated. */
export function blockingDuration(lastEvaluatedAt: string | undefined, isBlocking: boolean): string | null {
  if (!isBlocking || !lastEvaluatedAt) return null
  try {
    const d = new Date(lastEvaluatedAt)
    if (isNaN(d.getTime())) return null
    const mins = Math.floor((Date.now() - d.getTime()) / 60000)
    if (mins < 1) return 'blocking for < 1 minute'
    return `blocking for ${mins} minute${mins !== 1 ? 's' : ''}`
  } catch { return null }
}

/** #502: Check if an override is expired. */
export function isOverrideExpired(expiresAt: string | undefined): boolean {
  if (!expiresAt) return false
  try {
    return new Date(expiresAt).getTime() < Date.now()
  } catch { return false }
}

export function GateDetailPanel({ node, gates, onClose }: Props) {
  const panelRef = useRef<HTMLDivElement>(null)

  // Find the gate by matching node label (gate name)
  const gate = gates.find(g => g.name === node.label || g.name === node.id.replace(/^gate-/, ''))

  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    if (e.key === 'Escape') onClose()
  }, [onClose])

  const handleOutsideClick = useCallback((e: MouseEvent) => {
    if (panelRef.current && !panelRef.current.contains(e.target as Node)) {
      onClose()
    }
  }, [onClose])

  useEffect(() => {
    document.addEventListener('keydown', handleKeyDown)
    document.addEventListener('mousedown', handleOutsideClick)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      document.removeEventListener('mousedown', handleOutsideClick)
    }
  }, [handleKeyDown, handleOutsideClick])

  const expression = gate?.expression ?? node.expression ?? ''
  const lastEvalAt = gate?.lastEvaluatedAt ?? node.lastEvaluatedAt
  const isBlocking = !(gate?.ready ?? node.state === 'Pass')
  const blockingMsg = blockingDuration(lastEvalAt, isBlocking)
  const tokens = tokenizeCEL(expression)
  const overrides = gate?.overrides ?? []
  const activeOverrides = overrides.filter(o => !isOverrideExpired(o.expiresAt))
  const expiredOverrides = overrides.filter(o => isOverrideExpired(o.expiresAt))

  return (
    <div
      ref={panelRef}
      role="dialog"
      aria-label={`Gate detail: ${node.label}`}
      style={{
        position: 'absolute',
        zIndex: 100,
        top: '40px',
        right: '16px',
        width: '360px',
        background: 'var(--color-bg)',
        border: '1px solid #1e293b',
        borderRadius: '6px',
        boxShadow: '0 4px 24px rgba(0,0,0,0.5)',
        fontSize: '0.8rem',
        color: '#cbd5e1',
        maxHeight: '80vh',
        overflowY: 'auto',
      }}
    >
      {/* Header */}
      <div style={{
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        padding: '0.5rem 0.75rem',
        borderBottom: '1px solid #1e293b',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <span style={{ fontFamily: 'monospace', fontSize: '0.7rem', color: 'var(--color-accent)' }}>🔒</span>
          <span style={{ fontWeight: 600, color: 'var(--color-text)', fontFamily: 'monospace', fontSize: '0.82rem' }}>
            {node.label}
          </span>
          <HealthChip state={node.state} nodeType="PolicyGate" size="sm" />
        </div>
        <button
          onClick={onClose}
          aria-label="Close gate detail"
          style={{
            background: 'none', border: 'none', cursor: 'pointer',
            color: '#64748b', fontSize: '1rem', lineHeight: 1, padding: '2px 4px',
          }}
        >×</button>
      </div>

      {/* Status row */}
      <div style={{ padding: '0.4rem 0.75rem', borderBottom: '1px solid #1e293b' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span style={{ color: '#64748b', fontSize: '0.72rem' }}>
            {gate?.reason ?? node.message ?? (isBlocking ? 'Blocking' : 'Passing')}
          </span>
          {lastEvalAt && (
            <span
              title={`Last evaluated: ${lastEvalAt}`}
              style={{ color: 'var(--color-text-faint)', fontSize: '0.68rem' }}
            >
              evaluated {relativeTime(lastEvalAt)}
            </span>
          )}
        </div>
        {blockingMsg && (
          <div style={{ color: '#ef4444', fontSize: '0.7rem', marginTop: '0.2rem' }} aria-label={blockingMsg}>
            {blockingMsg}
          </div>
        )}
      </div>

      {/* CEL expression (syntax highlighted) */}
      {expression && (
        <div style={{ padding: '0.5rem 0.75rem', borderBottom: '1px solid #1e293b' }}>
          <div style={{ color: 'var(--color-text-muted)', fontSize: '0.68rem', textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '0.3rem' }}>
            Expression
          </div>
          <pre
            aria-label="CEL expression"
            style={{
              margin: 0,
              fontFamily: 'monospace',
              fontSize: '0.75rem',
              background: '#020817',
              border: '1px solid #1e293b',
              borderRadius: '4px',
              padding: '0.4rem 0.5rem',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-word',
              lineHeight: 1.5,
            }}
          >
            {tokens.map((t, i) => (
              <span key={i} style={{ color: TOKEN_COLORS[t.type] }}>{t.text}</span>
            ))}
          </pre>
        </div>
      )}

      {/* Active overrides */}
      {activeOverrides.length > 0 && (
        <div style={{ padding: '0.5rem 0.75rem', borderBottom: '1px solid #1e293b' }}>
          <div style={{ color: '#f59e0b', fontSize: '0.68rem', textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '0.3rem' }}>
            Active Overrides
          </div>
          {activeOverrides.map((o, i) => (
            <div key={i} style={{
              background: '#1a1200',
              border: '1px solid #78350f',
              borderRadius: '4px',
              padding: '0.3rem 0.4rem',
              marginBottom: '0.3rem',
            }}>
              <div style={{ color: '#fef08a', fontSize: '0.75rem', fontWeight: 600 }}>{o.reason}</div>
              <div style={{ display: 'flex', gap: '0.75rem', marginTop: '0.15rem', fontSize: '0.68rem', color: '#92400e' }}>
                {o.createdBy && <span>by {o.createdBy}</span>}
                {o.stage && <span>stage: {o.stage}</span>}
                {o.expiresAt && <span>expires {relativeTime(o.expiresAt)}</span>}
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Expired overrides (audit records) */}
      {expiredOverrides.length > 0 && (
        <div style={{ padding: '0.5rem 0.75rem' }}>
          <div style={{ color: 'var(--color-text-faint)', fontSize: '0.68rem', textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '0.3rem' }}>
            Override History
          </div>
          {expiredOverrides.map((o, i) => (
            <div key={i} style={{
              background: 'var(--color-bg)',
              border: '1px solid #1e293b',
              borderRadius: '4px',
              padding: '0.3rem 0.4rem',
              marginBottom: '0.3rem',
              opacity: 0.7,
            }}>
              <div style={{ color: '#64748b', fontSize: '0.75rem' }}>{o.reason}</div>
              <div style={{ display: 'flex', gap: '0.75rem', marginTop: '0.15rem', fontSize: '0.68rem', color: 'var(--color-border)' }}>
                {o.createdBy && <span>by {o.createdBy}</span>}
                {o.expiresAt && <span title={o.expiresAt}>expired</span>}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
