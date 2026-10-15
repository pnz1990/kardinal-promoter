// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/PolicyGatesPanel.tsx — Collapsible panel with the policy gates of
// the bundle on screen, each with its state and CEL expression (#340). App
// passes only that bundle's gate instances, never the templates.
import { lazy, Suspense, useState, useEffect } from 'react'
import type { GateState, PolicyGate } from '../types'
import { HealthChip, type HealthState } from './HealthChip'
import { gateDisplay, gateLabel } from '../stage/stageModel'
import '../styles/PolicyGatesPanel.css'
import { ApprovalQuorum } from './ApprovalQuorum'
import { ago } from '../timeFormat'

// Verify gates are rarer than the rest of the panel: their quorum loads on demand.
const VerifyQuorum = lazy(() => import('./VerifyQuorum').then(m => ({ default: m.VerifyQuorum })))

interface Props {
  gates: PolicyGate[]
  loading?: boolean
  /** Called after an approve, reject or revoke from an approval gate, so the
   *  parent refreshes the gates. */
  onDecided?: () => void
  /** Fold the panel (the stage drawer is open and shows its environment's gates). */
  collapsed?: boolean
  /** The Bundle on its way into an environment (status.environments[].incomingBundle, else the
   *  pipeline's newest): `kardinal approve <pipeline> <env>` approves it. Gates of any other Bundle
   *  get the CLI line that names theirs. */
  incomingFor?: (environment: string) => string | undefined
}

function formatAge(iso: string | undefined): string {
  return ago(iso) || '—'
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
    ? ['Pending', `${waiting} waiting`, 'not ready, not holding the version']
    : superseded > 0
    ? ['Unknown', `${superseded} superseded`, 'version replaced by a newer one, not evaluated again']
    : rejected > 0
    ? ['Unknown', `${rejected} rejected`, 'version rejected, not evaluated again']
    : ['Ready', `${total} passing`, undefined]
  return (
    <span title={meaning} className="gates-panel__summary" data-health={health}>
      {label}
      {meaning && <span className="sr-only">, {meaning}</span>}
    </span>
  )
}

/** Whether gate's Bundle is the one on its way into its environment (unknown counts as yes). */
function isIncoming(gate: PolicyGate, incomingFor?: (environment: string) => string | undefined): boolean {
  const incoming = gate.environment ? incomingFor?.(gate.environment) : undefined
  return !incoming || gate.bundle === incoming
}

export function PolicyGatesPanel({ gates, loading, onDecided, collapsed, incomingFor }: Props) {
  const blockedCount = count(gates, 'Block')
  // #524: auto-expand when any gate holds the bundle — the blocked state is the
  // most important information on screen and should not be hidden behind a
  // click. A waiting gate is not what the bundle waits on, so it does not.
  const [open, setOpen] = useState(false)
  useEffect(() => {
    if (blockedCount > 0 && !collapsed) setOpen(true)
  }, [blockedCount, collapsed])
  // The stage drawer shows the gates of the environment it is open on, so the
  // panel folds away while it is open (U3).
  useEffect(() => {
    if (collapsed) setOpen(false)
  }, [collapsed])

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
        {open ? '▾' : '▸'} Gates ({gates.length})
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
                {/* The stage model's words (gateDisplay): an approval still collecting its quorum
                    reads "Waiting for approval", as on the map and in the drawer. */}
                <HealthChip
                  state={gateDisplay(gate).value}
                  nodeType="PolicyGate"
                  size="sm"
                  label={gateDisplay(gate).label}
                />
                <span className="gates-panel__name" title={gate.name}>
                  {gateLabel(gate)}
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
                <ApprovalQuorum approval={gate.approval} pipeline={gate.pipeline} bundle={gate.bundle} environment={gate.environment}
                  current={isIncoming(gate, incomingFor)}
                  namespace={gate.namespace} onDecided={onDecided} />
              )}
              {gate.verify && (
                <Suspense fallback={null}>
                  <VerifyQuorum verify={gate.verify} pipeline={gate.pipeline} namespace={gate.namespace} />
                </Suspense>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
