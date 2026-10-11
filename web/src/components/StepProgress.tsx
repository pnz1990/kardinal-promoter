// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/StepProgress.tsx — a promotion's sub-steps from status.steps[], with their
// timing bars (UI-STEPTIME-01), shown by the node details and the stage drawer.
import type { PromotionStep, StepStatus } from '../types'
import { stepTimeline } from '../stepTimeline'
import '../styles/NodeDetail.css'

/** How one entry of status.steps[] is drawn; its colour comes from data-step-state (NodeDetail.css). */
const STEP_VIEW: Record<StepStatus['state'], { icon: string; label: string }> = {
  Completed: { icon: '✓', label: 'done' },
  InProgress: { icon: '▶', label: 'running' },
  Failed: { icon: '✗', label: 'failed' },
  Pending: { icon: '○', label: 'not started' },
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
    <div className="node-detail__section">
      <h4 className="node-detail__heading node-detail__heading--spaced">
        Promotion Steps
      </h4>
      {list.length === 0 ? (
        <div className="node-detail__hint node-detail__hint--flush">
          Steps appear here once this promotion starts.
        </div>
      ) : (
        <ol aria-label="Promotion steps" className="node-detail__steps">
          {list.map((s, i) => {
            const shown = effectiveStepState(s, step.state)
            const view = STEP_VIEW[shown] ?? STEP_VIEW.Pending
            const note = stepNote(s, shown, step)
            const current = shown === 'InProgress' || shown === 'Failed'
            return (
              <li
                key={`${i}-${s.name}`}
                data-step-state={shown}
                data-current={current || undefined}
                className="node-detail__step"
              >
                <span aria-hidden="true" className="node-detail__step-icon">
                  {view.icon}
                </span>
                <span className="node-detail__step-name">
                  {s.name}
                </span>
                <span className="sr-only">{view.label}</span>
                {note && (
                  <span className="node-detail__step-note">
                    {note}
                  </span>
                )}
                {hasBars && (
                  // Waterfall: where this step sits in the promotion's time span.
                  <span aria-hidden="true" data-testid="step-bar-track" className="node-detail__bar-track">
                    {bars[i] && (
                      <span
                        data-testid="step-bar"
                        data-offset={bars[i]!.offset.toFixed(1)}
                        data-width={bars[i]!.width.toFixed(1)}
                        className="node-detail__bar"
                        style={{ left: `${bars[i]!.offset}%`, width: `${bars[i]!.width}%` }}
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
