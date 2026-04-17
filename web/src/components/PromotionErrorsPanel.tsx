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

// components/PromotionErrorsPanel.tsx — Cross-environment error aggregation.
// Groups PromotionStep failures by (stepType, message-prefix) and surfaces them
// as a single panel above the DAG, replacing the need to click each failed node.
//
// Adapted from kro-ui ErrorsTab.tsx groupErrorPatterns() — simplified for
// kardinal's domain (PromotionStep failures, not RGD conditions).
// #528

import { useState } from 'react'
import type { PromotionStep } from '../types'

/** Minimum grouping for a single failed step. */
interface ErrorGroup {
  /** Step type where failures occurred (e.g. "git-push", "health-check"). */
  stepType: string
  /** Canonical error message (from the step with most recent failure). */
  message: string
  /** Short prefix (first 80 chars) used as the group key. */
  messagePrefix: string
  /** Number of affected environments. */
  count: number
  /** Affected environments with their PromotionStep details. */
  environments: Array<{
    environment: string
    namespace: string
    stepName: string
    fullMessage: string
  }>
}

/**
 * groupStepErrors — aggregate failed PromotionSteps into error groups.
 *
 * Groups by (stepType, message-prefix). Sorted by count desc then stepType asc.
 * Pure function — no side effects.
 */
export function groupStepErrors(steps: PromotionStep[]): ErrorGroup[] {
  const failedSteps = steps.filter(s => s.state === 'Failed' && s.stepType !== '')

  const acc = new Map<string, ErrorGroup>()

  for (const step of failedSteps) {
    const stepType = inferStepType(step)
    const message = step.message ?? '(no error message)'
    const messagePrefix = message.slice(0, 80)
    const key = `${stepType}::${messagePrefix}`

    if (!acc.has(key)) {
      acc.set(key, {
        stepType,
        message,
        messagePrefix,
        count: 0,
        environments: [],
      })
    }

    const group = acc.get(key)!
    group.count++
    group.environments.push({
      environment: step.environment,
      namespace: step.namespace,
      stepName: step.name,
      fullMessage: message,
    })
    // Use the most recent (last seen) message as canonical.
    group.message = message
  }

  return Array.from(acc.values())
    .sort((a, b) => {
      if (b.count !== a.count) return b.count - a.count
      return a.stepType.localeCompare(b.stepType)
    })
}

/** Infer a human-readable step type from the PromotionStep.
 * The stepType field is the high-level type (e.g. "promotion", "rollback").
 * The message prefix often contains the sub-step that failed (e.g. "git-push: ...").
 */
function inferStepType(step: PromotionStep): string {
  // Check if the message starts with a known sub-step name.
  const knownSubSteps = [
    'git-clone', 'git-commit', 'git-push',
    'kustomize-set-image', 'helm-set-image',
    'open-pr', 'wait-for-merge', 'health-check',
    'integration-test', 'config-merge',
  ]
  const msg = step.message?.toLowerCase() ?? ''
  for (const sub of knownSubSteps) {
    if (msg.startsWith(sub) || msg.includes(`${sub}:`)) {
      return sub
    }
  }
  // Fall back to the high-level step type from spec.
  return step.stepType || 'promotion'
}

// ── Component ─────────────────────────────────────────────────────────────

interface Props {
  steps: PromotionStep[]
  /** Called when the user clicks on an environment name to navigate to its node. */
  onSelectEnvironment?: (environment: string) => void
}

/**
 * PromotionErrorsPanel — shown when failedStepCount > 0.
 * Groups promotion step failures by type+message to surface systemic issues
 * without requiring the user to click each failed DAG node individually.
 * #528
 */
export default function PromotionErrorsPanel({ steps, onSelectEnvironment }: Props) {
  const groups = groupStepErrors(steps)
  const [expandedRaw, setExpandedRaw] = useState<Set<string>>(new Set())
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set())

  if (groups.length === 0) return null

  const totalFailed = groups.reduce((sum, g) => sum + g.count, 0)

  function toggleRaw(key: string) {
    setExpandedRaw(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key); else next.add(key)
      return next
    })
  }

  function toggleGroup(key: string) {
    setExpandedGroups(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key); else next.add(key)
      return next
    })
  }

  return (
    <div
      data-testid="promotion-errors-panel"
      style={{
        margin: '0 1.5rem 1rem',
        border: '1px solid #7f1d1d',
        borderRadius: '6px',
        background: '#1c0a0a',
        overflow: 'hidden',
      }}
    >
      {/* Header */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        gap: '8px',
        padding: '10px 14px',
        background: '#2d0f0f',
        borderBottom: '1px solid #7f1d1d',
      }}>
        <span style={{ color: '#ef4444', fontSize: '14px' }}>✗</span>
        <span style={{ fontWeight: 600, color: '#fca5a5', fontSize: '13px' }}>
          {totalFailed} environment{totalFailed !== 1 ? 's' : ''} failed
        </span>
        <span style={{ color: '#6b7280', fontSize: '12px' }}>
          — {groups.length} distinct failure pattern{groups.length !== 1 ? 's' : ''}
        </span>
      </div>

      {/* Error groups */}
      {groups.map((group, gi) => {
        const key = `${group.stepType}::${group.messagePrefix}`
        const isGroupExpanded = expandedGroups.has(key)
        const isRawExpanded = expandedRaw.has(key)

        return (
          <div
            key={key}
            data-testid="error-group"
            style={{
              borderBottom: gi < groups.length - 1 ? '1px solid #3f1515' : undefined,
            }}
          >
            {/* Group header row */}
            <div style={{
              display: 'flex',
              alignItems: 'flex-start',
              gap: '10px',
              padding: '10px 14px',
            }}>
              {/* Step type badge */}
              <span
                data-testid="error-step-type"
                style={{
                  fontSize: '11px',
                  fontWeight: 600,
                  padding: '2px 7px',
                  borderRadius: '3px',
                  background: '#450a0a',
                  border: '1px solid #991b1b',
                  color: '#fca5a5',
                  flexShrink: 0,
                  fontFamily: 'monospace',
                  whiteSpace: 'nowrap',
                  marginTop: '1px',
                }}
              >
                {group.stepType}
              </span>

              {/* Affected count */}
              <span
                data-testid="error-count"
                style={{
                  fontSize: '11px',
                  color: '#dc2626',
                  flexShrink: 0,
                  fontWeight: 600,
                  marginTop: '2px',
                }}
              >
                {group.count}×
              </span>

              {/* Message preview + controls */}
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{
                  fontSize: '12px',
                  color: '#fca5a5',
                  fontFamily: 'monospace',
                  wordBreak: 'break-all',
                  lineHeight: 1.4,
                }}>
                  {isRawExpanded ? group.message : group.messagePrefix}
                  {group.message.length > 80 && !isRawExpanded && (
                    <span style={{ color: '#6b7280' }}>…</span>
                  )}
                </div>

                {/* Controls */}
                <div style={{ display: 'flex', gap: '10px', marginTop: '6px' }}>
                  {group.message.length > 80 && (
                    <button
                      onClick={() => toggleRaw(key)}
                      style={{
                        background: 'none',
                        border: 'none',
                        padding: 0,
                        cursor: 'pointer',
                        color: '#6366f1',
                        fontSize: '11px',
                        textDecoration: 'underline',
                      }}
                    >
                      {isRawExpanded ? 'Hide raw' : 'Show raw'}
                    </button>
                  )}
                  <button
                    data-testid="expand-environments"
                    onClick={() => toggleGroup(key)}
                    style={{
                      background: 'none',
                      border: 'none',
                      padding: 0,
                      cursor: 'pointer',
                      color: 'var(--color-text-muted)',
                      fontSize: '11px',
                      textDecoration: 'underline',
                    }}
                  >
                    {isGroupExpanded
                      ? 'Collapse'
                      : `${group.count} affected environment${group.count !== 1 ? 's' : ''} ▸`}
                  </button>
                </div>
              </div>
            </div>

            {/* Expanded environment list */}
            {isGroupExpanded && (
              <div style={{
                padding: '0 14px 10px 14px',
                display: 'flex',
                flexWrap: 'wrap',
                gap: '6px',
              }}>
                {group.environments.map(env => (
                  <button
                    key={`${env.namespace}/${env.environment}`}
                    data-testid="environment-link"
                    onClick={() => onSelectEnvironment?.(env.environment)}
                    style={{
                      background: '#2d1515',
                      border: '1px solid #7f1d1d',
                      borderRadius: '4px',
                      padding: '3px 10px',
                      cursor: 'pointer',
                      color: '#fca5a5',
                      fontSize: '12px',
                      fontFamily: 'monospace',
                    }}
                  >
                    {env.environment}
                    {env.namespace !== 'default' && (
                      <span style={{ color: '#6b7280', marginLeft: '4px' }}>
                        ({env.namespace})
                      </span>
                    )}
                  </button>
                ))}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
