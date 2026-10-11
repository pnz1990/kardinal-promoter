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
import '../styles/PromotionErrorsPanel.css'

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

/** PromotionStep states that count as a failed promotion. */
const FAILED_STATES = new Set(['Failed', 'AbortedByAlarm'])

/**
 * groupStepErrors — aggregate failed PromotionSteps into error groups.
 *
 * Groups by (stepType, message-prefix). Sorted by count desc then stepType asc.
 * Pure function — no side effects.
 */
export function groupStepErrors(steps: PromotionStep[]): ErrorGroup[] {
  const failedSteps = steps.filter(s => FAILED_STATES.has(s.state))

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

/**
 * Name of the step that failed, e.g. "git-push" or "health-check".
 *
 * Order: the status.steps[] entry marked Failed; else the one still running
 * (a health-check timeout or a PR closed without merging is recorded on the
 * PromotionStep, not on the step entry); else a step name in the message;
 * else "promotion". spec.stepType is the update strategy, not the failed step,
 * so it is never used as the label.
 */
export function inferStepType(step: PromotionStep): string {
  const list = step.steps ?? []
  const failed = list.find(s => s.state === 'Failed')
  if (failed) return failed.name
  const current = step.currentStepIndex !== undefined ? list[step.currentStepIndex] : undefined
  if (current && current.state !== 'Completed') return current.name
  const running = list.find(s => s.state === 'InProgress')
  if (running) return running.name

  const knownSubSteps = [
    'git-clone', 'git-commit', 'git-push',
    'kustomize-set-image', 'helm-set-image', 'argocd-set-image',
    'open-pr', 'wait-for-merge', 'health-check',
    'config-merge', 'render', 'render-manifests',
  ]
  const msg = step.message?.toLowerCase() ?? ''
  for (const sub of knownSubSteps) {
    if (msg.startsWith(sub) || msg.includes(`${sub}:`) || msg.includes(`step ${sub}`)) {
      return sub
    }
  }
  return 'promotion'
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
    <div data-testid="promotion-errors-panel" className="promotion-errors">
      {/* Header */}
      <div className="promotion-errors__header">
        <span className="promotion-errors__icon">✗</span>
        <span className="promotion-errors__title">
          {totalFailed} environment{totalFailed !== 1 ? 's' : ''} failed
        </span>
        <span className="promotion-errors__subtitle">
          — {groups.length} distinct failure pattern{groups.length !== 1 ? 's' : ''}
        </span>
      </div>

      {/* Error groups */}
      {groups.map(group => {
        const key = `${group.stepType}::${group.messagePrefix}`
        const isGroupExpanded = expandedGroups.has(key)
        const isRawExpanded = expandedRaw.has(key)

        return (
          <div key={key} data-testid="error-group" className="promotion-errors__group">
            {/* Group header row */}
            <div className="promotion-errors__row">
              {/* Step type badge */}
              <span data-testid="error-step-type" className="promotion-errors__step">
                {group.stepType}
              </span>

              {/* Affected count */}
              <span data-testid="error-count" className="promotion-errors__count">
                {group.count}×
              </span>

              {/* Message preview + controls */}
              <div className="promotion-errors__body">
                <div className="promotion-errors__message">
                  {isRawExpanded ? group.message : group.messagePrefix}
                  {group.message.length > 80 && !isRawExpanded && (
                    <span className="promotion-errors__ellipsis">…</span>
                  )}
                </div>

                {/* Controls */}
                <div className="promotion-errors__controls">
                  {group.message.length > 80 && (
                    <button
                      onClick={() => toggleRaw(key)}
                      className="promotion-errors__link promotion-errors__link--raw"
                    >
                      {isRawExpanded ? 'Hide raw' : 'Show raw'}
                    </button>
                  )}
                  <button
                    data-testid="expand-environments"
                    onClick={() => toggleGroup(key)}
                    className="promotion-errors__link"
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
              <div className="promotion-errors__envs">
                {group.environments.map(env => (
                  <button
                    key={`${env.namespace}/${env.environment}`}
                    data-testid="environment-link"
                    onClick={() => onSelectEnvironment?.(env.environment)}
                    className="promotion-errors__env"
                  >
                    {env.environment}
                    {env.namespace !== 'default' && (
                      <span className="promotion-errors__env-ns">
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
