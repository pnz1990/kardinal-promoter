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

// components/StageDetailPanel.tsx — Per-stage approval workflow detail (#463).
// Shows step states, bake countdown timer, PR URL, and integration test pass rates.
// Opened by clicking a PromotionStep node in the DAG. Closes on outside click or Escape.
import { useEffect, useRef, useCallback } from 'react'
import type { GraphNode, PromotionStep } from '../types'
import { HealthChip } from './HealthChip'

interface Props {
  /** The DAG node that was clicked — provides environment and state context. */
  node: GraphNode
  /** Steps for the active bundle scoped to this stage's environment. */
  steps: PromotionStep[]
  /** Called when the panel should close. */
  onClose: () => void
}

/** #501: Format bake progress as a percentage string for the progress bar. */
export function bakePct(elapsed: number, target: number): number {
  if (target <= 0) return 0
  return Math.min(100, Math.round((elapsed / target) * 100))
}

/** #501: Format a bake countdown string: "12m / 30m (40%)" */
export function formatBakeCountdown(elapsed: number, target: number): string {
  if (target <= 0) return ''
  const pct = bakePct(elapsed, target)
  return `${elapsed}m / ${target}m (${pct}%)`
}

/** #501: List of promotion sub-step types in execution order (for display). */
const STEP_SEQUENCE = [
  { type: 'git-clone',          label: 'Git clone' },
  { type: 'kustomize-set-image', label: 'Set image' },
  { type: 'helm-set-image',      label: 'Helm set image' },
  { type: 'git-commit',          label: 'Git commit' },
  { type: 'git-push',            label: 'Git push' },
  { type: 'open-pr',             label: 'Open PR' },
  { type: 'wait-for-merge',      label: 'Wait for merge' },
  { type: 'health-check',        label: 'Health check' },
]

/** #501: Get step icon and color based on current index position. */
export function stepIconFor(index: number, currentIndex: number, active: boolean): {
  icon: string
  color: string
} {
  if (index < currentIndex) return { icon: '✓', color: 'var(--color-success)' }
  if (index === currentIndex) {
    if (!active) return { icon: '○', color: 'var(--color-text-faint)' }
    return { icon: '▶', color: '#f59e0b' }
  }
  return { icon: '○', color: 'var(--color-border)' }
}

export function StageDetailPanel({ node, steps, onClose }: Props) {
  const panelRef = useRef<HTMLDivElement>(null)

  // Find the PromotionStep for this environment
  const step = steps.find(s => s.environment === node.environment)

  // Close on Escape key
  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    if (e.key === 'Escape') onClose()
  }, [onClose])

  // Close on outside click
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

  const bakeTarget = step?.bakeTargetMinutes ?? 0
  const bakeElapsed = step?.bakeElapsedMinutes ?? 0
  const bakeResets = step?.bakeResets ?? 0
  const hasBake = bakeTarget > 0
  const currentStepIndex = step?.currentStepIndex ?? 0
  const isActive = step?.state === 'Promoting' || step?.state === 'HealthChecking' ||
    step?.state === 'WaitingForMerge'

  return (
    <div
      ref={panelRef}
      role="dialog"
      aria-label={`Stage detail: ${node.environment}`}
      style={{
        position: 'absolute',
        zIndex: 100,
        top: '40px',
        right: '16px',
        width: '320px',
        background: 'var(--color-bg)',
        border: '1px solid #1e293b',
        borderRadius: '6px',
        boxShadow: '0 4px 24px rgba(0,0,0,0.5)',
        fontSize: '0.8rem',
        color: '#cbd5e1',
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
          <span style={{ fontWeight: 600, color: 'var(--color-text)' }}>{node.environment}</span>
          <HealthChip state={node.state} size="sm" />
        </div>
        <button
          onClick={onClose}
          aria-label="Close stage detail"
          style={{
            background: 'none',
            border: 'none',
            cursor: 'pointer',
            color: '#64748b',
            fontSize: '1rem',
            lineHeight: 1,
            padding: '2px 4px',
          }}
        >×</button>
      </div>

      {/* Bake countdown (#501) */}
      {hasBake && (
        <div style={{ padding: '0.5rem 0.75rem', borderBottom: '1px solid #1e293b' }}>
          <div style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            marginBottom: '0.3rem',
          }}>
            <span style={{ color: 'var(--color-text-muted)', fontSize: '0.72rem', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
              Bake
            </span>
            <span
              title={`Bake progress: ${bakeElapsed}m of ${bakeTarget}m elapsed`}
              style={{ fontFamily: 'monospace', color: 'var(--color-text)' }}
            >
              {formatBakeCountdown(bakeElapsed, bakeTarget)}
            </span>
          </div>
          {/* Progress bar */}
          <div style={{
            height: '4px',
            background: 'var(--color-surface)',
            borderRadius: '2px',
            overflow: 'hidden',
          }}>
            <div
              role="progressbar"
              aria-valuenow={bakeElapsed}
              aria-valuemin={0}
              aria-valuemax={bakeTarget}
              aria-label={`Bake progress: ${bakePct(bakeElapsed, bakeTarget)}%`}
              style={{
                height: '100%',
                width: `${bakePct(bakeElapsed, bakeTarget)}%`,
                background: bakeElapsed >= bakeTarget ? 'var(--color-success)' : 'var(--color-accent)',
                borderRadius: '2px',
                transition: 'width 0.5s ease',
              }}
            />
          </div>
          {bakeResets > 0 && (
            <div style={{ fontSize: '0.68rem', color: '#f59e0b', marginTop: '0.25rem' }}>
              ⚠ Timer reset {bakeResets} time{bakeResets !== 1 ? 's' : ''} due to health alarm
            </div>
          )}
        </div>
      )}

      {/* PR URL (#501) */}
      {(step?.prURL || node.prURL) && (
        <div style={{ padding: '0.4rem 0.75rem', borderBottom: '1px solid #1e293b' }}>
          <a
            href={step?.prURL ?? node.prURL}
            target="_blank"
            rel="noopener noreferrer"
            style={{ color: 'var(--color-accent)', textDecoration: 'none', fontSize: '0.75rem' }}
            aria-label="Open pull request"
          >
            View PR ↗
          </a>
        </div>
      )}

      {/* Step list (#501) */}
      {step && (
        <div style={{ padding: '0.5rem 0.75rem' }}>
          <div style={{ color: 'var(--color-text-muted)', fontSize: '0.68rem', textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '0.35rem' }}>
            Steps
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.15rem' }}>
            {STEP_SEQUENCE.map((s, i) => {
              const { icon, color } = stepIconFor(i, currentStepIndex, isActive)
              return (
                <div
                  key={s.type}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: '0.5rem',
                    padding: '0.1rem 0',
                    opacity: i > currentStepIndex ? 0.45 : 1,
                  }}
                >
                  <span style={{ color, fontFamily: 'monospace', minWidth: '1rem', textAlign: 'center', fontSize: '0.75rem' }}>
                    {icon}
                  </span>
                  <span style={{
                    fontSize: '0.75rem',
                    color: i === currentStepIndex ? 'var(--color-text)' : 'var(--color-text-muted)',
                    fontWeight: i === currentStepIndex ? 600 : 400,
                  }}>
                    {s.label}
                  </span>
                  {i === currentStepIndex && step.message && (
                    <span style={{ fontSize: '0.68rem', color: '#64748b', marginLeft: 'auto', maxWidth: '100px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {step.message}
                    </span>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      )}

      {/* No step found */}
      {!step && (
        <div style={{ padding: '0.75rem', color: 'var(--color-text-faint)', fontSize: '0.75rem' }}>
          No active promotion step for this environment.
        </div>
      )}
    </div>
  )
}
