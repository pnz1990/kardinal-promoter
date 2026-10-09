// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/NodeDetail.tsx — Detail panel shown when a DAG node is clicked.
// For PolicyGate nodes: shows CEL expression and last evaluated timestamp.
// For PromotionStep nodes: shows the step sequence the controller resolved for
// this promotion (status.steps[]), conditions, events, and promote/rollback.
// Promote and Roll back follow the same rule as the pipeline lane
// (pipelineActions.ts), so they only show where they can do something.
//
// #326: NodeDetail is rendered as a sibling of the DAGView in App.tsx's flex
// layout, so it shifts the DAG left rather than overlapping it.
//
// Steps are passed as a prop (managed by App's 5s poll) rather than fetched
// independently, eliminating the 3s polling race condition (issue #322).
//
// #333: CEL expression syntax highlighting.
//
// #527: EventsPanel — K8s events for the selected step, refreshed with each poll.
import { useState, useEffect } from 'react'
import type { GraphEdge, GraphNode, PromotionStep, Bundle, StepStatus } from '../types'
import { HealthChip } from './HealthChip'
import { api } from '../api/client'
import EventsPanel, { type StepEvent } from './EventsPanel'
import CopyButton from './CopyButton'
import { stepTimeline } from '../stepTimeline'
import { PipelineActionDialog, type PipelineActionKind } from './PipelineActionDialog'
import { formatElapsedSince } from '../timeFormat'
import { isHttpURL } from '../prLink'
import { canPromote, canRollback } from '../pipelineActions'

interface Props {
  node: GraphNode | null
  onClose: () => void
  /** Bundle name — needed for fallback step lookup if steps prop not provided. */
  bundleName?: string
  /** Pipeline name — needed for the promote action. */
  pipelineName?: string
  /** Namespace of the pipeline. Defaults to 'default'. */
  namespace?: string
  /** Steps for the active bundle — from parent poll, no independent fetch needed (#322). */
  steps?: PromotionStep[]
  /** Active bundle — used for the image diff preview in Promoting/WaitingForMerge state (#563). */
  activeBundle?: Bundle
  /** Called after a promote or rollback request succeeds, so the parent can refresh. */
  onActionDone?: () => void
  /** Graph nodes of the active bundle: the current state of this node and its upstream
   *  environments. Without them Promote stays hidden (the upstream state is unknown). */
  nodes?: GraphNode[]
  /** Graph edges of the active bundle — used to find the upstream environments. */
  edges?: GraphEdge[]
}

/** PromotionStep states in which work is running (Go never reports "Running"). */
// RollingBack is an end state: the step failed health and a rollback Bundle
// took over; the rollback Bundle's own step is the one in flight.
const IN_FLIGHT_STATES = new Set(['Promoting', 'WaitingForMerge', 'HealthChecking', 'Verifying'])

/** Format an ISO timestamp to a human-readable string. */
function formatTimestamp(iso: string): string {
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return iso
    const now = Date.now()
    const diffMs = now - d.getTime()
    const diffSec = Math.floor(diffMs / 1000)
    if (diffSec < 60) return `${diffSec}s ago`
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`
    return d.toLocaleString()
  } catch {
    return iso
  }
}

// ── #333: CEL syntax highlighter ─────────────────────────────────────────────
// Tokenizes a CEL expression for basic keyword/identifier/string/operator coloring.
// Adapted from kro-ui KroCodeBlock pattern, simplified for CEL-only use.

type CELTokenType = 'keyword' | 'function' | 'string' | 'number' | 'operator' | 'boolean' | 'identifier' | 'plain'
interface CELToken { type: CELTokenType; text: string }

const CEL_KEYWORDS = new Set(['true', 'false', 'null', 'in', 'has', 'all', 'exists', 'map', 'filter', 'exists_one', 'type'])
const CEL_KRO_FUNCTIONS = new Set([
  'json.marshal', 'json.unmarshal', 'lists.setAtIndex',
  'lists.insertAtIndex', 'lists.removeAtIndex', 'random.seededInt', 'random.seededString',
  'changewindow.isAllowed', 'changewindow.isBlocked', 'lowerAscii', 'contains', 'startsWith', 'endsWith',
  'matches', 'size', 'int', 'uint', 'double', 'string', 'bytes', 'duration', 'timestamp',
])

function tokenizeCEL(expr: string): CELToken[] {
  const tokens: CELToken[] = []
  let i = 0
  const len = expr.length

  while (i < len) {
    const ch = expr[i]

    // String literal: single or double quoted
    if (ch === '"' || ch === "'") {
      const quote = ch
      let j = i + 1
      while (j < len && expr[j] !== quote) {
        if (expr[j] === '\\') j++ // skip escape
        j++
      }
      j++ // closing quote
      tokens.push({ type: 'string', text: expr.slice(i, j) })
      i = j
      continue
    }

    // Number
    if (/[0-9]/.test(ch) || (ch === '-' && /[0-9]/.test(expr[i + 1] ?? ''))) {
      let j = i + 1
      while (j < len && /[0-9.]/.test(expr[j])) j++
      tokens.push({ type: 'number', text: expr.slice(i, j) })
      i = j
      continue
    }

    // Operator / punctuation
    if (/[!&|=<>+\-*/%()[\]{},.:@]/.test(ch)) {
      // Multi-char operators: !=, ==, <=, >=, &&, ||
      const two = expr.slice(i, i + 2)
      if (['!=', '==', '<=', '>=', '&&', '||'].includes(two)) {
        tokens.push({ type: 'operator', text: two })
        i += 2
        continue
      }
      tokens.push({ type: 'operator', text: ch })
      i++
      continue
    }

    // Whitespace
    if (/\s/.test(ch)) {
      let j = i + 1
      while (j < len && /\s/.test(expr[j])) j++
      tokens.push({ type: 'plain', text: expr.slice(i, j) })
      i = j
      continue
    }

    // Identifier or keyword
    if (/[a-zA-Z_$]/.test(ch)) {
      let j = i + 1
      while (j < len && /[a-zA-Z0-9_.[\]]/.test(expr[j])) j++
      const word = expr.slice(i, j)
      // check if it's a kro function call prefix (e.g. "json.unmarshal")
      let type: CELTokenType = 'identifier'
      if (CEL_KEYWORDS.has(word.toLowerCase())) {
        type = word === 'true' || word === 'false' ? 'boolean' : 'keyword'
      } else if (CEL_KRO_FUNCTIONS.has(word)) {
        type = 'function'
      } else if (/^[a-zA-Z_][a-zA-Z0-9_]*$/.test(word) && j < len && expr[j] === '(') {
        // Function call: word followed by (
        type = 'function'
      }
      tokens.push({ type, text: word })
      i = j
      continue
    }

    // Fallback
    tokens.push({ type: 'plain', text: ch })
    i++
  }
  return tokens
}

const CEL_TOKEN_COLORS: Record<CELTokenType, string> = {
  keyword: 'var(--color-warning)',     // true/false/in/has etc
  function: 'var(--color-info)',       // function calls
  string: 'var(--color-success)',      // string literals
  number: 'var(--color-accent)',       // numbers
  operator: 'var(--color-text)',       // operators
  boolean: 'var(--color-warning)',     // boolean literals
  identifier: 'var(--color-code)',     // identifiers (bundle.X, schedule.X)
  plain: 'var(--color-text-muted)',    // whitespace and anything else
}

/** #333: Syntax-highlighted CEL expression block. */
function CELBlock({ expression }: { expression: string }) {
  const tokens = tokenizeCEL(expression)
  return (
    <code style={{
      display: 'block',
      background: 'var(--color-bg)',
      borderRadius: '4px',
      padding: '0.5rem 0.75rem',
      fontSize: '0.8rem',
      fontFamily: 'monospace',
      wordBreak: 'break-all',
      whiteSpace: 'pre-wrap',
    }}>
      {tokens.map((token, idx) => (
        <span key={idx} style={{ color: CEL_TOKEN_COLORS[token.type] }}>
          {token.text}
        </span>
      ))}
    </code>
  )
}
// ── end CEL syntax highlighter ────────────────────────────────────────────────


/** How one entry of status.steps[] is drawn. */
const STEP_VIEW: Record<StepStatus['state'], { icon: string; color: string; label: string }> = {
  Completed: { icon: '✓', color: 'var(--color-success)', label: 'done' },
  InProgress: { icon: '▶', color: 'var(--color-warning)', label: 'running' },
  Failed: { icon: '✗', color: 'var(--color-error)', label: 'failed' },
  Pending: { icon: '○', color: 'var(--color-text-faint)', label: 'not started' },
}

/** "850ms", "2.0s", "3m 4s". */
function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  const sec = ms / 1000
  if (sec < 60) return `${sec.toFixed(1)}s`
  return `${Math.floor(sec / 60)}m ${Math.round(sec % 60)}s`
}

/**
 * The state to draw for a step. A step the engine left InProgress is shown as
 * failed once the promotion itself has failed (e.g. a health-check timeout is
 * recorded on the PromotionStep, not on the step entry).
 */
function effectiveStepState(s: StepStatus, promotionState: string): StepStatus['state'] {
  if (s.state === 'InProgress' && (promotionState === 'Failed' || promotionState === 'AbortedByAlarm')) return 'Failed'
  return s.state
}

function stepNote(s: StepStatus, shown: StepStatus['state'], promotion: PromotionStep): string | null {
  if (shown === 'Failed') return s.message || (s.state === 'InProgress' ? promotion.message ?? null : null)
  if (shown === 'InProgress') {
    if (promotion.state === 'WaitingForMerge') return 'waiting for merge'
    if (promotion.state === 'HealthChecking') return 'checking health'
    // Verifying waits for post-deploy hooks and Argo Rollouts analyses.
    if (promotion.state === 'Verifying') return 'verifying: post-deploy hooks and analyses'
    return null
  }
  if (shown === 'Completed' && s.durationMs) return formatDuration(s.durationMs)
  return null
}

/**
 * Step progress for a PromotionStep node, from status.steps[]: the sequence
 * the controller resolved for this promotion (it differs per update strategy,
 * bundle type, approval mode and layout, so it is never hard-coded here).
 */
function StepProgress({ step }: { step: PromotionStep }) {
  const list = step.steps ?? []
  const bars = stepTimeline(list)
  const hasBars = bars.some(b => b !== null)
  return (
    <div style={{ marginBottom: '0.75rem' }}>
      <h4 style={{ fontSize: '0.8rem', color: 'var(--color-text)', marginBottom: '0.5rem' }}>
        Promotion Steps
      </h4>
      {list.length === 0 ? (
        <div style={{ fontSize: '0.8rem', color: 'var(--color-text-faint)', fontStyle: 'italic' }}>
          Steps appear here once this promotion starts.
        </div>
      ) : (
        <ol
          aria-label="Promotion steps"
          style={{
            listStyle: 'none',
            margin: 0,
            background: 'var(--color-bg)',
            border: '1px solid var(--color-border-muted)',
            borderRadius: '4px',
            padding: '0.5rem 0.75rem',
          }}
        >
          {list.map((s, i) => {
            const shown = effectiveStepState(s, step.state)
            const view = STEP_VIEW[shown] ?? STEP_VIEW.Pending
            const note = stepNote(s, shown, step)
            const current = shown === 'InProgress' || shown === 'Failed'
            return (
              <li
                key={`${i}-${s.name}`}
                data-step-state={shown}
                style={{
                  display: 'flex',
                  flexWrap: 'wrap',
                  alignItems: 'baseline',
                  gap: '0.5rem',
                  marginBottom: i < list.length - 1 ? '0.3rem' : 0,
                }}
              >
                <span aria-hidden="true" style={{ fontSize: '0.7rem', color: view.color, width: '12px', flexShrink: 0 }}>
                  {view.icon}
                </span>
                <span style={{
                  fontSize: '0.75rem',
                  color: shown === 'Pending' ? 'var(--color-text-faint)' : 'var(--color-text)',
                  fontFamily: 'monospace',
                  fontWeight: current ? 600 : 400,
                }}>
                  {s.name}
                </span>
                <span className="sr-only">{view.label}</span>
                {note && (
                  <span style={{
                    fontSize: '0.68rem',
                    color: shown === 'Completed' ? 'var(--color-text-faint)' : view.color,
                    overflowWrap: 'anywhere',
                  }}>
                    {note}
                  </span>
                )}
                {hasBars && (
                  // Waterfall: where this step sits in the promotion's time span.
                  <span
                    aria-hidden="true"
                    data-testid="step-bar-track"
                    style={{ flexBasis: '100%', height: '3px', marginLeft: '20px', background: 'var(--color-border-muted)', borderRadius: '2px', position: 'relative' }}
                  >
                    {bars[i] && (
                      <span
                        data-testid="step-bar"
                        data-offset={bars[i]!.offset.toFixed(1)}
                        data-width={bars[i]!.width.toFixed(1)}
                        style={{
                          position: 'absolute', top: 0, bottom: 0, borderRadius: '2px',
                          left: `${bars[i]!.offset}%`, width: `${bars[i]!.width}%`,
                          background: view.color,
                          opacity: shown === 'Completed' ? 0.75 : 1,
                        }}
                      />
                    )}
                  </span>
                )}
              </li>
            )
          })}
        </ol>
      )}
    </div>
  )
}

const labelStyle = { color: 'var(--color-text)' } as const

export function NodeDetail({ node, onClose, bundleName, pipelineName, namespace = 'default', steps, activeBundle, onActionDone, nodes = [], edges = [] }: Props) {
  const [stepDetail, setStepDetail] = useState<PromotionStep | null>(null)
  const [stepLoading, setStepLoading] = useState(false)
  // Promote / rollback: the dialog being confirmed and the last result.
  const [confirmAction, setConfirmAction] = useState<PipelineActionKind | null>(null)
  const [actionResult, setActionResult] = useState<string | null>(null)
  const [celValid, setCelValid] = useState<boolean | null>(null)
  const [celError, setCelError] = useState<string | null>(null)
  // #330: tick counter to update elapsed timers every second while panel is open.
  const [, setTick] = useState(0)
  // #527: Kubernetes events for the selected PromotionStep.
  const [stepEvents, setStepEvents] = useState<StepEvent[] | null>(null)

  const isPolicyGate = node?.type === 'PolicyGate'
  const isPromotionStep = node?.type === 'PromotionStep'
  const promotionState = stepDetail?.state ?? node?.state ?? ''
  const isActiveNode = isPromotionStep && IN_FLIGHT_STATES.has(promotionState)
  // The namespace of the bundle on screen; its name may repeat in others.
  const bundleNs = activeBundle?.namespace ?? namespace

  // A different node starts with no dialog and no stale result.
  useEffect(() => {
    setConfirmAction(null)
    setActionResult(null)
  }, [node?.id])

  // #330: tick every second so elapsed timers stay current.
  useEffect(() => {
    if (!isActiveNode) return
    const id = setInterval(() => setTick(t => t + 1), 1000)
    return () => clearInterval(id)
  }, [isActiveNode])

  /** Validate the CEL expression of a PolicyGate node; re-run when the expression changes. */
  const expression = isPolicyGate ? node?.expression : undefined
  useEffect(() => {
    setCelValid(null)
    setCelError(null)
    if (!expression) return
    let ignore = false
    api.validateCEL(expression)
      .then(res => {
        if (ignore) return
        setCelValid(res.valid)
        setCelError(res.error ?? null)
      })
      .catch(() => {
        // Validation is advisory: leave the chip empty when the check itself fails.
      })
    return () => { ignore = true }
  }, [expression])

  // Derive step detail from parent-provided steps prop (no independent fetch/poll).
  // Falls back to a one-shot fetch if steps prop is not provided.
  useEffect(() => {
    if (!node || !isPromotionStep) {
      setStepDetail(null)
      return
    }
    // Prefer prop-provided steps (updated by parent 5s poll).
    if (steps) {
      const match = steps.find(s => s.environment === node.environment)
      setStepDetail(match ?? null)
      return
    }
    // Fallback: one-shot fetch (no polling — parent handles refresh).
    if (!bundleName) return
    let ignore = false
    setStepLoading(true)
    api.getSteps(bundleName, bundleNs)
      .then(ss => {
        if (ignore) return
        const match = ss.find(s => s.environment === node.environment)
        setStepDetail(match ?? null)
      })
      .catch(() => { if (!ignore) setStepDetail(null) })
      .finally(() => { if (!ignore) setStepLoading(false) })
    return () => { ignore = true }
  }, [node?.id, isPromotionStep, steps, bundleName, bundleNs])

  // #527: Kubernetes events for the selected PromotionStep. stepDetail is a new
  // object after every parent poll, so events refresh on the same 5s cadence.
  const stepName = stepDetail?.name
  const stepNs = stepDetail?.namespace ?? namespace
  useEffect(() => {
    setStepEvents(null)
  }, [stepName, stepNs])
  useEffect(() => {
    if (!isPromotionStep || !stepName) return
    let ignore = false
    api.getStepEvents(stepNs, stepName)
      .then(evs => { if (!ignore) setStepEvents(evs) })
      .catch(() => { if (!ignore) setStepEvents(prev => prev ?? []) })
    return () => { ignore = true }
  }, [isPromotionStep, stepName, stepNs, stepDetail])

  if (!node) return null

  // #330: elapsed time since the PromotionStep started (Go sets startedAt on step nodes).
  const elapsedDisplay = isActiveNode
    ? formatElapsedSince(node.startedAt ?? node.lastEvaluatedAt)
    : ''
  const showsImages = IN_FLIGHT_STATES.has(node.state)
  // The selected node is a copy taken at click time; the graph has its current state.
  const liveNode = nodes.find(n => n.id === node.id) ?? node
  const canAct = isPromotionStep && !!pipelineName && !!node.environment
  const showPromote = canAct && canPromote(liveNode, nodes, edges)
  const showRollback = canAct && canRollback(liveNode)

  return (
    <div data-testid="node-detail" style={{
      width: '340px',
      minWidth: '300px',
      height: '100%',
      background: 'var(--color-surface)',
      borderLeft: '1px solid var(--color-border)',
      padding: '1.5rem',
      overflowY: 'auto',
      flexShrink: 0,
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '1rem' }}>
        <h3 style={{ fontSize: '1rem', fontWeight: 600, margin: 0 }}>{node.label}</h3>
        <button
          onClick={onClose}
          style={{
            background: 'none',
            border: 'none',
            color: 'var(--color-text-muted)',
            cursor: 'pointer',
            fontSize: '1.25rem',
            lineHeight: 1,
          }}
          aria-label="Close"
        >
          ×
        </button>
      </div>

      <div style={{ marginBottom: '0.75rem' }}>
        <HealthChip state={node.state} nodeType={node.type} size="md" />
      </div>

      <div style={{ fontSize: '0.85rem', color: 'var(--color-text-muted)', marginBottom: '0.5rem' }}>
        <strong style={labelStyle}>Type:</strong> {node.type}
      </div>
      <div style={{ fontSize: '0.85rem', color: 'var(--color-text-muted)', marginBottom: '0.5rem' }}>
        <strong style={labelStyle}>Environment:</strong> {node.environment}
      </div>

      {/* #330: Elapsed duration timer for active PromotionStep nodes */}
      {isActiveNode && (
        <div style={{ fontSize: '0.85rem', color: 'var(--color-warning)', marginBottom: '0.5rem' }}>
          <strong>Elapsed:</strong>{' '}
          {elapsedDisplay || 'running…'}
        </div>
      )}

      {/* Promote / rollback — only where they can do something (pipelineActions.ts).
          The result stays visible after the step moves on and the buttons go. */}
      {canAct && (showPromote || showRollback || actionResult) && (
        <div style={{ marginBottom: '0.75rem', display: 'flex', flexDirection: 'column', gap: '0.4rem' }}>
          {showPromote && (
            <button
              type="button"
              onClick={() => setConfirmAction('promote')}
              title={`Promote ${pipelineName} to ${node.environment}`}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '0.4rem',
                padding: '0.35rem 0.8rem',
                background: 'var(--color-accent-bg)',
                color: 'var(--color-accent)',
                border: '1px solid var(--color-accent)',
                borderRadius: '4px',
                cursor: 'pointer',
                fontSize: '0.8rem',
                fontWeight: 500,
              }}
            >
              <span aria-hidden="true">▶</span>
              <span>Promote to {node.environment}</span>
            </button>
          )}
          {/* Rollback button (#331) */}
          {showRollback && (
            <button
              type="button"
              onClick={() => setConfirmAction('rollback')}
              title={`Roll back ${node.environment} to the previous verified version`}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '0.4rem',
                padding: '0.35rem 0.8rem',
                background: 'transparent',
                color: 'var(--color-error)',
                border: '1px solid var(--color-error)',
                borderRadius: '4px',
                cursor: 'pointer',
                fontSize: '0.8rem',
                fontWeight: 500,
              }}
            >
              <span aria-hidden="true">↩</span>
              <span>Roll back {node.environment}</span>
            </button>
          )}
          {actionResult && (
            <div role="status" style={{ fontSize: '0.75rem', color: 'var(--color-success)' }}>
              {actionResult}
            </div>
          )}
        </div>
      )}

      {confirmAction && pipelineName && node.environment && (
        <PipelineActionDialog
          kind={confirmAction}
          pipelineName={pipelineName}
          environment={node.environment}
          namespace={namespace}
          onDone={message => {
            setConfirmAction(null)
            setActionResult(message)
            onActionDone?.()
          }}
          onCancel={() => setConfirmAction(null)}
        />
      )}

      {/* PromotionStep: step progress log */}
      {isPromotionStep && stepLoading && (
        <div style={{ marginBottom: '0.75rem' }} data-testid="step-skeleton">
          {/* #784: skeleton loading state — shimmer placeholders while step details load */}
          <style>{`
            @keyframes shimmer-nd {
              0% { background-position: 200% 0; }
              100% { background-position: -200% 0; }
            }
          `}</style>
          <span className="sr-only" role="status">
            Loading step details
          </span>
          {[75, 55, 65, 45].map((w, i) => (
            <div
              key={i}
              aria-hidden="true"
              style={{
                height: '20px',
                borderRadius: '4px',
                background: 'linear-gradient(90deg, var(--color-surface) 25%, var(--color-surface-2) 50%, var(--color-surface) 75%)',
                backgroundSize: '200% 100%',
                animation: 'shimmer-nd 1.5s infinite',
                marginBottom: '0.5rem',
                width: `${w}%`,
              }}
            />
          ))}
        </div>
      )}
      {isPromotionStep && !stepLoading && stepDetail && (
        <StepProgress step={stepDetail} />
      )}
      {isPromotionStep && !stepLoading && !stepDetail && !bundleName && (
        <div style={{ fontSize: '0.8rem', color: 'var(--color-text-faint)', marginBottom: '0.75rem', fontStyle: 'italic' }}>
          Step sequence available when promotion is active.
        </div>
      )}

      {/* PolicyGate: CEL expression display with server-side syntax validation */}
      {isPolicyGate && node.expression && (
        <div style={{ marginBottom: '0.75rem' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.4rem' }}>
            <h4 style={{ fontSize: '0.8rem', color: 'var(--color-text)', margin: 0 }}>
              CEL Expression
            </h4>
            {/* Syntax validity chip — populated by POST /api/v1/ui/validate-cel */}
            {celValid === true && (
              <span style={{
                fontSize: '0.65rem', background: 'var(--color-success-bg)', color: 'var(--color-success)',
                borderRadius: '4px', padding: '1px 6px',
              }}>✓ valid</span>
            )}
            {celValid === false && (
              <span
                style={{
                  fontSize: '0.65rem', background: 'var(--color-error-bg)', color: 'var(--color-error)',
                  borderRadius: '4px', padding: '1px 6px', cursor: 'help',
                }}
                title={celError ?? 'syntax error'}
              >✗ error</span>
            )}
            {/* #339: copy-to-clipboard for CEL expression */}
            <CopyButton text={node.expression} title="Copy expression" />
          </div>
          {/* #333: CEL expression with syntax highlighting */}
          <div style={{ border: `1px solid ${celValid === false ? 'var(--color-error)' : 'var(--color-border)'}`, borderRadius: '4px' }}>
            <CELBlock expression={node.expression} />
          </div>
          {celValid === false && celError && (
            <div style={{ fontSize: '0.7rem', color: 'var(--color-error)', marginTop: '0.25rem' }}>
              {celError}
            </div>
          )}
        </div>
      )}

      {/* PolicyGate: last evaluated timestamp */}
      {isPolicyGate && node.lastEvaluatedAt && (
        <div style={{ fontSize: '0.8rem', color: 'var(--color-text-muted)', marginBottom: '0.5rem' }}>
          <strong style={labelStyle}>Last evaluated:</strong>{' '}
          <span title={node.lastEvaluatedAt}>
            {formatTimestamp(node.lastEvaluatedAt)}
          </span>
        </div>
      )}

      {/* PolicyGate: placeholder when expression is not yet populated */}
      {isPolicyGate && !node.expression && (
        <div style={{ fontSize: '0.8rem', color: 'var(--color-text-faint)', marginBottom: '0.75rem', fontStyle: 'italic' }}>
          CEL expression will appear here when the graph API populates it.
        </div>
      )}

      {node.message && (
        <div style={{ fontSize: '0.85rem', color: 'var(--color-text-muted)', marginBottom: '0.75rem' }}>
          <strong style={labelStyle}>Message:</strong> {node.message}
        </div>
      )}

      {/* #563: Image diff preview — shown when environment is actively promoting.
          Surfaces "what will change" without requiring the operator to open the PR. */}
      {activeBundle?.images && activeBundle.images.length > 0 && showsImages && (
        <div style={{
          marginBottom: '0.75rem',
          padding: '0.6rem 0.75rem',
          background: 'var(--color-surface)',
          borderRadius: '6px',
          border: '1px solid var(--color-border)',
        }}>
          <div style={{ fontSize: '0.75rem', color: 'var(--color-text-muted)', marginBottom: '0.4rem', fontWeight: 600 }}>
            Promoting Image{activeBundle.images.length > 1 ? 's' : ''}
          </div>
          {activeBundle.images.map((img, i) => (
            <div key={i} style={{
              fontFamily: 'monospace',
              fontSize: '0.78rem',
              color: 'var(--color-text)',
              wordBreak: 'break-all',
              marginBottom: i < activeBundle.images!.length - 1 ? '0.3rem' : 0,
            }}>
              {img.repository && <span style={{ color: 'var(--color-code)' }}>{img.repository}</span>}
              {img.tag && <><span style={{ color: 'var(--color-text-muted)' }}>:</span><span style={{ color: 'var(--color-success)' }}>{img.tag}</span></>}
              {!img.tag && img.digest && (
                <><span style={{ color: 'var(--color-text-muted)' }}>@</span><span style={{ color: 'var(--color-accent)' }}>{img.digest.slice(0, 16)}…</span></>
              )}
            </div>
          ))}
        </div>
      )}

      {node.prURL && (
        <div style={{ marginBottom: '0.75rem' }}>
          {!isHttpURL(node.prURL) ? (
            // Only http(s) links are opened; anything else is shown as text.
            <div style={{ fontSize: '0.8rem', color: 'var(--color-text-muted)', overflowWrap: 'anywhere' }}>
              <strong style={labelStyle}>Pull request:</strong> <code>{node.prURL}</code>
            </div>
          ) : node.state === 'WaitingForMerge' ? (
            /* Prominent merge CTA when WaitingForMerge */
            <a
              href={node.prURL}
              target="_blank"
              rel="noopener noreferrer"
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: '0.4rem',
                padding: '0.4rem 0.9rem',
                background: 'var(--color-accent)',
                color: 'var(--color-bg)',
                borderRadius: '4px',
                fontSize: '0.82rem',
                fontWeight: 600,
                textDecoration: 'none',
              }}
            >
              <span aria-hidden="true">↗</span>
              <span>Open Pull Request — Merge to Deploy</span>
            </a>
          ) : (
            <a
              href={node.prURL}
              target="_blank"
              rel="noopener noreferrer"
              style={{ color: 'var(--color-accent)', fontSize: '0.85rem' }}
            >
              View Pull Request ↗
            </a>
          )}
        </div>
      )}

      {/* PromotionStep outputs from graph node */}
      {node.outputs && Object.keys(node.outputs).length > 0 && (
        <div style={{ marginBottom: '0.75rem' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.4rem' }}>
            <h4 style={{ fontSize: '0.8rem', color: 'var(--color-text)', margin: 0 }}>Step Outputs</h4>
            {/* #339: copy-to-clipboard for step outputs JSON */}
            <CopyButton text={JSON.stringify(node.outputs, null, 2)} title="Copy outputs as JSON" />
          </div>
          {Object.entries(node.outputs).map(([k, v]) => (
            <div key={k} style={{ fontSize: '0.8rem', color: 'var(--color-text-muted)', marginBottom: '0.2rem', overflowWrap: 'anywhere' }}>
              <span style={{ color: 'var(--color-code)' }}>{k}</span>:{' '}
              {isHttpURL(v) ? (
                <a href={v} target="_blank" rel="noopener noreferrer" style={{ color: 'var(--color-accent)' }} title={v}>
                  {v.length > 40 ? v.slice(0, 37) + '…' : v}
                </a>
              ) : (
                <span style={{ fontFamily: 'monospace' }}>{v}</span>
              )}
            </div>
          ))}
        </div>
      )}

      {/* #341/#529: Kubernetes conditions panel — show condition history from step status */}
      {isPromotionStep && stepDetail?.conditions && stepDetail.conditions.length > 0 && (
        <div style={{ marginBottom: '0.75rem' }}>
          {/* #529: Summary header — healthy count at a glance */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.4rem' }}>
            <h4 style={{ fontSize: '0.8rem', color: 'var(--color-text)', margin: 0 }}>
              Conditions
            </h4>
            {(() => {
              const total = stepDetail.conditions!.length
              const trueCount = stepDetail.conditions!.filter(c => c.status === 'True').length
              const allHealthy = trueCount === total
              return (
                <span
                  data-testid="conditions-summary"
                  style={{
                    fontSize: '0.7rem',
                    color: allHealthy ? 'var(--color-success)' : 'var(--color-error)',
                    fontFamily: 'monospace',
                  }}
                >
                  {trueCount}/{total} healthy
                </span>
              )
            })()}
          </div>
          <div style={{
            background: 'var(--color-bg)',
            border: '1px solid var(--color-border-muted)',
            borderRadius: '4px',
            padding: '0.4rem 0.6rem',
          }}>
            {stepDetail.conditions.map((cond, i) => (
              <div key={i} style={{
                display: 'flex',
                gap: '0.5rem',
                alignItems: 'flex-start',
                fontSize: '0.75rem',
                marginBottom: i < stepDetail.conditions!.length - 1 ? '0.3rem' : 0,
              }}>
                <span style={{
                  color: cond.status === 'True' ? 'var(--color-success)' : cond.status === 'False' ? 'var(--color-error)' : 'var(--color-text-muted)',
                  flexShrink: 0,
                  fontFamily: 'monospace',
                }}>
                  {cond.status === 'True' ? '✓' : cond.status === 'False' ? '✗' : '?'}
                </span>
                <div>
                  <span style={{ color: 'var(--color-text)', fontWeight: 600 }}>{cond.type}</span>
                  {/* #529: reason field — CamelCase code for quick triage */}
                  {cond.reason && (
                    <span
                      data-testid="condition-reason"
                      style={{
                        color: 'var(--color-text-faint)',
                        marginLeft: '0.4rem',
                        fontFamily: 'monospace',
                        fontSize: '0.7rem',
                      }}
                    >
                      [{cond.reason}]
                    </span>
                  )}
                  {cond.message && (
                    <span style={{ color: 'var(--color-text-muted)', marginLeft: '0.4rem' }}>— {cond.message}</span>
                  )}
                  {cond.lastTransitionTime && (
                    <div style={{ color: 'var(--color-text-faint)', fontSize: '0.68rem', marginTop: '0.1rem' }}>
                      {formatTimestamp(cond.lastTransitionTime)}
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* #527: Kubernetes events stream — shown for PromotionStep nodes */}
      {isPromotionStep && (
        <EventsPanel
          events={stepEvents}
          stepName={stepDetail?.name}
          namespace={stepNs}
        />
      )}
    </div>
  )
}
