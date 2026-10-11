// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/PolicyGatesPanel.tsx — Collapsible panel with the policy gates of
// the bundle on screen, each with its state and CEL expression (#340). App
// passes only that bundle's gate instances, never the templates.
import { useState, useEffect } from 'react'
import type { GateState, PolicyGate } from '../types'
import { HealthChip, type HealthState } from './HealthChip'
import '../styles/PolicyGatesPanel.css'
import { ApprovalQuorum } from './ApprovalQuorum'

interface Props {
  gates: PolicyGate[]
  loading?: boolean
  /** Called after an approve, reject or revoke from an approval gate, so the
   *  parent refreshes the gates. */
  onDecided?: () => void
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
  return (
    <span title={meaning} className="gates-panel__summary" data-health={health}>
      {label}
      {meaning && <span className="sr-only">, {meaning}</span>}
    </span>
  )
}

export function PolicyGatesPanel({ gates, loading, onDecided }: Props) {
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
      <div className="gates-panel" aria-label="policy-gates-loading" aria-busy="true">
        {/* Skeleton header bar mimicking the accordion button */}
        {[0, 1].map(i => (
          <div key={i} className="gates-panel__skeleton" />
        ))}
      </div>
    )
  }

  if (gates.length === 0) return null

  return (
    <div className="gates-panel">
      <button
        onClick={() => setOpen(o => !o)}
        className="gates-panel__toggle"
        aria-expanded={open}
      >
        {open ? '▾' : '▸'} Policy Gates ({gates.length})
        <GateSummaryChip gates={gates} />
      </button>

      {open && (
        <div className="gates-panel__list">
          {gates.map(gate => (
            <div
              key={`${gate.namespace}/${gate.name}`}
              className="gates-panel__gate"
            >
              <div className="gates-panel__gate-head">
                <HealthChip
                  state={gateState(gate)}
                  nodeType="PolicyGate"
                  size="sm"
                />
                <span className="gates-panel__name">
                  {gate.name}
                </span>
                <span className="gates-panel__meta">
                  {gate.namespace} · {formatAge(gate.lastEvaluatedAt)}
                </span>
              </div>
              {gate.expression && (
                <code className="gates-panel__expr">
                  {gate.expression}
                </code>
              )}
              {!gate.ready && gate.reason && (
                <div className="gates-panel__reason" data-blocking={gateState(gate) === 'Block' || undefined}>
                  {gate.reason}
                </div>
              )}
              {gate.approval && (
                <ApprovalQuorum approval={gate.approval} bundle={gate.bundle} environment={gate.environment}
                  namespace={gate.namespace} onDecided={onDecided} />
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
