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

// components/ReleaseMetricsBar.tsx — Release efficiency metrics panel (#465).
// Computed client-side from the last 10 bundles of the pipeline:
//  - Time to prod: mean time from bundle creation until the pipeline's last
//    environment passed its health check (status.environments[].healthCheckedAt).
//  - Rollback rate: share of bundles that are rollbacks (spec.provenance.rollbackOf).
//  - Deploys: bundles that reached the last environment.
// The bar is hidden until at least one bundle has reached the last environment.
import type { Bundle, DeploymentMetrics } from '../types'
import { sortBundlesNewestFirst } from '../bundleSelection'

/** Number of most recent bundles the metrics cover. */
const WINDOW = 10

/** Computed release efficiency metrics. */
export interface ReleaseMetrics {
  /** Number of bundles analyzed (at most WINDOW). */
  totalBundles: number
  /** Number of those bundles that are rollbacks. */
  rollbackCount: number
  /** Percentage of bundles that are rollbacks (0-100). */
  rollbackRatePct: number
  /** Mean time from bundle creation to the last environment's health check, in hours. */
  meanTtpHours: number
  /** Bundles that reached the last environment. */
  deployCount: number
}

/** Time the bundle passed the health check in `env`, in ms, or undefined. */
function verifiedAt(b: Bundle, env: string): number | undefined {
  const at = b.environments?.find(e => e.name === env)?.healthCheckedAt
  if (!at) return undefined
  const t = new Date(at).getTime()
  return isNaN(t) ? undefined : t
}

/**
 * Compute release efficiency metrics from the last `window` bundles.
 * `finalEnvironment` is the pipeline's last environment (usually prod).
 * Returns null when no bundle in the window has reached it.
 * Does not mutate the input array.
 */
export function computeReleaseMetrics(
  bundles: Bundle[],
  finalEnvironment: string | undefined,
  window = WINDOW,
): ReleaseMetrics | null {
  if (!finalEnvironment) return null
  const recent = sortBundlesNewestFirst(bundles).slice(0, window)

  let ttpSum = 0
  let deployCount = 0
  for (const b of recent) {
    const done = verifiedAt(b, finalEnvironment)
    if (done === undefined) continue
    deployCount++
    const created = b.createdAt ? new Date(b.createdAt).getTime() : NaN
    ttpSum += isNaN(created) ? 0 : Math.max(0, done - created)
  }
  if (deployCount === 0) return null

  const rollbackCount = recent.filter(b => !!b.provenance?.rollbackOf).length
  return {
    totalBundles: recent.length,
    rollbackCount,
    rollbackRatePct: Math.round((rollbackCount / recent.length) * 100),
    meanTtpHours: ttpSum / deployCount / 3_600_000,
    deployCount,
  }
}

/** Format hours: < 1h → "< 1h", < 48h → "Xh", otherwise "Xd". */
export function formatHours(hours: number): string {
  if (hours < 1) return '< 1h'
  if (hours < 48) return `${Math.round(hours)}h`
  return `${Math.round(hours / 24)}d`
}

/** Format minutes: < 60 → "Xm", < 48h → "XhYm", otherwise "Xd". */
export function formatMinutes(minutes: number): string {
  if (minutes < 60) return `${minutes}m`
  if (minutes < 48 * 60) {
    const m = minutes % 60
    return m ? `${Math.floor(minutes / 60)}h${m}m` : `${minutes / 60}h`
  }
  return `${Math.round(minutes / 1440)}d`
}

/** Color for the change failure rate (DORA: elite 0-15%). */
export function cfrColor(millis: number): string {
  if (millis <= 150) return 'var(--color-success)'
  if (millis <= 300) return 'var(--color-warning)'
  return 'var(--color-error)'
}

/** Color for the rollback rate percentage. */
function rollbackColor(pct: number): string {
  if (pct === 0) return 'var(--color-success)'
  if (pct < 20) return 'var(--color-warning)'
  return 'var(--color-error)'
}

interface MetricCellProps {
  label: string
  value: string
  sub?: string
  color?: string
  last?: boolean
}

function MetricCell({ label, value, sub, color, last }: MetricCellProps) {
  return (
    <div style={{ flex: 1, padding: '0.5rem 0.75rem', borderRight: last ? undefined : '1px solid var(--color-border-muted)' }}>
      <div style={{ fontSize: '0.65rem', color: 'var(--color-text-muted)', textTransform: 'uppercase', letterSpacing: '0.06em', marginBottom: '0.2rem' }}>
        {label}
      </div>
      <div style={{ fontSize: '1rem', fontWeight: 700, color: color ?? 'var(--color-text)', fontVariantNumeric: 'tabular-nums' }}>
        {value}
      </div>
      {sub && (
        <div style={{ fontSize: '0.65rem', color: 'var(--color-text-muted)', marginTop: '0.1rem' }}>
          {sub}
        </div>
      )}
    </div>
  )
}

interface ReleaseMetricsBarProps {
  bundles: Bundle[]
  /** The pipeline's last environment; metrics count bundles that reached it. */
  finalEnvironment?: string
  /** The controller's Pipeline.status.deploymentMetrics: adds the change
   *  failure rate and time to restore (DORA stability) when it has deployments. */
  deploymentMetrics?: DeploymentMetrics
}

/**
 * ReleaseMetricsBar renders inline release efficiency metrics for a pipeline.
 * Computed client-side from the bundle list — no new backend API needed.
 */
export function ReleaseMetricsBar({ bundles, finalEnvironment, deploymentMetrics }: ReleaseMetricsBarProps) {
  const metrics = computeReleaseMetrics(bundles, finalEnvironment)
  if (!metrics) return null

  const scope = `last ${metrics.totalBundles} bundle${metrics.totalBundles === 1 ? '' : 's'}`
  const dm = deploymentMetrics
  const stability = dm && (dm.deployments ?? 0) > 0 ? dm : undefined
  const cfrMillis = stability?.changeFailureRateMillis ?? 0
  return (
    <section
      aria-label="Release metrics"
      style={{
        background: 'var(--color-surface)',
        border: '1px solid var(--color-border-muted)',
        borderRadius: '6px',
        display: 'flex',
        overflow: 'hidden',
        marginBottom: '1rem',
      }}
    >
      <MetricCell
        label={`Time to ${finalEnvironment}`}
        value={formatHours(metrics.meanTtpHours)}
        sub={`mean, ${scope}`}
        color="var(--color-code)"
      />
      <MetricCell
        label="Rollback rate"
        value={`${metrics.rollbackRatePct}%`}
        sub={`${metrics.rollbackCount} rollback${metrics.rollbackCount === 1 ? '' : 's'}`}
        color={rollbackColor(metrics.rollbackRatePct)}
      />
      <MetricCell
        label={`Deploys to ${finalEnvironment}`}
        value={String(metrics.deployCount)}
        sub={scope}
        color="var(--color-accent)"
        last={!stability}
      />
      {stability && (
        <MetricCell
          label="Change failure rate"
          value={`${(cfrMillis / 10).toFixed(1)}%`}
          sub={`${stability.failedDeployments ?? 0} of ${stability.deployments} deployments`}
          color={cfrColor(cfrMillis)}
        />
      )}
      {stability && (
        <MetricCell
          label="Time to restore"
          value={(stability.restoredFailures ?? 0) > 0 ? formatMinutes(stability.meanTimeToRestoreMinutes ?? 0) : '—'}
          sub={(stability.restoredFailures ?? 0) > 0
            ? `mean of ${stability.restoredFailures} restored`
            : 'no restored failure'}
          color="var(--color-code)"
          last
        />
      )}
    </section>
  )
}
