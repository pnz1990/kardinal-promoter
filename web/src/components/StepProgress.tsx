// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/StepProgress.tsx — a promotion's sub-steps from status.steps[], with their
// timing bars (UI-STEPTIME-01), shown by the node details and the stage drawer.
import type { PromotionStep, StepStatus } from '../types'
import { stepTimeline } from '../stepTimeline'

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
export function StepProgress({ step }: { step: PromotionStep }) {
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
                  fontFamily: 'var(--k-font-mono)',
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
