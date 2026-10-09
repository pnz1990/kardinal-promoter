// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/PolicyGatesPanel.tsx — Collapsible panel with the policy gates of
// the bundle on screen, each with its state and CEL expression (#340). App
// passes only that bundle's gate instances, never the templates.
import { useState, useEffect } from 'react'
import type { GateState, PolicyGate } from '../types'
import { HealthChip, healthChipColors, type HealthState } from './HealthChip'
import { ApprovalQuorum } from './ApprovalQuorum'

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

/**
 * The state shown for a gate, decided by the UI API (graph.GateState) so it
 * matches the DAG node. Only Block holds the bundle back, the rule the
 * sidebar's Blocked count uses (E2E-R19).
 */
function gateState(gate: PolicyGate): GateState {
  return gate.state
}

const count = (gates: PolicyGate[], ...states: GateState[]) =>
  gates.filter(g => states.includes(gateState(g))).length

/**
 * Collapsed summary chip: N blocked, else N waiting (not ready, not holding
 * the bundle, or not evaluated yet), else N superseded, else N rejected, else
 * N passing.
 */
function GateSummaryChip({ gates }: { gates: PolicyGate[] }) {
  const blocked = count(gates, 'Block')
  const waiting = count(gates, 'Waiting', 'Pending')
  const superseded = count(gates, 'Superseded')
  const rejected = count(gates, 'Rejected')
  const total = gates.length
  if (total === 0) return null
  // meaning: read by screen readers and shown on hover.
  const [health, label, meaning]: [HealthState, string, string | undefined] = blocked > 0
    ? ['Error', `${blocked} blocked`, undefined]
    : waiting > 0
    ? ['Pending', `${waiting} waiting`, 'not ready, not holding the bundle']
    : superseded > 0
    ? ['Unknown', `${superseded} superseded`, 'bundle superseded, not evaluated again']
    : rejected > 0
    ? ['Unknown', `${rejected} rejected`, 'bundle rejected, not evaluated again']
    : ['Ready', `${total} passing`, undefined]
  const { bg, text, border } = healthChipColors(health)
  return (
    <span title={meaning} style={{
      fontSize: '0.65rem',
      background: bg,
      color: text,
      border: `1px solid ${border}`,
      borderRadius: '4px',
      padding: '1px 6px',
      marginLeft: '0.5rem',
    }}>
      {label}
      {meaning && <span className="sr-only">, {meaning}</span>}
    </span>
  )
}

export function PolicyGatesPanel({ gates, loading }: Props) {
  const blockedCount = count(gates, 'Block')
  // #524: auto-expand when any gate holds the bundle — the blocked state is the
  // most important information on screen and should not be hidden behind a
  // click. A waiting gate is not what the bundle waits on, so it does not.
  const [open, setOpen] = useState(false)
  useEffect(() => {
    if (blockedCount > 0) setOpen(true)
  }, [blockedCount])

  if (loading) {
    return (
      <div style={{ marginBottom: '0.75rem' }} aria-label="policy-gates-loading" aria-busy="true">
        <style>{`
          @keyframes shimmer-pg {
            0% { background-position: 200% 0; }
            100% { background-position: -200% 0; }
          }
        `}</style>
        {/* Skeleton header bar mimicking the accordion button */}
        {[70, 50].map((w, i) => (
          <div
            key={i}
            style={{
              height: '16px',
              borderRadius: '3px',
              background: 'linear-gradient(90deg, var(--color-surface) 25%, var(--color-surface-2) 50%, var(--color-surface) 75%)',
              backgroundSize: '200% 100%',
              animation: 'shimmer-pg 1.5s infinite',
              marginBottom: '0.4rem',
              width: `${w}%`,
            }}
          />
        ))}
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
          color: 'var(--color-accent)',
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
          borderLeft: '2px solid var(--color-border)',
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
                borderBottom: '1px solid var(--color-border-muted)',
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                <HealthChip
                  state={gateState(gate)}
                  nodeType="PolicyGate"
                  size="sm"
                />
                <span style={{ fontSize: '0.8rem', color: 'var(--color-text)', fontWeight: 500 }}>
                  {gate.name}
                </span>
                <span style={{ fontSize: '0.65rem', color: 'var(--color-text-faint)', marginLeft: 'auto' }}>
                  {gate.namespace} · {formatAge(gate.lastEvaluatedAt)}
                </span>
              </div>
              {gate.expression && (
                <code style={{
                  fontSize: '0.72rem',
                  color: 'var(--color-code)',
                  background: 'var(--color-bg)',
                  border: '1px solid var(--color-border-muted)',
                  borderRadius: '3px',
                  padding: '2px 6px',
                  fontFamily: 'monospace',
                  wordBreak: 'break-all',
                }}>
                  {gate.expression}
                </code>
              )}
              {!gate.ready && gate.reason && (
                <div style={{
                  fontSize: '0.7rem',
                  color: gateState(gate) === 'Block' ? 'var(--color-error)' : 'var(--color-text-muted)',
                }}>
                  {gate.reason}
                </div>
              )}
              {gate.approval && (
                <ApprovalQuorum approval={gate.approval} bundle={gate.bundle} environment={gate.environment} />
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
