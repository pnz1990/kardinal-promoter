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
//  - Time to prod: mean time from bundle creation until every final
//    environment passed its health check (status.environments[].healthCheckedAt).
//    The final environments are those no other environment waits for: prod in
//    test, uat, prod; every environment of a last wave (#1580).
//  - Rollback rate: share of bundles that are rollbacks (spec.provenance.rollbackOf).
//  - Deploys: bundles that reached every final environment.
// The bar is hidden until at least one bundle has reached them.
import type { Bundle, DeploymentMetrics } from '../types'
import { sortBundlesNewestFirst } from '../bundleSelection'
import '../styles/ReleaseMetricsBar.css'

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
  /** Mean time from bundle creation to the last final environment's health check, in hours. */
  meanTtpHours: number
  /** Bundles that reached every final environment. */
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
 * When the bundle passed the health check in every one of `envs` (the latest
 * of those times), in ms, or undefined while one of them has not.
 */
function verifiedInAll(b: Bundle, envs: string[]): number | undefined {
  let done = -Infinity
  for (const env of envs) {
    const t = verifiedAt(b, env)
    if (t === undefined) return undefined
    done = Math.max(done, t)
  }
  return done
}

/**
 * Compute release efficiency metrics from the last `window` bundles.
 * `finalEnvironments` are the pipeline's final environments (usually prod;
 * every environment of a last wave); a bundle counts once it passed the
 * health check in all of them.
 * Returns null when no bundle in the window has reached them.
 * Does not mutate the input array.
 */
export function computeReleaseMetrics(
  bundles: Bundle[],
  finalEnvironments: string[] | undefined,
  window = WINDOW,
): ReleaseMetrics | null {
  if (!finalEnvironments || finalEnvironments.length === 0) return null
  const recent = sortBundlesNewestFirst(bundles).slice(0, window)

  let ttpSum = 0
  let deployCount = 0
  for (const b of recent) {
    const done = verifiedInAll(b, finalEnvironments)
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

/** The colour of a metric value: a status bucket, or the code and accent colours. */
type MetricTone = 'verified' | 'blocked' | 'failed' | 'code' | 'accent'

/** The tone of one of the var(--color-*) values cfrColor and rollbackColor return. */
function toneOf(color: string): MetricTone {
  if (color.includes('success')) return 'verified'
  if (color.includes('warning')) return 'blocked'
  return 'failed'
}

interface MetricCellProps {
  label: string
  value: string
  sub?: string
  tone?: MetricTone
  last?: boolean
  /** Tooltip for the cell. */
  title?: string
}

function MetricCell({ label, value, sub, tone, last, title }: MetricCellProps) {
  return (
    <div title={title} className={`release-metrics__cell${last ? ' release-metrics__cell--last' : ''}`}>
      <div className="release-metrics__label">
        {label}
      </div>
      <div className="release-metrics__value" data-tone={tone}>
        {value}
      </div>
      {sub && (
        <div className="release-metrics__sub">
          {sub}
        </div>
      )}
    </div>
  )
}

interface ReleaseMetricsBarProps {
  bundles: Bundle[]
  /** The pipeline's final environments (terminalEnvironments); metrics count
   *  bundles that reached all of them. */
  finalEnvironments?: string[]
  /** The controller's Pipeline.status.deploymentMetrics: adds the change
   *  failure rate and time to restore (DORA stability) when it has deployments. */
  deploymentMetrics?: DeploymentMetrics
}

/**
 * ReleaseMetricsBar renders inline release efficiency metrics for a pipeline.
 * Computed client-side from the bundle list — no new backend API needed.
 */
export function ReleaseMetricsBar({ bundles, finalEnvironments, deploymentMetrics }: ReleaseMetricsBarProps) {
  const metrics = computeReleaseMetrics(bundles, finalEnvironments)
  if (!metrics || !finalEnvironments) return null
  // One final environment is named; a final wave is counted.
  const target = finalEnvironments.length === 1 ? finalEnvironments[0] : `all ${finalEnvironments.length} final envs`
  const targetTitle = finalEnvironments.length === 1 ? undefined : `Final environments: ${finalEnvironments.join(', ')}`

  const scope = `last ${metrics.totalBundles} version${metrics.totalBundles === 1 ? '' : 's'}`
  const dm = deploymentMetrics
  const stability = dm && (dm.deployments ?? 0) > 0 ? dm : undefined
  const cfrMillis = stability?.changeFailureRateMillis ?? 0
  return (
    <section
      aria-label="Release metrics"
      className="release-metrics"
    >
      <MetricCell
        label={`Time to ${target}`}
        title={targetTitle}
        value={formatHours(metrics.meanTtpHours)}
        sub={`mean, ${scope}`}
        tone="code"
      />
      <MetricCell
        label="Rollback rate"
        value={`${metrics.rollbackRatePct}%`}
        sub={`${metrics.rollbackCount} rollback${metrics.rollbackCount === 1 ? '' : 's'}`}
        tone={toneOf(rollbackColor(metrics.rollbackRatePct))}
      />
      <MetricCell
        label={`Deploys to ${target}`}
        title={targetTitle}
        value={String(metrics.deployCount)}
        sub={scope}
        tone="accent"
        last={!stability}
      />
      {stability && (
        <MetricCell
          label="Change failure rate"
          value={`${(cfrMillis / 10).toFixed(1)}%`}
          sub={`${stability.failedDeployments ?? 0} of ${stability.deployments} deployments`}
          tone={toneOf(cfrColor(cfrMillis))}
        />
      )}
      {stability && (
        <MetricCell
          label="Time to restore"
          value={(stability.restoredFailures ?? 0) > 0 ? formatMinutes(stability.meanTimeToRestoreMinutes ?? 0) : '—'}
          sub={(stability.restoredFailures ?? 0) > 0
            ? `mean of ${stability.restoredFailures} restored`
            : 'no restored failure'}
          tone="code"
          last
        />
      )}
    </section>
  )
}
