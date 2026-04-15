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
//
// lib/conditions.ts — Helpers for Kubernetes-style conditions (#529).
//
// Kubernetes conditions follow the pattern:
//   type: Ready, status: True  → healthy
//   type: Ready, status: False → unhealthy
//   type: ReconciliationSuspended, status: True  → unhealthy (inverted)
//   type: ReconciliationSuspended, status: False → healthy (inverted)
//
// Adapted from kro-ui's lib/conditions.ts pattern.

/**
 * Condition types where status=True is UNHEALTHY (inverted semantics).
 * e.g. ReconciliationSuspended=True means "currently suspended" → bad.
 */
const INVERTED_CONDITION_TYPES = new Set([
  'ReconciliationSuspended',
  'Degraded',
  'Paused',
  'Stalled',
  'OutOfSync',
])

/**
 * isHealthyCondition returns true if the condition represents a healthy state.
 *
 * Most conditions: status=True → healthy, status=False → unhealthy.
 * Inverted conditions: status=True → unhealthy, status=False → healthy.
 */
export function isHealthyCondition(type: string, status: string): boolean {
  const isInverted = INVERTED_CONDITION_TYPES.has(type)
  if (isInverted) {
    return status === 'False'
  }
  return status === 'True'
}

/**
 * conditionStatusLabel returns a terse display label for a condition status.
 */
export function conditionStatusLabel(type: string, status: string): string {
  const healthy = isHealthyCondition(type, status)
  if (status === 'Unknown') return '?'
  return healthy ? '✓' : '✗'
}

/**
 * conditionSortOrder returns a numeric sort key — failing conditions first.
 * Lower numbers sort first (failing = 0, unknown = 1, healthy = 2).
 */
export function conditionSortOrder(type: string, status: string): number {
  if (status === 'Unknown') return 1
  return isHealthyCondition(type, status) ? 2 : 0
}

export interface Condition {
  type: string
  status: string
  message?: string
  reason?: string
  lastTransitionTime?: string
}

/**
 * sortConditions returns a new array sorted with failing conditions first.
 */
export function sortConditions(conditions: Condition[]): Condition[] {
  return [...conditions].sort((a, b) =>
    conditionSortOrder(a.type, a.status) - conditionSortOrder(b.type, b.status)
  )
}

/**
 * conditionsSummary returns { healthy, total } counts.
 */
export function conditionsSummary(conditions: Condition[]): { healthy: number; total: number } {
  const healthy = conditions.filter(c => isHealthyCondition(c.type, c.status)).length
  return { healthy, total: conditions.length }
}
